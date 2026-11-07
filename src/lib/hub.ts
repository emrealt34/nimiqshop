/**
 * hub.ts — Nimiq Hub & Nimiq Pay integration (ported from hub.js). Wallet
 * login (challenge → signMessage → /auth/hub-login), Nimiq Pay provider,
 * Lightning URI helpers and mobile redirect-result recovery.
 */
import { CFG } from './config';
import { bytesToHex } from './format';
import { detectMobilePlatform, getNimiqProvider, inNimiqPay, initNimiqMiniApp, openInNimiqPay } from './miniapp';
import { authChallenge, hubLogin as apiHubLogin, stakeAddressChallenge, linkStakeAddress } from './api';
import { saveSession } from './session';
import { ensureLib } from './vendorLoad';
import { saneBolt11 } from './pay';
import { pickHostSigner, sameStakeAddress } from './stakeLinkIntent';
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
// A stake-wallet link is a second, different SIGN_MESSAGE flow: it has its own
// challenge (purpose-bound server-side) and its own pending entry, so a Hub
// redirect can be resumed into the right one.
const LINK_KEY = 'nimshop.pendingStakeLink';

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
  return 'nimshop';
}

function challengeMessage(challenge: any): string {
  const msg = challenge && challenge.message;
  if (typeof msg === 'string' && msg.trim()) return msg;
  const nonce = String((challenge && challenge.nonce) || '');
  return `nimshop login: ${nonce}`;
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

export type HubLoginResult = { address: string } | { redirecting: true };

function hubSignMessage(message: string, accountAddress?: string): Promise<{ signed: any } | { redirecting: true }> {
  const h = getHub();
  // On a phone the default popup is the wrong tool: browsers block a popup
  // that opens after an await, and a Hub window that opens without its request
  // just shows the Nimiq logo and waits. Use Hub's own REDIRECT behaviour there.
  // The pending login is already saved in sessionStorage, and
  // initHubRedirectHandling resumes it when Hub sends the user back.
  // Desktop keeps the default popup (a mismatched PopupRequestBehavior is what
  // Hub renders as Invalid request, so no custom popup object is built here).
  const HubApi = (window as any).HubApi;
  const behavior = detectMobilePlatform() && !inNimiqPay() && HubApi && HubApi.RedirectRequestBehavior
    ? new HubApi.RedirectRequestBehavior()
    : undefined;
  const response = h.signMessage({
    appName: hubAppName(),
    message: String(message),
    // Hub's documented signMessage option is `signer`. A link URL can hint
    // which wallet is expected, while a normal link lets Hub show its wallet
    // selector; the signed result reports the actual signer for the backend.
    ...(accountAddress ? { signer: String(accountAddress) } : {}),
  }, behavior);
  // RedirectRequestBehavior returns void (or resolves before navigation), not
  // a signature. A void response can also come from Hub's own fallback
  // redirect, even without a mobile user agent. Preserve the challenge.
  return Promise.resolve(response).then((signed) =>
    behavior || signed === undefined ? { redirecting: true as const } : { signed }
  );
}

/** Browser Hub login. Must be called from a click; signMessage is started
 *  before any network wait when a challenge was prefetched. */
export function loginWithHub(_onProgress?: (step: 'challenge' | 'sign') => void): Promise<HubLoginResult> {
  const ready = (window as any).HubApi ? Promise.resolve() : loadHubApi();
  const cached = takePrefetch();

  const signWith = (challenge: HubChallenge) => {
    savePending(LOGIN_KEY, { challenge_token: challenge.challenge_token, message: challenge.message });
    let response: ReturnType<typeof hubSignMessage>;
    try { response = hubSignMessage(challenge.message); }
    catch (err) { clearPending(LOGIN_KEY); return Promise.reject(err); }
    return response
      .then<HubLoginResult>((result) => {
        if ('redirecting' in result) return result;
        clearPending(LOGIN_KEY);
        return finishLogin(challenge.challenge_token, result.signed);
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

/** Pays one star rating through Hub's checkout (the buyer's own wallet signs).
 *  Desktop opens Hub in a popup and resolves with the transaction hash. On a
 *  phone Hub's redirect behaviour is used: the page reloads when Hub returns,
 *  and the rating flow resumes from its saved pending entry (no hash needed,
 *  the shop finds the transaction on chain).
 *
 *  `sender` is the address the buyer signed in with. The shop only accepts a
 *  rating paid by that address, and a Hub holding several addresses would
 *  otherwise let the buyer pay from any of them and only learn afterwards that
 *  the transaction "does not match this rating". With `sender` Hub skips the
 *  address choice; with `forceSender` it refuses instead of silently falling
 *  back to another address when that one is not in this Hub. */
export async function sendRatingWithHub(req: {
  recipient: string;
  value: number;
  fee: number;
  extraData: string;
  sender?: string;
}): Promise<{ hash?: string }> {
  await loadHubApi();
  const h = getHub();
  const HubApi = (window as any).HubApi;
  const mobile = detectMobilePlatform() && !inNimiqPay();
  const behavior = mobile && HubApi && HubApi.RedirectRequestBehavior
    ? new HubApi.RedirectRequestBehavior()
    : undefined;
  const sender = String(req.sender || '').trim();
  const signed = await h.checkout({
    appName: hubAppName(),
    recipient: req.recipient,
    value: req.value,
    fee: req.fee,
    extraData: req.extraData,
    ...(sender ? { sender, forceSender: true } : {}),
  }, behavior);
  return { hash: signed && typeof signed.hash === 'string' ? signed.hash : undefined };
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
    // Already inside Pay: the provider failed to load. Opening the nimiqpay://
    // link here would just try to launch the app we are running in.
    if (inNimiqPay()) throw new Error(tr('hub.bridgeFailed'));
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
  if (!signed || typeof signed !== 'object') throw new Error(tr('hub.incomplete'));
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

/* ---------------- Linking a staking wallet ---------------- */

/**
 * Raised when the buyer asks to link a wallet the HOST cannot sign for.
 *
 * Nimiq Pay's provider signs with the account it holds and takes no signer
 * (`sign(message)` has no address parameter, unlike Hub's `signMessage`), so a
 * `?link_stake=` URL naming a wallet that is not in Pay cannot be fulfilled
 * there. Guessing would link the account Pay does hold — the buyer would only
 * learn it was the wrong one when his stake never moved the rate. Its own type
 * so the cashback card can say "that wallet is not in your Pay" instead of the
 * generic "could not be linked".
 */
export class StakeLinkWalletNotInHostError extends Error {
  readonly expected: string;
  /** The accounts the host CAN sign for — what the buyer has to choose from. */
  readonly available: string[];
  constructor(expected: string, available: string[]) {
    super(`Nimiq Pay holds ${available.length} wallet(s), none of them ${expected}`);
    this.name = 'StakeLinkWalletNotInHostError';
    this.expected = expected;
    this.available = available;
  }
}

/**
 * Proves ownership of a staking wallet so the shop can read this account's
 * cashback from it.
 *
 * This is the answer to the customer who stakes from his regular wallet but
 * checks out through Nimiq Pay: the shop signs him in as whichever wallet he
 * picks there, and since one account == one address, his stake was invisible to
 * the account that actually buys. He signs ONE message with the staking wallet —
 * the same message login signs — and the backend verifies both the signature
 * and that the wallet delegates to our validator before it records the link.
 *
 * Which wallet app signs is the host's decision, exactly as it is for login
 * (SiteShell and CheckoutFlow both branch on `inNimiqPay()`): inside Nimiq Pay
 * the wallet IS Pay, and outside it the wallet is Hub. A link that ignored that
 * and always went to Hub opened hub.nimiq.com inside the Pay app, which is the
 * "logo page" dead end CheckoutFlow already documents for login.
 *
 * Returns the server's link response (which carries the refreshed cashback
 * view), or { redirecting: true } when Hub took over the page on a phone;
 * initHubRedirectHandling finishes the link when Hub sends the buyer back.
 */
export async function linkStakingWallet(expectedAddress?: string): Promise<Record<string, any> | { redirecting: true }> {
  const expected = String(expectedAddress || '').trim();
  return inNimiqPay() ? linkWithNimiqPay(expected) : linkWithHub(expected);
}

/** The Hub half: a wallet picker (desktop popup, phone redirect). */
async function linkWithHub(expected: string): Promise<Record<string, any> | { redirecting: true }> {
  if (!(window as any).HubApi) await loadHubApi();
  // Hand over the expected wallet so the backend can refuse a wallet that would
  // exceed the limit before Hub asks the buyer to sign for it.
  const challenge: any = await stakeAddressChallenge(expected || undefined);
  const message = String(challenge?.message || '');
  const token = String(challenge?.challenge_token || '');
  if (!message || !token) throw new Error(tr('hub.failed'));

  // Save before asking Hub: on a phone its redirect unloads this page.
  savePending(LINK_KEY, { challenge_token: token, expected_stake_address: expected });
  let response: ReturnType<typeof hubSignMessage>;
  try {
    response = hubSignMessage(message, expected || undefined);
  } catch (err) {
    clearPending(LINK_KEY);
    throw err;
  }
  const result = await response;
  if ('redirecting' in result) return { redirecting: true };
  clearPending(LINK_KEY);
  return finishStakeLink(token, result.signed, expected);
}

/**
 * The Nimiq Pay half: sign through the Mini App provider, never through Hub.
 *
 * Mirrors loginWithNimiqPay step for step — same provider lookup, same
 * `connect()` guard, same `listAccounts()` / `sign()` pair — with two
 * differences the link flow needs. The challenge comes from the stake-link
 * endpoint rather than the login one (the backend binds each to its purpose),
 * and the address is NOT assumed: Pay's `SignatureResult` is
 * `{ publicKey, signature }` with no address, so the signer is the account Pay
 * reported, and when the buyer asked for a specific wallet it has to be one of
 * them.
 *
 * There is no redirect and no install sheet here: the buyer is already inside
 * the app, so a provider that never arrived is a bridge failure and nothing
 * else can be offered.
 */
async function linkWithNimiqPay(expected: string): Promise<Record<string, any>> {
  const sdkProvider = await initNimiqMiniApp();
  const payProvider: any = sdkProvider || getNimiqProvider();
  if (!canUsePayProvider(payProvider)) throw new Error(tr('hub.bridgeFailed'));
  if (typeof payProvider.sign !== 'function') throw new Error(tr('hub.incomplete'));

  // Ask Pay which wallets it holds BEFORE requesting a challenge. Pay signs with
  // the account it has and takes no signer, so the wallet that will sign is
  // knowable here and now — and naming it in the challenge is what lets the
  // backend refuse a wallet that would exceed the limit before the buyer is
  // asked to sign anything, while still allowing one that is already linked.
  if (payProvider.connected === false && payProvider.connect) await payProvider.connect();
  const accounts = await payProvider.listAccounts();
  const held = (Array.isArray(accounts) ? accounts : [])
    .map((account: unknown) => String(account || '').trim())
    .filter(Boolean);
  if (!held.length) throw new Error(tr('hub.incomplete'));
  const signer = pickHostSigner(held, expected);
  if (!signer) throw new StakeLinkWalletNotInHostError(expected, held);

  const challenge: any = await stakeAddressChallenge(signer);
  const message = String(challenge?.message || '');
  const token = String(challenge?.challenge_token || '');
  if (!message || !token) throw new Error(tr('hub.failed'));

  savePending(LINK_KEY, { challenge_token: token, expected_stake_address: signer });
  try {
    const signed: any = await payProvider.sign(message);
    clearPending(LINK_KEY);
    // Pay's SignatureResult is { publicKey, signature } and carries no address,
    // so the address is the account Pay reported — exactly what login does.
    if (!signed || signed.error || !signed.publicKey || !signed.signature) throw new Error(tr('hub.incomplete'));
    return linkStakeAddress({
      challenge_token: token,
      stake_address: signer,
      public_key: toHex(signed.publicKey),
      signature: toHex(signed.signature),
    });
  } catch (err) {
    clearPending(LINK_KEY);
    throw err;
  }
}

/**
 * Which account signs, and whether two addresses name the same wallet, both live
 * in lib/stakeLinkIntent — pure functions, so scripts/test-stake-link-host.mjs
 * pins them on every push instead of only in a browser. `toHex` is the helper
 * that does belong here: Hub answers with Uint8Array, Pay with hex strings, and
 * the backend takes either.
 */
function toHex(value: unknown): string {
  return value instanceof Uint8Array ? bytesToHex(value) : String(value);
}

async function finishStakeLink(challengeToken: string, signed: any, expectedAddress = ''): Promise<Record<string, any>> {
  if (!signed || typeof signed !== 'object') throw new Error(tr('hub.incomplete'));
  const signer = String(signed.signer || signed.address || '').trim();
  // Hub reports the wallet that actually signed. A link URL names an
  // expectation; if Hub answered with a different wallet the proof is for the
  // wrong one and must not be recorded.
  if (!signer || (expectedAddress && !sameStakeAddress(signer, expectedAddress))) throw new Error(tr('hub.incomplete'));
  const publicKey =
    signed.signerPublicKey || signed.publicKey || (signed.signer instanceof Uint8Array ? signed.signer : undefined);
  const signature = signed.signature;
  if (!publicKey || !signature) throw new Error(tr('hub.incomplete'));
  return linkStakeAddress({
    challenge_token: challengeToken,
    stake_address: signer,
    public_key: toHex(publicKey),
    signature: toHex(signature),
  });
}

/* ---------------- Nimiq Pay: direct Nimiq Pay ---------------- */
export function lightningPaymentURI(invoice: string): string {
  const raw = String(invoice || '').trim();
  // Same gate as the copy button and the pay hand-off (lib/pay): shape +
  // amount + bech32 checksum, so a mangled invoice can never be launched.
  if (!saneBolt11(raw)) {
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
  if (!loadPending(LOGIN_KEY) && !loadPending(PAY_KEY) && !loadPending(LINK_KEY)) return;
  (async () => {
    try {
      await loadHubApi();
      const h = getHub();
      const HubApi = (window as any).HubApi;

      h.on(
        HubApi.RequestType.SIGN_MESSAGE,
        async (result: any) => {
          // A link and a login both arrive as SIGN_MESSAGE; the pending entry
          // says which flow was in flight when Hub redirected away.
          const linking = loadPending(LINK_KEY);
          if (linking) {
            clearPending(LINK_KEY);
            try {
              const linked = await finishStakeLink(
                String(linking.challenge_token || ''),
                result,
                String(linking.expected_stake_address || ''),
              );
              window.dispatchEvent(new CustomEvent('nimshop:stake-linked', { detail: linked }));
            } catch (e) {
              window.dispatchEvent(
                new CustomEvent('nimshop:hub-error', { detail: { message: friendlyHubError(e) } })
              );
            }
            return;
          }
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
          clearPending(LINK_KEY);
        }
      );

      h.checkRedirectResponse();
    } catch {
      /* hub lib missing: popup flows still work via direct errors */
    }
  })();
}
