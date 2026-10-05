/**
 * SiteShell.tsx — React port of shell.js: top bar, main nav, cart button,
 * language switcher, account chip/menu, mobile tab bar, login sheet,
 * awaiting-payment badge, Hub redirect handling. Rendered once per page
 * (Astro mounts it inside <main> or as a sibling).
 *
 * ALL user-visible strings in this file come from the i18n layer via useT().
 * To change wording edit src/i18n/locales/*.ts — this file never hard-codes
 * a customer-facing label.
 */
import { useEffect, useRef, useState, type ReactNode } from 'react';
import { monogramLetter } from '../ui/UnifiedThumb';
import { createPortal } from 'react-dom';
import { Icon } from '../ui/Icon';
import { Identicon } from '../ui/Identicon';
import { LanguageSwitcher } from '../ui/LanguageSwitcher';
import { useCart } from '../../lib/cartStore';
import { useSheet, useToast } from '../AppProviders';
import { isAuthed, getAddress, signOut, subscribeSession } from '../../lib/session';
import { formatWalletAddress, shortAddr } from '../../lib/format';
import { applyNimiqPayChrome, inNimiqPay, initNimiqMiniApp } from '../../lib/miniapp';
import { NimiqPayInstallDialog } from '../ui/NimiqPayInstallDialog';
import { loginWithHub, loginWithNimiqPay, prefetchHubLogin, initHubRedirectHandling, friendlyHubError } from '../../lib/hub';
import { listQuotes, listOrders } from '../../lib/api';
import { siteName } from '../../lib/config';
import { useRouteKey, useRouter } from '../../lib/router';
import { getTheme, setTheme, type Theme } from '../../lib/theme';
import { useT, t as i18nT } from '../../i18n';
import { asset, homeHref, pagePath } from '../../lib/asset';

export type ShellKey =
  | 'shop'
  | 'activity'
  | 'orders'
  | 'support'
  | 'product'
  | 'profile'
  | 'track'
  | 'order'
  | 'cashback'
  | 'none';

/**
 * Nav data. The `labelKey` points at a translation entry under `nav.*` so the
 * same data renders in 6 languages without duplication.
 */
const NAV: { key: ShellKey; labelKey: 'shop' | 'activity' | 'orders' | 'cashback' | 'support'; href: string; icon: Parameters<typeof Icon>[0]['name'] }[] = [
  { key: 'shop',        labelKey: 'shop',       href: '/',             icon: 'bag' },
  { key: 'activity',    labelKey: 'activity',   href: '/activity',     icon: 'pulse' },
  { key: 'orders',      labelKey: 'orders',     href: '/orders',       icon: 'receipt' },
  { key: 'cashback',    labelKey: 'cashback',   href: '/cashback',     icon: 'spark' },
  // Owner (2026-10-05): the leaderboard lives INSIDE the cashback page — no
  // separate nav tab any more.
  { key: 'support',     labelKey: 'support',    href: '/support',      icon: 'headset' },
];

/* ---------------- Badge counters ---------------- */

