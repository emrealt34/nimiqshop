/**
 * vendorLoad.ts — lazy, idle-time loader for the two vendored libraries
 * (HubApi.umd.js, qrcode.js). Ported verbatim from vendor-load.js.
 */
import { asset } from './asset';

// asset(): on GitHub Pages the site lives under /nimiqshop/, so a bare
// '/vendor/…' would 404 (breaking wallet login and payment QR codes).
const SRC: Record<string, string> = {
  HubApi: asset('/vendor/HubApi.umd.js'),
  qrcode: asset('/vendor/qrcode.js'),
};
const pending = new Map<string, Promise<void>>();

export function ensureLib(name: string): Promise<void> {
  if (typeof window === 'undefined') return Promise.reject(new Error('no window'));
  const globalName = name === 'HubApi' ? 'HubApi' : 'qrcode';
  if ((window as unknown as Record<string, unknown>)[globalName]) return Promise.resolve();
  if (pending.has(name)) return pending.get(name) as Promise<void>;
  const p = new Promise<void>((resolve, reject) => {
    const s = document.createElement('script');
    s.src = SRC[name];
    s.async = true;
    s.onload = () => resolve();
    s.onerror = () => {
      pending.delete(name);
      reject(new Error(name + ' failed to load'));
    };
    document.head.appendChild(s);
  });
  pending.set(name, p);
  return p;
}

if (typeof window !== 'undefined') {
  // Warm ONLY the wallet SDK after the visitor first interacts with the page
  // (pointerdown / keydown / touchstart), never during initial page load — an
  // eager idle load during startup costs 27 KB of unused JS in Lighthouse.
  const warm = () => {
    window.removeEventListener('pointerdown', warm);
    window.removeEventListener('keydown', warm);
    window.removeEventListener('touchstart', warm);
    ensureLib('HubApi').catch(() => {});
  };
  window.addEventListener('pointerdown', warm, { once: true, passive: true });
  window.addEventListener('keydown', warm, { once: true });
  window.addEventListener('touchstart', warm, { once: true, passive: true });
}
