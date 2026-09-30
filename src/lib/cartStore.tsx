/**
 * cartStore.tsx — React context/store port of cart.js (localStorage cart).
 * Multi-product, adjustable quantity, per-item local-currency + NIM totals.
 * The checkout flow (delivery/quote-pay) is ported separately in
 * checkoutFlow.ts; this store owns the cart state and the cart sheet UI.
 */
import { createContext, useCallback, useContext, useEffect, useRef, useState, type ReactNode } from 'react';
import { COUNTRY_CCY, MAX_QTY, fmtMoney, fmtNIM, countryName } from './format';
import { cachedNimRate, cachedFX, getAccountLimits } from './api';
import { isAuthed } from './session';

export type CartItem = {
  id: string;
  type: string;
  name: string;
  image: string;
  bgColor: string;
  country: string;
  currency: string;
  pkg: string;
  value: number;
  denomination: string;
  coinAmount: number;
  unitUSD: number;
  qty: number;
  brand?: string;
  brand_id?: string;
  category?: string;
  /** supplier per-product delivery channel: by_phone | by_email | by_account */
  delivery_type?: string;
};

const KEY = 'nimshop_cart';

let memoryCart: CartItem[] = [];

export function readCart(): CartItem[] {
  try {
    const rows: unknown = JSON.parse(localStorage.getItem(KEY) || '[]');
    if (!Array.isArray(rows)) return [];
    const finite = (value: unknown) => Number.isFinite(Number(value)) ? Math.max(0, Number(value)) : 0;
    return rows.filter((row) => row && typeof row.id === 'string' && row.id.trim() &&
      typeof row.country === 'string' && /^[A-Za-z]{2}$/.test(row.country) && finite(row.qty) >= 1)
      .slice(0, 100).map((row) => ({
        ...row, country: row.country.toUpperCase(), qty: Math.min(MAX_QTY, Math.floor(finite(row.qty))),
        name: String(row.name || row.id), type: String(row.type || ''), image: String(row.image || ''),
        bgColor: String((row as any).bgColor || (row as any).bg || ''),
        currency: String(row.currency || ''), pkg: String(row.pkg || ''), denomination: String(row.denomination || ''),
        value: finite(row.value), coinAmount: finite(row.coinAmount), unitUSD: finite(row.unitUSD),
        brand: String(row.brand || ''), brand_id: String(row.brand_id || ''), category: String(row.category || ''), delivery_type: String((row as any).delivery_type || ''),
      }));
  } catch { return memoryCart; }
}

export function saveCart(next: CartItem[]): void {
  memoryCart = next;
  try { localStorage.setItem(KEY, JSON.stringify(next)); } catch { /* embedded/private storage */ }
  window.dispatchEvent(new CustomEvent('nimshop:cart-changed'));
}

export function itemKey(it: { id: string; pkg?: string; value?: number | string; country?: string }): string {
  return it.id + '|' + (it.pkg ? 'pkg:' + it.pkg : 'val:' + it.value) + '|' + (it.country || '');
}

function bestImage(p: any): string {
  const i = p.images || {};
  return i.large || i.medium || i.small || (Object.keys(i).length ? Object.values(i)[0] : '');
}

function unitUSD(p: any, { pkg, value }: { pkg?: string; value?: number }): number {
  if (pkg) {
    const k = (p.packages || []).find((x: any) => x.package_id === pkg);
    return k ? k.value || 0 : 0;
  }
  return value || p.min_value || 0;
}

function sessionMarket(): { usd_per_btc: number; usd_per_nim: number } | null {
  const j = cachedNimRate();
  return j && Number(j.usd_per_btc) > 0 ? j : null;
}
function sessionFX(): Record<string, number> | null {
  return cachedFX();
}
function isRealCurrencyCode(code: unknown): boolean {
  const ccy = String(code || '').toUpperCase();
  if (!ccy) return false;
  try {
    new Intl.NumberFormat('en-US', { style: 'currency', currency: ccy }).format(1);
    return true;
  } catch {
    return false;
  }
}

