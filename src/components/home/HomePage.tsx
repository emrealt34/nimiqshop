/**
 * HomePage.tsx — React port of pages/home.js: storefront catalog (gift
 * cards / top-ups / eSIMs), search, country picker, sort, hide-out-of-stock,
 * responsive product grid with chunked render + card caching. Behavior is
 * faithful to the original (dedup, self-healing missing list, geo suggest).
 */
import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { Icon } from '../ui/Icon';
import { WalletBalance } from '../wallet/WalletBalance';
import { FlagMark } from '../ui/FlagMark';
import { ComeBackBanner, ErrorDetail } from '../ui/uiKit';
import { flattenBrands, markReportedOutOfStock, type Product } from '../../lib/catalog';
import { UnifiedThumb } from '../ui/UnifiedThumb';
import { orderedCountries } from '../../lib/countries';
import { listGiftCards, listTopups, listEsims, listUnavailableFamilies, searchProducts, getProduct, getFXRates, cachedFX, onRatesChange } from '../../lib/api';
import {
  catalogHasItems,
  readCachedCatalog,
  writeCachedCatalog,
  type CatalogMap,
} from '../../lib/catalogCache';
import { countryName, parseCurrencyValue } from '../../lib/format';
import { countryCandidates } from '../../lib/hostLang';
import { useToast } from '../AppProviders';
import { useInNimiqPay, openInNimiqPay } from '../../lib/miniapp';
import { NimiqPayInstallDialog } from '../ui/NimiqPayInstallDialog';
import { siteName } from '../../lib/config';
import { getShelfSeed, shelfSeedFresh, type ShelfSeed } from '../../lib/shelfSeed';
import { prefetchRoute } from '../../lib/router';

/** Build-time shelf seed: SSR reads the module singleton (set by Base.astro),
 *  the client reads the inlined window.__SHELF_SEED — same bytes, so the
 *  hydrated tree matches the server-rendered cards. */
function seedOf(): ShelfSeed | null {
  const w = typeof window !== 'undefined' ? (window as unknown as { __SHELF_SEED?: ShelfSeed }).__SHELF_SEED : null;
  const s = w || getShelfSeed();
  return shelfSeedFresh(s) ? s : null;
}
function seedMaps(): Record<string, Product[]> {
  const s = seedOf();
  if (!s) return { gift_card: [], phone_refill: [], esim: [] };
  return {
    gift_card: (s.maps.gift_card as Product[]) || [],
    phone_refill: (s.maps.phone_refill as Product[]) || [],
    esim: (s.maps.esim as Product[]) || [],
  };
}
import { useT } from '../../i18n';
import { pagePath } from '../../lib/asset';


/** Category chips — labels are i18n keys (the array is module-level, so it
 *  cannot call useT() itself; the render maps the key through t()). */
const CATS: { key: string; labelKey: string; shortKey: string }[] = [
  { key: 'all', labelKey: 'home.catEverything', shortKey: 'home.catAllShort' },
  { key: 'gift_card', labelKey: 'home.catGiftCards', shortKey: 'home.catCardsShort' },
  { key: 'phone_refill', labelKey: 'home.catTopups', shortKey: 'home.catTopups' },
  { key: 'esim', labelKey: 'home.catEsims', shortKey: 'home.catEsims' },
];

/**
 * A comparable "price" for sorting. Re-parses the raw min price and only
 * accepts it when a real currency was detected, so "1720 Minecoins", "1 month"
 * or "Aylık fizy Premium" are never mistaken for money. Returns a USD value so
 * prices in different currencies (TRY, USD, EUR…) rank correctly, or `null`
 * for non-money items. Null sorts last in both directions — a "no price" item
 * must never top the "low to high" list.
 */
function usdSortKey(p: Product, fx: Record<string, number> | null): number | null {
  const parsed = parseCurrencyValue(p.min_raw);
  const min = parsed.value;
  const ccy = parsed.currency;
  if (min <= 0 || !ccy) return null;
  const rate = fx ? Number(fx[ccy]) || 0 : 0;
  if (rate > 0) return min * rate; // FX available → true cross-currency ranking
  return min; // FX unknown → raw value (still correct within one country)
}

