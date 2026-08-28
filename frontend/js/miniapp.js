/* miniapp.js — Nimiq Pay mini-app compatibility layer.
 *
 * nim.shop is a static web app, so it already runs inside Nimiq Pay's WebView
 * with no changes. This layer adds the mini-app niceties:
 *   - detect that we are running inside Nimiq Pay,
 *   - read the user's Nimiq Pay language (instead of navigator.language),
 *   - expose the injected Nimiq provider (window.nimiq), which the official
 *     @nimiq/mini-app-sdk's init() helper also returns, and
 *   - build the deeplink that opens the shop inside Nimiq Pay.
 *
 * For the fully-typed wallet flows (listAccounts / onRequestSignMessage /
 * send via the provider) you add the official `@nimiq/mini-app-sdk` npm package;
 * its init() resolves to the same provider exposed by getNimiqProvider() here.
 *
 * Docs: https://nimiq.dev/mini-apps/  Hub API: https://nimiq.dev/hub/api-reference
 */

export function inNimiqPay() {
  return typeof window !== 'undefined' && !!(window.nimiqPay);
}

// The injected Nimiq provider inside Nimiq Pay (null outside it). This is what
// the mini-app SDK's init() resolves to; use it for wallet reads and native
// sendBasicTransaction / sendBasicTransactionWithData approvals.
export function getNimiqProvider() {
  return (typeof window !== 'undefined' && window.nimiq) || null;
}

// Open this shop inside Nimiq Pay via the official native deeplink.
// The mini-app docs specify: nimiqpay://miniapp?url=your-app.com
export function openInNimiqPay(onUnavailable) {
  const target = location.origin + location.pathname + location.search;
  const deeplink = 'nimiqpay://miniapp?url=' + encodeURIComponent(target);
  const ua = navigator.userAgent || '';
  const isIOS = /iPad|iPhone|iPod/.test(ua) || (navigator.platform === 'MacIntel' && navigator.maxTouchPoints > 1);
  const isAndroid = /Android/i.test(ua);
  let leftPage = false;
  const markLeft = () => { leftPage = true; };
  window.addEventListener('pagehide', markLeft, { once: true });
  window.location.href = deeplink;
  window.setTimeout(() => {
    window.removeEventListener('pagehide', markLeft);
    if (!leftPage && typeof onUnavailable === 'function') {
      onUnavailable({ isIOS, isAndroid, deeplink });
    }
  }, 1400);
  return deeplink;
}

export const NIMIQ_PAY_IOS_URL = 'https://apps.apple.com/app/nimiq-pay/id6471844738';
export const NIMIQ_PAY_ANDROID_URL = 'https://play.google.com/store/apps/details?id=com.nimiq.pay';

/* ---- Platform detection (mobile browsers) -------------------------------- */
export function detectMobilePlatform() {
  if (typeof navigator === 'undefined') return null;
  const ua = navigator.userAgent;
  if (/Android/i.test(ua)) return 'android';
  if (/iPad|iPhone|iPod/i.test(ua)) return 'ios';
  // iPadOS 13+ reports a desktop UA but exposes touch points.
  if (/Macintosh/i.test(ua) && typeof navigator.maxTouchPoints === 'number' && navigator.maxTouchPoints > 1) return 'ios';
  return null;
}
