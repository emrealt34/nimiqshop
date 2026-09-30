/**
 * hub.ts — Nimiq Hub & Nimiq Pay integration (ported from hub.js). Wallet
 * login (challenge → signMessage → /auth/hub-login), Nimiq Pay provider,
 * Lightning URI helpers and mobile redirect-result recovery.
 */
import { CFG } from './config';
import { bytesToHex } from './format';
import { getNimiqProvider, initNimiqMiniApp, openInNimiqPay } from './miniapp';
import { authChallenge, hubLogin as apiHubLogin } from './api';
import { saveSession } from './session';
import { ensureLib } from './vendorLoad';
import { t as tr } from '../i18n';

let hub: any = null;

async function loadHubApi() {
  await ensureLib('HubApi');
}

function getHub(): any {
  if (!hub) {
    const HubApi = (window as any).HubApi;
    if (!HubApi) throw new Error(tr('hub.bridgeFailed'));
    hub = new HubApi(CFG.HUB_URL);
  }
  return hub;
}

const LOGIN_KEY = 'nimshop.pendingLogin';
const PAY_KEY = 'nimshop.pendingPay';

function savePending(key: string, data: Record<string, unknown>) {
  try {
    sessionStorage.setItem(key, JSON.stringify({ ...data, ts: Date.now() }));
  } catch {
    /* ignore */
  }
}
function loadPending(key: string, maxAgeMs = 10 * 60 * 1000): Record<string, any> | null {
  try {
    const raw = sessionStorage.getItem(key);
    if (!raw) return null;
    const data = JSON.parse(raw);
    if (!data || !data.ts || Date.now() - data.ts > maxAgeMs) return null;
    return data;
  } catch {
    return null;
  }
}
function clearPending(key: string) {
  try {
    sessionStorage.removeItem(key);
  } catch {
    /* ignore */
  }
}

export function friendlyHubError(err: unknown): string {
  const msg = String((err && ((err as Error).message || String(err))) || '');
  if (/cancel|rejected|abort|closed/i.test(msg)) {
    return tr('hub.cancelled');
  }
  if (/popup/i.test(msg)) {
    return tr('hub.popup');
  }
  return tr('hub.failed');
}

/* ---------------- Login via Hub signMessage ---------------- */
function hubAppName(): string {
  return 'nim.shop';
}

function challengeMessage(challenge: any): string {
  const msg = challenge && challenge.message;
  if (typeof msg === 'string' && msg.trim()) return msg;
  const nonce = String((challenge && challenge.nonce) || '');
  return `nim.shop login: ${nonce}`;
}

function canUsePayProvider(payProvider: any): boolean {
  return !!(payProvider && typeof payProvider.listAccounts === 'function');
}

type HubChallenge = { challenge_token: string; message: string; nonce?: string };

let prefetched: { challenge: HubChallenge; at: number } | null = null;
const PREFETCH_MAX_MS = 4 * 60 * 1000;

async function fetchChallenge(): Promise<HubChallenge> {
  const raw: any = await authChallenge();
  const message = challengeMessage(raw);
  const token = String(raw?.challenge_token || '');
  if (!token || !message) throw new Error(tr('hub.failed'));
  return { challenge_token: token, message, nonce: raw?.nonce };
}

/** Warm HubApi + a login challenge so the Hub button can open the popup
 *  in the same click (no await before window.open → no Invalid request). */
export async function prefetchHubLogin(): Promise<void> {
  try {
    await loadHubApi();
    const challenge = await fetchChallenge();
    prefetched = { challenge, at: Date.now() };
  } catch {
    /* click path will fetch again */
  }
}

function takePrefetch(): HubChallenge | null {
  const p = prefetched;
  prefetched = null;
  if (!p || Date.now() - p.at > PREFETCH_MAX_MS) return null;
  return p.challenge;
}

function hubSignMessage(message: string): Promise<any> {
  const h = getHub();
  // Default popup behavior from HubApi — do not wrap a custom one; a
  // mismatched PopupRequestBehavior is what Hub renders as Invalid request.
  return h.signMessage({
    appName: hubAppName(),
    message: String(message),
  });
}

/** Browser Hub login. Must be called from a click; signMessage is started
 *  before any network wait when a challenge was prefetched. */
