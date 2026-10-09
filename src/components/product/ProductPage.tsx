import { supplierIssues } from '../../lib/supplierProblems';
import { CashbackFeeNotice } from '../checkout/CashbackFeeNotice';
import { productMoney } from '../../lib/productMoney';
/**
 * ProductPage.tsx — React port of pages/product.js. Loads a product family by
 * id + country, renders the hero, denomination chooser (packages grid or range
 * slider/type-in), quantity stepper, live NIM buy label, how-to-redeem card,
 * and wires Add to cart + Buy (via openSingleBuyFlow).
 */
import { useCallback, useEffect, useMemo, useState } from 'react';
import { Icon } from '../ui/Icon';
import { UnifiedThumb } from '../ui/UnifiedThumb';
import { FlagMark } from '../ui/FlagMark';
import { AppRoot } from '../AppRoot';
import { friendlyApiMessage, getProduct, getNimRate, getFXRates, getProductPrice, onRatesChange } from '../../lib/api';
import { loadCashbackBps, cashbackEarnLine } from '../../lib/cashback';
import { StakerCashbackLine } from '../staker/StakerCashback';
import { WalletBalance } from '../wallet/WalletBalance';
import { brandMetaFor } from '../../lib/catalogMeta';
import { useCartSafe } from '../../lib/cartStore';
import { useSheet, useToast } from '../AppProviders';
import {
  countryName,
  fmtNIM,
  fmtMoney,
  queryParam,
  cleanSupplierTerms,
  stripCryptoCopy,
  stripHtml,
  enCopy,
  MAX_QTY,
} from '../../lib/format';
import { mapKind, extractLogo } from '../../lib/catalog';
import { openSingleBuyFlow } from '../checkout/CheckoutFlow';
import { useWalletBalance } from '../wallet/WalletBalance';
import { guardLowBalance } from '../wallet/LowBalanceSheet';
import { safeRichHTML } from '../../lib/format';
import { useRouter } from '../../lib/router';
import { useT, t as i18nT } from '../../i18n';
import { asset, pagePath } from '../../lib/asset';

function cleanName(name: unknown): string {
  if (!name) return '';
  let s = String(name).trim();
  const md = s.match(/^\[(.+?)\]\(.+?\)$/);
  if (md) s = md[1].trim();
  return s;
}

/**
 * Delivery chips. These used to be baked English sentences on a module-level
 * function; they are i18n keys now so they follow the switcher. Callers resolve
 * them with t() — see chipsOf().
 */
const CHIPS: Record<string, { hero: string; bolt: string; lock: string }> = {
  phone_refill: {
    hero: 'productPage.chipInstantTopup',
    bolt: 'productPage.chipInstantTopup',
    lock: 'productPage.chipCreditNumber',
  },
  esim: {
    hero: 'productPage.chipInstantEsim',
    bolt: 'productPage.chipEsimEmail',
    lock: 'productPage.chipQrEmail',
  },
  gift_card: {
    hero: 'productPage.chipInstantEmail',
    bolt: 'productPage.chipInstantEmail',
    lock: 'productPage.chipCodeEmail',
  },
};

function chipKeys(type: string) {
  return CHIPS[type] || CHIPS.gift_card;
}

/**
 * Fallback marketing copy when the supplier sends no usable description.
 * Returns a KEY + interpolation params instead of a rendered sentence so the
 * text re-translates the instant the language changes.
 */
function fallbackBlurbKey(type: string): string {
  return type === 'phone_refill'
    ? 'productPage.blurbTopup'
    : type === 'esim'
      ? 'productPage.blurbEsim'
      : 'productPage.blurbGift';
}

function fallbackBlurbParams(name: string, country: string) {
  return {
    brand: name || i18nT('productPage.brandThisProduct'),
    where: countryName(country) || country || i18nT('productPage.whereThisCountry'),
  };
}

function descriptionOf(desc: unknown, country: string, type: string, name: string): { description: string; descKey?: string; descParams?: Record<string, string> } {
  const r = cleanDescription(desc, country, type, name);
  return 'key' in r ? { description: '', descKey: r.key, descParams: r.params } : { description: r.text };
}

/**
 * Returns either the supplier's own text (left as-is: it is third-party copy)
 * or a `{ key, params }` pair for our translated fallback.
 */
function cleanDescription(desc: unknown, country: string, type = 'gift_card', name = ''): { text: string } | { key: string; params: Record<string, string> } {
  const fb = () => ({ key: fallbackBlurbKey(type), params: fallbackBlurbParams(name, country) });
  if (!desc) return fb();
  let s = stripHtml(String(desc));
  // Same rail-scrub the supplier terms use (see stripCryptoCopy in lib/format):
  // "Pay with Bitcoin, Litecoin … and Arbitrum" never reaches the screen.
  s = stripCryptoCopy(s);
  s = enCopy(s);
  if (!s || s.length < 10) return fb();
  if (type === 'phone_refill' && /gift card/i.test(s)) return fb();
  return { text: s };
}


type Pkg = {
  package_id: string;
  value: number;
  currency: string;
  denomination: string;
  localized_denomination: string;
  coin_amount: string;
  coin: string;
  brand?: string;
  brand_id?: string;
  category?: string;
  family?: string;
  delivery_type?: string;
};

type ProductDetail = {
  id: string;
  family: string;
  name: string;
  bg_color: string;
  rich: {
    description: string;
    howToRedeem: string;
    terms: string;
    redeemGeo: string;
    note: string;
    brandUrl: string;
  } | null;
  type: string;
  delivery_type?: string;
  country: string;
  currency: string;
  packages: Pkg[] | null;
  range: { min: number; max: number; step: number; currency: string } | null;
  in_stock: boolean;
  /** e-money family: provider may request identity verification (KYC). */
  is_e_money?: boolean;
  logo_url: string;
  images: { large?: string };
  /** Supplier copy, or '' when the translated fallback key below is used. */
  description: string;
  /** When set, the description is OUR string: render t(key, params). */
  descKey?: string;
  descParams?: Record<string, string>;
  _family: any;
  _families?: any[];
};

