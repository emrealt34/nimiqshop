/* hub.js — Nimiq Hub & Nimiq Pay integration.
 *
 * Nimiq Hub is Nimiq's hosted wallet UI. It runs on Nimiq's own origin in a
 * popup or iframe; it holds the user's keys (Keyguard) and NEVER exposes
 * them to this site. We only ever receive:
 *   - an address the user chose,
 *   - signatures the user explicitly approved,
 *   - transactions the user explicitly approved (hash + sender).
 *
 * The HubApi library is vendored locally (vendor/HubApi.umd.js) — no CDN,
 * so there is no runtime supply chain to compromise.
 *
 * Flows:
 *   login:     challenge (server nonce) -> hub.signMessage -> POST /auth/hub-login
 *   Nimiq Pay: injected Mini App provider sendBasicTransactionWithData -> tx hash
 *   Standalone browser: Hub checkout with (recipient, value in Luna) -> tx hash
 *
 * Mobile fallback: when popups are blocked, @nimiq/hub-api performs a
 * full-page redirect; the result is picked up on page load via
 * checkRedirectResponse() + hub.on(...) listeners (initHubRedirectHandling).
 * Pending login/payment context survives the redirect in sessionStorage.
 */
import { CFG, bytesToHex } from './util.js';
import { inNimiqPay, getNimiqProvider } from './miniapp.js';
import { authChallenge, hubLogin as apiHubLogin } from './api.js';
import { saveSession } from './session.js';

let hub = null;

import { ensureLib } from './vendor-load.js';

async function loadHubApi() { await ensureLib('HubApi'); }

function getHub() {
  if (!hub) {
    const HubApi = window.HubApi;
    if (!HubApi) throw new Error('Wallet bridge failed to load. Please reload the page.');
    hub = new HubApi(CFG.HUB_URL);
  }
  return hub;
}

const LOGIN_KEY = 'nimshop.pendingLogin';
const PAY_KEY = 'nimshop.pendingPay';

function savePending(key, data) {
  try { sessionStorage.setItem(key, JSON.stringify({ ...data, ts: Date.now() })); } catch { /* ignore */ }
}
function loadPending(key, maxAgeMs = 10 * 60 * 1000) {
  try {
    const raw = sessionStorage.getItem(key);
    if (!raw) return null;
    const data = JSON.parse(raw);
    if (!data || !data.ts || Date.now() - data.ts > maxAgeMs) return null;
    return data;
  } catch { return null; }
}
function clearPending(key) {
  try { sessionStorage.removeItem(key); } catch { /* ignore */ }
}

export function friendlyHubError(err) {
  const msg = (err && (err.message || String(err))) || '';
  if (/cancel|rejected|abort|closed/i.test(msg)) {
    return 'You cancelled the wallet request. Nothing was signed or sent.';
  }
  if (/popup/i.test(msg)) {
    return 'Your browser blocked the wallet window. Allow popups for this site and try again.';
  }
  return 'The wallet request could not be completed. Please try again.';
}

/* ---------------- Login via Hub signMessage ---------------- */

/**
 * Full wallet login: fetch server challenge, let the user sign it in Hub,
 * verify server-side, store the session JWT.
 * Returns { address } on success. Throws Error with friendly message.
 */
export async function loginWithHub(onProgress) {
  const step = onProgress || (() => {});
  await loadHubApi(); // lazy vendor: parsed during idle, awaited here just in case
  step('challenge');
  const challenge = await authChallenge();

  // Persist so a mobile redirect can finish the flow after the page reload.
  savePending(LOGIN_KEY, { challenge_token: challenge.challenge_token, message: challenge.message });

  step('sign');
  let signed;
  if (inNimiqPay() && getNimiqProvider()) {
    const provider = getNimiqProvider();
    if (provider.connected === false && provider.connect) await provider.connect();
    const accounts = provider.listAccounts ? await provider.listAccounts() : [];
    const address = accounts && accounts[0];
    const signature = provider.sign ? await provider.sign(challenge.message) : null;
    signed = { address, publicKey: signature && signature.publicKey, signature: signature && signature.signature };
  } else {
    const h = getHub();
    signed = await h.signMessage({
      appName: CFG.APP_NAME,
      message: challenge.message,
    });
  }
  clearPending(LOGIN_KEY);
  return finishLogin(challenge.challenge_token, signed);
}

async function finishLogin(challengeToken, signed) {
  // HubApi >= 1.14 returns { signer: address, signerPublicKey, signature }.
  // Older versions returned { address, signer: publicKey, signature }.
  const address = signed.address || signed.signer;
  const publicKey = signed.signerPublicKey || signed.publicKey ||
    (signed.signer instanceof Uint8Array ? signed.signer : undefined);
  const signature = signed.signature;
  if (!address || !publicKey || !signature) {
    throw new Error('The wallet returned an incomplete signature. Please try again.');
  }
  const res = await apiHubLogin({
    challenge_token: challengeToken,
    address,
    public_key: publicKey instanceof Uint8Array ? bytesToHex(publicKey) : String(publicKey),
    signature: signature instanceof Uint8Array ? bytesToHex(signature) : String(signature),
  });
  saveSession(res.token, res.user.nimiq_address || address);
  return { address: res.user.nimiq_address || address };
}

/* ---------------- Nimiq Pay: direct BTC Lightning ---------------- */

export function lightningPaymentURI(invoice) {
  const raw = String(invoice || '').trim();
  if (!/^ln(?:bc|tb|bcrt)[a-z0-9]+$/i.test(raw)) {
    throw new Error('CryptoRefills returned an invalid Lightning invoice.');
  }
  return 'lightning:' + raw;
}

// Keep the quote reference while the host wallet handles the Lightning
// payment. The merchant never receives NIM or BTC in this flow; CryptoRefills is
// the Lightning payee and its webhook is the only fulfillment confirmation.
export function rememberLightningPayment(invoice, context) {
  const uri = lightningPaymentURI(invoice);
  if (context) savePending(PAY_KEY, { ...context, invoice: uri });
  return uri;
}

/* ---------------- Redirect result recovery (mobile) ---------------- */

export function initHubRedirectHandling({ onLogin }) {
  // The redirect result stays recoverable until consumed, so subscribing a
  // few hundred ms after load (idle-time vendor load) is safe.
  (async () => {
    try {
      await loadHubApi();
      const h = getHub();
      const HubApi = window.HubApi;

      h.on(HubApi.RequestType.SIGN_MESSAGE, async (result) => {
        const pending = loadPending(LOGIN_KEY);
        if (!pending) return;
        clearPending(LOGIN_KEY);
        try {
          const r = await finishLogin(pending.challenge_token, result);
          if (onLogin) onLogin(r.address);
        } catch (e) {
          window.dispatchEvent(new CustomEvent('nimshop:hub-error', { detail: { message: friendlyHubError(e) } }));
        }
      }, () => { clearPending(LOGIN_KEY); });

      // Triggers the handlers above if this page load is a redirect return.
      h.checkRedirectResponse();
    } catch { /* hub lib missing: popup flows still work via direct errors */ }
  })();
}