export function loginWithHub(_onProgress?: (step: 'challenge' | 'sign') => void): Promise<{ address: string }> {
  const ready = (window as any).HubApi ? Promise.resolve() : loadHubApi();
  const cached = takePrefetch();

  const signWith = (challenge: HubChallenge) => {
    savePending(LOGIN_KEY, { challenge_token: challenge.challenge_token, message: challenge.message });
    return hubSignMessage(challenge.message)
      .then((signed) => {
        clearPending(LOGIN_KEY);
        return finishLogin(challenge.challenge_token, signed);
      })
      .catch((err) => {
        clearPending(LOGIN_KEY);
        throw err;
      });
  };

  if (cached && (window as any).HubApi) {
    return signWith(cached);
  }

  return ready.then(async () => {
    const challenge = cached || (await fetchChallenge());
    return signWith(challenge);
  });
}

/** Nimiq Pay login. Inside the app this signs via the Mini App provider.
 *  Outside it tries the `nimiqpay://` deeplink, then the caller should show
 *  the App Store / Play Store install sheet. Never opens Hub. */
export async function loginWithNimiqPay(
  _onProgress?: (step: 'challenge' | 'sign') => void
): Promise<{ address: string } | { needsInstall: true }> {
  const sdkProvider = await initNimiqMiniApp();
  const payProvider: any = sdkProvider || getNimiqProvider();
  if (!canUsePayProvider(payProvider)) {
    return new Promise((resolve) => {
      openInNimiqPay(() => resolve({ needsInstall: true }));
    });
  }
  const challenge = await fetchChallenge();
  savePending(LOGIN_KEY, { challenge_token: challenge.challenge_token, message: challenge.message });
  try {
    if (payProvider.connected === false && payProvider.connect) await payProvider.connect();
    const accounts = await payProvider.listAccounts();
    const address = Array.isArray(accounts) ? accounts[0] : null;
    const signature = payProvider.sign ? await payProvider.sign(challenge.message) : null;
    clearPending(LOGIN_KEY);
    return finishLogin(challenge.challenge_token, {
      address,
      publicKey: signature && signature.publicKey,
      signature: signature && signature.signature,
    });
  } catch (err) {
    clearPending(LOGIN_KEY);
    throw err;
  }
}

async function finishLogin(challengeToken: string, signed: any): Promise<{ address: string }> {
  const address = signed.address || signed.signer;
  const publicKey =
    signed.signerPublicKey || signed.publicKey || (signed.signer instanceof Uint8Array ? signed.signer : undefined);
  const signature = signed.signature;
  if (!address || !publicKey || !signature) {
    throw new Error(tr('hub.incomplete'));
  }
  const res = await apiHubLogin({
    challenge_token: challengeToken,
    address,
    public_key: publicKey instanceof Uint8Array ? bytesToHex(publicKey) : String(publicKey),
    signature: signature instanceof Uint8Array ? bytesToHex(signature) : String(signature),
  });
  // The JWT arrives as an HttpOnly cookie set on this response; nothing
  // script-readable comes back to store. What is kept is session metadata,
  // which is enough to render the signed-in state and to know when it ends.
  saveSession({
    uid: String(res.user?.id || ''),
    address: String(res.user?.nimiq_address || address || ''),
    expiresAt: Number(res.expires_at || 0),
  });
  return { address: res.user.nimiq_address || address };
}

/* ---------------- Nimiq Pay: direct BTC Lightning ---------------- */
export function lightningPaymentURI(invoice: string): string {
  const raw = String(invoice || '').trim();
  if (!/^ln(?:bc|tb|bcrt)[a-z0-9]+$/i.test(raw)) {
    throw new Error(tr('hub.payRequest'));
  }
  return 'lightning:' + raw;
}

export function rememberLightningPayment(invoice: string, context?: Record<string, unknown>): string {
  const uri = lightningPaymentURI(invoice);
  if (context) savePending(PAY_KEY, { ...context, invoice: uri });
  return uri;
}

/* ---------------- Redirect result recovery (mobile) ---------------- */
export function initHubRedirectHandling({ onLogin }: { onLogin?: (address: string) => void }): void {
  (async () => {
    try {
      await loadHubApi();
      const h = getHub();
      const HubApi = (window as any).HubApi;

      h.on(
        HubApi.RequestType.SIGN_MESSAGE,
        async (result: any) => {
          const pending = loadPending(LOGIN_KEY);
          if (!pending) return;
          clearPending(LOGIN_KEY);
          try {
            const r = await finishLogin(pending.challenge_token, result);
            if (onLogin) onLogin(r.address);
          } catch (e) {
            window.dispatchEvent(
              new CustomEvent('nimshop:hub-error', { detail: { message: friendlyHubError(e) } })
            );
          }
        },
        () => {
          clearPending(LOGIN_KEY);
        }
      );

      h.checkRedirectResponse();
    } catch {
      /* hub lib missing: popup flows still work via direct errors */
    }
  })();
}
