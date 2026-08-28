/* pages/home.js — FIXED: No refund, only email. OOS hidden by default. No duplicates, correct currency, side-by-side. */
import { bootShell } from '../shell.js';
import { el, icon, flag, countryName, $, replaceChildren, debounce } from '../util.js';
import { inNimiqPay, openInNimiqPay, NIMIQ_PAY_IOS_URL, NIMIQ_PAY_ANDROID_URL } from '../miniapp.js';
import { listGiftCards, listTopups, listEsims, searchProducts, getGeo, getProduct } from '../api.js';
import { toast, skeletonCards, emptyState, errorState, giftToggle } from '../ui.js';
import { flattenBrands, productThumb } from '../catalog.js';
import { orderedCountries } from '../countries.js';

bootShell('shop');

const catalogs = { gift_card: null, phone_refill: null, esim: null };
let activeCat = 'all';
let searchTerm = '';
let searchResults = null;
let country = 'TR';
let sortKey = 'default';
let hideOutOfStock = true;
let userChoseCountry = false; // geo suggestions never override a manual pick

// Full country picker: the backend accepts any ISO-2 code the supplier
// catalog covers, so the UI must not hide countries behind a tiny list.
function countryOptions() {
  const { popular, rest } = orderedCountries();
  const opt = ([code, name]) => el('option', { value: code, selected: code === country, text: `${flag(code)} ${name}`.trim() });
  return [
    el('optgroup', { label: 'Popular' }, ...popular.map(opt)),
    el('optgroup', { label: 'All countries' }, ...rest.map(opt)),
  ];
}

const main = $('#main');
// Mount guard: client-side navigation can import this module more than
// once; wipe #main so a re-mount never stacks a second page shell on
// top of the first (the duplicate locked-card bug).
replaceChildren(main);

main.append(
  el('section.hero.container.fade-in', {},
    el('div', {},
      el('h1', {}, 'Gift cards, top-ups & eSIMs.', el('br'), el('span.gold-text', { text: 'Delivered like a package.' })),
      el('p.lede', { text: 'Buy from hundreds of global brands and pay instantly with NIM — the browser blockchain. Your wallet is your account.' }),
      el('div.hero-actions', {},
        el('a.btn.btn-gold.btn-lg', { href: '#browse', on: { click: (e) => { e.preventDefault(); $('#browse').scrollIntoView({ behavior: 'smooth', block: 'start' }); } } }, icon('bag', 20), el('span', { text: 'Browse the shelf' })),
        el('a.btn.btn-ghost.btn-lg', { href: '/activity' }, icon('pulse', 20), el('span', { text: 'See live payments' })),
        inNimiqPay() ? el('span.chip', {}, icon('check', 15), 'Running in Nimiq Pay') : el('button.btn.btn-ghost.btn-lg', { on: { click: () => openInNimiqPay(({ isIOS, isAndroid }) => {
              const store = isIOS ? NIMIQ_PAY_IOS_URL : isAndroid ? NIMIQ_PAY_ANDROID_URL : null;
              if (store) { toast('Nimiq Pay is not installed. Opening the app store…', 'info'); window.location.href = store; }
              else toast('Nimiq Pay could not be opened.', 'error');
            }) } }, icon('wallet', 20), el('span', { text: 'Open in Nimiq Pay' })),
      ),
    ),
    el('aside.kf-pkg', { 'aria-label': 'What is in the package' },
      el('div.kf-stamp', {}, 'FAST ⚡', document.createElement('br'), 'DELIVERY'),
      el('h2', { text: 'Inside the package', style: { fontSize: '1.1rem', textTransform: 'uppercase', letterSpacing: '0.08em' } }),
      el('ul', {},
        el('li', {}, icon('lock', 17), 'No internal balance — your NIM never sits with us'),
        el('li', {}, icon('bolt', 17), 'Nimiq Pay: pay in NIM, settled in ~5 s'),
        el('li', {}, icon('star', 17), 'Real buyer ratings from completed orders'),
        el('li', {}, icon('gift', 17), 'Codes land on your email instantly'),
        el('li', {}, icon('check', 17), 'Open-source, verifiable build'),
      ),
    ),
  ),
  el('div.container#browse', {},
    el('div.toolbar', {},
      el('div.seg#catSeg'),
      el('div.searchbox', {}, icon('search', 18), el('input.input#searchInput', { type: 'search', placeholder: 'Search brands…', 'aria-label': 'Search products', autocomplete: 'off' })),
    ),
    el('div.toolbar.filters', {},
      el('label.field', {}, el('span.xs.faint', { text: 'Country' }), el('select.input#countrySel', { 'aria-label': 'Country' }, ...countryOptions())),
      el('label.field', {}, el('span.xs.faint', { text: 'Sort' }), el('select.input#sortSel', { 'aria-label': 'Sort' }, el('option', { value: 'default', text: 'Featured' }), el('option', { value: 'price-asc', text: 'Price: low to high' }), el('option', { value: 'price-desc', text: 'Price: high to low' }), el('option', { value: 'name', text: 'Name A-Z' }))),
      // Same two-row structure as the Country/Sort fields (invisible caption
      // spacer) so the toggle lands exactly in the SELECT row, centered.
      el('label.field', {},
        el('span.xs.faint', { text: '\u00A0' }),
        giftToggle({ title: 'Hide out of stock', sub: '', ico: 'eye', id: 'hideOOS', checked: hideOutOfStock, inline: true }).node,
      ),
      el('span.xs.faint#geoNote', {}),
    ),
    el('div#gridWrap', {}, el('div.grid.products', {}, skeletonCards(10))),
  ),
);

