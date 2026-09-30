/**
 * router.tsx — client-side routing for the static Astro site.
 *
 * Every route is its own static HTML document, so a plain link click used to
 * throw the whole document away (HTML + CSS + JS + hydration + wallet/session
 * bootstrap again). This router intercepts same-origin links, pushes the new
 * URL and swaps ONLY the page component:
 *
 *   • the shell (topbar, nav, tabbar, cart, account menu) stays mounted,
 *   • cart / toast / sheet / session providers stay mounted (cart survives),
 *   • pages are lazy-loaded chunks and are prefetched on hover/focus, so a
 *     navigation normally paints with zero network requests,
 *   • back/forward (popstate) works like a normal site.
 *
 * Anything that is NOT a known route (external links, /api/..., downloads,
 * new-tab / middle clicks) falls through to the browser untouched.
 */
import {
  createContext,
  Fragment,
  lazy,
  Suspense,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useRef,
  useState,
  type ComponentType,
  type ReactNode,
} from 'react';
import type { ShellKey } from '../components/shell/SiteShell';
import { siteName } from './config';
import { onLangChange, t as i18nT, useT } from '../i18n';
import { pagePath } from './asset';

/* -------------------------------------------------------------------- base */

/** Astro `base` without trailing slash ('' at the root, '/nimiqshop' on GitHub
 *  Pages). Routes are matched WITHOUT it and pushed to history WITH it. */
const BASE = String(import.meta.env.BASE_URL || '/').replace(/\/+$/, '');

/** '/nimiqshop/orders' → '/orders'. Paths that are not under the base (e.g. a
 *  raw '/orders' link) are returned unchanged. */
export function stripBase(pathname: string): string {
  const s = String(pathname || '/');
  if (!BASE) return s;
  if (s === BASE) return '/';
  if (s.startsWith(BASE + '/')) return s.slice(BASE.length) || '/';
  return s;
}

/* ------------------------------------------------------------------ routes */

/** Each entry points at the page's CONTENT component (not the AppRoot wrapper —
 *  the shell + providers must stay mounted across navigations). */
const imports: Record<string, () => Promise<unknown>> = {
  '/': () => import('../components/home/HomePage'),
  '/cart': () => import('../components/cart/CartPage'),
  '/product': () => import('../components/product/ProductPage'),
  '/orders': () => import('../components/orders/OrdersPage'),
  '/order': () => import('../components/order/OrderPage'),
  '/profile': () => import('../components/profile/ProfilePage'),
  '/activity': () => import('../components/activity/ActivityPage'),
  '/support': () => import('../components/support/SupportPage'),
  '/track': () => import('../components/track/TrackPage'),
  '/admin': () => import('../components/admin/AdminPage'),
  '/cashback': () => import('../components/cashback/CashbackPage'),
  '/plant-trees': () => import('../components/trees/PlantTreesPage'),
};

const PAGES: Record<string, ComponentType> = {
  '/': lazy(() => imports['/']().then((m: any) => ({ default: m.HomePage }))),
  '/cart': lazy(() => imports['/cart']().then((m: any) => ({ default: m.CartView }))),
  '/product': lazy(() => imports['/product']().then((m: any) => ({ default: m.ProductPage }))),
  '/orders': lazy(() => imports['/orders']().then((m: any) => ({ default: m.OrdersView }))),
  '/order': lazy(() => imports['/order']().then((m: any) => ({ default: m.OrderView }))),
  '/profile': lazy(() => imports['/profile']().then((m: any) => ({ default: m.ProfileView }))),
  '/activity': lazy(() => imports['/activity']().then((m: any) => ({ default: m.ActivityView }))),
  '/support': lazy(() => imports['/support']().then((m: any) => ({ default: m.SupportView }))),
  '/track': lazy(() => imports['/track']().then((m: any) => ({ default: m.TrackView }))),
  '/admin': lazy(() => imports['/admin']().then((m: any) => ({ default: m.AdminContent }))),
  '/cashback': lazy(() => imports['/cashback']().then((m: any) => ({ default: m.CashbackView }))),
  '/plant-trees': lazy(() => imports['/plant-trees']().then((m: any) => ({ default: m.PlantTreesView }))),
};

