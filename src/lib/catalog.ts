/**
 * catalog.ts — shared catalog normalization + product visuals logic. Ported
 * from catalog.js. The product object shape is preserved so the shop grid and
 * product detail behave identically.
 */
import { cleanFamilyName, parseCurrencyValue } from './format';
import { brandMetaFor, brandMetaForTitle } from './catalogMeta';

export type Product = {
  id: string;
  family: string;
  brand_id: string;
  name: string;
  type: 'gift_card' | 'phone_refill' | 'esim';
  country: string;
  currency: string;
  in_stock: boolean;
  is_out_of_stock: boolean;
  /** Refused at checkout recently: shown as out of stock, never hidden. */
  reported_oos?: boolean;
  logo_url: string;
  bg_color: string;
  min_raw: string;
  max_raw: string;
  min_value: number;
  max_value: number;
  range: { min: number; max: number; step: number; currency: string } | null;
  images: { large?: string };
  product_type: string;
  /** True when any supplier brand of this family sits in the e-money
   *  category — prepaid card products where the provider may ask for
   *  identity verification. Grid + product page show a KYC notice. */
  is_e_money: boolean;
};

/**
 * deliveryChannelOf — the ONE rule for "does this product need a phone or an email?".
 * Source of truth is the supplier's per-product `delivery_type`
 * ("by_phone" | "by_email" | "by_account"). Full scan of api.cryptorefills.com
 * (189 countries, 61,037 products, 2026-09-16): 809 products disagree with
 * their category, 33 families mix both. Category is ONLY the fallback.
 */
export type DeliveryChannelKind = 'phone' | 'email' | 'account';
export function deliveryChannelOf(p: { delivery_type?: string | null; type?: string | null; kind?: string | null; category?: string | null } | null | undefined): DeliveryChannelKind {
  const dt = String(p?.delivery_type || '').toLowerCase();
  if (dt === 'by_phone') return 'phone';
  if (dt === 'by_email') return 'email';
  if (dt === 'by_account') return 'account';
  if (p?.type === 'phone_refill') return 'phone';
  if (p?.type === 'gift_card' || p?.type === 'esim') return 'email';
  return mapKind(p?.kind, p?.category) === 'phone_refill' ? 'phone' : 'email';
}
/** True when the line is credited to a phone number. */
export function needsPhone(p: Parameters<typeof deliveryChannelOf>[0]): boolean { return deliveryChannelOf(p) === 'phone'; }

export function mapKind(kind?: string | null, category?: string | null): Product['type'] {
  const k = (kind || '').toLowerCase();
  const c = (category || '').toLowerCase();
  if (k === 'giftcard' || k === 'gift_card') return 'gift_card';
  if (k === 'mobile_recharge') return c === 'e-sim' ? 'esim' : 'phone_refill';
  if (c === 'e-sim' || k === 'esim') return 'esim';
  return 'gift_card';
}

export function extractLogo(family: { logo_url?: unknown; logo_base_url?: unknown } | null | undefined): string {
  if (!family) return '';
  const raw = String(family.logo_url || family.logo_base_url || '');
  const urlMatch = raw.match(/https?:\/\/[^\s)\]]+/);
  return urlMatch ? urlMatch[0] : raw.replace(/[[\]]/g, '');
}

function normalizeBrand(brand: any, country = 'US'): Product | null {
  if (!brand) return null;
  const familyRaw = brand.family || brand.family_name || brand.name || brand.id || '';
  const family = cleanFamilyName(familyRaw);
  if (!family) return null;
  const countryCode = (brand.country_code || brand.country || country).toUpperCase();
  const minParsed = parseCurrencyValue(brand.min);
  const maxParsed = parseCurrencyValue(brand.max);
  const currency = minParsed.currency || maxParsed.currency || brand.currency || 'USD';
  const logo = extractLogo(brand);
  return {
    id: family,
    family,
    brand_id: brand.brand_id || '',
    name: family,
    type: mapKind(brand.kind, brand.category),
    country: countryCode,
    currency,
    in_stock: !brand.is_out_of_stock,
    is_out_of_stock: !!brand.is_out_of_stock,
    logo_url: logo,
    bg_color: brand.bg_color || '#FFFFFF',
    min_raw: brand.min || '',
    max_raw: brand.max || '',
    min_value: minParsed.value,
    max_value: maxParsed.value,
    range: minParsed.value > 0 && maxParsed.value > 0 ? { min: minParsed.value, max: maxParsed.value, step: 1, currency } : null,
    images: logo ? { large: logo } : {},
    product_type: brand.product_type || 'digital',
    is_e_money: String(brand.category || '').toLowerCase() === 'e-money',
  };
}

