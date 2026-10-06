import { CashbackFeeNotice } from '../checkout/CashbackFeeNotice';
import { productMoney } from '../../lib/productMoney';
/**
 * CartSheet.tsx — React port of the cart sheet UI (openCart/draw in cart.js).
 * Owns item rows, qty controls, remove, empty, local/NIM totals, and now the
 * full CheckoutFlow (delivery → Nimiq Pay Lightning → success/summary).
 */
import { useEffect, useState } from 'react';
import { brandMetaForTitle } from '../../lib/catalogMeta';
import { Icon } from '../ui/Icon';
import { useCart, readCart, saveCart, itemKey, cartPriceLabel, cartSubtitle, cartCurrency, cartMoney, rowUSD } from '../../lib/cartStore';
import { UnifiedThumb } from '../ui/UnifiedThumb';
import { useSheet, useToast } from '../AppProviders';
import { COUNTRY_CCY, MAX_QTY, fmtMoney, fmtNIM } from '../../lib/format';
import { getFXRates, getNimRate, getProduct, onRatesChange } from '../../lib/api';
import { loadCashbackBps, cashbackEarnLine } from '../../lib/cashback';
import { readAppliedCashbackCode, CASHBACK_CODE_EVENT } from '../../lib/cashbackCode';
import { isAuthed } from '../../lib/session';
import { openLoginSheet } from '../shell/SiteShell';
import { CheckoutFlow } from '../checkout/CheckoutFlow';
import { useWalletBalance } from '../wallet/WalletBalance';
import { coversTarget, neededWholeNim, shortByWholeNim } from '../../lib/walletBalance';
import { guardLowBalance } from '../wallet/LowBalanceSheet';
import { WalletBalance } from '../wallet/WalletBalance';
import { CashbackCodeField } from '../cashback/CashbackCodeField';
import { useRouter } from '../../lib/router';
import { useT, t as i18nT } from '../../i18n';
import { pagePath } from '../../lib/asset';

export function openCartSheet(opts: {
  openSheet: (o: { title: string; wide?: boolean; render: (close: () => void) => React.ReactNode }) => void;
}): void {
  opts.openSheet({
    // The sheet header is rendered outside this component, so it reads the
    // module-level translator (kept in sync by I18nProvider on every switch).
    title: i18nT('cartSheet.sheetTitle'),
    wide: true,
    render: (close) => <CartSheetContent close={close} />,
  });
}

type Mode = 'cart' | 'checkout';