// `titleKey` points at a pageTitle.* entry — the tab title follows the
// language switcher like every other string on the page (resolve it with t()
// and interpolate the brand from config, which differs per deployment).
export type Route = { path: string; key: ShellKey; titleKey: string; comp: ComponentType };

const SITE = siteName(); // brand from config.js (default shop.nimiqbase.com)

export const ROUTES: Route[] = [
  { path: '/', key: 'shop', titleKey: 'pageTitle.shop', comp: PAGES['/'] },
  { path: '/cart', key: 'shop', titleKey: 'pageTitle.cart', comp: PAGES['/cart'] },
  { path: '/product', key: 'product', titleKey: 'pageTitle.product', comp: PAGES['/product'] },
  { path: '/orders', key: 'orders', titleKey: 'pageTitle.orders', comp: PAGES['/orders'] },
  { path: '/order', key: 'order', titleKey: 'pageTitle.order', comp: PAGES['/order'] },
  { path: '/profile', key: 'profile', titleKey: 'pageTitle.profile', comp: PAGES['/profile'] },
  { path: '/activity', key: 'activity', titleKey: 'pageTitle.activity', comp: PAGES['/activity'] },
  { path: '/support', key: 'support', titleKey: 'pageTitle.support', comp: PAGES['/support'] },
  { path: '/track', key: 'track', titleKey: 'pageTitle.track', comp: PAGES['/track'] },
  { path: '/admin', key: 'none', titleKey: 'pageTitle.admin', comp: PAGES['/admin'] },
  { path: '/cashback', key: 'cashback', titleKey: 'pageTitle.cashback', comp: PAGES['/cashback'] },
  { path: '/plant-trees', key: 'plant-trees', titleKey: 'pageTitle.plantTrees', comp: PAGES['/plant-trees'] },
];

/** Fallback path per shell key — only used during SSR (no window there). */
const KEY_PATH: Record<ShellKey, string> = {
  shop: '/',
  activity: '/activity',
  orders: '/orders',
  support: '/support',
  product: '/product',
  profile: '/profile',
  track: '/track',
  order: '/order',
  cashback: '/cashback',
  'plant-trees': '/plant-trees',
  none: '/admin',
};

export function normalizePath(p: string): string {
  let s = stripBase(String(p || '/'));
  const q = s.indexOf('?');
  if (q >= 0) s = s.slice(0, q);
  const h = s.indexOf('#');
  if (h >= 0) s = s.slice(0, h);
  s = s.replace(/\/index\.html?$/i, '/');
  if (s.length > 1 && s.endsWith('/')) s = s.slice(0, -1);
  return s || '/';
}

export function routeFor(pathname: string): Route | null {
  const p = normalizePath(pathname);
  return ROUTES.find((r) => r.path === p) || null;
}

/** Warm up a route's JS chunk (called on hover / focus, so clicks are instant). */
const warmed = new Set<string>();
export function prefetchRoute(pathname: string): void {
  const p = normalizePath(pathname);
  const load = imports[p];
  if (!load || warmed.has(p)) return;
  warmed.add(p);
  void load().catch(() => {
    warmed.delete(p); // allow a later hover to retry after connectivity returns
  });
}

/** Warm every route chunk once the page is idle. Pages that are not in the main
 *  nav (/profile, /track, /admin, /order) are only reachable from a
 *  menu or a feed row, so nothing would prefetch them on hover — without this
 *  their first open shows the skeleton while the chunk downloads. */
let warming = false;
export function warmAllRoutes(): void {
  if (warming || typeof window === 'undefined') return;
  warming = true;
  try {
    const conn = (navigator as any)?.connection;
    if (conn && (conn.saveData || /2g/.test(String(conn.effectiveType || '')))) return;
  } catch { /* no Network Information API */ }
  const start = () => ROUTES.forEach((r, i) => setTimeout(() => prefetchRoute(r.path), i * 220));
  const idle = (window as any).requestIdleCallback;
  if (typeof idle === 'function') idle(() => start(), { timeout: 2000 });
  else setTimeout(start, 1200);
}

/* ------------------------------------------------------------------- state */

type Loc = { path: string; search: string; hash: string };

function readLoc(): Loc {
  if (typeof window === 'undefined') return { path: '/', search: '', hash: '' };
  return {
    path: normalizePath(window.location.pathname),
    search: window.location.search || '',
    hash: window.location.hash || '',
  };
}

