/** Public file URL that respects Astro `base` (GitHub Pages lives under /nimiqshop/). */
export function asset(path: string): string {
  const base = String(import.meta.env.BASE_URL || '/').replace(/\/$/, '');
  const p = path.startsWith('/') ? path : `/${path}`;
  return `${base}${p}`;
}

export function homeHref(): string {
  const base = String(import.meta.env.BASE_URL || '/');
  return base.endsWith('/') ? base : `${base}/`;
}

/** In-app path that respects `base` (`/orders` → `/nimiqshop/orders` on Pages). */
export function pagePath(p: string): string {
  if (!p || p === '/') return homeHref();
  return asset(p);
}