const CATS = [
  { key: 'all', label: 'Everything' },
  { key: 'gift_card', label: 'Gift cards' },
  { key: 'phone_refill', label: 'Top-ups' },
  { key: 'esim', label: 'eSIMs' },
];

const catSeg = $('#catSeg');
for (const c of CATS) {
  const b = el('button' + (c.key === 'all' ? '.active' : ''), { text: c.label, on: { click: () => { activeCat = c.key; searchTerm = ''; $('#searchInput').value = ''; searchResults = null; [...catSeg.children].forEach((x) => x.classList.toggle('active', x === b)); renderGrid(); } } });
  catSeg.appendChild(b);
}

$('#searchInput').addEventListener('input', debounce(async (e) => {
  searchTerm = e.target.value.trim();
  if (!searchTerm) { searchResults = null; renderGrid(); return; }
  try { searchResults = await searchProducts(searchTerm); renderGrid(); } catch (err) { replaceChildren($('#gridWrap'), errorState(err.message, () => { searchResults = null; return loadCatalogs(country); })); }
}, 300));

$('#countrySel').addEventListener('change', (e) => {
  country = e.target.value;
  userChoseCountry = true;
  try { localStorage.setItem('nimshop_country', country); } catch {}
  loadCatalogs(country);
});
// Restore the last manually chosen country (beats the geo suggestion).
try {
  const saved = localStorage.getItem('nimshop_country');
  if (saved && /^[A-Za-z]{2}$/.test(saved)) { country = saved.toUpperCase(); userChoseCountry = true; $('#countrySel').value = country; }
} catch {}
$('#sortSel').addEventListener('change', (e) => { sortKey = e.target.value; renderGrid(); });
$('#hideOOS').addEventListener('change', (e) => { hideOutOfStock = e.target.checked; renderGrid(); });

function normalizeList(data) {
  // Brand rows (catalog/search responses) → products; already-normalized
  // arrays pass through untouched.
  if (!data) return [];
  if (data.categories) return flattenBrands(data, country);
  if (Array.isArray(data)) {
    if (data.length > 0 && data[0].family && !data[0].name) return flattenBrands(data, country);
    return data;
  }
  return [];
}

async function loadCatalogs(c = country) {
  // Same stale-callback guard as renderGrid: never drive the grid after
  // the visitor left the shop page.
  if (!document.getElementById('gridWrap')) return;
  country = c;
  const results = await Promise.allSettled([listGiftCards(c, false), listTopups(c, false), listEsims(c, false)]);
  catalogs.gift_card = results[0].status === 'fulfilled' ? flattenBrands(results[0].value, country) : [];
  catalogs.phone_refill = results[1].status === 'fulfilled' ? flattenBrands(results[1].value, country) : [];
  catalogs.esim = results[2].status === 'fulfilled' ? flattenBrands(results[2].value, country) : [];
  if (results.every((r) => r.status === 'rejected')) {
    replaceChildren($('#gridWrap'), errorState('Catalog could not be loaded: ' + (results[0].reason?.message || ''), () => loadCatalogs(country)));
    return;
  }
  renderGrid();
}

