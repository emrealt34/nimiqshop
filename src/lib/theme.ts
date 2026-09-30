/** Theme handling — siyah tema as a real feature. Choice persists across reloads. */
export type Theme = 'light' | 'dark';

const KEY = 'nimshop.theme';

/** Read current theme from DOM or storage. */
export function getTheme(): Theme {
  if (typeof document !== 'undefined') {
    const v = document.documentElement.getAttribute('data-theme');
    if (v === 'dark' || v === 'light') return v;
  }
  try {
    const s = localStorage.getItem(KEY);
    if (s === 'dark' || s === 'light') return s as Theme;
  } catch {}
  // fallback: system preference
  if (typeof window !== 'undefined' && window.matchMedia?.('(prefers-color-scheme: dark)').matches) return 'dark';
  return 'light';
}

export function setTheme(t: Theme): void {
  try { localStorage.setItem(KEY, t); } catch {}
  if (typeof document !== 'undefined') {
    document.documentElement.setAttribute('data-theme', t);
    // update color-scheme & theme-color for browser UI
    document.documentElement.style.colorScheme = t;
    const mc = document.querySelector('meta[name="theme-color"]');
    if (mc) mc.setAttribute('content', t === 'dark' ? '#1a1c20' : '#E7DAC0');
    try {
      window.dispatchEvent(new CustomEvent('nimshop:theme', { detail: t }));
    } catch {}
  }
}

export function toggleTheme(): Theme {
  const n: Theme = getTheme() === 'dark' ? 'light' : 'dark';
  setTheme(n);
  return n;
}


