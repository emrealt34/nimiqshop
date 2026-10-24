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
import { useCart, readCart, saveCart, itemKey, cartPriceLabel, cartSubtitle, cartCurrency, cartMoney, rowUSD, maxQtyForRow } from '../../lib/cartStore';
import { UnifiedThumb } from '../ui/UnifiedThumb';
import { useSheet, useToast } from '../AppProviders';
import { COUNTRY_CCY, MAX_QTY, fmtMoney, fmtNIM } from '../../lib/format';
import { getFXRates, getNimRate, getProduct, onRatesChange } from '../../lib/api';
import { loadCashbackBps, cashbackEarnLine } from '../../lib/cashback';
import { clearAppliedCashbackCode, readAppliedCashbackCode, verifyAppliedCashbackCode, CASHBACK_CODE_EVENT } from '../../lib/cashbackCode';
import { isAuthed } from '../../lib/session';
import { openLoginSheet } from '../shell/SiteShell';
import { CheckoutFlow } from '../checkout/CheckoutFlow';
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
  const [totals, setTotals] = useState<{ nim: number; local: { amount: number; ccy: string } | null; usd: number; bps: number } | null>(null);
  const [cashbackCode, setCashbackCode] = useState(() => readAppliedCashbackCode());
  // A promo code is proven once, when it is typed in, and then trusted for
  // five minutes. Pressing "Ödemeye geç" asks the shop again: a code that
  // died in the meantime must be named HERE, in the cart, not discovered
  // after the order was paid without it.
  const [promoError, setPromoError] = useState('');
  const [promoChecking, setPromoChecking] = useState(false);

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
  // One Lightning invoice can only carry so much face value (the payment
  // partner's per-order ceiling): the row's + stops at the same number the
  // store clamps to, exactly like MAX_QTY and the daily order count.
  const moneyMax = (it: { id: string; pkg?: string; value?: number | string; country?: string }) => {
    const row = items.find((o) => itemKey(o) === itemKey(it));
    return row ? maxQtyForRow(items, row) : MAX_QTY;
  };
  const tryAdd = (k: string, cur: number) => {
    if (orderLimit > 0 && liveQty() >= orderLimit) {
      toast(t('cartSheet.limitToast', { count: orderLimit }), 'error');
      return;
    }
    const row = items.find((o) => itemKey(o) === k);
    const moneyRoom = row ? maxQtyForRow(items, row) : MAX_QTY;
    if (cur < MAX_QTY && cur < moneyRoom) changeQty(k, cur + 1);
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
    const sync = () => {
      setCashbackCode(readAppliedCashbackCode());
      // Whatever the field just did (new code applied, old one removed), the
      // refusal from the previous attempt is history — do not let it sit there
      // arguing with a code that is no longer on the cart.
      setPromoError('');
    };
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
    if (promoChecking) return;

    // PROVE THE PROMO CODE BEFORE LEAVING THE CART (owner, 2026-10-19: "promo
    // kodu geçersizse o ilk şeyde söylemeli, ödemeye geç butonuna basarken").
    // The cart shows the code's cashback as if it were earned; a code that has
    // since expired, run out or been retired used to ride all the way to the
    // order and get dropped there, and the buyer only noticed when the cashback
    // never arrived. Ask now, at the button, and if the shop refuses: say so
    // here, drop the code, and stay put — one more tap continues without it.
    setPromoError('');
    if (readAppliedCashbackCode()) {
      setPromoChecking(true);
      try {
        const verdict = await verifyAppliedCashbackCode(Number(totals?.usd) || 0);
        if (verdict.state === 'fresh') {
          setCashbackCode(verdict.code);
        } else if (verdict.state === 'rejected') {
          const message = verdict.status === 403
            ? t('cashback.codeLockedNow', { code: verdict.code })
            : t('cashback.codeInvalidNow', { code: verdict.code });
          clearAppliedCashbackCode(); // the promo card folds back to "no code"
          setCashbackCode(null);
          setPromoError(message);
          toast(message, 'error');
          return;
        }
        // 'unknown' (offline / 5xx) is not a refusal: never block on our outage.
      } finally {
        setPromoChecking(false);
      }
    }

    // Keep the cart sheet clear of a second shortfall warning. The explicit
    // balance prompt belongs to the actual Nimiq Pay button in CheckoutFlow.
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
          const mMax = moneyMax(it);
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
                  disabled={it.qty >= MAX_QTY || (orderLimit > 0 && atLimit) || it.qty >= mMax}
                  aria-label={t('cartSheet.increase')}
                  style={(it.qty >= MAX_QTY || (orderLimit > 0 && atLimit) || it.qty >= mMax) ? { opacity: 0.4, cursor: 'not-allowed' } : {}}
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
      {/* Show the balance here without repeating the payment shortfall; the
          explicit prompt opens only when the buyer presses the pay control. */}
      <WalletBalance
        variant="line"
        targetNim={totals && totals.nim > 0 ? totals.nim : 0}
        targetTotal
        signInHint
        showVerdict={false}
        className="mt-2"
      />
      <details className="checkout-details-min"><summary>{t('cartSheet.detailsSummary')}</summary><div style={{ marginTop: 8 }}><CashbackFeeNotice example="nim" /></div></details>
      {/* An invalid promo code is reported HERE, where the button was pressed
          — not after the order exists. The code is already removed from the
          cart by the time this shows, so a second press just carries on. */}
      {promoError ? (
        <div className="alert error mt-2" role="alert" style={{ marginBottom: 0, display: 'flex', gap: 8, alignItems: 'flex-start' }}>
          <Icon name="alert" size={16} />
          <div className="small">{promoError}</div>
        </div>
      ) : null}
      <button
        className="btn btn-gold btn-block btn-lg mt-2"
        onClick={startCheckout}
        disabled={promoChecking}
        aria-busy={promoChecking ? 'true' : 'false'}
        style={{ display: 'inline-flex', alignItems: 'center', gap: '8px', justifyContent: 'center', minWidth: 0, paddingInline: '16px' }}
      >
        <Icon name="nimiq" size={20} />
        <span className="btn-label" style={{ flex: '1 1 auto', minWidth: 0, whiteSpace: 'normal', overflowWrap: 'anywhere', textAlign: 'center', lineHeight: 1.25 }}>
          {promoChecking ? t('cashback.codeChecking') : t('cartSheet.checkoutCta')}
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