function allProducts() { return [...(catalogs.gift_card || []), ...(catalogs.phone_refill || []), ...(catalogs.esim || [])]; }

function productPriceValue(p) {
  // For sorting low to high — use min_value, but ensure it's numeric and not 0
  return p.min_value || 0;
}

function sortProducts(items) {
  const arr = [...items];
  if (sortKey === 'price-asc') {
    arr.sort((a, b) => {
      const av = productPriceValue(a);
      const bv = productPriceValue(b);
      if (av === 0 && bv === 0) return 0;
      if (av === 0) return 1;
      if (bv === 0) return -1;
      return av - bv;
    });
  } else if (sortKey === 'price-desc') {
    arr.sort((a, b) => {
      const av = productPriceValue(a);
      const bv = productPriceValue(b);
      if (av === 0 && bv === 0) return 0;
      if (av === 0) return 1;
      if (bv === 0) return -1;
      return bv - av;
    });
  } else if (sortKey === 'name') {
    arr.sort((a, b) => (a.name || '').localeCompare(b.name || ''));
  }
  return arr;
}

function productPriceText(p) {
  // Show correct currency — not always USD — use original min_raw like TRY100, €10, $5
  if (p.min_raw && p.max_raw && p.min_raw !== p.max_raw) return `${p.min_raw} - ${p.max_raw}`;
  if (p.min_raw) return p.min_raw;
  return p.currency ? `in ${p.currency}` : '';
}

// Card nodes are cached per product so a re-render (sort change, rate
// refresh, geo reload) only REORDERS existing cards instead of rebuilding
// them. Images therefore never reload and never flash at their intrinsic
// size — the classic "big image, then it shrinks" mobile bug.
const cardCache = new Map();
function cachedCard(p) {
  const key = `${p.id}|${p.country}`;
  let node = cardCache.get(key);
  if (!node) {
    node = productCard(p);
    cardCache.set(key, node);
  }
  return node;
}

/* ---------------- Missing-product memory (self-healing list) ----------------
 * product.js records families that answered "not available"; renderGrid
 * hides them so nobody else clicks into the same dead end. Session-scoped
 * and self-expiring — the card returns if the product comes back.
 */
function missingProducts() {
  try {
    const arr = JSON.parse(sessionStorage.getItem('nimshop_missing') || '[]');
    const cutoff = Date.now() - 6 * 3600 * 1000;
    return arr.filter((m) => m.at > cutoff);
  } catch { return []; }
}
function isMissingProduct(id, country) {
  return missingProducts().some((m) => m.id === id && m.country === country);
}
function recordMissingProduct(id, country) {
  try {
    const arr = missingProducts();
    if (!arr.some((m) => m.id === id && m.country === country)) {
      arr.push({ id, country, at: Date.now() });
      sessionStorage.setItem('nimshop_missing', JSON.stringify(arr.slice(-100)));
    }
  } catch {}
}