type RouterValue = {
  loc: Loc;
  key: ShellKey | null;
  navigate: (to: string, opts?: { replace?: boolean }) => void;
  prefetch: (to: string) => void;
};

const RouterCtx = createContext<RouterValue>({
  loc: { path: '/', search: '', hash: '' },
  key: null,
  navigate: () => {},
  prefetch: () => {},
});

export function useRouter(): RouterValue {
  return useContext(RouterCtx);
}

/** Active nav key from the router (`null` when there is no router above). */
export function useRouteKey(): ShellKey | null {
  return useContext(RouterCtx).key;
}

/* ----------------------------------------------------------------- outlet */

function RouteFallback() {
  return (
    <div className="container" style={{ paddingTop: 24, paddingBottom: 40 }} aria-busy="true">
      <div className="skeleton-card" style={{ height: 140, borderRadius: 14, background: 'var(--surface-2)' }} />
      <div className="skeleton-card" style={{ height: 90, marginTop: 14, borderRadius: 14, background: 'var(--surface-2)' }} />
    </div>
  );
}

/** Designed dead-end for unknown routes — never a blank screen or silent Home. */
function NotFound() {
  const { t } = useT();
  return (
    <div className="container" style={{ paddingTop: 44, paddingBottom: 64, textAlign: 'center' }}>
      <div style={{ fontSize: '3rem', fontWeight: 800, letterSpacing: '-0.02em' }}>404</div>
      <div className="strong mt-1">{t('stat.notFoundTitle')}</div>
      <div className="small muted mt-1">{t('stat.notFoundText')}</div>
      <div style={{ display: 'flex', gap: '8px', justifyContent: 'center', marginTop: 18, flexWrap: 'wrap' }}>
        <a className="btn btn-gold" href={pagePath('/')}>{t('stat.backToShop')}</a>
        <a className="btn btn-ghost" href={pagePath('/support')}>{t('stat.getHelp')}</a>
      </div>
    </div>
  );
}

