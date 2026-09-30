/**
 * miniapp.ts — Nimiq Pay Mini App layer.
 * Official SDK: `init()` waits for the injected `window.nimiq` provider.
 */
import { useEffect, useState } from 'react';
import { init as initMiniAppSdk, getHostLanguage } from '@nimiq/mini-app-sdk';

export function inNimiqPay(): boolean {
  if (typeof window === 'undefined') return false;
  const w = window as unknown as { nimiqPay?: unknown; nimiq?: unknown };
  if (w.nimiqPay || w.nimiq) return true;
  try {
    if (sessionStorage.getItem('nimshop_in_pay') === '1') return true;
  } catch {
    /* private mode */
  }
  const ua = typeof navigator !== 'undefined' ? navigator.userAgent : '';
  if (/NimiqPay|nimiq-pay/i.test(ua)) return true;
  if (typeof document !== 'undefined' && document.documentElement.classList.contains('in-nimiq-pay')) return true;
  return false;
}

/**
 * Hydration-safe Nimiq-Pay detection. The static build renders OUTSIDE Pay, so
 * the server HTML and the FIRST client render must agree. Reading
 * `inNimiqPay()` during render would mismatch the moment the host injects its
 * provider, throwing React hydration errors (#418/#422) exactly inside Pay.
 * This hook starts `false` (matching SSR) and flips to the real value after
 * mount, so Pay-specific UI appears one beat late but hydration never throws.
 *
 * Use it in any component whose FIRST PAINT depends on the Pay environment.
 * Code that only runs after mount (effects, event handlers, sheets opened on
 * click) may keep calling `inNimiqPay()` directly.
 */
export function useInNimiqPay(): boolean {
  const [inPay, setInPay] = useState(false);
  useEffect(() => {
    setInPay(inNimiqPay());
  }, []);
  return inPay;
}

/** Mark the embedded app without disabling the buyer's pinch zoom. */
export function applyNimiqPayChrome(): void {
  if (typeof document === 'undefined' || !inNimiqPay()) return;
  document.documentElement.classList.add('in-nimiq-pay');
  const m = document.querySelector('meta[name="viewport"]');
  if (m) {
    m.setAttribute(
      'content',
      'width=device-width, initial-scale=1, viewport-fit=cover'
    );
  }
}

let sdkReady: Promise<unknown | null> | null = null;
// The official SDK returns the provider from init(). Do not assume that every
// host also mirrors that object on window.nimiq: the returned provider is the
// authority, especially for staking methods.
let sdkProvider: unknown = null;

/**
 * Official Mini App SDK entry. Outside Nimiq Pay this resolves immediately
 * with null (no 2.5s hang on first paint). Inside Pay it waits for the
 * injected provider, then `listAccounts` / `sign` / staking methods work.
 */
export function initNimiqMiniApp(): Promise<unknown | null> {
  if (typeof window === 'undefined') return Promise.resolve(null);
  if (sdkReady) return sdkReady;
  const already = (window as unknown as { nimiq?: unknown }).nimiq;
  if (already) {
    sdkProvider = already;
    sdkReady = Promise.resolve(already);
    return sdkReady;
  }
  if (!inNimiqPay()) {
    sdkReady = Promise.resolve(null);
    return sdkReady;
  }
  sdkReady = initMiniAppSdk({ timeout: 8000 })
    .then((p) => {
      sdkProvider = p || (window as unknown as { nimiq?: unknown }).nimiq || null;
      return sdkProvider;
    })
    .catch(() => {
      sdkProvider = (window as unknown as { nimiq?: unknown }).nimiq || null;
      return sdkProvider;
    });
  return sdkReady;
}

export function getNimiqProvider(): unknown {
  if (typeof window === 'undefined') return null;
  return (window as unknown as { nimiq?: unknown }).nimiq || sdkProvider || null;
}

export function hostLanguage(): string {
  try {
    return getHostLanguage() || (typeof navigator !== 'undefined' ? navigator.language.split('-')[0] : 'en') || 'en';
  } catch {
    return 'en';
  }
}

export function openInNimiqPay(onUnavailable?: (info: { isIOS: boolean; isAndroid: boolean; deeplink: string }) => void): string {
  const target = location.origin + location.pathname + location.search;
  const deeplink = 'nimiqpay://miniapp?url=' + encodeURIComponent(target);
  const platform = detectMobilePlatform();
  const isIOS = platform === 'ios';
  const isAndroid = platform === 'android';
  // Desktop has no Nimiq Pay handler — show App Store / Play Store at once.
  // On a phone, still fire the deeplink so an installed app can open, but do
  // not wait: the install sheet must appear immediately if we stay on the page.
  if (platform) {
    try {
      window.location.href = deeplink;
    } catch {
      /* ignore */
    }
  }
  onUnavailable?.({ isIOS, isAndroid, deeplink });
  return deeplink;
}

export const NIMIQ_PAY_IOS_URL = 'https://apps.apple.com/app/nimiq-pay/id6471844738';
export const NIMIQ_PAY_ANDROID_URL = 'https://play.google.com/store/apps/details?id=com.nimiq.pay';

export function detectMobilePlatform(): 'android' | 'ios' | null {
  if (typeof navigator === 'undefined') return null;
  const ua = navigator.userAgent;
  if (/Android/i.test(ua)) return 'android';
  if (/iPad|iPhone|iPod/i.test(ua)) return 'ios';
  if (/Macintosh/i.test(ua) && typeof navigator.maxTouchPoints === 'number' && navigator.maxTouchPoints > 1) return 'ios';
  return null;
}