export function CartSheetContent({ close }: { close: () => void }) {
  const { t } = useT();
  const { navigate } = useRouter();
  const baseCart = useCart();
  const cart = baseCart;
  const { toast } = useToast();
  const { openSheet, closeSheet } = useSheet();
  const [mode, setMode] = useState<Mode>('cart');
  // Bumped when the live-rate reconciliation corrects the build-time snapshot;
  // the totals effect depends on it, so the cart never shows a NIM figure
  // computed from rates the market has moved away from.
  const [rateTick, setRateTick] = useState(0);
  const [checkoutItems, setCheckoutItems] = useState<any[] | null>(null);
  // The buyer's own reading of the wallet, so the cart can warn BEFORE the
  // checkout sheet opens (shared reading — no extra request).
  const { state: walletState } = useWalletBalance();
  const [lowBalDismissed, setLowBalDismissed] = useState(false);
  const [totals, setTotals] = useState<{ nim: number; local: { amount: number; ccy: string } | null; usd: number; bps: number } | null>(null);
  const [cashbackCode, setCashbackCode] = useState(() => readAppliedCashbackCode());

  const { items } = cart;
  // The cart's own money (TR cart → ₺): every row and the total use it.
  const cartCcy = cartCurrency(items);
  // Daily order allowance (max_orders − used_orders). 0 = unknown/unlimited.
  const orderLimit = baseCart.orderLimit;
  const liveQty = () => readCart().reduce((s, it) => s + (it.qty || 0), 0);
  const cartCount = cart.count;
  const atLimit = orderLimit > 0 && cartCount >= orderLimit;

  const changeQty = (key: string, qty: number) => baseCart.setQty(key, qty);
  const removeStored = (key: string) => changeQty(key, 0);
  const tryAdd = (k: string, cur: number) => {
    if (orderLimit > 0 && liveQty() >= orderLimit) {
      toast(t('cartSheet.limitToast', { count: orderLimit }), 'error');
      return;
    }
    if (cur < MAX_QTY) changeQty(k, cur + 1);
  };

  useEffect(() => {
    let alive = true;
    (async () => {
      try {
        const [fxR, nimR, cbR] = await Promise.allSettled([getFXRates(), getNimRate(), loadCashbackBps()]);
        const rates = fxR.status === 'fulfilled' && fxR.value ? fxR.value.usd_per_unit : null;
        const market = nimR.status === 'fulfilled' ? nimR.value : null;
        const bps = cbR.status === 'fulfilled' ? cbR.value : 0;
        let usd = 0;
        let known = false;
        const nimUsd = Number(market && market.usd_per_nim);
        const btcUsd = Number(market && market.usd_per_btc);
        let nim = 0;
        const its = cart.items;
        for (const it of its) {
          // Single source of truth = the supplier's BTC invoice amount. When a
          // line carries it, convert coin → NIM (matching the backend cashback
          // lock); the old face-USD → NIM path over-promised and disagreed with
          // what is actually charged. Face USD is only a fallback when no BTC
          // amount is known yet.
          if (it.coinAmount > 0 && btcUsd > 0 && nimUsd > 0) {
            const usdV = it.coinAmount * it.qty * btcUsd;
            if (usdV > 0) {
              usd += usdV;
              nim += usdV / nimUsd;
              known = true;
            }
            continue;
          }
          const rate = rates && it.currency ? rates[String(it.currency).toUpperCase()] : null;
          if (it.value > 0 && rate) {
            usd += it.value * it.qty * Number(rate);
            known = true;
          } else {
            const u = rowUSD(it);
            if (u > 0) {
              usd += u;
              known = true;
            }
          }
        }
        if (nim <= 0 && nimUsd > 0 && known) nim = usd / nimUsd;
        // One currency for the whole cart: its own country's money (see
        // cartCurrency) — a USD-priced line no longer pushes the total to USD.
        const local = cartMoney(its, rates);
        if (alive) setTotals({ nim, local, usd, bps });
      } catch {
        /* total stays hidden */
      }
    })();
    return () => {
      alive = false;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [items, rateTick]);

  useEffect(
    () =>
      onRatesChange(() => {
        setRateTick((n) => n + 1);
      }),
    []
  );

  useEffect(() => {
    const sync = () => setCashbackCode(readAppliedCashbackCode());
    window.addEventListener(CASHBACK_CODE_EVENT, sync);
    return () => window.removeEventListener(CASHBACK_CODE_EVENT, sync);
  }, []);

  // Heal old cart items that were saved without bgColor (before global bg fix) - fetch from catalog
  useEffect(() => {
    const missing = items.filter((it) => !it.bgColor && it.name);
    if (!missing.length) return;
    let alive = true;
    (async () => {
      const updates = new Map<string, string>();
      for (const it of missing) {
        try {
          const meta = await brandMetaForTitle(it.name, it.country);
          if (meta && meta.bg) updates.set(itemKey(it), meta.bg);
        } catch {}
      }
      if (!alive || !updates.size) return;
      const current = readCart();
      let changed = false;
      const next = current.map((row) => {
        const key = itemKey(row);
        const bg = updates.get(key);
        if (bg && !row.bgColor) { changed = true; return { ...row, bgColor: bg }; }
        return row;
      });
      if (changed) saveCart(next);
    })();
    return () => { alive = false; };
  }, [items]);

  useEffect(() => {
    let alive = true;
    const source = readCart();
    const suspect = source.filter((it) => it && it.pkg && !isRealCurrencyCode(it.currency));
    if (!suspect.length) return;
    (async () => {
      const next = [...source];
      let changed = false;
      for (const bad of suspect) {
        try {
          const raw = await getProduct(bad.id, bad.country);
          const families: any[] = Array.isArray(raw) ? raw : raw && raw.products ? [raw] : [];
          const allProducts: any[] = families.flatMap((f: any) => Array.isArray(f.products) ? f.products : []);
          const pkg = allProducts.find((p: any) => p && p.product_id === bad.pkg) || null;
          const parsed = parsePackageMoney(pkg, COUNTRY_CCY[String(bad.country || '').toUpperCase()] || '');
          if (!parsed) continue;
          const idx = next.findIndex((it) => itemKey(it) === itemKey(bad));
          if (idx === -1) continue;
          next[idx] = { ...next[idx], value: parsed.value, currency: parsed.currency };
          changed = true;
        } catch {
          /* keep legacy cart item as-is if supplier data is unavailable */
        }
      }
      if (!alive || !changed) return;
      // Merge metadata into the latest cart; do not resurrect removed items
      // or overwrite quantities changed while the supplier request ran.
      saveCart(readCart().map((item) => {
        const original = items.find((old) => itemKey(old) === itemKey(item));
        const update = next.find((row) => itemKey(row) === itemKey(item));
        return original && update && item.value === original.value && item.currency === original.currency
          ? { ...item, value: update.value, currency: update.currency } : item;
      }));
    })();
    return () => {
      alive = false;
    };
  }, [items]);

  const startCheckout = async () => {
    if (!isAuthed()) {
      openLoginSheet({ openSheet, closeSheet, toast });
      // After login the user re-taps Checkout (login sheet opens on top).
      return;
    }
    if (!items.length) return;
    // Owner (2026-10-06): the shortfall is a POPUP with options, not a toast —
    // the whole basket is priced here, so this is the moment to ask. It opens on
    // top of the cart sheet and only the buyer's explicit choice continues.
    const cartNim = totals && totals.nim > 0 ? totals.nim : 0;
    const proceed = await guardLowBalance({
      openSheet,
      targetNim: cartNim,
      availableNim: walletState.availableNim,
      ready: walletState.status === 'ready',
    });
    if (!proceed) return;
    setCheckoutItems([...items]);
    setMode('checkout');
  };

  // If mode is checkout and items changed to empty externally, fall back
  if (mode === 'checkout' && !checkoutItems) return null;

  if (mode === 'checkout' && checkoutItems) {
    return (
      <CheckoutFlow
        items={checkoutItems}
        onClose={() => setMode('cart')}
        onNavigateOrders={() => {
          close();
          navigate('/orders');
        }}
      />
    );
  }

  const effectiveBps = cashbackCode?.cashback_bps || totals?.bps || 0;
  const cashbackMeta = cashbackCode ? { source: 'code', code: cashbackCode.code } : undefined;

  if (!items.length) {
    return (
      <div className="center" style={{ padding: '30px 10px', textAlign: 'center' }}>
        <div style={{ marginBottom: '12px', display: 'flex', justifyContent: 'center' }}>
          <Icon name="bag" size={34} />
        </div>
        <div className="strong">{t('cartSheet.emptyTitle')}</div>
        <div className="small muted mt-1">{t('cartSheet.emptyText')}</div>
        <a className="btn btn-gold mt-2" href={pagePath("/")} style={{ display: 'inline-flex', alignItems: 'center', gap: '6px' }}>
          <Icon name="bag" size={18} />
          <span>{t('cartSheet.browseShop')}</span>
        </a>
      </div>
    );
  }

  return (
    <div>
      <div className="cart-list">
        {items.map((it) => {
          const k = itemKey(it);
          return (
            <div className="cart-row" key={k}>
              {/* Size comes from the row's --cart-thumb track (fixes.css): the
                  wrapper may never be wider than its column, or the artwork
                  slides under the −/+ cluster on phones. */}
              <div style={{ minWidth: 0 }}>
                <UnifiedThumb src={it.image || ''} alt={it.name} bg={it.bgColor || 'rgb(255,255,255)'} />
              </div>
              <div className="cart-main">
                <div className="cart-main-top">
                  <div className="cart-copy">
                    <div className="strong">{it.name}</div>
                    <div className="xs faint">{cartSubtitle(it)}</div>
                  </div>
                  <div className="cart-price strong" data-ccy={cartCcy}>{cartPriceLabel(it, cartCcy)}</div>
                </div>
              </div>
              <div className="cart-controls">
                <button className="cart-q" type="button" aria-label={t('cartSheet.decrease')} onClick={() => changeQty(k, it.qty - 1)}>
                  −
                </button>
                <span className="cart-qty">{it.qty}</span>
                <button
                  className="cart-q"
                  type="button"
                  disabled={it.qty >= MAX_QTY || (orderLimit > 0 && atLimit)}
                  aria-label={t('cartSheet.increase')}
                  style={(it.qty >= MAX_QTY || (orderLimit > 0 && atLimit)) ? { opacity: 0.4, cursor: 'not-allowed' } : {}}
                  onClick={() => tryAdd(k, it.qty)}
                >
                  +
                </button>
                <button className="cart-q cart-x" type="button" aria-label={t('cartSheet.removeItem', { name: it.name })} onClick={() => removeStored(k)}>
                  <Icon name="x" size={15} />
                </button>
              </div>
            </div>
          );
        })}
      </div>
      {orderLimit > 0 && (
        <div className={`xs mt-1 ${atLimit ? 'limit-reached' : ''}`} style={{ display: 'flex', alignItems: 'center', gap: 6, justifyContent: 'center', textAlign: 'center', lineHeight: 1.4 }}>
          {atLimit ? (
            <>
              <Icon name="info" size={13} />
              <span>{t('cartSheet.limitReached', { count: orderLimit })}</span>
            </>
          ) : (
            <span className="faint">{t('cartSheet.limitProgress', { count: cartCount, limit: orderLimit })}</span>
          )}
        </div>
      )}
      <CashbackCodeField compact />
      <div className="cart-total mt-2" style={{ display: 'grid', gridTemplateColumns: 'minmax(0, 1fr) auto', alignItems: 'start', gap: '12px' }}>
        <div className="small faint" style={{ minWidth: 0 }}>
          {t('cartSheet.savedLocal', { count: items.length })}
        </div>
        {totals && totals.nim > 0 ? (
          <div style={{ minWidth: 0, textAlign: 'right' }}>
            <div className="strong">{t('cartSheet.totalApprox', { nim: fmtNIM(Math.round(totals.nim), 0) })}</div>
            {totals.local ? (
              <div className="small muted" data-money-ccy={totals.local.ccy} style={{ textAlign: 'right', marginTop: '4px' }}>
                ≈ {fmtMoney(totals.local.amount, totals.local.ccy)}
              </div>
            ) : totals.usd > 0 ? (
              /* Only when a row could not be expressed in the cart's money at
                 all (no FX rate yet): USD is the shop's common denominator, not
                 a display choice. */
              <div className="small muted" data-money-ccy="USD" style={{ textAlign: 'right', marginTop: '4px' }}>
                ≈ ${totals.usd.toLocaleString('en-US', { maximumFractionDigits: 2 })}
              </div>
            ) : null}
          </div>
        ) : (
          <div className="strong" style={{ minWidth: 0, textAlign: 'right' }}>{t('cartSheet.totalDash')}</div>
        )}
      </div>
      {totals && totals.nim > 0 ? (
        <div className="xs faint mt-1" style={{ textAlign: 'center', maxWidth: '36ch', marginInline: 'auto', lineHeight: 1.45 }}>
          {cashbackEarnLine(totals.nim, effectiveBps, cashbackMeta)}
        </div>
      ) : null}
      {/* Owner (2026-10-05): "your cart" — the buyer's own NIM, and whether the
          cart actually fits in it, right where the checkout starts. */}
      <WalletBalance variant="line" targetNim={totals && totals.nim > 0 ? totals.nim : 0} targetTotal signInHint className="mt-2" />
      {/* The cart's own shortfall card — same wording and same two numbers as
          the pay screen's, and just as non-blocking: "continue anyway" only
          hides the card, the button below keeps working. */}
      {(() => {
        const cartNim = totals && totals.nim > 0 ? totals.nim : 0;
        if (lowBalDismissed || walletState.status !== 'ready' || !(cartNim > 0)) return null;
        if (coversTarget(cartNim, walletState.availableNim)) return null;
        return (
          <div className="alert warn mt-2" data-testid="low-balance-card" style={{ marginBottom: 0, textAlign: 'left', display: 'flex', gap: '8px', alignItems: 'flex-start' }}>
            <Icon name="alert" size={16} />
            <div className="small" style={{ flex: 1 }}>
              <div className="strong">{t('checkout.flowLowBalance')}</div>
              <div className="mt-1">
                {t('checkout.flowLowBalanceBody', {
                  need: neededWholeNim(cartNim),
                  have: Math.max(0, Math.floor(walletState.availableNim)),
                  short: shortByWholeNim(cartNim, walletState.availableNim),
                })}
              </div>
              <div className="row mt-2" style={{ gap: 8, flexWrap: 'wrap' }}>
                <button type="button" className="btn btn-sm btn-gold" onClick={() => setLowBalDismissed(true)}>
                  {t('checkout.flowLowBalanceAnyway')}
                </button>
              </div>
            </div>
          </div>
        );
      })()}
      <details className="checkout-details-min"><summary>{t('cartSheet.detailsSummary')}</summary><div style={{ marginTop: 8 }}><CashbackFeeNotice example="nim" /></div></details>
      <button className="btn btn-gold btn-block btn-lg mt-2" onClick={startCheckout} style={{ display: 'inline-flex', alignItems: 'center', gap: '8px', justifyContent: 'center', minWidth: 0, paddingInline: '16px' }}>
        <Icon name="nimiq" size={20} />
        <span className="btn-label" style={{ flex: '1 1 auto', minWidth: 0, whiteSpace: 'normal', overflowWrap: 'anywhere', textAlign: 'center', lineHeight: 1.25 }}>
          {t('cartSheet.checkoutCta')}
        </span>
      </button>
      <div className="row between mt-1">
        <button className="btn btn-ghost btn-sm" onClick={baseCart.clearCart}>
          {t('cartSheet.emptyCart')}
        </button>
      </div>
    </div>
  );
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

function parsePackageMoney(p: any, _fallbackCurrency = ''): { value: number; currency: string } | null {
  const money = productMoney(p);
  return money.value > 0 && money.currency ? money : null;
}