export function HomePage() {
  const { t } = useT();
  const inPay = useInNimiqPay();
  const { toast } = useToast();
  const [catalogs, setCatalogs] = useState<Record<string, Product[]>>(seedMaps);
  const [activeCat, setActiveCat] = useState('all');
  const [searchTerm, setSearchTerm] = useState('');
  const [searchResults, setSearchResults] = useState<Product[] | null>(null);
  const [country, setCountry] = useState('TR');
  const [sortKey, setSortKey] = useState('default');
  // The operator's global catalog rule decides whether out-of-stock products
  // are removed server-side. When the rule is "show", keep them visible by
  // default and mark them on the card; buyers can still hide them locally.
  const [hideOutOfStock, setHideOutOfStock] = useState(true);
  const [fx, setFx] = useState<Record<string, number> | null>(() => (typeof cachedFX === 'function' ? cachedFX() : null));
  const [showPayInstall, setShowPayInstall] = useState(false);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [loadErrorDetail, setLoadErrorDetail] = useState('');
  const [shelfNote, setShelfNote] = useState('');
  const [busy, setBusy] = useState(true);
  // Which country's catalog is on screen right now. Together with `busy` it is
  // what releases Base.astro's market hold (the seeded grid is the build's own
  // market, see the CSS block) — released in the commit that shows the
  // visitor's own shelf, never before.
  const [loadedCountry, setLoadedCountry] = useState('');
  const userChoseCountry = useRef(false);
  const mountAlive = useRef(true);
  const catalogRequest = useRef(0);

  // The visitor's saved country is read during the first CLIENT render, not
  // inside an effect. The effect-only version restored it one render late, so
  // opening the shop fetched the SSR-default 'TR' shelf first, painted it, and
  // only then switched to the saved country — a visible flip plus a wasted
  // catalog round trip on every load ("settings arrive after the page does").
  //
  // The `country` STATE still starts at 'TR': this is a static build, the
  // server always renders that default, and the hydration render must match
  // the server HTML byte for byte (rendering the saved value in the
  // initializer would fail hydration and rebuild the whole island). Reading
  // into a ref keeps the render identical while letting the effects below act
  // on the saved value from the very first commit.
  const savedCountry = useRef<string | null>(null);
  if (savedCountry.current === null) {
    let v = '';
    if (typeof window !== 'undefined') {
      try {
        const s = localStorage.getItem('nimshop_country');
        if (s && /^[A-Za-z]{2}$/.test(s)) v = s.toUpperCase();
      } catch {}
    }
    savedCountry.current = v; // '' = nothing saved (or server render)
  }

  // The market for a visitor who has not chosen one: their own locale (device
  // region, then the country the host/device language usually means) — never
  // their IP. Computed once, during the first CLIENT render like savedCountry
  // above, so the shelf effect below can already skip the SSR-default 'TR'
  // fetch: a US visitor used to see the Turkish shelf for a beat and then the
  // swap to theirs (measured: "Amazon.com.tr" before "Amazon.com"). On the
  // server there is no navigator, so this is '' and the render is unchanged.
  const marketDefault = useRef<string>('');
  if (!marketDefault.current && typeof window !== 'undefined') {
    try {
      const all = [...orderedCountries().popular, ...orderedCountries().rest];
      marketDefault.current = countryCandidates().find((code) => all.some(([c]) => c === code)) || '';
    } catch { marketDefault.current = ''; }
  }

  // Restore the visitor's country — the saved one when they have chosen before,
  // otherwise the locale-derived market. Runs before the shelf-load effect in
  // the same commit, so the first fetch already targets the right country and
  // no wrong shelf is ever painted. Only an explicit choice sets
  // userChoseCountry (it must survive later renders; the locale default must not
  // block a future change of the device region).
  useEffect(() => {
    mountAlive.current = true;
    const cc = savedCountry.current || marketDefault.current;
    if (savedCountry.current) userChoseCountry.current = true;
    if (cc) setCountry(cc);
    return () => {
      mountAlive.current = false;
    };
  }, []);

  const loadCatalogs = useCallback(async (c: string) => {
    const request = ++catalogRequest.current;
    const current = () => mountAlive.current && request === catalogRequest.current;
    setLoadError(null);
    setShelfNote('');
    const cached = readCachedCatalog(c);
    const seed = seedOf();
    const instant = cached || (seed && seed.country === c ? seedMaps() : { gift_card: [], phone_refill: [], esim: [] });
    setCatalogs(instant);
    if (catalogHasItems(instant)) setLoadedCountry(c);
    setBusy(true);
    const results = await Promise.allSettled([listGiftCards(c, false), listTopups(c, false), listEsims(c, false)]);
    const reported = await listUnavailableFamilies(c);
    if (!current()) return;
    const keys = ['gift_card', 'phone_refill', 'esim'];
    const merged: CatalogMap = { gift_card: [], phone_refill: [], esim: [] };
    const missing: string[] = [];
    results.forEach((result, index) => {
      const key = keys[index];
      if (result.status === 'fulfilled') merged[key] = markReportedOutOfStock(flattenBrands(result.value, c), reported);
      else {
        missing.push(key);
        merged[key] = cached?.[key] || [];
      }
    });
    if (!current()) return;
    setCatalogs(merged);
    setBusy(false);
    if (catalogHasItems(merged)) setLoadedCountry(c);
    if (!missing.length) writeCachedCatalog(c, merged);
    else if (catalogHasItems(merged)) {
      setShelfNote(t('home.shelfNote'));
    } else {
      setLoadError(t('home.loadError'));
      setLoadErrorDetail(missing.length ? missing.join(' · ') : '');
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const firstShelfLoad = useRef(true);
  useEffect(() => {
    // First mount only: when the visitor's country (saved, or the locale-derived
    // market) differs from the SSR default, skip the SSR-default shelf — the
    // restore effect (declared above) already ran in this commit and this
    // effect re-fires with the right country immediately. Saves the wasted
    // round trip and the wrong-shelf flash. Every later run (manual picks)
    // loads normally.
    if (firstShelfLoad.current) {
      firstShelfLoad.current = false;
      const cc = savedCountry.current || marketDefault.current;
      if (cc && cc !== country) return;
    }
    void loadCatalogs(country);
  }, [country, loadCatalogs]);

  // Release Base.astro's market hold: the visitor's own shelf is on screen.
  useEffect(() => {
    if (typeof document === 'undefined') return;
    const root = document.documentElement;
    if (!root.hasAttribute('data-market-hold')) return;
    if (!busy && loadedCountry && loadedCountry === country) root.removeAttribute('data-market-hold');
  }, [busy, loadedCountry, country]);

  useEffect(() => {
    getFXRates().then((r) => {
      if (mountAlive.current && r?.usd_per_unit) setFx(r.usd_per_unit);
    }).catch(() => {});
    // Shelf prices are converted with the FX table; when the live API corrects
    // the build-time snapshot, re-render with the corrected rates.
    const off = onRatesChange((r) => {
      if (mountAlive.current && r?.usd_per_unit) setFx(r.usd_per_unit);
    });
    return off;
  }, []);

  const applyCountry = (code: string, manual: boolean) => {
    // Invalidate the previous response immediately, before React's effect.
    ++catalogRequest.current;
    setCountry(code);
    if (manual) {
      userChoseCountry.current = true;
      try { localStorage.setItem('nimshop_country', code); } catch {}
    }
    setSearchTerm('');
    setSearchResults(null);
    if (code === country) void loadCatalogs(code);
  };

  useEffect(() => {
    let active = true;
    const term = searchTerm.trim();
    setSearchResults(null);
    if (!term) return;
    // DS172411 (setTimeout): closure only, never a string — no untrusted data is evaluated.
    const timer = setTimeout(async () => {
      try {
        const res = await searchProducts(term, country);
        if (active) setSearchResults(normalizeList(res, country));
      } catch {
        const query = term.toLowerCase();
        const local = Object.values(catalogs).flat().filter((p) => p.name.toLowerCase().includes(query));
        if (active) setSearchResults(local);
      }
    }, 300);
    return () => { active = false; clearTimeout(timer); };
  }, [searchTerm, country, catalogs]);

  const items = useMemo(() => {
    let list: Product[];
    if (searchTerm.trim() && searchResults) list = searchResults;
    else list = activeCat === 'all' ? [...catalogs.gift_card, ...catalogs.phone_refill, ...catalogs.esim] : catalogs[activeCat] || [];
    // Cards refused at checkout (reported_oos) are never hidden: they read "Out of stock".
    if (hideOutOfStock) list = list.filter((p) => p.in_stock !== false || p.reported_oos);
    list = list.filter((p) => !isMissingProduct(p.id, p.country));
    if (sortKey === 'price-asc' || sortKey === 'price-desc') {
      const dir = sortKey === 'price-asc' ? 1 : -1;
      list = [...list].sort((a, b) => {
        const ka = usdSortKey(a, fx);
        const kb = usdSortKey(b, fx);
        // Items without a money price always sink to the bottom.
        if (ka === null && kb === null) return 0;
        if (ka === null) return 1;
        if (kb === null) return -1;
        return (ka - kb) * dir;
      });
    } else if (sortKey === 'name') {
      list = [...list].sort((a, b) => a.name.localeCompare(b.name, undefined, { sensitivity: 'base', numeric: true }));
    }
    return list;
  }, [catalogs, activeCat, searchTerm, searchResults, hideOutOfStock, sortKey, fx]);

  return (
    <>
      <section className="hero container fade-in">
        <div>
          <h1>
            {/* The {' '} is a real DOM space, not decoration: under 719px the
                CSS hides the <br> (compact phone hero), and without a space of
                its own the two sentences physically run together —
                "…top-ups & eSIMs.Pay with NIM…" — on screen, in innerText and
                for screen readers. On desktop it is an invisible trailing
                space before the line break. fixes.css mirrors it with a
                .gold-text::before safety net; adjacent whitespace collapses,
                so only one space ever renders. */}
            {t('home.heroTitle1')}{' '}
            <br />
            <span className="gold-text">{t('home.heroTitle2')}</span>
          </h1>
          <p className="lede">
            {t('home.heroLede')}
          </p>
          <div className="hero-actions">
            <a className="btn btn-gold btn-lg" href="#browse" style={{ display: 'inline-flex', alignItems: 'center', gap: '8px' }}>
              <Icon name="bag" size={20} />
              <span>{t('home.browseShelf')}</span>
            </a>
            <a className="btn btn-ghost btn-lg hero-cashback" href={pagePath("/cashback")} style={{ display: 'inline-flex', alignItems: 'center', gap: '8px' }}>
              <Icon name="coins" size={20} />
              <span>{t('home.earnCashback')}</span>
            </a>
            {!inPay && (
              <button
                className="btn btn-ghost btn-lg"
                onClick={() => {
                  openInNimiqPay(() => setShowPayInstall(true));
                }}
                style={{ display: 'inline-flex', alignItems: 'center', gap: '8px' }}
              >
                <Icon name="nimiq" size={20} />
                <span>{t('home.openInPay')}</span>
              </button>
            )}
          </div>
        </div>
        <aside className="kf-pkg" aria-label={t('home.packageAria')}>
          <div className="kf-pkg-head">
            <h2 style={{ fontSize: '1.1rem', textTransform: 'uppercase', letterSpacing: '0.08em', margin: 0 }}>{t('home.insidePackage')}</h2>
            <div className="kf-stamp">{t('home.fastDelivery')}</div>
          </div>
          <ul>
            <li style={{ display: 'flex', gap: '8px', alignItems: 'center' }}>
              <Icon name="lock" size={17} /> {t('home.bulletNoBalance')}
            </li>
            <li style={{ display: 'flex', gap: '8px', alignItems: 'center' }}>
              <Icon name="bolt" size={17} /> {t('home.bulletLightning')}
            </li>
            <li style={{ display: 'flex', gap: '8px', alignItems: 'center' }}>
              <Icon name="star" size={17} /> {t('home.bulletRatings')}
            </li>
            <li style={{ display: 'flex', gap: '8px', alignItems: 'center' }}>
              <Icon name="gift" size={17} /> {t('home.bulletInstant')}
            </li>
            <li style={{ display: 'flex', gap: '8px', alignItems: 'center' }}>
              <Icon name="check" size={17} /> {t('home.bulletOpenSource')}
            </li>
          </ul>
        </aside>
      </section>

      {/* Owner (2026-10-05): "ana sayfada NIM kuru gözükse güzel olabilirdi" —
          the wallet's NIM and its USD value, in its own row right under the
          hero. It renders NOTHING until there is a wallet to read, so a
          first-time visitor still sees the untouched marketing hero. */}
      <div className="container">
        <WalletBalance variant="card" className="mt-3" />
      </div>

      <div className="container how-strip" aria-label={t('home.howAria', { site: siteName() })}>
        {/* One number + one short line per step, at every width: the sub
            descriptions made steps 2 and 3 wrap to two or three lines while
            step 1 stayed one, so the strip never read as three siblings. */}
        <div className="how-step">
          <span className="how-n">1</span>
          <div>
            <div className="strong">{t('home.how1Title')}</div>
          </div>
        </div>
        <div className="how-step">
          <span className="how-n">2</span>
          <div>
            <div className="strong">{t('home.how2Title')}</div>
          </div>
        </div>
        <div className="how-step">
          <span className="how-n">3</span>
          <div>
            <div className="strong">{t('home.how3Title')}</div>
          </div>
        </div>
      </div>

      <div className="container">
        <ComeBackBanner />
      </div>

      <div className="container" id="browse">
        <div className="toolbar">
          <div className="seg" id="catSeg">
            {CATS.map((c) => (
              <button
                key={c.key}
                className={c.key === activeCat ? 'active' : ''}
                aria-pressed={c.key === activeCat}
                onClick={() => {
                  setActiveCat(c.key);
                  setSearchTerm('');
                  setSearchResults(null);
                }}
              >
                <span className="seg-lg">{t(c.labelKey)}</span>
                <span className="seg-sm">{t(c.shortKey)}</span>
              </button>
            ))}
          </div>
          <div className="searchbox">
            <Icon name="search" size={18} />
            <input
              className="input"
              type="search"
              placeholder={t('home.searchPlaceholder')}
              aria-label={t('home.searchAria')}
              autoComplete="off"
              value={searchTerm}
              onChange={(e) => {
                setSearchTerm(e.target.value);
              }}
            />
          </div>
        </div>
        <div className="toolbar filters">
          {/* NOTE: this is a <div>, not a <label> — a label would forward its
              click to the nested button (double toggle + the caption opening
              the popup). Same for the out-of-stock toggle below. */}
          <div className="field">
            <span className="xs faint">{t('home.labelCountry')}</span>
            <CountryPicker country={country} onChange={applyCountry} />
          </div>
          <label className="field">
            <span className="xs faint">{t('home.labelSort')}</span>
            <select className="input" id="sortSel" aria-label={t('home.labelSort')} value={sortKey} onChange={(e) => setSortKey(e.target.value)}>
              <option value="default">{t('home.sortFeatured')}</option>
              <option value="price-asc">{t('home.sortPriceAsc')}</option>
              <option value="price-desc">{t('home.sortPriceDesc')}</option>
              <option value="name">{t('home.sortName')}</option>
            </select>
          </label>
          <div className="field">
            <span className="xs faint">&nbsp;</span>
            <label className="toggle oos-toggle" htmlFor="hideOOS">
              <input type="checkbox" id="hideOOS" checked={hideOutOfStock} onChange={(e) => setHideOutOfStock(e.target.checked)} />
              <Icon name={hideOutOfStock ? 'eye-off' : 'eye'} size={16} />
              <span>{hideOutOfStock ? t('home.hideOos') : t('home.showOos')}</span>
            </label>
          </div>
        </div>
        {shelfNote && <div className="catalog-notice" role="status">
          <span>{shelfNote}</span>
          <button type="button" className="btn btn-ghost btn-sm" disabled={busy} onClick={() => loadCatalogs(country)}>{t('home.tryAgain')}</button>
        </div>}
        <div id="gridWrap" aria-busy={busy || (!!searchTerm.trim() && searchResults === null)}>
          {loadError ? (
            <ErrorState message={loadError} detail={loadErrorDetail} onRetry={() => loadCatalogs(country)} />
          ) : (busy && !catalogHasItems(catalogs)) || (!!searchTerm.trim() && searchResults === null) ? (
            <SkeletonGrid />
          ) : items.length === 0 ? (
            <EmptyState
              title={searchTerm ? t('home.noResults') : t('home.nothingHere')}
              text={
                searchTerm
                  ? t('home.noMatch', { term: searchTerm })
                  : hideOutOfStock
                  ? t('home.noneInStock')
                  : t('home.catalogEmpty')
              }
            />
          ) : (
            <ProductGrid products={items} onBuy={toast} allowWarm={!busy} />
          )}
        </div>
      </div>
      {showPayInstall && <NimiqPayInstallDialog onClose={() => setShowPayInstall(false)} />}
    </>
  );
}

function normalizeList(data: any, country: string): Product[] {
  if (!data) return [];
  if (data.categories) return flattenBrands(data, country);
  if (Array.isArray(data)) {
    if (data.length > 0 && data[0].family && !data[0].name) return flattenBrands(data, country);
    return data;
  }
  return [];
}

/* ---------------- Missing-product memory (self-healing list) ---------------- */
function missingProducts(): { id: string; country: string; at: number }[] {
  try {
    const arr = JSON.parse(sessionStorage.getItem('nimshop_missing:v2') || '[]');
    const cutoff = Date.now() - 6 * 3600 * 1000;
    return arr.filter((m: any) => m.at > cutoff);
  } catch {
    return [];
  }
}
function isMissingProduct(id: string, country: string): boolean {
  return missingProducts().some((m) => m.id === id && m.country === country);
}

function CountryPicker({ country, onChange }: { country: string; onChange: (code: string, manual: boolean) => void }) {
  const { t } = useT();
  const [open, setOpen] = useState(false);
  const wrapRef = useRef<HTMLDivElement>(null);
  const { popular, rest } = orderedCountries();
  const all = [...popular, ...rest];
  const current = all.find(([c]) => c === country) || [country, countryName(country)];

  // Close on outside click / Escape — the popup is a floating layer, so it
  // must not stay open when the buyer taps or tabs away.
  useEffect(() => {
    if (!open) return;
    const onDown = (e: MouseEvent | TouchEvent) => {
      if (!wrapRef.current || !wrapRef.current.contains(e.target as Node)) setOpen(false);
    };
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') setOpen(false);
    };
    document.addEventListener('mousedown', onDown);
    document.addEventListener('touchstart', onDown);
    document.addEventListener('keydown', onKey);
    return () => {
      document.removeEventListener('mousedown', onDown);
      document.removeEventListener('touchstart', onDown);
      document.removeEventListener('keydown', onKey);
    };
  }, [open]);

  return (
    <div className="country-pick" id="countryPick" ref={wrapRef} style={{ position: 'relative' }}>
      <button
        className="input btn-country"
        type="button"
        aria-haspopup="listbox"
        aria-expanded={open}
        /* The visible label IS the selected country, so the accessible name is
           "Shop country: <name>" — the audit requires the name to contain the
           visible text, and a bare "Country" label failed it. */
        aria-label={`${t('home.labelCountry')}: ${current[1]}`}
        title={current[1]}
        onClick={() => setOpen((o) => !o)}
        style={{ display: 'inline-flex', alignItems: 'center', gap: '8px', justifyContent: 'space-between', width: '100%' }}
      >
        <span className="btn-country-main" style={{ display: 'inline-flex', gap: '8px', alignItems: 'center' }}>
          <FlagMark country={country} size={18} />
          {/* data-fit: the country name is a control label, not a paragraph —
              it keeps one line and gives up a little type size (never below
              10px, wrapping if it must) instead of ending in an ellipsis. */}
          <span className="btn-country-label" data-fit="wrap" data-fit-min="10">{current[1]}</span>
        </span>
        <Icon name="chevron-down" size={15} style={{ color: "var(--stamp)" }} />
      </button>
      {open && (
        <div
          className="country-pop open"
          role="listbox"
          aria-label={t('home.labelCountry')}
          // COUNTRY-PICKER FIX: app.css ships `.country-pop { display: none }`
          // (the vanilla build toggled it with an inline style). React renders
          // this node conditionally instead, so the CSS rule hid the popup
          // completely and the button looked dead. Force it visible here.
          style={{ display: 'block', position: 'absolute', top: 'calc(100% + 4px)', left: 0, zIndex: 40, background: 'var(--surface-1)', border: '1.5px solid var(--line-strong)', color: 'var(--ink)', borderRadius: 12, maxHeight: 320, overflow: 'auto', boxShadow: 'var(--shadow-pop)', minWidth: 220 }}
        >
          {popular.length > 0 && (
            <div>
              <div className="xs faint country-pop-label" style={{ padding: '8px 12px 4px' }}>
                {t('home.popular')}
              </div>
              {popular.map(countryRow)}
            </div>
          )}
          {rest.length > 0 && (
            <div>
              <div className="xs faint country-pop-label" style={{ padding: '8px 12px 4px' }}>
                {t('home.allCountries')}
              </div>
              {rest.map(countryRow)}
            </div>
          )}
        </div>
      )}
    </div>
  );

  function countryRow([code, name]: [string, string]) {
    return (
      <button
        key={code}
        className="country-row"
        type="button"
        role="option"
        data-cc={code}
        aria-selected={code === country}
        title={name}
        onClick={(e) => {
          e.stopPropagation();
          setOpen(false);
          onChange(code, true);
        }}
        style={{
          display: 'flex',
          alignItems: 'center',
          gap: '10px',
          width: '100%',
          padding: '8px 12px',
          border: '0',
          background: code === country ? 'var(--surface-2)' : 'none',
          cursor: 'pointer',
          textAlign: 'left',
          fontWeight: 700,
          fontSize: '0.92rem',
          color: 'var(--ink)',
        }}
      >
        <FlagMark country={code} size={16} />
        <span>{name}</span>
        <span className="xs faint country-row-cc" style={{ marginLeft: 'auto' }}>
          {code}
        </span>
      </button>
    );
  }
}

function ProductGrid({ products, onBuy, allowWarm }: { products: Product[]; onBuy: (msg: string, k?: 'info') => void; allowWarm: boolean }) {
  const { t } = useT();
  const wrapRef = useRef<HTMLDivElement>(null);
  // Warm the family payload + route chunk for cards the shopper can already
  // see, so tapping one opens a product page whose data is in cache. Capped
  // and idle-scheduled: this must never compete with the paint it speeds up.
  useEffect(() => {
    const root = wrapRef.current;
    // Gate: while the country switch is still in flight the visible cards may
    // be the build-time seed of a DIFFERENT country — warming those would
    // fetch a catalog snapshot nobody asked for. Warm once busy clears.
    if (!root || !allowWarm || typeof IntersectionObserver === 'undefined') return;
    // Set when the effect tears down (country switch, filter change): any
    // warm already queued via setTimeout is dropped instead of fetching a
    // family for cards that are no longer on screen.
    let cancelled = false;
    const io = new IntersectionObserver((entries) => {
      for (const en of entries) {
        if (!en.isIntersecting) continue;
        const a = en.target as HTMLAnchorElement;
        io.unobserve(a);
        if (warmedCards.has(a)) continue;
        warmedCards.add(a);
        if (warmedCards.size > 24) return;
        const href = a.getAttribute('href') || '';
        const u = new URL(href, location.href);
        const id = u.searchParams.get('id');
        const cc = u.searchParams.get('country');
        if (id) {
          // DS172411 (setTimeout): closure only, never a string — no untrusted data is evaluated.
          window.setTimeout(() => {
            if (cancelled) return;
            getProduct(id, cc || undefined).catch(() => {});
            prefetchRoute('/product');
          }, 0);
        }
      }
    }, { rootMargin: '150px' });
    root.querySelectorAll('a.product-card[href]').forEach((a) => io.observe(a));
    return () => { cancelled = true; io.disconnect(); };
  }, [products, allowWarm]);
  return (
    <div ref={wrapRef} className="grid products fade-in">
      {products.map((p) => (
        <a
          key={p.id + '|' + p.country}
          className="product-card"
          href={p.in_stock !== false ? pagePath(`/product?id=${encodeURIComponent(p.id)}&country=${encodeURIComponent(p.country)}`) : undefined}
          aria-label={t('home.viewProduct', { name: p.name })}
          onMouseEnter={() => getProduct(p.id, p.country).catch(() => {})}
          onFocus={() => getProduct(p.id, p.country).catch(() => {})}
          onPointerDown={() => {
            // Touch devices never hover: the press is the first intent we
            // get, and it lands ~100-300 ms before the click navigation —
            // enough for the family payload and the route chunk to arrive.
            getProduct(p.id, p.country).catch(() => {});
            prefetchRoute('/product');
          }}
          onClick={(e) => {
            if (p.in_stock === false) {
              e.preventDefault();
              onBuy(t('home.soldOutToast'), 'info');
            }
          }}
        >
          <ProductThumb p={p} oos={p.in_stock === false} />
          <div className="p-name" title={p.name}>
            {p.name}
          </div>
          {/* Price sits DIRECTLY under the product name on every card (user
              request, Getir example), one clamped line so a denomination-style
              range can never stretch a card again; the full range lives in
              the title. The dashed separator + country chip anchor below. */}
          <span
            className="p-price"
            title={productPriceText(p, t)}
            style={{ fontSize: '0.85rem', display: 'block', width: '100%', textAlign: 'center', whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis', margin: '2px 0 8px' }}
          >
            {productPriceText(p, t)}
          </span>
          <div className="p-meta" style={{ display: 'flex', flexDirection: 'column', alignItems: 'center', gap: '6px', flexWrap: 'nowrap', width: '100%' }}>
            <span className="chip" style={{ display: 'inline-flex', alignItems: 'center', gap: '4px' }}>
              <FlagMark country={p.country} size={16} />
              <span className="chip-txt">{countryName(p.country)}</span>
            </span>
          </div>
        </a>
      ))}
    </div>
  );
}

/** Cards whose payload we already warmed this pageview. */
const warmedCards = new Set<HTMLAnchorElement>();

function ProductThumb({ p, oos = false }: { p: Product; oos?: boolean }) {
  const { t } = useT();
  // Per user request: 100% identical everywhere — ONE single global tile
  // (UnifiedThumb). No fallback layers and no fallback files: when a product has
  // no (loadable) logo the tile shows the global icon-on-background placeholder.
  return (
    <div style={{ position: 'relative' }}>
      <UnifiedThumb src={p.logo_url} alt={p.name} bg={p.bg_color || 'rgb(255,255,255)'} />
      {oos && <div className="oos" style={{ position: 'absolute', inset: 0, zIndex: 2, display: 'grid', placeItems: 'center', background: 'var(--scrim, rgba(20, 16, 12, 0.78))', color: 'var(--on-scrim, #FFF6E8)', fontWeight: 900, letterSpacing: '0.14em', textTransform: 'uppercase', fontSize: 'var(--fs-sm)' }}>{t('shop.outOfStock')}</div>}
      {/* e-money families: tell the buyer up front that the provider may ask
          for identity verification. Styled as the site's stamp aesthetic —
          gold pill, ink outline, hard package shadow — so it reads as part of
          the card on every logo colour. Absolute: zero layout shift, visible
          even under the sold-out scrim (zIndex 3). */}
      {p.is_e_money && (
        <div className="kyc-badge" style={{ position: 'absolute', left: '50%', transform: 'translateX(-50%)', bottom: 6, zIndex: 3, display: 'inline-flex', alignItems: 'center', gap: 4, padding: '3px 9px', borderRadius: 'var(--pill)', background: 'linear-gradient(180deg, var(--gold-300), var(--gold-500))', color: 'var(--on-gold)', border: '1.5px solid var(--line-strong)', boxShadow: 'inset 0 1px 0 rgba(255, 246, 232, 0.45), 2px 2px 0 rgba(78, 61, 40, 0.35)', fontSize: '0.62rem', lineHeight: 1.25, fontWeight: 900, letterSpacing: '0.05em', textTransform: 'uppercase', whiteSpace: 'nowrap' }}>
          <Icon name="shield" size={11} style={{ strokeWidth: 2.6 }} />
          {t('home.kycBadge')}
        </div>
      )}
    </div>
  );
}

function productPriceText(p: Product, t: (k: string, v?: Record<string, string | number>) => string): string {
  if (p.min_raw && p.max_raw && p.min_raw !== p.max_raw) return `${p.min_raw} - ${p.max_raw}`;
  if (p.min_raw) return p.min_raw;
  return p.currency ? t('home.priceIn', { currency: p.currency }) : '';
}

/* ---------------- UI primitives ---------------- */
function SkeletonGrid() {
  // Four cards, not ten. `#gridWrap` is min-height:62vh, so with four cards the
  // grid area is exactly as tall as the empty state and as the error state —
  // the three states swap without changing the document height. Ten cards made
  // the loading state ~990px against the empty state's 567px, and that collapse
  // pulled the footer 423px up the page when a catalog request failed: the
  // single largest CLS number in the Lighthouse report (0.05), on every device
  // whenever the API is slow or down. A grid of real products grows the page
  // *downwards*, which is harmless — the footer only leaves the viewport.
  return (
    <div className="grid products">
      {Array.from({ length: 4 }).map((_, i) => (
        <div key={i} className="skeleton-card" style={{ height: 190, borderRadius: 14 }} />
      ))}
    </div>
  );
}

function EmptyState({ title, text }: { title: string; text: string }) {
  return (
    <div className="center" style={{ padding: '40px 10px', textAlign: 'center' }}>
      <div style={{ marginBottom: 12, display: 'flex', justifyContent: 'center', color: 'var(--faint)' }}>
        <Icon name="search" size={40} />
      </div>
      <div className="strong">{title}</div>
      <div className="small muted mt-1">{text}</div>
    </div>
  );
}

function ErrorState({ message, detail, onRetry }: { message: string; detail?: string; onRetry: () => void }) {
  const { t } = useT();
  return (
    <div className="center" style={{ padding: '40px 10px', textAlign: 'center' }}>
      <div style={{ marginBottom: 12, display: 'flex', justifyContent: 'center', color: 'var(--stamp-ink)' }}>
        <Icon name="alert" size={40} />
      </div>
      <div className="strong">{t('errors.wentWrong')}</div>
      <div className="small muted mt-1">{message}</div>
      {/* Which shelves could not be loaded — the exact failure, not just "went wrong". */}
      <ErrorDetail detail={detail} />
      <button className="btn btn-gold mt-2" onClick={onRetry}>
        {t('home.tryAgain')}
      </button>
    </div>
  );
}