export function rowUSD(it: CartItem): number {
  const ccy = String(it.currency || 'USD').toUpperCase();
  const fx = sessionFX();
  if (it.value > 0 && fx && fx[ccy]) return it.value * it.qty * Number(fx[ccy]);
  const m = sessionMarket();
  if (it.coinAmount > 0 && m) return it.coinAmount * it.qty * Number(m.usd_per_btc);
  if (it.value > 0 && ccy === 'USD') return it.value * it.qty;
  return (it.unitUSD || 0) * it.qty;
}
export function rowNIM(it: CartItem): number {
  const usd = rowUSD(it);
  const m = sessionMarket();
  if (usd > 0 && m && Number(m.usd_per_nim) > 0) return usd / Number(m.usd_per_nim);
  return 0;
}

function approximateCountryMoney(it: CartItem): { amount: number; ccy: string } | null {
  const usd = rowUSD(it);
  const fx = sessionFX();
  const ccy = COUNTRY_CCY[String(it.country || '').toUpperCase()] || '';
  const rate = fx && ccy ? Number(fx[ccy]) : 0;
  if (!(usd > 0) || !(rate > 0) || !ccy) return null;
  return { amount: usd / rate, ccy };
}

export function cartPriceLabel(it: CartItem): string {
  if (it.value > 0 && it.currency && isRealCurrencyCode(it.currency)) return fmtMoney(it.value * it.qty, it.currency);
  const approx = approximateCountryMoney(it);
  if (approx) return '≈ ' + fmtMoney(approx.amount, approx.ccy);
  const nim = rowNIM(it);
  if (nim > 0) {
    return '≈ ' + fmtNIM(Math.round(nim), 0) + ' NIM';
  }
  return '—';
}

export function cartSubtitle(it: CartItem): string {
  const denom = String(it.denomination || '').trim();
  const useful = denom && !/^package$/i.test(denom);
  const label = useful ? denom : it.value > 0 ? fmtMoney(it.value, it.currency) : '';
  const country = countryName(it.country) || it.country || '';
  return [country || '', label || 'Package'].filter(Boolean).join(' ');
}

type CartState = {
  items: CartItem[];
  count: number;
  /** Remaining daily order allowance from /account/limits (0 = unknown/unlimited). */
  orderLimit: number;
  /**
   * Adds a product to the cart. Returns `false` (without changing the cart)
   * when the product's country differs from what is already in the cart —
   * one cart can only ever hold a single country's items. Returns `'limit'`
   * when the account's max-order allowance is already fully in the cart.
   */
  addToCart: (p: any, opts?: { pkg?: string; value?: number; qty?: number; denomination?: string; force?: boolean; coinAmount?: number }) => boolean | 'limit';
  setQty: (k: string, qty: number) => void;
  removeItem: (k: string) => void;
  clearCart: () => void;
};

const CartContext = createContext<CartState | null>(null);
const EMPTY_CART: CartState = {
  items: [],
  count: 0,
  orderLimit: 0,
  addToCart: () => true,
  setQty: () => {},
  removeItem: () => {},
  clearCart: () => {},
};

// Astro can briefly render an island while hydration is being recovered. Do
// not crash the entire page if that transient render has no provider yet.
// The normal hydrated tree still uses the real CartProvider state.
export const useCart = () => useContext(CartContext) || EMPTY_CART;

/** useCart that never throws — returns null when no provider (e.g. Astro SSR).
 *  Components rendered both client-side (with provider) and SSR should use this
 *  and guard cart actions behind `if (cart)`. */
export const useCartSafe = () => useContext(CartContext);