export function Router({
  initialKey,
  initialPath,
  shell,
  initialPage,
  wrap,
}: {
  initialKey: ShellKey;
  /** Only needed for SSR; on the client the path comes from window.location. */
  initialPath?: string;
  /** Persistent chrome (top bar + tab bar). Never remounts. */
  shell: ReactNode;
  /** The page that this document was server-rendered with. */
  initialPage: ReactNode;
  /** Providers rendered here share routing with dialogs as well as pages. */
  wrap?: (content: ReactNode) => ReactNode;
}) {
  const ssrPath = normalizePath(initialPath || KEY_PATH[initialKey] || '/');
  const initialNorm = useMemo(
    () => (typeof window === 'undefined' ? ssrPath : normalizePath(window.location.pathname)),
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [],
  );

  const [loc, setLoc] = useState<Loc>(() => (typeof window === 'undefined' ? { path: ssrPath, search: '', hash: '' } : readLoc()));

  const navigate = useCallback((to: string, opts?: { replace?: boolean }) => {
    if (typeof window === 'undefined') return;
    const url = new URL(to, window.location.href);
    // external target or not one of our routes → let the browser handle it
    if (url.origin !== window.location.origin || !routeFor(url.pathname)) {
      // unknown in-app path without the base → keep the user inside the site
      if (url.origin === window.location.origin && BASE && !url.pathname.startsWith(BASE + '/') && url.pathname !== BASE) {
        window.location.href = pagePath(url.pathname) + url.search + url.hash;
        return;
      }
      window.location.href = url.href;
      return;
    }
    const next: Loc = { path: normalizePath(url.pathname), search: url.search || '', hash: url.hash || '' };
    const cur = readLoc();
    if (next.path === cur.path && next.search === cur.search) {
      // same page, maybe a different #section
      if (next.hash && next.hash !== cur.hash) {
        window.history.pushState(null, '', pagePath(next.path) + next.search + next.hash);
        setLoc(next);
      }
      return;
    }
    const dest = pagePath(next.path) + url.search + (next.hash || '');
    if (opts?.replace) window.history.replaceState(window.history.state, '', dest);
    else window.history.pushState(null, '', dest);
    setLoc(next);
  }, []);

  /* link interception + browser back/forward + hover prefetch */
  useEffect(() => {
    const isModified = (e: MouseEvent) => e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey;

    const onClick = (e: MouseEvent) => {
      if (e.defaultPrevented || isModified(e)) return;
      const el = e.target as Element | null;
      const a = el?.closest?.('a[href]') as HTMLAnchorElement | null;
      if (!a) return;
      if (a.hasAttribute('download')) return;
      const target = a.getAttribute('target');
      if (target && target !== '_self') return;
      const href = a.getAttribute('href') || '';
      if (!href || href.startsWith('#') || href.startsWith('mailto:') || href.startsWith('tel:')) return;
      let url: URL;
      try {
        url = new URL(a.href, window.location.href);
      } catch {
        return;
      }
      if (url.origin !== window.location.origin) return;
      if (!routeFor(url.pathname)) return; // /api/..., files, unknown paths → normal load
      e.preventDefault();
      navigate(url.pathname + url.search + url.hash);
    };

    const onPop = () => setLoc(readLoc());

    const onHover = (e: Event) => {
      const el = e.target as Element | null;
      const a = el?.closest?.('a[href]') as HTMLAnchorElement | null;
      if (!a) return;
      if (a.getAttribute('target') && a.getAttribute('target') !== '_self') return;
      let url: URL;
      try {
        url = new URL(a.href, window.location.href);
      } catch {
        return;
      }
      if (url.origin !== window.location.origin) return;
      prefetchRoute(url.pathname);
    };

    document.addEventListener('click', onClick);
    window.addEventListener('popstate', onPop);
    document.addEventListener('pointerover', onHover, { passive: true });
    document.addEventListener('focusin', onHover);
    return () => {
      document.removeEventListener('click', onClick);
      window.removeEventListener('popstate', onPop);
      document.removeEventListener('pointerover', onHover);
      document.removeEventListener('focusin', onHover);
    };
  }, [navigate]);

  /* idle prefetch: every route chunk in the background → instant navigation */
  useEffect(() => {
    warmAllRoutes();
  }, []);

  /* title + scroll position for the new page. The title is re-applied on a
     language change through the module-level subscription — the shell above
     this router is not necessarily inside the I18nProvider, so a React
     dependency on the language would never fire. */
  const first = useRef(true);
  useEffect(() => {
    const applyTitle = () => {
      const r = routeFor(loc.path);
      if (r && typeof document !== 'undefined') document.title = i18nT(r.titleKey, { site: SITE });
    };
    applyTitle();
    const off = onLangChange(applyTitle);
    if (first.current) {
      first.current = false;
      return off;
    }
    if (!loc.hash && typeof window !== 'undefined') window.scrollTo(0, 0);
    return off;
  }, [loc.path, loc.search, loc.hash]);

  const route = routeFor(loc.path);
  const onInitial = loc.path === initialNorm;
  // Unknown routes (direct load OR client navigation) get a designed 404 —
  // never a silent Home and never a blank content area.
  let pageNode: ReactNode;
  if (!route) pageNode = <NotFound />;
  else if (onInitial) pageNode = initialPage;
  else {
    const Comp = route.comp;
    pageNode = <Comp />;
  }

  const value = useMemo<RouterValue>(
    () => ({ loc, key: route?.key ?? initialKey, navigate, prefetch: prefetchRoute }),
    [loc, route, initialKey, navigate],
  );

  const content = <>
      {shell}
      <div id="page-content" tabIndex={-1}>
      <Suspense fallback={<RouteFallback />}>
        {/* keyed by path+query so a new ?id=… remounts the page and re-reads it */}
        <Fragment key={loc.path + loc.search}>{pageNode}</Fragment>
        <RouteFocus />
      </Suspense>
      </div>
    </>;
  return (
    <RouterCtx.Provider value={value}>
      {wrap ? wrap(content) : content}
    </RouterCtx.Provider>
  );
}

/** Run after the lazy page has committed, so its anchor actually exists. */
function RouteFocus() {
  const { loc } = useRouter();
  const initial = useRef(true);
  useEffect(() => {
    if (loc.hash) {
      let id = loc.hash.slice(1);
      try { id = decodeURIComponent(id); } catch { /* malformed fragment */ }
      document.getElementById(id)?.scrollIntoView();
    } else if (!initial.current) document.getElementById('page-content')?.focus({ preventScroll: true });
    initial.current = false;
  }, [loc.path, loc.search, loc.hash]);
  return null;
}
