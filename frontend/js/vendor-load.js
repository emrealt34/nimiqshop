/* vendor-load.js — lazy, idle-time loader for the two vendored libraries.
 *
 * HubApi.umd.js (27KB) and qrcode.js (56KB) used to load as <script defer> in
 * every page's head: they executed inside the pre-render window, and HubApi
 * forced a synchronous layout (Lighthouse "forced reflow, 34ms"). Neither is
 * needed to RENDER anything — HubApi is a login-flow dependency, qrcode a
 * QR-button dependency. They now load after first paint (requestIdleCallback
 * warm-up) and every consumer awaits ensureLib() as a safety net.
 */
const SRC = {
  HubApi: '/vendor/HubApi.umd.js',
  qrcode: '/vendor/qrcode.js',
};
const pending = new Map();

export function ensureLib(name) {
  if (typeof window === 'undefined') return Promise.reject(new Error('no window'));
  const globalName = name === 'HubApi' ? 'HubApi' : 'qrcode';
  if (window[globalName]) return Promise.resolve();
  if (pending.has(name)) return pending.get(name);
  const p = new Promise((resolve, reject) => {
    const s = document.createElement('script');
    s.src = SRC[name];
    s.async = true;
    s.onload = () => resolve();
    s.onerror = () => { pending.delete(name); reject(new Error(name + ' failed to load')); };
    document.head.appendChild(s);
  });
  pending.set(name, p);
  return p;
}

/* Warm both after rendering settles so a login tap / QR tap finds them
 * already parsed (and their layout work happened during idle, not FCP). */
if (typeof window !== 'undefined') {
  const warm = () => { ensureLib('HubApi').catch(() => {}); ensureLib('qrcode').catch(() => {}); };
  if ('requestIdleCallback' in window) requestIdleCallback(warm, { timeout: 2500 });
  else setTimeout(warm, 1200);
}