function familyToProduct(family: any, fallbackId: string, countryParam: string): ProductDetail | null {
  if (!family) return null;
  const familyName = cleanName(family.family || family.brand || fallbackId);
  const countryCode = (family.country_code || countryParam).toUpperCase();
  const logo = extractLogo(family);
  const products = family.products || [];
  const packages: Pkg[] = [];
  let range: ProductDetail['range'] = null;
  let currency = 'USD';
  for (const p of products) {
    // by_account = utility/bill payments needing an account number the shop
    // never collects — cannot be fulfilled, so never offered.
    if (String(p.delivery_type || '').toLowerCase() === 'by_account') continue;
    if (p.range) {
      range = { min: p.range.min, max: p.range.max, step: p.range.step_size || p.range.step || 1, currency: p.range.currency || 'USD' };
      currency = p.range.currency || currency;
      continue;
    }
    const money = productMoney(p);
    const faceValue = money.value;
    const faceCurrency = money.currency;
    let denomLabel = p.denomination || p.localized_denomination || '';
    if (!denomLabel) denomLabel = p.product_id || '';
    if (faceValue > 0 || denomLabel) {
      packages.push({
        package_id: p.product_id,
        value: faceValue,
        currency: faceCurrency,
        denomination: denomLabel || `${faceValue} ${faceCurrency}`.trim(),
        localized_denomination: p.localized_denomination || denomLabel,
        coin_amount: p.coin_amount || p.original_coin_amount || '',
        coin: p.coin || 'BTC',
        brand: p.__brand || family.brand || familyName,
        brand_id: p.__brand_id || family.brand_id || '',
        category: p.__category || family.category || '',
        family: family.family || familyName,
        delivery_type: p.delivery_type || family.delivery_type || '',
      });
    }
  }
  const bgColor = family.bg_color || '';
  const richRaw = family.rich_description || null;
  // Kind follows the SUPPLIER delivery channel when every product agrees on
  // one; category is only the fallback. (Root fix for phone/email mix-ups.)
  const dts = new Set<string>(products.map((p: any) => String(p.delivery_type || '').toLowerCase()).filter(Boolean));
  let kind = mapKind(family.kind, family.category);
  if (dts.size === 1) {
    const dt = Array.from(dts)[0];
    if (dt === 'by_phone') kind = 'phone_refill';
    else if (dt === 'by_email' && kind === 'phone_refill') kind = 'gift_card';
  }
  const familyDeliveryType = dts.size === 1 ? Array.from(dts)[0] : '';
  const marketing = (richRaw && (richRaw.description || richRaw.note)) || '';
  return {
    id: familyName,
    family: familyName,
    name: familyName,
    bg_color: bgColor,
    rich: richRaw
      ? {
          description: enCopy(richRaw.description || ''),
          howToRedeem: enCopy(richRaw.how_to_redeem || ''),
          terms: enCopy(stripCryptoCopy(richRaw.term_and_conditions || richRaw.product_tc_hint || '', { scrubNames: false })),
          redeemGeo: enCopy(richRaw.redeem_geo || ''),
          note: enCopy(richRaw.note || ''),
          brandUrl: richRaw.brand_url || '',
        }
      : null,
    type: kind,
    delivery_type: familyDeliveryType,
    country: countryCode,
    currency,
    packages: packages.length ? packages : null,
    range,
    in_stock: !family.is_out_of_stock,
    is_e_money: String(family.category || '').toLowerCase() === 'e-money',
    logo_url: logo,
    images: logo ? { large: logo } : {},
    ...descriptionOf(marketing, countryParam, kind, familyName),
    _family: family,
  };
}

function familiesToProduct(families: any[], fallbackId: string, countryParam: string): ProductDetail | null {
  if (!families || !families.length) return null;
  if (families.length === 1) return familyToProduct(families[0], fallbackId, countryParam);
  const base = families.find((f) => f && (f.family || f.brand)) || families[0];
  const familyName = cleanName(base.family || base.brand || fallbackId);
  // Merge products across all families with same family name, tagging each product with its originating brand
  const allProducts: any[] = [];
  for (const f of families) {
    const prods = Array.isArray(f.products) ? f.products : [];
    for (const p of prods) {
      allProducts.push({
        ...p,
        __brand: f.brand || f.family || familyName,
        __brand_id: f.brand_id || '',
        __category: f.category || '',
        __kind: f.kind || '',
        __family: f.family || familyName,
      });
    }
  }
  const mergedFamily: any = {
    ...base,
    family: familyName,
    brand: base.brand || familyName,
    products: allProducts,
    // In stock if ANY variant is in stock
    is_out_of_stock: families.every((f) => !!f.is_out_of_stock),
    _families: families,
  };
  // Prefer richest description among variants
  for (const f of families) {
    if (f.rich_description && !mergedFamily.rich_description) mergedFamily.rich_description = f.rich_description;
    if (f.product_tc && !mergedFamily.product_tc) mergedFamily.product_tc = f.product_tc;
    if (f.logo_url && !mergedFamily.logo_url) mergedFamily.logo_url = f.logo_url;
  }
  const detail = familyToProduct(mergedFamily, fallbackId, countryParam);
  if (detail) {
    (detail as any)._families = families;
    // ANY e-money variant flags the merged detail (base may be the
    // non-e-money duplicate of the same family).
    detail.is_e_money = families.some((f: any) => String(f?.category || '').toLowerCase() === 'e-money');
  }
  return detail;
}

/** Redeem instructions — i18n keys, resolved at render so a language switch is
 *  instant. Shape mirrors the old literal map: title / 3 steps / note. */
const REDEEM_STEPS: Record<string, { title: string; steps: string[]; note: string }> = {
  gift_card: {
    title: 'productPage.redeemTitle',
    steps: ['productPage.redeemStep1', 'productPage.redeemStep2', 'productPage.redeemStep3'],
    note: 'productPage.redeemNote',
  },
  phone_refill: {
    title: 'productPage.worksTitle',
    steps: ['productPage.topupStep1', 'productPage.topupStep2', 'productPage.topupStep3'],
    note: 'productPage.topupNote',
  },
  esim: {
    title: 'productPage.installTitle',
    steps: ['productPage.redeemStep1', 'productPage.esimStep2', 'productPage.esimStep3'],
    note: 'productPage.esimNote',
  },
};

function recordMissing(id: string, country: string) {
  try {
    const arr = JSON.parse(sessionStorage.getItem('nimshop_missing:v2') || '[]');
    if (!arr.some((m: any) => m.id === id && m.country === country)) {
      arr.push({ id, country, at: Date.now() });
      sessionStorage.setItem('nimshop_missing:v2', JSON.stringify(arr.slice(-100)));
    }
  } catch {}
}