function renderGrid() {
  const wrap = $('#gridWrap');
  // Stale-callback guard: a country/sort/geo callback from a PREVIOUS page
  // view resolves after the visitor navigated away — #gridWrap no longer
  // exists and render must be a no-op (the old code crashed on clear(null)).
  if (!wrap) return;
  let items;
  if (searchTerm && searchResults) items = normalizeList(searchResults);
  else items = activeCat === 'all' ? allProducts() : (catalogs[activeCat] || []);
  if (hideOutOfStock) items = items.filter(p => p.in_stock !== false);
  // Self-healing list: cards whose product page proved unavailable are
  // hidden for the rest of the session (product.js records them).
  items = items.filter((p) => !isMissingProduct(p.id, p.country));
  if (!items.length) {
    replaceChildren(wrap, emptyState({ iconName: 'search', title: searchTerm ? 'No results' : 'Nothing here yet', text: searchTerm ? `No products matched “${searchTerm}”.` : (hideOutOfStock ? 'No in-stock products. Uncheck "Hide out of stock" to see all.' : 'The catalog is empty right now.') }));
    return;
  }
  items = sortProducts(items);
  const grid = el('div.grid.products.fade-in');
  replaceChildren(wrap, grid);
  // CHUNKED RENDER: appending 60+ cards in one go was the single 63ms main-
  // thread task Lighthouse flagged (TBT). Batches of 12 per frame render
  // progressively and keep every task under ~15ms; the isConnected guard
  // drops the loop the moment the visitor leaves the page.
  const BATCH = 12;
  let i = 0;
  const step = () => {
    if (!grid.isConnected) return;
    const end = Math.min(i + BATCH, items.length);
    for (; i < end; i++) grid.appendChild(cachedCard(items[i]));
    if (i < items.length) requestAnimationFrame(step);
  };
  step();
}

function productCard(p) {
  const meta = {
    gift_card: { cls: 'thumb-gc', icon: 'gift' },
    phone_refill: { cls: 'thumb-tu', icon: 'phone' },
    esim: { cls: 'thumb-es', icon: 'globe' },
  }[p.type] || { cls: 'thumb-gc', icon: 'bag' };

  const countryChip = el('span.chip', { style: { display: 'inline-flex', alignItems: 'center', gap: '4px', maxWidth: '160px' } },
    el('span', { text: flag(p.country), style: { flexShrink: '0' } }),
    el('span', { text: `${p.country} ${countryName(p.country)}`.trim(), style: { overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap', maxWidth: '130px' } })
  );

  const prefetch = () => { getProduct(p.id, p.country).catch(() => {}); };
  // REAL anchor: right-click -> "Open in new tab", middle-click, and
  // copy-link all work natively. The shell still intercepts plain left
  // clicks for instant SPA navigation; out-of-stock cards block the click.
  const productHref = `/product?id=${encodeURIComponent(p.id)}&country=${encodeURIComponent(p.country)}`;
  const card = el('a.product-card', {
    href: p.in_stock !== false ? productHref : 'javascript:void(0)',
    'aria-label': `View ${p.name}`,
    on: {
      // Prefetch the product page's data on hover/focus/touch: by the time
      // the visitor clicks, the catalog is already in cache — the product
      // page opens near-instantly.
      mouseenter: prefetch,
      focus: prefetch,
      touchstart: prefetch,
      click: (e) => { if (p.in_stock === false) { e.preventDefault(); toast('This one is sold out right now.', 'info'); } },
    },
  },
    el('div.thumb.' + meta.cls, { style: p.bg_color ? { background: p.bg_color } : {} }, productThumb(p)),
    el('div.p-name', { text: p.name, title: p.name, style: { wordBreak: 'break-word', overflow: 'hidden', textOverflow: 'ellipsis', lineHeight: '1.2' } }),
    el('div.p-meta', { style: { display: 'flex', gap: '6px', flexWrap: 'wrap', alignItems: 'center' } }, countryChip, el('span.p-price', { text: productPriceText(p), style: { fontSize: '0.8rem', whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis', maxWidth: '110px' } })),
    p.in_stock === false ? el('div.oos', { text: 'OUT OF STOCK' }) : null,
  );
  return card;
}

loadCatalogs(country);
(async () => {
  try {
    const geo = await getGeo();
    // The geo lookup may resolve AFTER the visitor navigated away — the
    // country selector no longer exists; updating a dead page crashed.
    if (!document.getElementById('countrySel')) return;
    const cc = String((geo && geo.country) || '').toUpperCase();
    // Never override a manual pick; only suggest when the visitor hasn't chosen.
    if (!cc || cc === country || userChoseCountry) return;
    const sel = $('#countrySel');
    if (!sel || ![...sel.options].some((o) => o.value === cc)) return;
    country = cc;
    sel.value = country;
    const note = $('#geoNote');
    if (note) note.textContent = geo.cloudflare ? 'Showing products for your location (via Cloudflare).' : 'Showing products for your location.';
    loadCatalogs(country);
  } catch {}
})();