export function CartProvider({ children }: { children: ReactNode }) {
  const [items, setItems] = useState<CartItem[]>([]);
  const itemsRef = useRef<CartItem[]>([]);
  const [orderLimit, setOrderLimit] = useState(0);

  // Remaining daily order allowance (max_orders − used_orders). The cart must
  // never hold more items than this; 0 = unknown / unlimited / signed out.
  useEffect(() => {
    let alive = true;
    const refresh = () => {
      if (!isAuthed()) {
        setOrderLimit(0);
        return;
      }
      getAccountLimits()
        .then((L: any) => {
          if (!alive) return;
          const max = Number(L?.max_orders) || 0;
          const used = Number(L?.used_orders) || 0;
          setOrderLimit(max > 0 ? Math.max(0, max - used) : 0);
        })
        .catch(() => {
          if (alive) setOrderLimit(0);
        });
    };
    refresh();
    const t = setInterval(refresh, 60_000);
    window.addEventListener('nimshop:session', refresh);
    return () => {
      alive = false;
      clearInterval(t);
      window.removeEventListener('nimshop:session', refresh);
    };
  }, []);

  // Keep the first client render identical to Astro's server HTML. Reading
  // localStorage during render causes React hydration error 418/423.
  useEffect(() => {
    const sync = () => { itemsRef.current = readCart(); setItems(itemsRef.current); };
    sync();
    window.addEventListener('storage', sync);
    window.addEventListener('nimshop:cart-changed', sync);
    return () => {
      window.removeEventListener('storage', sync);
      window.removeEventListener('nimshop:cart-changed', sync);
    };
  }, []);

  const write = useCallback((next: CartItem[]) => {
    itemsRef.current = next;
    setItems(next);
    saveCart(next);
  }, []);

  const addToCart = useCallback(
    (p: any, { pkg, value, qty = 1, denomination = '', force = false, coinAmount }: { pkg?: string; value?: number; qty?: number; denomination?: string; force?: boolean; coinAmount?: number } = {}) => {
      const current = itemsRef.current;
      if (!force && current.some((it) => it.country !== String(p.country || '').toUpperCase())) return false;
      let addQty = Number.isFinite(qty) ? Math.min(MAX_QTY, Math.max(1, Math.floor(qty))) : 1;
      if (!force && orderLimit > 0) {
        const remaining = orderLimit - current.reduce((sum, it) => sum + it.qty, 0);
        if (remaining <= 0) return 'limit';
        addQty = Math.min(addQty, remaining);
      }
      const pkgData = pkg ? (p.packages || []).find((x: any) => x.package_id === pkg) : null;
      const entry: CartItem = {
        id: p.id, type: p.type || '', name: p.name || p.id,
        image: p.logo_url || bestImage(p), bgColor: String(p.bg_color || (p as any).bgColor || (p as any).bg || ''), country: String(p.country || '').toUpperCase(),
        currency: pkgData?.currency || p.currency, pkg: pkg || '',
        value: value || pkgData?.value || 0, denomination: denomination || pkgData?.denomination || '',
        coinAmount: coinAmount != null && coinAmount > 0 ? coinAmount : parseFloat(pkgData?.coin_amount) || 0,
        unitUSD: unitUSD(p, { pkg, value }), qty: addQty,
        brand: pkgData?.brand || (p as any).brand || p.id || '',
        brand_id: pkgData?.brand_id || (p as any).brand_id || '',
        category: pkgData?.category || (p as any).category || '',
        delivery_type: String(pkgData?.delivery_type || (p as any).delivery_type || ''),
      };
      // Cart TYPE follows the supplier's delivery channel, not the category.
      if (entry.delivery_type === 'by_phone' && entry.type !== 'phone_refill') entry.type = 'phone_refill';
      if (entry.delivery_type === 'by_email' && entry.type === 'phone_refill') entry.type = 'gift_card';
      const key = itemKey(entry);
      const existing = current.find((it) => itemKey(it) === key);
      const next = existing ? current.map((it) => it === existing ? { ...it, qty: Math.min(MAX_QTY, it.qty + addQty) } : it) : [...current, entry];
      write(next);
      return true;
    }, [write, orderLimit]
  );

  const setQty = useCallback((key: string, qty: number) => {
    if (!Number.isFinite(qty)) return;
    const current = itemsRef.current;
    const others = current.filter((it) => itemKey(it) !== key).reduce((sum, it) => sum + it.qty, 0);
    const cap = orderLimit > 0 ? Math.max(0, orderLimit - others) : MAX_QTY;
    write(current.map((it) => itemKey(it) === key ? { ...it, qty: Math.max(0, Math.min(MAX_QTY, cap, Math.floor(qty))) } : it).filter((it) => it.qty > 0));
  }, [write, orderLimit]);

  const removeItem = useCallback((key: string) => {
    write(itemsRef.current.filter((it) => itemKey(it) !== key));
  }, [write]);

  const clearCart = useCallback(() => { write([]); }, [write]);

  const count = items.reduce((s, it) => s + (it.qty || 0), 0);

  return (
    <CartContext.Provider value={{ items, count, orderLimit, addToCart, setQty, removeItem, clearCart }}>
      {children}
    </CartContext.Provider>
  );
}