/**
 * Marks cards the backend reports as sold out (a refused checkout). They read
 * "Out of stock" and stay on the shelf, so the buyer sees the state instead of
 * a dead card. Matching is case-insensitive on the family name.
 */
export function markReportedOutOfStock(products: Product[], families: string[]): Product[] {
  if (!families.length) return products;
  const set = new Set(families.map((f) => cleanFamilyName(f).trim().toLowerCase()));
  return products.map((p) =>
    set.has(String(p.family || '').trim().toLowerCase())
      ? { ...p, in_stock: false, is_out_of_stock: true, reported_oos: true }
      : p,
  );
}

export function flattenBrands(data: any, country = 'US'): Product[] {
  if (!data) return [];
  let brands: any[] = [];
  if (data.categories && Array.isArray(data.categories)) {
    for (const cat of data.categories) {
      if (cat.brands && Array.isArray(cat.brands)) brands.push(...cat.brands);
    }
  } else if (Array.isArray(data)) {
    brands = data;
  } else {
    return [];
  }
  // Group by family|country — a family can fan-out to multiple supplier brands
  // (e.g. Turk Telecom Data / Credits / Bundle, Vodafone TR 4 variants).
  // We must NOT drop variants: homepage shows one card per family, but that
  // card's price range and stock must be the MERGED view, and detail merging
  // (familiesToProduct) needs all brands' products. So we keep the full list
  // per key and synthesize a merged brand for the card.
  const grouped = new Map<string, any[]>();
  for (const b of brands) {
    const family = cleanFamilyName(b.family || '');
    if (!family) continue;
    const key = `${family.toLowerCase()}|${(b.country_code || country).toUpperCase()}`;
    if (!grouped.has(key)) grouped.set(key, []);
    grouped.get(key)!.push(b);
  }
  const out: Product[] = [];
  for (const list of grouped.values()) {
    if (list.length === 1) {
      const p = normalizeBrand(list[0], country);
      if (p) out.push(p);
      continue;
    }
    // De-duplicate exact same brand_id repeated across categories (e.g. Amazon.com.tr
    // appears in 4 categories with identical brand_id in fallback snapshot). Keep one
    // per distinct brand_id so a duplicated static snapshot doesn't inflate the card.
    const byBrandId = new Map<string, any>();
    for (const b of list) {
      const bid = String(b.brand_id || b.family || '').toLowerCase();
      if (!byBrandId.has(bid)) byBrandId.set(bid, b);
      else {
        const prev = byBrandId.get(bid);
        // Prefer in_stock variant within same brand_id
        if (prev.is_out_of_stock && !b.is_out_of_stock) byBrandId.set(bid, b);
      }
    }
    const distinct = [...byBrandId.values()];
    if (distinct.length === 1) {
      const p = normalizeBrand(distinct[0], country);
      if (p) out.push(p);
      continue;
    }
    // Fan-out: multiple distinct brand_ids under same family name (Turk Telecom,
    // Vodafone TR, H2O/SimpleMobile US etc). The single card must stay truthful
    // about stock (IN if any variant IN) but we keep the representative's price
    // display strings — merging min/max across unrelated categories (e.g. TRY vs
    // GB bundles) would produce a misleading "5GB - TRY2000" range. Detail page
    // (familiesToProduct) merges the actual SKUs, so the card's min/max is only
    // a teaser; correctness matters more there.
    const rep = distinct.find((b) => !b.is_out_of_stock) || distinct[0];
    const anyInStock = distinct.some((b) => !b.is_out_of_stock);
    const mergedBrand: any = {
      ...rep,
      is_out_of_stock: !anyInStock,
    };
    const p = normalizeBrand(mergedBrand, country);
    if (p) {
      // Preserve rep's parsed values, but ensure stock reflects merged.
      p.is_out_of_stock = !anyInStock;
      p.in_stock = anyInStock;
      // Any e-money variant flags the merged card (e.g. PCS Mastercard lives
      // in both e-commerce and e-money).
      p.is_e_money = distinct.some((b: any) => String(b.category || '').toLowerCase() === 'e-money');
      (p as any)._fanOutCount = distinct.length;
      (p as any)._brandIds = distinct.map((b) => b.brand_id);
      out.push(p);
    }
  }
  return out;
}

export { brandMetaFor, brandMetaForTitle };