/** Count of the buyer's orders/quotes still awaiting payment — red badge. */
function useAwaitingPaymentCount(): number {
  const [count, setCount] = useState(0);
  const inFlight = useRef(false);
  const alive = useRef(true);
  const routeKey = useRouteKey();
  const visible = useRef(true);
  const tick = () => {
    if (inFlight.current) return;
    if (!alive.current || !document.visibilityState || document.visibilityState !== 'visible') return;
    if (!isAuthed()) { setCount(0); return; }
    inFlight.current = true;
    const awaiting = (list: any) =>
      (list || []).filter((x: any) => {
        const st = String(x?.status || x?.quote?.status || '').toLowerCase();
        return st === 'awaiting_payment';
      }).length;
    Promise.all([listOrders().catch(() => []), listQuotes().catch(() => [])])
      .then(([o, q]) => { if (alive.current) setCount(awaiting(o) + awaiting(q)); })
      .catch(() => {})
      .finally(() => { inFlight.current = false; });
  };
  const onVis = () => {
    visible.current = document.visibilityState === 'visible';
    if (visible.current) tick();
  };
  useEffect(() => {
    alive.current = true;
    document.addEventListener('visibilitychange', onVis);
    tick();
    const t = setInterval(tick, 20_000);
    return () => {
      alive.current = false;
      document.removeEventListener('visibilitychange', onVis);
      clearInterval(t);
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [routeKey]);
  return count;
}

/* ---------------- Root ---------------- */

export function SiteShell({ activeKey }: { activeKey: ShellKey }) {
  useShellBootstrap();
  const routeKey = useRouteKey();
  const key = routeKey || activeKey;
  const awaiting = useAwaitingPaymentCount();
  return (
    <>
      <TopBar activeKey={key} awaiting={awaiting} />
      <TabBar activeKey={key} awaiting={awaiting} />
    </>
  );
}

/* ---------------- Brand ---------------- */

function Brand() {
  const name = siteName();
  const [logoFailed, setLogoFailed] = useState(false);
  // Break after the FIRST dot ("shop." / "nimiqbase.com"): the two lines are
  // then balanced, instead of a long "shop.nimiqbase." over a lone "com".
  const i = name.indexOf('.');
  const before = i > 0 && i < name.length - 1 ? name.slice(0, i) : null;
  const after = before !== null ? name.slice(i + 1) : null;
  return (
    <a className="brand" href={homeHref()} aria-label={name + ' home'}>
      {logoFailed ? (
        <span className="brand-mono" aria-hidden="true">{monogramLetter(name)}</span>
      ) : (
        <img
          src={asset("/img/brand-icon-50.webp")}
          srcSet={`${asset("/img/brand-icon-50.webp?v=nim-brand-1")} 50w, ${asset("/img/brand-icon-80.webp?v=nim-brand-1")} 80w, ${asset("/img/brand-icon.png?v=nim-brand-1")} 128w`}
          sizes="50px"
          alt=""
          width={50}
          height={50}
          decoding="async"
          fetchPriority="low"
          aria-hidden="true"
          style={{ display: 'block', borderRadius: '6px' }}
          onError={() => setLogoFailed(true)}
        />
      )}
      {/* No runtime re-fit, no wrapping (owner, 2026-10-05: "site adının
          küçülüp büyülmesini doğru bulmuyorum" + "logo tek satırda
          olmalı"): ONE constant type size, ONE line at every width; the
          narrow rails yield controls instead — see fixes.css. */}
      <span id="site-wordmark" title={name}>
        {before !== null ? (
          <>
            <span className="wm-head">{before}<span className="dot">.</span></span>
            <span className="wm-tail">{after}</span>
          </>
        ) : (
          name
        )}
      </span>
    </a>
  );
}

/* ---------------- Cart ---------------- */

function CartButton() {
  const { t } = useT();
  const { count } = useCart();
  const { openSheet } = useSheet();
  return (
    <button
      className="cart-btn"
      aria-label={t('nav.cart')}
      style={{ position: 'relative' }}
      onClick={() => {
        import('../cart/CartSheet').then((m) => m.openCartSheet({ openSheet }));
      }}
    >
      <Icon name="bag" size={20} />
      {count > 0 && <span className="cart-badge">{count}</span>}
    </button>
  );
}

/* ---------------- Theme toggle ---------------- */

function ThemeToggle() {
  const { t } = useT();
  const [theme, setThemeState] = useState<Theme>('light');
  useEffect(() => {
    try {
      const d = document.documentElement.getAttribute('data-theme') as Theme | null;
      if (d === 'dark' || d === 'light') setThemeState(d);
      else setThemeState(getTheme());
    } catch {}
    const onTheme = (e: Event) => {
      const tt = (e as CustomEvent).detail as Theme;
      if (tt === 'dark' || tt === 'light') setThemeState(tt);
    };
    const onStorage = (e: StorageEvent) => {
      if (e.key === 'nimshop.theme' && (e.newValue === 'dark' || e.newValue === 'light')) setThemeState(e.newValue as Theme);
    };
    window.addEventListener('nimshop:theme', onTheme as EventListener);
    window.addEventListener('storage', onStorage);
    const obs = new MutationObserver(() => {
      const v = document.documentElement.getAttribute('data-theme') as Theme | null;
      if (v === 'dark' || v === 'light') setThemeState(v);
    });
    obs.observe(document.documentElement, { attributes: true, attributeFilter: ['data-theme'] });
    return () => {
      window.removeEventListener('nimshop:theme', onTheme as EventListener);
      window.removeEventListener('storage', onStorage);
      obs.disconnect();
    };
  }, []);
  const toggle = () => {
    const next: Theme = theme === 'dark' ? 'light' : 'dark';
    setTheme(next);
    setThemeState(next);
  };
  const dark = theme === 'dark';
  return (
    <button
      className="theme-toggle"
      aria-label={dark ? t('theme.switchToLight') : t('theme.switchToDark')}
      title={dark ? t('theme.light') : t('theme.dark')}
      onClick={toggle}
      type="button"
    >
      <Icon name={dark ? 'sun' : 'moon'} size={18} />
    </button>
  );
}

/* ---------------- Account dropdown ---------------- */

function AccountArea() {
  const { t } = useT();
  const { navigate } = useRouter();
  const [authed, setAuthed] = useState<boolean | null>(null);
  const [open, setOpen] = useState(false);
  const wrapRef = useRef<HTMLDivElement>(null);
  const { openSheet, closeSheet } = useSheet();
  const { toast } = useToast();

  useEffect(() => {
    setAuthed(isAuthed());
    return subscribeSession((d) => setAuthed(d.authed));
  }, []);

  useEffect(() => {
    if (!open) return;
    const onDown = (e: MouseEvent | TouchEvent) => {
      if (!wrapRef.current || !wrapRef.current.contains(e.target as Node)) setOpen(false);
    };
    const onKey = (e: KeyboardEvent) => { if (e.key === 'Escape') setOpen(false); };
    document.addEventListener('mousedown', onDown);
    document.addEventListener('touchstart', onDown);
    document.addEventListener('keydown', onKey);
    return () => {
      document.removeEventListener('mousedown', onDown);
      document.removeEventListener('touchstart', onDown);
      document.removeEventListener('keydown', onKey);
    };
  }, [open]);

  const addr = getAddress();

  if (authed === null) {
    // Pre-hydration placeholder. It mirrors the real button's structure
    // (icon + .btn-label span, same flex/gap) so the reserved box is exactly
    // the button's box: a text-only span measured ~11 px narrower and the
    // whole control cluster jumped right when React swapped it in.
    return (
      <span className="btn btn-gold btn-sm" aria-hidden="true" style={{ visibility: 'hidden', display: 'inline-flex', alignItems: 'center', gap: '6px' }}>
        <Icon name="nimiq" size={18} />
        <span className="btn-label">{t('account.connect')}</span>
        {/* Phones swap to the short label instead of clipping the long one:
            "Connecter le portefeuille" can never fit a 500px row legibly,
            "Connecter" can. The aria-label keeps the full sentence. */}
        <span className="btn-label-short" aria-hidden="true">{t('account.connectShort')}</span>
      </span>
    );
  }

  const doSignOut = () => {
    signOut();
    toast(t('account.signedOutToast'), 'info');
    setOpen(false);
  };

  const inPay = inNimiqPay();

  const menuItem = (iconName: Parameters<typeof Icon>[0]['name'], label: string, fn: () => void, danger = false) => (
    <button
      key={label}
      className={danger ? 'danger' : ''}
      onClick={() => { setOpen(false); fn(); }}
      style={{ display: 'flex', alignItems: 'center', gap: '10px', width: '100%', background: 'none', border: '0', padding: '10px 14px', cursor: 'pointer', textAlign: 'left', fontSize: '0.95rem', color: 'inherit' }}
    >
      <Icon name={iconName} size={17} />
      <span>{label}</span>
    </button>
  );

  if (!authed) {
    return (
      <button
        className="btn btn-gold btn-sm"
        aria-label={t('account.connect')}
        onClick={() => openLoginSheet({ openSheet, closeSheet, toast })}
        style={{ display: 'inline-flex', alignItems: 'center', gap: '6px' }}
      >
        <Icon name="nimiq" size={18} />
        <span className="btn-label">{t('account.connect')}</span>
        <span className="btn-label-short" aria-hidden="true">{t('account.connectShort')}</span>
      </button>
    );
  }

  return (
    <div className="acct-wrap" ref={wrapRef} style={{ position: 'relative' }}>
      <button className="acct-btn" aria-label={t('account.accountMenu')} aria-haspopup="menu" aria-expanded={open} onClick={() => setOpen((o) => !o)} style={{ display: 'inline-flex', alignItems: 'center', gap: '6px' }}>
        <Identicon address={addr} className="identicon" size={22} />
        <span className="addr">{shortAddr(addr)}</span>
        <Icon name="chevron" size={14} />
      </button>
      {open && (
        <div className="acct-menu open" role="menu" style={{ position: 'absolute', right: 0, top: 'calc(100% + 6px)', zIndex: 50 }}>
          <div className="menu-head" style={{ padding: '12px 14px', display: 'flex', gap: '10px', alignItems: 'center' }}>
            <Identicon address={addr} className="identicon" size={32} />
            <div style={{ minWidth: 0 }}>
              <div className="strong small truncate" title={addr}>{shortAddr(addr, 12, 8)}</div>
              <div className="xs faint">{t('account.connectedVia', { hub: inPay ? t('account.pay') : t('account.hub') })}</div>
            </div>
          </div>
          {menuItem('user',     t('account.accountLimits'),   () => navigate('/profile'))}
          {menuItem('receipt',  t('account.myOrders'),        () => navigate('/orders'))}
          {menuItem('spark',    t('account.cashbackStaking'), () => navigate('/cashback'))}
          {menuItem('pulse',    t('account.publicActivity'),  () => navigate('/activity'))}
          {menuItem('headset',  t('nav.support'),  () => navigate('/support'))}
          <button
            className="danger"
            onClick={doSignOut}
            style={{ display: 'flex', alignItems: 'center', gap: '10px', width: '100%', background: 'none', border: '0', padding: '10px 14px', cursor: 'pointer', textAlign: 'left', fontSize: '0.95rem' }}
          >
            <Icon name="logout" size={17} />
            <span>{t('account.signOut')}</span>
          </button>
        </div>
      )}
    </div>
  );
}

/* ---------------- Top bar ---------------- */

function TopBar({ activeKey, awaiting }: { activeKey: ShellKey; awaiting?: number }) {
  const { t } = useT();
  return (
    <header className="topbar">
      {/* data-fit-row: the runtime re-fit (src/lib/fitText.ts) keeps the six
          links inside this row and tightens the row itself if a language needs
          more than the labels can shrink to. */}
      <div className="topbar-inner" data-fit-row>
        <Brand />
        <nav className="mainnav" aria-label={t('nav.primaryNav')} data-fit-group data-fit-min="10.5">
          {NAV.map((n) => (
            /* The badge hangs from a wrapper, not from the link: the link
               clips (overflow:hidden, the ellipsis budget) and would cut the
               absolutely-positioned badge down to a sliver of its corner. The
               wrapper hugs the link, so the badge sits at the same corner —
               just outside the clipped box. */
            <span className="nav-item-wrap" key={n.key}>
              <a
                href={pagePath(n.href)}
                className={n.key === activeKey ? 'active' : ''}
                aria-current={n.key === activeKey ? 'page' : undefined}
                data-fit-item
              >
                {t(`nav.${n.labelKey}` as any)}
              </a>
              {n.key === 'orders'  && (awaiting || 0) > 0 && <span className="nav-badge">{awaiting}</span>}
            </span>
          ))}
        </nav>
        {/* Absorbs the free space whenever the nav is hidden (below its
            breakpoint), pinning the controls to the right edge. Without it
            the whole control cluster sat left-of-centre with a dead empty
            half-bar on every width where the nav is not rendered. */}
        <div className="topbar-spacer" aria-hidden="true" />
        <ThemeToggle />
        <LanguageSwitcher />
        <CartButton />
        <AccountArea />
      </div>
    </header>
  );
}

/* ---------------- Mobile tab bar ---------------- */

function TabBar({ activeKey, awaiting }: { activeKey: ShellKey; awaiting?: number }) {
  const { t } = useT();
  const [host, setHost] = useState<HTMLElement | null>(null);
  useEffect(() => {
    let el = document.getElementById('tabbar-root');
    if (!el) {
      el = document.createElement('nav');
      el.id = 'tabbar-root';
      document.body.appendChild(el);
    }
    setHost(el);
    return () => { el?.remove(); };
  }, []);

  // --tabbar-h is the space the page keeps free above the bar (main padding,
  // footer margin, toasts). A long label ("Planter des arbres") may wrap to a
  // second line, so the bar's real height is measured instead of assumed.
  const barRef = useRef<HTMLElement | null>(null);
  useEffect(() => {
    const bar = barRef.current;
    if (!bar || typeof ResizeObserver === 'undefined') return;
    const root = document.documentElement;
    const apply = () => {
      if (getComputedStyle(bar).display === 'none') { root.style.removeProperty('--tabbar-h'); return; }
      // Height without the safe-area inset — every consumer adds var(--sab) itself.
      const sab = Math.max(0, parseFloat(getComputedStyle(bar).paddingBottom) - 6);
      const h = Math.ceil(bar.getBoundingClientRect().height - sab);
      if (h > 0) root.style.setProperty('--tabbar-h', `${Math.max(60, h)}px`);
    };
    apply();
    const ro = new ResizeObserver(apply);
    ro.observe(bar);
    window.addEventListener('resize', apply);
    return () => { ro.disconnect(); window.removeEventListener('resize', apply); root.style.removeProperty('--tabbar-h'); };
  }, [host]);

  const bar = (
    <nav className="tabbar" ref={barRef} aria-label={t('nav.primaryNav')}>
      {NAV.map((n) => (
        <a key={n.key} href={pagePath(n.href)} className={n.key === activeKey ? 'active' : ''} aria-current={n.key === activeKey ? 'page' : undefined}>
          <span className="tab-ico">
            <Icon name={n.icon} size={22} />
            {n.key === 'orders'  && (awaiting || 0) > 0 && <span className="nav-badge">{awaiting}</span>}
          </span>
          {/* A tab cell is one sixth of a phone screen: French and German labels
              ("Planter des arbres", "Bäume pflanzen") do not fit on one line at a
              readable size, so they are allowed to wrap instead of being cut. */}
          <span data-fit="wrap" data-fit-min="10">{t(`nav.${n.labelKey}` as any)}</span>
        </a>
      ))}
    </nav>
  );

  return host ? createPortal(bar, host) : bar;
}

/* ---------------- Login sheet ---------------- */

function LoginSheetContent({ close }: { close: () => void }) {
  const { t } = useT();
  const { toast } = useToast();
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState<'hub' | 'pay' | null>(null);
  const [showPayInstall, setShowPayInstall] = useState(false);
  const inPay = inNimiqPay();

  useEffect(() => {
    void prefetchHubLogin();
  }, []);

  const signedIn = (address: string) => {
    close();
    toast(t('account.signedInToast', { addr: formatWalletAddress(address) }), 'success');
  };

  return (
    <div>
      <div className="login-hero">
        <div className="identicon-lg placeholder" style={{ display: 'grid', placeItems: 'center', width: 56, height: 56 }}>
          <Icon name="nimiq" size={38} />
        </div>
        <h3 className="center" style={{ marginBottom: '8px' }}>{t('login.heroTitle')}</h3>
        <p className="muted small center" style={{ maxWidth: 400, margin: '0 auto' }}>
          {t('login.heroBody', { hub: inPay ? t('account.pay') : t('account.hub') })}
        </p>
      </div>
      <div className="mt-3" style={{ display: 'flex', flexDirection: 'column', gap: '8px' }}>
        <button
          className="btn btn-gold btn-block btn-lg"
          disabled={!!busy}
          onClick={() => {
            setError(null);
            if (inPay) {
              setBusy('pay');
              loginWithNimiqPay()
                .then((r) => {
                  if ('needsInstall' in r && r.needsInstall) {
                    setShowPayInstall(true);
                    return;
                  }
                  if ('address' in r) signedIn(r.address);
                })
                .catch((err) => setError(friendlyHubError(err)))
                .finally(() => setBusy(null));
              return;
            }
            setBusy('hub');
            loginWithHub()
              .then((r) => signedIn(r.address))
              .catch((err) => setError(friendlyHubError(err)))
              .finally(() => setBusy(null));
          }}
          style={{ display: 'inline-flex', alignItems: 'center', gap: '8px', width: '100%', justifyContent: 'center' }}
        >
          <Icon name="nimiq" size={20} />
          <span className="btn-label">{t('login.continueWith', { hub: inPay ? t('account.pay') : t('account.hub') })}</span>
        </button>
        {error && (
          <div className="alert error mt-2 login-error" style={{ marginBottom: 0 }}>
            <Icon name="alert" size={19} />
            <div>{error}</div>
          </div>
        )}
        <p className="xs faint center mt-2">{t('login.poweredBy', { hub: inPay ? t('account.pay') : t('account.hub') })}</p>
      </div>
      {showPayInstall && <NimiqPayInstallDialog onClose={() => setShowPayInstall(false)} />}
    </div>
  );
}

export function openLoginSheet(opts: {
  openSheet: (o: { title: string; render: (close: () => void) => ReactNode }) => void;
  closeSheet: (id?: number) => void;
  toast: (t: string, k?: 'success' | 'error' | 'info' | 'warn') => void;
}): void {
  opts.openSheet({
    title: i18nT('login.sheetTitle'),
    render: (close) => <LoginSheetContent close={close} />,
  });
}

/** Called once per page to wire Hub redirect-return handling + session toasts. */
export function useShellBootstrap(): void {
  const { t } = useT();
  const { toast } = useToast();
  useEffect(() => {
    applyNimiqPayChrome();
    initNimiqMiniApp();
    initHubRedirectHandling({
      onLogin: (address) => {
        toast(t('account.signedInToast', { addr: formatWalletAddress(address) }), 'success');
      },
    });
    const onHubError = (e: Event) => {
      const detail = (e as CustomEvent).detail;
      toast((detail && detail.message) || t('toast.walletFailed'), 'error');
    };
    window.addEventListener('nimshop:hub-error', onHubError);
    return () => window.removeEventListener('nimshop:hub-error', onHubError);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [t, toast]);
}