export function ProductPageShell() {
  return (
    <AppRoot activeKey="product">
      <ProductPage />
    </AppRoot>
  );
}

export function ProductPage() {
  const { t } = useT();
  const id = queryParam('id');
  const countryParam = queryParam('country');
  const [product, setProduct] = useState<ProductDetail | null>(null);
  const [unavailable, setUnavailable] = useState(false);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [nimUsd, setNimUsd] = useState<number | null>(null);
  const [btcUsd, setBtcUsd] = useState<number | null>(null);
  const [fx, setFx] = useState<Record<string, number> | null>(null);
  const [cbBps, setCbBps] = useState(100);

  // selection state
  const hasPackages = !!(product && product.packages && product.packages.length);
  const [pkg, setPkg] = useState('');
  const [value, setValue] = useState(0);
  const [qty, setQty] = useState(1);
  const [denomination, setDenomination] = useState('');
  const [supplierPrice, setSupplierPrice] = useState<any>(null);
  // priceState is stored as { key, params } (or raw text from the API) so the
  // status line re-translates with the rest of the page.
  const [, setPriceState] = useState<{ key?: string; params?: Record<string, string | number>; raw?: string }>({ key: 'productPage.priceSelectAmount' });

  const cart = useCartSafe();
  // The quantity stepper respects the account's daily order allowance too —
  // you can pick up to what's left of it, never beyond (limit ≠ 10).
  const maxQty = cart && cart.orderLimit > 0 ? Math.min(MAX_QTY, Math.max(0, cart.orderLimit - cart.count)) : MAX_QTY;
  const { openSheet, closeSheet } = useSheet();
  const { toast } = useToast();
  const { navigate } = useRouter();
  // The wallet reading the strip above the buttons already shows (shared
  // state, no extra request) — the same numbers feed the pre-purchase warning.
  const { state: walletState } = useWalletBalance();

  // init selection when product loads
  useEffect(() => {
    if (!product) return;
    if (product.packages && product.packages.length) {
      const first = product.packages[0];
      setPkg(first.package_id);
      setDenomination(first.denomination || `${first.value} ${first.currency || product.currency}`.trim());
      setValue(first.value);
    } else if (product.range) {
      setDenomination('range');
      setValue(product.range.min);
    }
  }, [product]);

  const load = useCallback(async () => {
    setLoading(true);
    setError('');
    setUnavailable(false);
    if (!id) {
      setError(t('productPage.noProductSelected'));
      setLoading(false);
      return;
    }
    if (!countryParam || countryParam.length !== 2) {
      setError(t('productPage.missingCountry'));
      setLoading(false);
      return;
    }
    // rates in parallel
    Promise.allSettled([getNimRate(), getFXRates(), loadCashbackBps()]).then((r) => {
      if (r[0].status === 'fulfilled') {
        setNimUsd(Number((r[0].value as any).usd_per_nim) || null);
        setBtcUsd(Number((r[0].value as any).usd_per_btc) || null);
      }
      if (r[1].status === 'fulfilled') setFx((r[1].value as any).usd_per_unit || null);
      if (r[2].status === 'fulfilled') setCbBps(r[2].value as number);
    });
    try {
      const data = await getProduct(id, countryParam, { force: true });
      let detail: ProductDetail | null = null;
      const raw: any = data as any;
      if (Array.isArray(raw) && raw.length > 0) detail = familiesToProduct(raw, id, countryParam);
      else if (raw && raw.products) detail = familyToProduct(raw, id, countryParam);
      if ((!detail && !(Array.isArray(raw) && raw.length === 0)) || (detail && !detail.id)) {
        throw new Error(t('productPage.unreadableResponse'));
      }
      if (!detail) {
        recordMissing(id, countryParam);
        setUnavailable(true);
        setLoading(false);
        return;
      }
      // A recovered product must reappear in the home catalog immediately.
      try {
        const missing = JSON.parse(sessionStorage.getItem('nimshop_missing:v2') || '[]');
        sessionStorage.setItem('nimshop_missing:v2', JSON.stringify(missing.filter((m: any) => m.id !== id || m.country !== countryParam)));
      } catch { /* storage is best-effort */ }
      if (!detail.bg_color || !detail.logo_url) {
        const meta = await brandMetaFor(detail.name || id, countryParam).catch(() => ({ bg: '', logo: '' }));
        if (!detail.bg_color && meta.bg) detail = { ...detail, bg_color: meta.bg };
        if (!detail.logo_url && meta.logo) detail = { ...detail, logo_url: meta.logo };
      }
      // Seed the selection IN THE SAME BATCH as the product. Doing it in a
      // separate effect left one render where the range box was mounted with
      // value=0 (it printed "0" instead of the range minimum on first paint).
      if (detail.packages && detail.packages.length) {
        const first = detail.packages[0];
        setPkg(first.package_id);
        setDenomination(first.denomination || `${first.value} ${first.currency || detail.currency}`.trim());
        setValue(first.value);
      } else if (detail.range) {
        setDenomination('range');
        setValue(detail.range.min);
      }
      setProduct(detail);
    } catch (err) {
      const notFound = Number((err as { status?: number })?.status) === 404;
      if (notFound) {
        recordMissing(id, countryParam);
        setUnavailable(true);
      } else {
        setError(friendlyApiMessage(err, t('errors.loadProduct')));
      }
    } finally {
      setLoading(false);
    }
  }, [id, countryParam, t]);

  useEffect(() => {
    load();
  }, [load]);

  // The displayed NIM price is coin_amount(BTC) x btc_usd / nim_usd. The market
  // leg starts from the build-time snapshot (instant paint); when the live API
  // corrects it, the number on screen follows. Prices the buyer reads must not
  // stay pinned to whatever the last deploy baked.
  useEffect(
    () =>
      onRatesChange((r) => {
        if (Number(r.usd_per_nim) > 0) setNimUsd(Number(r.usd_per_nim));
        if (Number(r.usd_per_btc) > 0) setBtcUsd(Number(r.usd_per_btc));
      }),
    []
  );

  // Price checking is a GET, not /quotes and not order creation. A debounced,
  // short-lived result cannot outlive the selected brand/country/face value.
  useEffect(() => {
    let alive = true; let timer: ReturnType<typeof setTimeout>;
    setSupplierPrice(null);
    // Only an actual structured range needs a face-value price lookup.
    // Fixed SKUs already carry a catalog coin estimate and are validated by
    // exact denomination at checkout. Never infer monetary type from a name
    // or label: a fixed SKU can validate successfully even when the legacy
    // face-value price endpoint returns 404.
    if (!product?.range || product.packages?.length || !Number.isFinite(value) || value <= 0) {
      setPriceState({ key: 'productPage.priceFixedSku' });
      return;
    }
    setPriceState({ key: 'productPage.priceChecking' });
    let attempts = 0; // bounded transient-failure guard: never hammer forever
    const check = async () => {
      try {
        const selectedPkg = product.packages?.find((p) => p.package_id === pkg) as any;
        const brandForPrice = selectedPkg?.brand || product._family?.brand || product.family || product.id;
        const price = await getProductPrice(brandForPrice, product.country, value);
        if (!alive) return;
        const remaining = Date.parse(price.price_expires_at || '') - Date.now();
        if (price.coin !== 'BTC' || remaining <= 0 || !Number.isFinite(remaining) || !(Number(price.coin_amount) > 0)) throw new Error('Price unavailable');
        setSupplierPrice(price);
        setPriceState({ key: 'productPage.priceChecked' });
        // Drop the old figure as soon as its freshness expires, before refetch.
        // DS172411 (setTimeout): closure only, never a string — no untrusted data is evaluated.
        timer = setTimeout(() => { setSupplierPrice(null); setPriceState({ key: 'productPage.priceRefreshing' }); check(); }, remaining);
      } catch (e: any) {
        if (!alive) return;
        setSupplierPrice(null);
        // A definite "this product can't be priced" (supplier 404) must never be
        // auto-retried every few seconds — that hammered the supplier and spammed
        // the server log. Stop and show a stable message; re-select to re-check.
        if (supplierIssues(e).length) { setPriceState({ raw: friendlyApiMessage(e) }); return; }
        const notAvailable = e && (e.status === 404 || (e.data && e.data.not_available) || (e && /no live supplier price/i.test(String(e.detail || e.message || ''))));
        if (notAvailable) {
          setPriceState({ key: 'productPage.priceNone' });
          return;
        }
        // Transient failures (429/5xx/network) get a few spaced retries, then stop.
        attempts += 1;
        if (attempts >= 4) {
          setPriceState({ key: 'productPage.priceRetryFailed' });
          return;
        }
        setPriceState({ key: 'productPage.priceCheckingAgain' });
        timer = setTimeout(check, 30000);
      }
    };
    timer = setTimeout(check, 450);
    return () => { alive = false; clearTimeout(timer); };
    // Intentionally keyed on the primitives that invalidate the price. The
    // nested product fields (range/packages/brand) only change together with
    // product?.id, and re-running on every `product`/`pkg` object identity
    // would restart the 450 ms debounce on unrelated renders.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [product?.id, product?.country, value, denomination]);

  // ---------------- helpers for estimates ----------------
  const localToUSD = useCallback(
    (amount: number, code?: string | null) => {
      if (!amount || amount <= 0) return 0;
      const rate = fx && code ? fx[String(code).toUpperCase()] : null;
      if (!rate || rate <= 0) return Number(amount);
      return Number(amount) * rate;
    },
    [fx]
  );

  const nimPriceFromUSD = useCallback(
    (usd: number) => {
      if (!usd || usd <= 0) return '';
      if (nimUsd && nimUsd > 0) {
        const nim = Number(usd) / nimUsd;
        return `${fmtNIM(Math.round(nim), 0)} NIM`;
      }
      return '';
    },
    [nimUsd]
  );

  const nimPriceFromBTC = useCallback(
    (btcAmount: unknown) => {
      if (!btcAmount) return '';
      const btc = parseFloat(String(btcAmount));
      if (!btc || btc <= 0) return '';
      if (nimUsd && btcUsd && nimUsd > 0 && btcUsd > 0) {
        const nim = (btc * btcUsd) / nimUsd;
        return `${fmtNIM(Math.round(nim), 0)} NIM`;
      }
      return '';
    },
    [nimUsd, btcUsd]
  );

  const currentProductNIM = useCallback(() => {
    const q = qty || 1;
    const k = product && product.packages ? product.packages.find((x) => x.package_id === pkg) : null;
    if (k && k.coin_amount && nimUsd && btcUsd && nimUsd > 0 && btcUsd > 0) {
      const btc = parseFloat(String(k.coin_amount)) * q;
      if (btc > 0) return (btc * btcUsd) / nimUsd;
    }
    const usd = localToUSD((value || 0) * q, k ? k.currency : product?.currency);
    if (usd > 0 && nimUsd && nimUsd > 0) return usd / nimUsd;
    return 0;
  }, [product, pkg, value, qty, nimUsd, btcUsd, localToUSD]);

  const buyNIM = useMemo(() => {
    if (!product) return '';
    const q = qty || 1;
    if (supplierPrice?.coin_amount) {
      const estimate = nimPriceFromBTC(Number(supplierPrice.coin_amount) * q);
      if (estimate) return estimate;
    }
    const k = product.packages ? product.packages.find((x) => x.package_id === pkg) : null;
    if (k && k.coin_amount) {
      const t = nimPriceFromBTC(parseFloat(k.coin_amount) * q);
      if (t) return t;
    }
    return nimPriceFromUSD(localToUSD((value || 0) * q, k ? k.currency : product.currency));
  }, [product, pkg, value, qty, nimPriceFromBTC, nimPriceFromUSD, localToUSD, supplierPrice]);

  const buyLabel = useMemo(() => {
    if (!product) return '';
    const q = qty || 1;
    const nm = (product.name || '').length > 16 ? (product.name || '').slice(0, 15) + '…' : product.name || t('productPage.buyNameThisItem');
    // Nimiq Pay adds its network fee on top of the converted item amount.
    // Keep that visible on the product-card CTA too, not only after checkout
    // opens, so the first price the buyer sees is not mistaken for the final
    // amount.
    if (buyNIM) return q > 1
      ? t('productPage.buyLabelMultiWithPrice', { qty: q, name: nm, nim: buyNIM })
      : t('productPage.buyLabelWithPrice', { name: nm, nim: buyNIM });
    return q > 1
      ? t('productPage.buyLabelMultiNoPrice', { qty: q, name: nm })
      : t('productPage.buyLabelNoPrice', { name: nm });
  }, [product, qty, buyNIM, t]);

  // `cashbackEarnLine` translates through the imperative dictionary, so the
  // memo has to depend on `t` too — without it the line kept the previous
  // language until the product or the rate changed.
  const cashbackLine = useMemo(() => {
    if (!product) return '';
    return cashbackEarnLine(currentProductNIM(), cbBps);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [product, currentProductNIM, cbBps, t]);

  if (loading) {
    return (
      <div className="container">
        <div className="skeleton-row" style={{ display: 'flex', alignItems: 'center', gap: 12, marginBottom: 16 }}>
          <span className="skeleton-btn" style={{ width: 64, height: 32, borderRadius: 8, display: 'inline-block' }} />
        </div>
        <div className="pd pd-skeleton fade-in mt-2" aria-busy="true" aria-label={t('productPage.loadingProduct')}>
          <div className="pd-top">
            <div className="skeleton-box" style={{ width: 'clamp(120px, 40vw, 160px)', aspectRatio: '4 / 3', borderRadius: 12, flexShrink: 0 }} />
            <div className="pd-info" style={{ flex: 1 }}>
              <div className="skeleton-box" style={{ width: '60%', maxWidth: 320, height: 28, borderRadius: 8 }} />
              <div className="skeleton-box mt-1" style={{ width: '40%', maxWidth: 240, height: 22, borderRadius: 8 }} />
              <div className="skeleton-box mt-2" style={{ width: '92%', height: 12, borderRadius: 6 }} />
              <div className="skeleton-box mt-1" style={{ width: '70%', height: 12, borderRadius: 6 }} />
            </div>
          </div>
          <div className="card mt-2">
            <div className="skeleton-box" style={{ width: 180, height: 16, borderRadius: 8 }} />
            <div className="packages-grid mt-2" style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(140px, 1fr))', gap: 12 }}>
              {[0, 1, 2, 3].map((i) => (
                <div key={i} className="skeleton-box" style={{ height: 76, borderRadius: 12 }} />
              ))}
            </div>
          </div>
        </div>
      </div>
    );
  }

  if (unavailable) {
    return (
      <div className="container">
        <a className="btn btn-ghost btn-sm" href={pagePath("/")} style={{ display: 'inline-flex', alignItems: 'center', gap: '6px' }}>
          <Icon name="back" size={16} />
          <span className="btn-label">{t('nav.shop')}</span>
        </a>
        <div className="card fade-in mt-2">
          <div className="empty">
            <div className="empty-ico">
              <Icon name="search" size={32} />
            </div>
            <h3>{t('productPage.notAvailableTitle')}</h3>
            <p className="small muted">
              {t('productPage.notAvailableText', { country: countryName(countryParam) || countryParam || t('productPage.whereThisCountry') })}
            </p>
            <div className="row" style={{ gap: '10px', justifyContent: 'center', flexWrap: 'wrap' }}>
              <a className="btn btn-gold" href={pagePath("/")} style={{ display: 'inline-flex', alignItems: 'center', gap: '6px' }}>
                <Icon name="bag" size={16} />
                <span className="btn-label">{t('cartSheet.browseShop')}</span>
              </a>
              <button className="btn btn-outline" onClick={() => load()} style={{ display: 'inline-flex', alignItems: 'center', gap: '6px' }}>
                <Icon name="refresh" size={16} />
                <span className="btn-label">{t('common.tryAgain')}</span>
              </button>
            </div>
          </div>
        </div>
      </div>
    );
  }

  if (error || !product) {
    return (
      <div className="container">
        <a className="btn btn-ghost btn-sm" href={pagePath("/")} style={{ display: 'inline-flex', alignItems: 'center', gap: '6px' }}>
          <Icon name="back" size={16} />
          <span className="btn-label">{t('nav.shop')}</span>
        </a>
        <div className="alert error mt-2" style={{ display: 'flex', gap: '8px', alignItems: 'center' }}>
          <Icon name="alert" size={19} />
          <div>{error || t('errors.loadProduct')}</div>
        </div>
        <div className="row mt-2" style={{ gap: '10px', flexWrap: 'wrap' }}>
          <button className="btn btn-gold" type="button" onClick={() => load()}>
            {t('common.tryAgain')}
          </button>
          <a className="btn btn-outline" href={pagePath("/")}>
            {t('cartSheet.browseShop')}
          </a>
        </div>
      </div>
    );
  }

  const dead = !hasPackages && !product.range;

  /**
   * The product page's own gate. Owner (2026-10-06): the shortfall must ask the
   * buyer directly, "popup olarak … seçenek sunacaktı" — continue anyway,
   * refresh, or cancel — and it is the SELLING action that asks, not adding an
   * item to the cart (the cart is priced as a whole at checkout).
   */
  const askAffordable = () =>
    guardLowBalance({
      openSheet,
      targetNim: currentProductNIM(),
      availableNim: walletState.availableNim,
      ready: walletState.status === 'ready',
    });

  const doAddToCart = () => {
    if (!cart) return;
    const ok = cart.addToCart(
      {
        id: product.id,
        type: product.type,
        name: product.name,
        logo_url: product.logo_url,
        country: product.country,
        currency: product.currency,
        packages: product.packages || [],
        range: product.range,
      },
      { pkg, value, qty, denomination, coinAmount: Number(supplierPrice && Number(supplierPrice.coin_amount) > 0 ? supplierPrice.coin_amount : 0) }
    );
    if (ok === 'limit') {
      toast(
        t('productPage.cartLimitToast', { limit: cart.orderLimit, count: cart.count }),
        'error'
      );
      return;
    }
    if (!ok) {
      const foreign = cart.items[0]?.country;
      toast(
        t('productPage.cartCountryToast', { country: countryName(foreign) || foreign || t('productPage.cartCountryOther') }),
        'error'
      );
      return;
    }
    toast(t('productPage.addedToast', { name: product.name }), 'success');
  };

  // Effective kind follows the SELECTED package's supplier channel (mixed
  // families: e.g. DE Vodafone sells PIN vouchers by email AND direct credit
  // by phone under one name).
  const selPkgForKind = product.packages?.find((x) => x.package_id === pkg) as any;
  const selDT = String(selPkgForKind?.delivery_type || product.delivery_type || '').toLowerCase();
  const effectiveType = selDT === 'by_phone' ? 'phone_refill' : selDT === 'by_email' ? (product.type === 'esim' ? 'esim' : 'gift_card') : product.type;
  const info = REDEEM_STEPS[effectiveType] || REDEEM_STEPS.gift_card;
  const chips = chipKeys(effectiveType);

  const doBuy = async () => {
    if (!(await askAffordable())) return;
    const selectedPkgForBuy = product.packages?.find((x) => x.package_id === pkg) as any;
    openSingleBuyFlow({
      product: {
        id: product.id,
        name: product.name,
        type: effectiveType,
        country: product.country,
        currency: product.currency,
        pkg,
        value,
        qty,
        denomination,
        image: product.logo_url,
        brand: selectedPkgForBuy?.brand || (product as any)._family?.brand || product.name,
        brand_id: selectedPkgForBuy?.brand_id || (product as any)._family?.brand_id || '',
        category: selectedPkgForBuy?.category || (product as any)._family?.category || '',
        delivery_type: selectedPkgForBuy?.delivery_type || (product as any).delivery_type || '',
      } as any,
      openSheet,
      closeSheet,
      toast,
      onNavigateOrders: () => {
        navigate('/orders');
      },
    });
  };

  const termsText = cleanSupplierTerms(product._family ? product._family.product_tc : '');

  return (
    <div className="container">
      <div className="pd fade-in">
        <a className="pd-back btn btn-ghost btn-sm" href={pagePath("/")} style={{ display: 'inline-flex', alignItems: 'center', gap: '6px' }}>
          <Icon name="back" size={16} />
          <span className="btn-label">{t('nav.shop')}</span>
        </a>
        <div className="pd-top">
          <div className="pd-image" style={{ position: 'relative' }}>
            <UnifiedThumb src={product.logo_url || ''} alt={product.name} bg={product.bg_color || 'rgb(255,255,255)'} />
            {product.is_e_money && (
              <div className="kyc-badge" style={{ position: 'absolute', left: '50%', transform: 'translateX(-50%)', bottom: 8, zIndex: 3, display: 'inline-flex', alignItems: 'center', gap: 5, padding: '4px 13px', borderRadius: 'var(--pill)', background: 'linear-gradient(180deg, var(--gold-300), var(--gold-500))', color: 'var(--on-gold)', border: '1.5px solid var(--line-strong)', boxShadow: 'inset 0 1px 0 rgba(255, 246, 232, 0.45), 2px 2px 0 rgba(78, 61, 40, 0.35)', fontSize: '0.72rem', lineHeight: 1.25, fontWeight: 900, letterSpacing: '0.05em', textTransform: 'uppercase', whiteSpace: 'nowrap' }}>
                <Icon name="shield" size={13} style={{ strokeWidth: 2.6 }} />
                {t('home.kycBadge')}
              </div>
            )}
          </div>
          <div className="pd-info">
            <h1 style={{ margin: '0 0 8px', fontSize: 'clamp(1.3rem, 1.2rem + 1vw, 1.8rem)', lineHeight: 1.2, wordBreak: 'break-word' }}>{product.name}</h1>
            {product.is_e_money && (
              <div className="kyc-note" role="note" style={{ display: 'flex', gap: '8px', alignItems: 'flex-start', margin: '0 0 10px', padding: '8px 12px', borderRadius: 'var(--r-l)', background: 'var(--gold-grad-soft)', border: '1px solid var(--line)', fontSize: 'var(--fs-sm)', lineHeight: 1.4, color: 'var(--ink)' }}>
                <span aria-hidden="true" style={{ fontWeight: 900, flex: '0 0 auto', color: 'var(--gold-600)' }}>ⓘ</span>
                <span>{t('productPage.kycNotice')}</span>
              </div>
            )}
            <div className="chips-row" style={{ display: 'flex', gap: '8px', flexWrap: 'wrap', alignItems: 'center' }}>
              <span className="chip" style={{ display: 'inline-flex', alignItems: 'center', gap: '6px' }}>
                {t(chips.hero)}
              </span>
              <span className="chip" style={{ display: 'inline-flex', alignItems: 'center', gap: '6px' }}>
                <img src={asset("/img/nimiq-hexagon.png?v=40")} draggable={false} alt="NIM" width={14} height={14} style={{ pointerEvents: "none", borderRadius: 3 }} /> {t('productPage.payWithNimChip')}
              </span>
              <span className="chip" style={{ display: 'inline-flex', alignItems: 'center', gap: '6px' }}>
                <FlagMark country={product.country} size={18} /> {countryName(product.country)}
              </span>
            </div>
            <p className="mt-2 small">
              {/* Mixed families must describe the selected package, not the family-wide kind. */}
              {product.descKey ? t(fallbackBlurbKey(effectiveType), product.descParams) : product.description}
            </p>
          </div>
        </div>

        <div className="card mt-2">
          <div className="card-title">{t('productPage.denominationTitle')}</div>
          {hasPackages ? (
            <PackageChooser
              product={product}
              pkg={pkg}
              setPkg={setPkg}
              setValue={setValue}
              setDenomination={setDenomination}
              nimPriceFromBTC={nimPriceFromBTC}
              nimPriceFromUSD={nimPriceFromUSD}
              localToUSD={localToUSD}
            />
          ) : product.range ? (
            <RangeChooser
              product={product}
              value={value}
              setValue={setValue}
              liveNim={supplierPrice && Number(supplierPrice.coin_amount) > 0 ? nimPriceFromBTC(supplierPrice.coin_amount) : null}
            />
          ) : (
            <div className="alert info" style={{ display: 'flex', gap: '8px', alignItems: 'center' }}>
              <Icon name="info" size={18} />
              <div className="small">{t('productPage.unavailableCountry', { country: countryName(product.country) || product.country })}</div>
            </div>
          )}
        </div>

        <div className="card mt-2">
          <div className="card-title">{t('productPage.quantityTitle')}</div>
          <div className="qty-stepper">
            <div className="stepper">
              <button className="qty-btn" onClick={() => setQty((q) => Math.max(1, q - 1))} disabled={qty <= 1} style={{ opacity: qty <= 1 ? 0.4 : 1 }}>
                −
              </button>
              <span className="qty-val">{qty}</span>
              <button className="qty-btn" onClick={() => setQty((q) => Math.min(maxQty, q + 1))} disabled={qty >= maxQty} style={{ opacity: qty >= maxQty ? 0.4 : 1 }}>
                +
              </button>
            </div>
            {cart && cart.orderLimit > 0 && qty >= maxQty && (
              <div className="xs faint mt-1">
                {maxQty === 0
                  ? t('productPage.limitReachedToday')
                  : t('productPage.limitRemaining', { max: maxQty })}
              </div>
            )}
          </div>
        </div>

        {/* Owner (2026-10-05): the balance used to appear only after an amount
            was chosen. It now sits above the buy buttons with the number of
            items the wallet actually covers — "neyi alıp alamayacağımı da
            göreyim". */}
        <WalletBalance variant="line" targetNim={currentProductNIM()} className="mt-2" />

        <HowToRedeemCard product={product} deliveryType={effectiveType} info={info} termsText={termsText} />

        {/* Owner (2026-10-04): same collapsed design as the pay screen —
            closed by default, opens on tap. */}
        <details className="checkout-details-min">
          <summary>{t('productPage.feesDetails')}</summary>
          <div style={{ marginTop: 8 }}><CashbackFeeNotice example="nim" /></div>
        </details>
        <div className="mt-3 row" style={{ gap: '12px' }}>
          <button className="btn btn-outline btn-block btn-lg" onClick={doAddToCart} disabled={dead} style={{ display: 'inline-flex', alignItems: 'center', gap: '8px', justifyContent: 'center', opacity: dead ? 0.5 : 1 }}>
            <Icon name="bag" size={14} />
            <span className="btn-label">{t('productPage.addToCart')}</span>
          </button>
          <button className="btn btn-gold btn-block btn-lg" onClick={doBuy} disabled={dead} title={dead ? t('productPage.notPurchasableTitle', { country: product.country || t('productPage.whereThisCountry') }) : undefined} style={{ display: 'inline-flex', alignItems: 'center', gap: '8px', justifyContent: 'center', opacity: dead ? 0.5 : 1 }}>
            <img src={asset("/img/nimiq-hexagon.png?v=40")} draggable={false} alt="NIM" width={14} height={14} style={{ pointerEvents: "none", borderRadius: 3 }} />
            <span className="btn-label">{dead ? t('productPage.notAvailableHere') : buyLabel}</span>
          </button>
        </div>
        {cashbackLine && (
          <div className="small muted mt-1" style={{ textAlign: 'center' }}>
            {cashbackLine}
          </div>
        )}
        <StakerCashbackLine />
      </div>
    </div>
  );
}

/**
 * CryptoRefills returns `denomination` as "25 TRY" but `localized_denomination`
 * sometimes flips the code in front for non-symbol currencies ("TRY25", "AED50").
 * Flip it back so the package button reads like money instead of a typo.
 */
function prettyFace(denom: string, localized: string): string {
  const raw = String(localized || denom || '').trim();
  const flipped = raw.match(/^([A-Z]{3})\s*([\d][\d.,\s]*)$/);
  return flipped ? `${flipped[2].trim()} ${flipped[1]}` : raw;
}

function PackageChooser({
  product,
  pkg,
  setPkg,
  setValue,
  setDenomination,
  nimPriceFromBTC,
  nimPriceFromUSD,
  localToUSD,
}: {
  product: ProductDetail;
  pkg: string;
  setPkg: (k: string) => void;
  setValue: (v: number) => void;
  setDenomination: (d: string) => void;
  nimPriceFromBTC: (b: unknown) => string;
  nimPriceFromUSD: (u: number) => string;
  localToUSD: (a: number, c?: string | null) => number;
}) {
  const { t } = useT();
  return (
    <div className="packages-grid mt-2" style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(140px, 1fr))', gap: 12, width: '100%', maxWidth: '100%' }}>
      {(product.packages || []).map((k) => {
        const isActive = k.package_id === pkg;
        const usdValue = k.value || 0;
        const nimPrice = (k.coin_amount ? nimPriceFromBTC(k.coin_amount) : '') || nimPriceFromUSD(localToUSD(usdValue, k.currency || product.currency));
        const faceLabel = prettyFace(k.denomination, k.localized_denomination) || (usdValue ? `$${usdValue}` : t('productPage.packageFallback'));
        return (
          <button
            key={k.package_id}
            className={'pd-pkg' + (isActive ? ' active' : '')}
            onClick={() => {
              setPkg(k.package_id);
              setDenomination(k.denomination || `${k.value} ${k.currency || product.currency}`.trim());
              setValue(k.value);
            }}
          >
            <div className="pd-pkg-face">{faceLabel}</div>
            {nimPrice ? (
              <div className="pd-pkg-nim">
                <img src={asset("/img/nimiq-hexagon.png?v=40")} draggable={false} alt="NIM" width={12} height={12} style={{ pointerEvents: "none", borderRadius: 2, verticalAlign: 'middle' }} />
                <span> {nimPrice}</span>
              </div>
            ) : (
              <div className="xs faint">{t('productPage.nimAtCheckout')}</div>
            )}
          </button>
        );
      })}
    </div>
  );
}

function RangeChooser({
  product,
  value,
  setValue,
  liveNim,
}: {
  product: ProductDetail;
  value: number;
  setValue: (v: number) => void;
  /** Authoritative per-item NIM from the supplier's live price for `value`. */
  liveNim?: string | null;
}) {
  const { t } = useT();
  const range = product.range!;
  const rangeCur = range.currency || product.currency;
  const rangeStep = range.step || 1;
  const snap = (v: number) => {
    let n = Math.round(v / rangeStep) * rangeStep;
    n = Math.max(range.min, Math.min(range.max, n));
    return n;
  };
  const [inputVal, setInputVal] = useState(String(value));
  // While the user is not typing, mirror the selected amount (which starts at
  // the range MIN). Only the raw keystrokes are shown while the field has
  // focus — otherwise the box kept showing the initial 0 after load.
  const [editing, setEditing] = useState(false);
  // The only number that matters is the supplier-confirmed price for the
  // selected amount. Never show a hand-rolled FX estimate as if it were real:
  // until the live price arrives we show a loading label instead. The old
  // min/max NIM band in this header is gone on purpose (owner request): the
  // chosen amount's own live NIM price by the buy button is the one number
  // the buyer needs, a seven-figure band only read as noise.
  const nimText = liveNim ? liveNim : t('productPage.nimPriceLoading');

  return (
    <div className="range-card" style={{ gridColumn: '1 / -1', width: '100%', boxSizing: 'border-box' }}>
      <div className="row between" style={{ flexWrap: 'wrap', gap: '8px' }}>
        <div>
          <div className="xs faint">{t('productPage.amountRange')}</div>
          <div className="strong">
            {fmtMoney(range.min, rangeCur)} - {fmtMoney(range.max, rangeCur)}
          </div>
        </div>
      </div>
      <div className="row" style={{ alignItems: 'flex-end', gap: '10px', flexWrap: 'wrap', marginTop: '10px' }}>
        <div className="field" style={{ marginBottom: 0, flex: '0 0 auto' }}>
          <label className="xs faint">{t('productPage.typeAmount', { ccy: rangeCur })}</label>
          <input
            className="input"
            type="text"
            inputMode="numeric"
            placeholder={String(range.min)}
            aria-label={t('productPage.customAmountAria', { ccy: rangeCur })}
            value={editing ? inputVal : String(value || range.min)}
            style={{ maxWidth: 160, minHeight: 44, fontWeight: 800, textAlign: 'center', fontFamily: 'var(--font-serif)', fontSize: '1.05rem' }}
            onChange={(e) => {
              const digits = e.target.value.replace(/[^0-9]/g, '');
              setInputVal(digits);
              if (!digits) return;
              setValue(snap(Number(digits)));
            }}
            onFocus={() => {
              setEditing(true);
              setInputVal(String(value || range.min));
            }}
            onBlur={() => {
              setEditing(false);
              setInputVal(String(value || range.min));
            }}
          />
        </div>
        <div className="xs faint" style={{ paddingBottom: '14px', flex: '1 1 160px', minWidth: 0 }}>
          {t('productPage.betweenAmounts', { min: fmtMoney(range.min, rangeCur), max: fmtMoney(range.max, rangeCur) })}
        </div>
      </div>
      <input
        type="range"
        min={range.min}
        max={range.max}
        step={range.step || 1}
        value={value}
        onChange={(e) => {
          setValue(Number(e.target.value));
          setInputVal(String(Number(e.target.value)));
        }}
        style={{ width: '100%', marginTop: '12px' }}
      />
      <div className="range-ends mt-1" style={{ display: 'flex', alignItems: 'center', gap: '10px', flexWrap: 'wrap' }}>
        <span className="small" style={{ flexShrink: 0 }}>{fmtMoney(range.min, rangeCur)}</span>
        <div style={{ flex: '1 1 auto', minWidth: 0, display: 'flex', flexDirection: 'column', alignItems: 'center', gap: '3px', textAlign: 'center' }}>
          <div style={{ fontFamily: 'var(--font-serif)', fontWeight: 900, fontSize: 'clamp(1.15rem, 1rem + 1vw, 1.5rem)', lineHeight: 1.2, color: 'var(--ink)', whiteSpace: 'nowrap' }}>
            {fmtMoney(value, rangeCur)}
          </div>
          <span className="big-nim" style={{ display: 'inline-flex', alignItems: 'center', justifyContent: 'center', gap: '6px', whiteSpace: 'normal', textAlign: 'center' }}>
            <img src={asset("/img/nimiq-hexagon.png?v=40")} draggable={false} alt="NIM" width={14} height={14} style={{ pointerEvents: "none", borderRadius: 2, verticalAlign: 'middle' }} />
            {nimText}
          </span>
        </div>
        <span className="small" style={{ flexShrink: 0, marginLeft: 'auto' }}>{fmtMoney(range.max, rangeCur)}</span>
      </div>
    </div>
  );
}

function HowToRedeemCard({ product, deliveryType, info, termsText }: { product: ProductDetail; deliveryType: string; info: { title: string; steps: string[]; note: string }; termsText: string }) {
  const { t } = useT();
  const rich = product.rich;
  const useRich = rich && rich.howToRedeem;
  // Use the same selected-package kind as the hero and redeem steps.
  const chips = chipKeys(deliveryType);
  return (
    <div className="card mt-2 howto">
      <div className="card-title" style={{ display: 'flex', alignItems: 'center', gap: '6px' }}>
        <Icon name={deliveryType === 'phone_refill' ? 'bolt' : deliveryType === 'esim' ? 'phone' : 'gift'} size={16} />
        <span>{t(info.title)} — {t('productPage.deliveredBy')}</span>
      </div>
      {rich && rich.redeemGeo && (
        <div className="alert info mt-1" style={{ marginBottom: 0, display: 'flex', gap: '6px', alignItems: 'center' }}>
          <Icon name="info" size={16} />
          <div className="small">
            {t('productPage.redeemGeo', { geo: stripHtml(rich.redeemGeo) })}{' '}
            <a href={pagePath("/")} style={{ fontWeight: 800 }}>{t('productPage.notInCountry', { country: countryName(product.country) || product.country || '' })}</a>
          </div>
        </div>
      )}
      {useRich ? (
        <div
          className="rich-html howto-rich"
          dangerouslySetInnerHTML={{ __html: safeRichHTML(rich.howToRedeem) }}
        />
      ) : (
        <ol className="howto-steps">
          {info.steps.map((st, i) => (
            <li key={i}>{t(st)}</li>
          ))}
        </ol>
      )}
      <div className="howto-note small" style={{ display: 'flex', gap: '6px', alignItems: 'center' }}>
        <Icon name="info" size={15} />
        <span>{t(info.note)}</span>
      </div>
      <div className="howto-mini row mt-1">
        <span className="chip" style={{ display: 'inline-flex', alignItems: 'center', gap: '4px' }}>
          <Icon name="bolt" size={13} /> {t(chips.bolt)}
        </span>
        <span className="chip" style={{ display: 'inline-flex', alignItems: 'center', gap: '4px' }}>
          <Icon name="lock" size={13} /> {t(chips.lock)}
        </span>
      </div>
      {(rich && rich.terms) || termsText ? (
        <details className="howto-terms mt-1">
          <summary className="xs">{rich && rich.terms ? t('productPage.termsFromSupplier') : t('productPage.supplierTerms')}</summary>
          {rich && rich.terms ? (
            <div className="xs muted rich-html" dangerouslySetInnerHTML={{ __html: safeRichHTML(rich.terms) }} />
          ) : (
            <div className="xs muted">{termsText}</div>
          )}
        </details>
      ) : null}
    </div>
  );
}
