import { supplierIssues, type SupplierIssue } from '../../lib/supplierProblems';
import { SupplierProblemNotice } from './SupplierProblemNotice';
import { CashbackFeeNotice } from './CashbackFeeNotice';
import { selectionPayload } from '../../lib/productMoney';
/**
 * CheckoutFlow.tsx — React port of the cart.js checkout flow. Rendered inside
 * a sheet; it is a small wizard that runs through the delivery steps, then
 * the BATCH-FIRST single-invoice flow (or per-item fallback), with the full
 * retry / skip / finish / blocked-items recovery behavior preserved. Paid
 * items leave the cart immediately; incomplete ones stay.
 *
 * Success and summary "beats" are rendered here rather than imperatively, so
 * the whole flow is driven by React state (no imperative body.textContent).
 */
import { useCallback, useEffect, useRef, useState } from 'react';
import { Icon } from '../ui/Icon';
import { ErrorDetail } from '../ui/uiKit';
import { useCart, itemKey, type CartItem } from '../../lib/cartStore';
import { useSheet, useToast } from '../AppProviders';
import { createQuote, createQuoteBatch, forgetQuote, getQuote, friendlyApiMessage, authorizePaymentLaunch, cachedNimRate, errorDetailLine } from '../../lib/api';
import { canRenewQuote, paymentInFlight } from '../../lib/pay';
import { buildOrderRequest, getGiftExtras, type DeliveryInfo } from '../../lib/delivery';
import { isValidEmail } from '../../lib/validate';
import { giftRowFields } from '../../lib/giftNote';
import { uuid } from '../../lib/format';
import { CombinedDeliveryPicker } from './CombinedDeliveryPicker';
import { needsPhone } from '../../lib/catalog';
import { useT, t as tr } from '../../i18n';
import { LightningPayBlock, useNimiqPayMissingDialog } from './LightningPayBlock';
import { LightningRailPills } from './payRailKit';
import { NimiqPayPayButton } from './NimiqPayPayButton';
import { inNimiqPay, detectMobilePlatform } from '../../lib/miniapp';
import { UsdtPayBlock } from './UsdtPayBlock';
import { PaymentCountdown } from './PaymentCountdown';
import { lightningPaymentURI, rememberLightningPayment } from '../../lib/hub';
import { PAID_STATUSES, quoteBolt11, quoteIdOf, isQuotePayable, launchLightningUri } from '../../lib/pay';
import { cashbackExclusiveNote, cashbackNimFromQuote, fmtCashbackNIM, loadCashbackBps } from '../../lib/cashback';
import { quoteFaceValue } from '../../lib/format';
import { StakerCashbackLine } from '../staker/StakerCashback';
import { currentCashbackCode } from '../../lib/cashbackCode';
import { nimAmountText, nimAmountFor } from '../../lib/nim';
import { refreshWalletBalance } from '../../lib/walletBalance';
import { guardLowBalance } from '../wallet/LowBalanceSheet';
import { useWalletBalance } from '../wallet/WalletBalance';
import { fmtUSD } from '../../lib/format';
import { deliveryLine, youGetText } from '../../lib/deliveryCopy';
import {
  dailyLimitFromError,
  limitHeadline,
  limitHint,
  limitResetLine,
  limitSentence,
  type DailyLimit,
} from '../../lib/dailyLimit';
import { isAuthed } from '../../lib/session';
import { loginWithHub, friendlyHubError } from '../../lib/hub';
import { deliverySummary, payRail, payActionLine , coinAmountLabel, coinAmountLabelFor } from '../../lib/deliveryCopy';
import { asset, pagePath } from '../../lib/asset';
import { normalizePath } from '../../lib/router';

type Phase =
  | { kind: 'delivery' }
  | { kind: 'pick-payment'; info: DeliveryInfo }
  | { kind: 'preparing'; label: string }
  | { kind: 'batch-pay'; quote: any; items: CartItem[] }
  | { kind: 'batch-failed'; issues?: SupplierIssue[]; message: string; detail?: string; activeCheckout?: boolean; items?: CartItem[] }
  | { kind: 'batch-blocked'; issues?: SupplierIssue[]; blocked: any[]; items: CartItem[] }
  | { kind: 'peritem-pay'; index: number; quote: any; name: string; qty: number; total: number; paidCount: number }
  | { kind: 'item-failed'; issues?: SupplierIssue[]; it: CartItem; reason: string; msg: string; detail?: string; index: number; total: number; paidCount: number }
  | { kind: 'limit-blocked'; limit: DailyLimit; subject: string; paidCount: number; restCount: number }
  | { kind: 'already-paid'; quoteId: string; status: string }
  | { kind: 'success'; paidCount: number; delivered: CartItem[] }
  | { kind: 'summary'; paid: CartItem[]; kept: CartItem[]; stoppedEarly: boolean };

/**
 * blockingCheckout — the unresolved checkout that triggered the safety hold.
 *
 * The 409 body names it (`quote_id`, `status`). When the buyer answers "continue
 * anyway" the shop asks this order what it actually is, because the hold fires
 * on what the shop knew at the LAST SYNC: a payment that has landed since then
 * (or that was never charged at all) changes the honest answer completely.
 *
 * Owner (2026-10-06): "you have unpaid order would you like to continue dedim
 * evet dedim sonra tamamlandı dedi ordersa attı beni" — saying yes produced a
 * terminal screen and a redirect instead of either a payment or an explanation.
 */
let blockingCheckout: { id: string; status: string } = { id: '', status: '' };

function rememberBlockingCheckout(e: unknown): void {
  const data = ((e as { data?: Record<string, unknown> })?.data || {}) as Record<string, unknown>;
  const id = String(data.quote_id || (e as { quote_id?: unknown })?.quote_id || '');
  if (!id) return;
  blockingCheckout = { id, status: String(data.status || '') };
}

/** Ask the shop whether the blocking checkout has been paid since. */
async function blockingCheckoutPaid(): Promise<{ paid: boolean; status: string; id: string }> {
  const id = blockingCheckout.id;
  if (!id) return { paid: false, status: '', id: '' };
  try {
    const res: any = await getQuote(id);
    const q = res?.quote || res || {};
    const st = String(q.status || '').toLowerCase();
    const paid = PAID_STATUSES.has(st) || /deliver|complete|paid|confirm/.test(st);
    return { paid, status: st || blockingCheckout.status, id };
  } catch {
    // Could not ask: treat it as unresolved. The server-side gate still refuses
    // a second money path for an order that really is unresolved, so this
    // choice can only ever be the conservative one.
    return { paid: false, status: '', id };
  }
}

function blockedFrom(e: any): any[] {
  const list = e && e.data && Array.isArray(e.data.blocked_items) ? e.data.blocked_items : [];
  return list.filter((b: any) => b && typeof b.index === 'number' && b.product_id);
}

/**
 * Phone lookup that survives re-renders: the cart store can hand out NEW item
 * objects, so a Map keyed only by object identity loses the number. Every
 * delivery step also stores the number under the product id — check both.
 */
export function phoneFor(it: any, phones: Map<unknown, string> | undefined | null): string {
  if (!phones || !it) return '';
  return phones.get(it) || phones.get(it.id) || phones.get(String(it.id)) || '';
}

function batchReqFor(
  items: CartItem[],
  phones: Map<unknown, string>,
  email = '',
  paymentMethod = 'nimiq_pay',
  cashbackDestination = 'cashback',
  anonymous = false
): Record<string, unknown>[] {
  // Gift extras ride along on EVERY row of the cart that collected them: the
  // note is chosen once, per cart, in the delivery step — and every step that
  // can set them (email · top-up · mixed) shows the toggle for top-ups too.
  // Gating it on "single top-up cart" used to drop the note for a mixed or a
  // two-number cart: the buyer wrote the message, the API never saw it.
  // The note has one carrier (an email), so what rides on the row is the
  // channel marker and the text — never a number, which nothing would text.
  const g = getGiftExtras();
  return items.map((it, idx) => {
    const den = String(it.denomination || '').trim();
    const row: Record<string, unknown> = {
      product_id: it.id,
      country: it.country,
      ...selectionPayload(den, it.value),
      quantity: Math.max(1, Math.min(10, Number(it.qty) || 1)),
      phone_number: phoneFor(it, phones),
      ...(it.brand ? { brand: it.brand } : {}),
      ...(it.brand_id ? { brand_id: it.brand_id } : {}),
      // Keep delivery data on each item as well as the batch envelope. Some
      // API deployments validate item rows independently.
      email: email.trim(),
      payment_method: paymentMethod,
      cashback_destination: cashbackDestination,
      // Anonymous checkouts remain in the public activity feed, but the
      // buyer wallet identity and payment transactions are hidden there. The
      // flag rides on every row AND on the batch envelope (the envelope wins).
      anonymous,
    };
    // One place decides what rides on the row: lib/giftNote, which sends only
    // what an emailed note needs. phone_number above is the top-up's own
    // number, already locked to the product's country and E.164-normalized by
    // the step — it is never reused as a note address. The identicon PNG rides
    // on the FIRST row only (~18KB): the API hoists the note off the first
    // row that carries it, and N rows × 18KB would be a needlessly fat POST.
    Object.assign(row, giftRowFields(idx === 0 ? g : { channel: g.channel, message: g.message }));
    return row;
  });
}

/**
 * SingleBuyFlow — a wrapper around CheckoutFlow for a single-product buy
 * (product page). Handles the auth gate with a "Connect wallet to pay" sheet,
 * then opens a checkout sheet running the single-item flow.
 */
export function openSingleBuyFlow(opts: {
  product: {
    id: string;
    name: string;
    type: string;
    country: string;
    currency: string;
    pkg?: string;
    value?: number;
    qty?: number;
    denomination?: string;
    image?: string;
    brand?: string;
    brand_id?: string;
    category?: string;
  };
  openSheet: (o: { title: string; wide?: boolean; render: (close: () => void) => React.ReactNode }) => void;
  closeSheet: (id?: number) => void;
  toast: (t: string, k?: 'success' | 'error' | 'info' | 'warn') => void;
  onNavigateOrders: () => void;
}): void {
  const { product, openSheet, toast, onNavigateOrders } = opts;
  const item: CartItem = {
    id: product.id,
    type: product.type,
    name: product.name,
    image: product.image || '',
    bgColor: String((product as any).bgColor || (product as any).bg || ''),
    country: product.country,
    currency: product.currency,
    pkg: product.pkg || '',
    value: product.value || 0,
    denomination: product.denomination || '',
    coinAmount: 0,
    unitUSD: product.value || 0,
    qty: product.qty || 1,
    brand: (product as any).brand || '',
    brand_id: (product as any).brand_id || '',
    category: (product as any).category || '',
    delivery_type: (product as any).delivery_type || '',
  };
  // Supplier delivery channel wins over the category-derived type.
  if (item.delivery_type === 'by_phone') item.type = 'phone_refill';
  else if (item.delivery_type === 'by_email' && item.type === 'phone_refill') item.type = 'gift_card';

  const go = () => {
    openSheet({
      title: tr('checkout.flowBuy', { name: product.name }),
      wide: true,
      render: (close) => (
        <CheckoutFlow items={[item]} onClose={close} onNavigateOrders={onNavigateOrders} />
      ),
    });
  };

  if (!isAuthed()) {
    openSheet({
      title: tr('checkout.flowBuy', { name: product.name }),
      wide: true,
      render: (close) => (
        <div className="center mt-2 mb-2" style={{ textAlign: 'center' }}>
          <div className="strong">{tr('checkout.flowConnectWallet')}</div>
          <button
            className="btn btn-gold btn-block btn-lg mt-2"
            onClick={() => {
              loginWithHub()
                .then(() => {
                  close();
                  toast(tr('checkout.flowConnected'), 'success');
                  setTimeout(go, 100);
                })
                .catch((e) => toast(friendlyHubError(e), 'error'));
            }}
            style={{ display: 'inline-flex', alignItems: 'center', gap: '8px', justifyContent: 'center' }}
          >
            <Icon name="nimiq" size={18} />
            <span className="btn-label">{tr('checkout.flowConnectNimiqWallet')}</span>
          </button>
        </div>
      ),
    });
    return;
  }
  go();
}


/** Zero-tap renewal: a dead invoice (window over, nothing charged) is replaced
 *  silently the moment the pay screen meets it — the buyer asked to BUY, not
 *  to read about invoices. The manual gate stays as the fallback once the
 *  per-flow budget is spent (a quote that renews into another dead quote must
 *  never loop forever). */
function AutoRenewOnce({ budgetKey, fire, label }: { budgetKey: string; fire: () => void; label: string }) {
  const started = useRef(false);
  useEffect(() => {
    if (started.current) return;
    started.current = true;
    (window as any)[budgetKey] = Math.max(0, (((window as any)[budgetKey] as number) ?? 0) - 1);
    fire();
  }, [budgetKey, fire]);
  return (
    <div className="center" style={{ padding: '26px 10px', textAlign: 'center' }} role="status">
      <div className="small muted">{label}</div>
    </div>
  );
}

export function CheckoutFlow({
  items,
  onClose,
  onNavigateOrders,
}: {
  items: CartItem[];
  onClose: () => void;
  onNavigateOrders: () => void;
}) {
  const { t } = useT();
  const cart = useCart();
  const { toast } = useToast();
  const [phase, setPhase] = useState<Phase>({ kind: 'delivery' });
  // Money moved: the cached balance is instantly wrong, so the strips must not
  // wait out their 25 s TTL after a purchase settles.
  useEffect(() => {
    if (phase.kind === 'summary') void refreshWalletBalance({ force: true });
  }, [phase.kind]);
  // Per-flow auto-renew budget (see AutoRenewOnce): a dead invoice is replaced
  // silently, but a quote that renews into another dead quote falls back to
  // the manual gate instead of creating invoices in a loop.
  useEffect(() => { (window as any).__autoRenewBudget = 2; }, []);
  // "Continue anyway" on the unresolved-checkout hold: once pressed, every
  // quote creation in this flow carries the buyer's ack to the backend gate.
  const ackRef = useRef(false);
  const [, setDelivery] = useState<DeliveryInfo | null>(null);
  const [activeItems, setActiveItems] = useState<CartItem[]>(items);
  const flowRef = useRef<{ done: boolean; started: boolean }>({ done: false, started: false });

  const startCheckout = useCallback(
    async (info: DeliveryInfo) => {
      if (flowRef.current.done || flowRef.current.started) return;
      flowRef.current.started = true;
      // Determine email-delivered vs top-up
      const tops = activeItems.filter((it) => needsPhone(it));
      const cards = activeItems.filter((it) => !needsPhone(it));
      // Run the flow
      runFlow(activeItems, info, cards, tops);
    },
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [activeItems]
  );

  // ---------------- Delivery step -> choose payment ----------------
  const onDeliveryDone = useCallback(
    (info: DeliveryInfo) => {
      setDelivery(info);
      // Combined Stage 2 already includes payment method + destination + privacy
      // → go straight to preparing/pay, no extra pick-payment sheet.
      startCheckout(info);
    },
    [startCheckout]
  );

  // ---------------- The orchestrator ----------------
  const runFlow = useCallback(
    async (initialItems: CartItem[], info: DeliveryInfo, cards: CartItem[], _tops: CartItem[]) => {
      let items: CartItem[] = initialItems;
      const pm = info.paymentMethod || 'nimiq_pay';
      const cd = info.cashbackDestination || 'cashback';
      const buildReq = (it: CartItem) =>
        buildOrderRequest(it, {
          email: info.email,
          phone: phoneFor(it, info.phones),
          gift: !!getGiftExtras().channel,
          paymentMethod: pm,
          cashbackDestination: cd,
          anonymous: !!info.anonymous,
        });

      // BATCH-FIRST for multi-item carts
      if (items.length > 1) {
        setPhase({ kind: 'preparing', label: t('checkout.flowPreparing', { n: items.length }) });
        let batch: any = null;
        let batchErr = '';
        let batchDetail = '';
        let batchBlocked: any[] = [];
        let batchLimit: DailyLimit | null = null;
        let batchActiveCheckout = false;
        let batchIssues: SupplierIssue[] = [];

        const tryBatch = async () => {
          batch = null;
          batchErr = '';
          batchDetail = '';
          batchBlocked = [];
          batchLimit = null;
          batchActiveCheckout = false;
          batchIssues = [];
          try {
            if (cards.length > 0 && !isValidEmail(info.email)) {
              throw new Error(t('checkout.flowNeedEmail'));
            }
            batch = await createQuoteBatch(batchReqFor(items, info.phones, info.email, pm, cd, !!info.anonymous), info.email.trim(), uuid(), currentCashbackCode(), pm, cd, !!info.anonymous, ackRef.current);
          } catch (e) {
            batchIssues = supplierIssues(e);
            batchLimit = dailyLimitFromError(e);
            batchActiveCheckout = (e as any)?.code === 'ACTIVE_CHECKOUT';
            if (batchActiveCheckout) rememberBlockingCheckout(e);
            batchErr = batchLimit
              ? limitSentence(batchLimit, t('checkout.flowCartSubject'))
              : friendlyApiMessage(e, t('checkout.flowBatchRejected'));
            // Exact failure (code · status · backend message) for the red screen.
            batchDetail = errorDetailLine(e);
            batchBlocked = blockedFrom(e);
          }
        };

        await tryBatch();
        let perItem = false;

        while (!perItem) {
          if (flowRef.current.done) return;
          if (!batch) {
            const lim = batchLimit;
            if (lim) {
              // The budget refused the WHOLE cart, so "try one by one" would
              // only repeat the same refusal per item (and burn a request per
              // line). Show the buyer's numbers and stop.
              const choice = await new Promise<'retry' | 'cart'>((resolve) => {
                setPhase({ kind: 'limit-blocked', limit: lim, subject: t('checkout.flowCartSubject'), paidCount: 0, restCount: items.length });
                (window as any).__promptResolver = (c: any) => resolve(c === 'retry' ? 'retry' : 'cart');
              });
              (window as any).__promptResolver = undefined;
              if (choice === 'retry') {
                await tryBatch();
                continue;
              }
              onClose();
              return;
            }
            if (batchBlocked.length && batchBlocked.length < items.length) {
              // Named culprits path
              const result = await new Promise<'details' | 'remove' | 'retry' | 'stop' | 'onebyone'>((resolve) => {
                const anyFixable = batchBlocked.some((b: any) => b.fixable);
                setPhase({ kind: 'batch-blocked', blocked: batchBlocked, items, issues: batchIssues });
                // Intercept: we render the prompt; use an event to resolve.
                (window as any).__promptResolver = (c: any) => {
                  if (!anyFixable && c === 'details') c = 'retry';
                  resolve(c);
                };
              });
              (window as any).__promptResolver = undefined;
              if (result === 'details') {
                // Re-run delivery steps (we keep same info for simplicity; the
                // original re-collects — we reuse the collected values).
                // Return to delivery entry; never resubmit unchanged invalid data.
                setActiveItems(items);
                flowRef.current.started = false;
                setPhase({ kind: 'delivery' });
                return;
              }
              if (result === 'remove') {
                const victims = batchBlocked.map((b) => items[b.index]).filter(Boolean);
                const names = victims.map((v: CartItem) => v.name || v.id);
                for (const v of victims) cart.removeItem(itemKey(v));
                items = items.filter((it) => !victims.includes(it));
                setActiveItems(items);
                toast(t('checkout.flowRemoved', { names: names.join(', ') }), 'warn');
                if (items.length === 0) {
                  onClose();
                  return;
                }
                if (items.length === 1) {
                  perItem = true;
                  break;
                }
                await tryBatch();
                continue;
              }
              if (result === 'retry') {
                await tryBatch();
                continue;
              }
              if (result === 'onebyone') {
                perItem = true;
                break;
              }
              onClose();
              return;
            }
            // Generic batch failure
            const c = await new Promise<'retry' | 'onebyone' | 'stop' | 'force'>((resolve) => {
              setPhase({ kind: 'batch-failed', message: batchErr || t('checkout.flowBatchUnconfirmed'), detail: batchDetail, activeCheckout: batchActiveCheckout, items, issues: batchIssues });
              (window as any).__promptResolver = (x: any) => resolve(x);
            });
            (window as any).__promptResolver = undefined;
            if (c === 'retry') {
              await tryBatch();
              continue;
            }
            if (c === 'force') {
              // "Continue anyway" is not a promise that a second payment is
              // the right thing to do: first ask what happened to the order
              // that raised the hold. If it has been paid (or is being
              // delivered) since the shop last looked, saying so — and NOT
              // charging twice or claiming a completion — is the only correct
              // answer.
              const prior = await blockingCheckoutPaid();
              if (prior.paid) {
                setPhase({ kind: 'already-paid', quoteId: prior.id, status: prior.status });
                return;
              }
              // Genuinely unresolved: the buyer's acknowledgement releases the
              // server-side hold and a fresh payment is created beside it.
              ackRef.current = true;
              await tryBatch();
              continue;
            }
            if (c === 'onebyone') {
              perItem = true;
              break;
            }
            onClose();
            return;
          }

          // ONE payment for the whole cart
          const paid = await new Promise<boolean>((resolve) => {
            setPhase({ kind: 'batch-pay', quote: batch, items });
            (window as any).__payResolver = (ok: boolean) => resolve(ok);
          });
          (window as any).__payResolver = undefined;
          if (!paid && (window as any).__lastPayReason === 'renew') {
            // The buyer tapped "new invoice": re-create the whole batch and
            // land back on a payable screen. The ack rides along so NO
            // safety path can hand back the dead quote instead of creating
            // (owner: "yeni fatura oluştur çalışmıyor", 2026-10-04).
            ackRef.current = true;
            await tryBatch();
            continue;
          }
          if (paid) {
            // Success
            cart.clearCart();
            bumpAwaiting(items.reduce((s, it) => s + (it.qty || 0), 0));
            setPhase({ kind: 'success', paidCount: 1, delivered: items });
            return;
          }
          // batch payment failed
          const c = await new Promise<'retry' | 'onebyone' | 'stop'>((resolve) => {
            // The pay step wrote the wallet's exact failure here (see
            // NimiqPayPayButton): the red screen names it instead of only
            // saying that the payment stayed unresolved.
            const payDetail = takeLastPayDetail();
            setPhase({ kind: 'batch-failed', message: t('checkout.flowUnresolvedPayment'), detail: payDetail, items });
            (window as any).__promptResolver = (x: any) => resolve(x);
          });
          (window as any).__promptResolver = undefined;
          if (c === 'retry') {
            await tryBatch();
            continue;
          }
          if (c === 'onebyone') {
            perItem = true;
            break;
          }
          onClose();
          return;
        }

        // per-item preparation
        if (perItem && items.length > 1) {
          // fall through to the per-item loop below with pre-quoted prices
        }
      }

      // ---------------- PER-ITEM fallback (single items, or explicit choice) ----------------
      const paidIdx: number[] = [];
      let stop = false;
      for (let i = 0; i < items.length; i++) {
        if (stop || flowRef.current.done) break;
        const it = items[i];
        let skip = false;
        while (!stop && !skip) {
          setPhase({ kind: 'preparing', label: t('checkout.flowPricing', { name: it.name }) });
          forgetQuote(buildReq(it));
          let quote: any = null;
          let qErr = '';
          let qDetail = '';
          let qReason = 'quote';
          let qLimit: DailyLimit | null = null;
          let qIssues: SupplierIssue[] = [];
          try {
            quote = await createQuote(ackRef.current ? { ...buildReq(it), ack_active_checkout: true } : buildReq(it));
          } catch (e) {
            qIssues = supplierIssues(e);
            qLimit = dailyLimitFromError(e);
            qReason = (e as any)?.code === 'ACTIVE_CHECKOUT' ? 'active-checkout' : qIssues.length ? 'supplier' : 'quote';
            if (qReason === 'active-checkout') rememberBlockingCheckout(e);
            qErr = qLimit
              ? limitSentence(qLimit, it.name)
              : friendlyApiMessage(e, t('checkout.flowNoLivePrice'));
            // The RED screen explains what happened; this line carries the
            // exact code/status/message so the buyer (and support) can act on
            // the real failure instead of a generic sentence.
            qDetail = errorDetailLine(e);
          }
          if (!quote) {
            const lim = qLimit;
            if (lim) {
              // A budget refusal is not a per-item problem and not a pricing
              // problem: "skip" would hit the same ceiling on the next line
              // and "retry in a few seconds" cannot refill a 24h window.
              const choice = await new Promise<'retry' | 'cart'>((resolve) => {
                setPhase({ kind: 'limit-blocked', limit: lim, subject: it.name, paidCount: paidIdx.length, restCount: items.length - i });
                (window as any).__promptResolver = (c: any) => resolve(c === 'retry' ? 'retry' : 'cart');
              });
              (window as any).__promptResolver = undefined;
              if (choice === 'retry') continue;
              stop = true;
              continue;
            }
            const c = await new Promise<'retry' | 'skip' | 'stop' | 'force'>((resolve) => {
              setPhase({ kind: 'item-failed', it, reason: qReason, msg: qErr, detail: qDetail, index: i, total: items.length, paidCount: paidIdx.length, issues: qIssues });
              (window as any).__promptResolver = (x: any) => resolve(x);
            });
            (window as any).__promptResolver = undefined;
            if (c === 'retry') continue;
            if (c === 'force') {
              const prior = await blockingCheckoutPaid();
              if (prior.paid) {
                setPhase({ kind: 'already-paid', quoteId: prior.id, status: prior.status });
                return;
              }
              ackRef.current = true;
              continue;
            }
            if (c === 'skip') {
              skip = true;
              continue;
            }
            stop = true;
            continue;
          }
          const paid = await new Promise<boolean>((resolve) => {
            setPhase({ kind: 'peritem-pay', index: i, quote, name: it.name, qty: it.qty || 1, total: items.length, paidCount: paidIdx.length });
            (window as any).__payResolver = (ok: boolean) => resolve(ok);
          });
          (window as any).__payResolver = undefined;
          if (!paid && (window as any).__lastPayReason === 'renew') {
            // Same one-tap renewal on the single-item path: loop again with
            // the ack set, which forgets the dead quote and creates a fresh
            // one for this item.
            ackRef.current = true;
            continue;
          }
          if (paid) {
            paidIdx.push(i);
            break;
          }
          const c = await new Promise<'retry' | 'skip' | 'stop'>((resolve) => {
            // Read once, then use for both: takeLastPayDetail clears the
            // handoff, so a second call would return an empty string.
            const payDetail = takeLastPayDetail();
            setPhase({ kind: 'item-failed', it, reason: (window as any).__lastPayReason || 'failed', msg: payDetail, detail: payDetail, index: i, total: items.length, paidCount: paidIdx.length });
            (window as any).__promptResolver = (x: any) => resolve(x);
          });
          (window as any).__promptResolver = undefined;
          if (c === 'retry') continue;
          if (c === 'skip') {
            skip = true;
            continue;
          }
          stop = true;
          continue;
        }
      }

      // ---------------- Reconcile ----------------
      const paidItems = paidIdx.map((idx) => items[idx]);
      if (paidItems.length) {
        const kept = items.filter((_, idx) => !paidIdx.includes(idx));
        cart.clearCart();
        // Re-add the un-paid items (they stay in the cart)
        for (const k of kept) {
          cart.addToCart(
            { id: k.id, type: k.type, name: k.name, country: k.country, currency: k.currency, packages: [{ package_id: k.pkg, value: k.value, currency: k.currency, denomination: k.denomination }] },
            { pkg: k.pkg, value: k.value, qty: k.qty, denomination: k.denomination, force: true }
          );
        }
        bumpAwaiting(paidItems.reduce((s, it) => s + (it.qty || 0), 0));
      }
      if (paidItems.length === items.length && items.length > 0) {
        setPhase({ kind: 'success', paidCount: paidItems.length, delivered: paidItems });
        return;
      }
      setPhase({
        kind: 'summary',
        paid: paidItems,
        kept: items.filter((_, idx) => !paidIdx.includes(idx)),
        stoppedEarly: stop,
      });
    },
    // eslint-disable-next-line react-hooks/exhaustive-deps
    []
  );

  useEffect(() => {
    const flow = flowRef.current;
    return () => {
      flow.done = true;
      (window as any).__promptResolver = undefined;
      (window as any).__payResolver = undefined;
    };
  }, []);

  // Navigate first, then close the checkout sheet. Closing the sheet first
  // could unmount the success beat before the router callback ran. The small
  // history/popstate fallback makes this reliable even when a sheet callback
  // has captured a stale router instance or the route callback is swallowed by
  // an already-unmounting sheet.
  const goToOrders = useCallback(() => {
    if (flowRef.current.done) return;
    flowRef.current.done = true;
    const ensureOrders = () => {
      if (typeof window === 'undefined' || normalizePath(window.location.pathname) === '/orders') return;
      try {
        window.history.pushState({}, '', pagePath('/orders'));
        window.dispatchEvent(new PopStateEvent('popstate'));
      } catch {
        try { window.location.assign(pagePath('/orders')); } catch {}
      }
    };
    try { onNavigateOrders(); } catch {}
    ensureOrders();
    window.setTimeout(ensureOrders, 80);
    // DS172411 (setTimeout): closure only, never a string — no untrusted data is evaluated.
    window.setTimeout(() => {
      if (typeof window !== 'undefined' && normalizePath(window.location.pathname) !== '/orders') {
        try { window.location.assign(pagePath('/orders')); } catch {}
      }
    }, 900);
    onClose();
  }, [onNavigateOrders, onClose]);

  return (
    <div>
      {phase.kind === 'delivery' && (
        <CombinedDeliveryPicker items={activeItems} onDone={onDeliveryDone} onBack={onClose} />
      )}
      {phase.kind === 'preparing' && <Preparing label={phase.label} />}
      {phase.kind === 'batch-pay' && (
        <BatchPayPhase quote={phase.quote} items={phase.items} onResult={(ok) => resolvePay(ok)} />
      )}
      {phase.kind === 'peritem-pay' && (
        <PerItemPayPhase
          quote={phase.quote}
          name={phase.name}
          qty={phase.qty}
          index={phase.index}
          total={phase.total}
          paidCount={phase.paidCount}
          onResult={(ok) => resolvePay(ok)}
        />
      )}
      {phase.kind === 'batch-failed' && (
        <FailurePrompt issues={phase.issues} message={phase.message} detail={phase.detail} activeCheckout={phase.activeCheckout} items={phase.items} onChoice={(c) => resolvePrompt(c)} onBackToCart={onClose} />
      )}
      {phase.kind === 'batch-blocked' && (
        <BlockedPrompt issues={phase.issues} blocked={phase.blocked} items={phase.items} onChoice={(c) => resolvePrompt(c)} />
      )}
      {phase.kind === 'limit-blocked' && (
        <DailyLimitPrompt
          limit={phase.limit}
          subject={phase.subject}
          paidCount={phase.paidCount}
          restCount={phase.restCount}
          onChoice={(c) => resolvePrompt(c)}
          onBackToCart={onClose}
        />
      )}
      {phase.kind === 'item-failed' && (
        <ItemFailedPrompt issues={phase.issues} it={phase.it} reason={phase.reason} msg={phase.msg} index={phase.index} total={phase.total} paidCount={phase.paidCount} onChoice={(c) => resolvePrompt(c)} onBackToCart={onClose} />
      )}
      {phase.kind === 'already-paid' && (
        <AlreadyPaidPhase
          quoteId={phase.quoteId}
          status={phase.status}
          onOpenOrders={goToOrders}
          onBackToCart={onClose}
        />
      )}
      {phase.kind === 'success' && (
        <SuccessBeat paidCount={phase.paidCount} delivered={phase.delivered} onDone={goToOrders} />
      )}
      {phase.kind === 'summary' && (
        <Summary paid={phase.paid} kept={phase.kept} stoppedEarly={phase.stoppedEarly} onTrack={goToOrders} onBackToCart={onClose} />
      )}
    </div>
  );
}

function resolvePay(ok: boolean) {
  const r = (window as any).__payResolver;
  if (r) r(ok);
}
function resolvePrompt(c: any) {
  const r = (window as any).__promptResolver;
  if (r) r(c);
}

function bumpAwaiting(n: number) {
  try {
    const prev = parseInt(sessionStorage.getItem('nimshop_awaiting') || '0', 10) || 0;
    sessionStorage.setItem('nimshop_awaiting', String(prev + n));
    window.dispatchEvent(new Event('nimshop:awaiting'));
  } catch {}
}

/* ---------------- Sub-components ---------------- */

function Preparing({ label }: { label: string }) {
  return (
    <div className="center" style={{ padding: '30px 10px', textAlign: 'center' }}>
      <div className="spinner" style={{ width: 28, height: 28, margin: '0 auto 12px' }} />
      <div className="strong">{label}</div>
    </div>
  );
}

/* Poll a quote until it fulfills/fails. */
function usePaymentPoll(quote: any, onResult: (ok: boolean, reason?: string) => void, onUpdate?: (q: any) => void) {
  useEffect(() => {
    if (!quote) return;
    let alive = true;
    let attempts = 0;
    let timer: any;
    const check = async () => {
      if (!alive) return;
      attempts++;
      try {
        const id = quoteIdOf(quote);
        if (!id) return;
        const result = await getQuote(id);
        const q = result.quote || result;
        if (!alive) return;
        onUpdate?.(q);
        const st = String(q.status || '');
        if (PAID_STATUSES.has(st)) {
          onResult(true, st);
          return;
        }
        // Failures/manual review stay visible and non-payable. Keep polling:
        // local expiry or a transient/manual state can still become delivered.
      } catch {}
      if (attempts >= 90) {
        (window as any).__lastPayReason = 'timeout';
        onResult(false, 'timeout');
        return;
      }
      timer = setTimeout(check, 5000);
    };
    check();
    return () => {
      alive = false;
      clearTimeout(timer);
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [quote]);
}

/** Exported for the pay-screen contract test: TEST MODE must render THIS real
 * screen (invoice · QR · countdown) with only ONE extra simulated-pay button. */
export function PayScreen({
  quote,
  stepNote,
  note,
  youGetLabel,
  onResult,
}: {
  quote: any;
  name: string;
  stepNote?: string;
  note?: React.ReactNode;
  /** What the buyer receives — product names & quantities, not a USD amount. */
  youGetLabel?: string;
  onResult: (ok: boolean) => void;
}) {
  const { t } = useT();
  // The short-balance popup is a sheet, so the pay screen needs the opener
  // (owner, 2026-10-06: "popup olarak çıkacaktı o yetersiz, seçenek sunacaktı").
  const { openSheet } = useSheet();
  const [liveQuote, setLiveQuote] = useState(quote);
  const [expired, setExpired] = useState(false);
  useEffect(() => { setLiveQuote(quote); setExpired(false); }, [quote]);
  const current = { ...quote, ...liveQuote };
  const invoice = quoteBolt11(current);

  /* The buyer's own NIM, already read for the strip above — no extra request. */
  const { state: walletState } = useWalletBalance();
  const needNim = Number(nimAmountFor(current, cachedNimRate())) || 0;
  const walletReady = walletState.status === 'ready';

  /**
   * THE WARNING, at the moment of the attempt.
   * Owner (2026-10-06): "bakiyemin yetmediği şeyleri almaya çalışırken karta ve
   * normal toast çıkmadı düzelt lütfen; toastı yanlış yere mi koydun yoksa".
   *
   * Two things were wrong:
   *  1. the toast fired ONLY once per quote, while the pay screen was still
   *     assembling — if the buyer was looking at the button, they missed it;
   *  2. the card was gated on `insidePay`, so the same shortfall said nothing at
   *     all outside Nimiq Pay even though the balance was known.
   * It now fires on the press as well (with a short cooldown, so a double tap
   * does not stack toasts) and the card no longer depends on the host.
   */
  /**
   * THE POPUP, NOT A TOAST (owner, 2026-10-06: "abi popup olarak çıkacaktı o
   * yetersiz, seçenek sunacaktı o kadar, lütfen toast değil ya"). Pressing any
   * pay affordance while the wallet is short now opens a dialog that states the
   * two figures and offers three ways out: continue anyway, refresh the balance
   * (re-evaluated live inside the dialog), or cancel. The attempt only proceeds
   * on an explicit choice — never silently, and never blocked either.
   */
  const askBeforePaying = useCallback(
    () =>
      guardLowBalance({
        openSheet,
        targetNim: needNim,
        availableNim: walletState.availableNim,
        ready: walletReady,
      }),
    [needNim, openSheet, walletReady, walletState.availableNim]
  );
  const settled = useRef(false);
  let uri = '';
  try { if (invoice) uri = lightningPaymentURI(invoice); } catch {}
  const finish = useCallback(
    (ok: boolean, reason?: string) => {
      if (settled.current) return;
      settled.current = true;
      (window as any).__lastPayReason = reason || (ok ? 'paid' : 'failed');
      onResult(ok);
    },
    [onResult]
  );

  usePaymentPoll(quote, finish, setLiveQuote);

  const usdtRail = payRail(current);
  const isUsdt = usdtRail.isUsdt;
  const insidePay = inNimiqPay();
  const notifyHeroMissing = useNimiqPayMissingDialog();
  // AutoRenewOnce fires once per MOUNT. A renewal that hands back the same
  // dead quote (or a quote born dead) left the old instance mounted, so the
  // "creating your new invoice…" line froze forever (live bug, 2026-10-04).
  // Keying it by quote + attempt remounts it for every new attempt.
  const [renewTick, setRenewTick] = useState(0);
  // Owner (2026-10-04): the pay hero shows the cashback this order will earn,
  // right under "what you get" — one short line, hidden when there is none.
  const [cbNimLabel, setCbNimLabel] = useState('');
  useEffect(() => {
    let alive = true;
    const compute = () => {
      const n = cashbackNimFromQuote({ ...quote, ...liveQuote });
      if (alive) setCbNimLabel(n > 0 ? fmtCashbackNIM(n) : '');
    };
    compute();
    void loadCashbackBps().then(compute).catch(() => {});
    return () => {
      alive = false;
    };
  }, [quote, liveQuote]);


  if (isUsdt) {
    // The stablecoin sheet must answer the SAME questions as the NIM one: what
    // am I buying, on which rail, and where does it get delivered. It used to
    // show only the wallet block, so a mixed cart never learned that one line
    // goes to a phone number.
    const usdtDest = deliveryLine(current);
    const usdtLocal = (() => { try { const { label } = quoteFaceValue(current); return label || ""; } catch { return ""; } })();
    const usdtYouGet = youGetLabel || youGetText(quote);
    return (
      <div>
        {stepNote && <div className="xs faint mb-1">{stepNote}</div>}
        <div className="center mt-1">
          <PaymentCountdown
            expiresAt={current.payment_expires_at || current.payment_expiry}
            onExpire={() => setExpired(true)}
          />
        </div>
        <div className="pay-hero mt-2">
          <div className="pay-hero-label">{t('checkout.flowUsdtDirect')}</div>
          <div className="pay-hero-amt">
            <img className="pay-nim-ico" src={asset("/img/usdt.png")} alt="USDT" draggable={false} width={22} height={22} style={{ pointerEvents: "none", borderRadius: 5 }} />
            <span className="big-nim">{coinAmountLabel(current) || t('checkout.flowAmountShownBelow')}</span>
          </div>
          <div className="pay-hero-youget">
            <span className="xs faint">{t('checkout.flowYouGet')}</span>
            <span className="strong pay-you-get">{usdtYouGet || t('checkout.instantDelivery')}</span>
          </div>
          {usdtDest && (
            <div className="pay-hero-del small">
              <Icon name={usdtDest.icon as any} size={14} /> {usdtDest.text}
            </div>
          )}
          {usdtLocal && <div className="small muted" style={{ marginTop: 6, fontWeight: 700 }}>{usdtLocal}</div>}
        </div>
        <details className="checkout-details-min"><summary>{t('checkout.flowDetailsNetwork')}</summary><div style={{ marginTop: 8 }}><CashbackFeeNotice example="usdt" /><div className="small muted mt-1">{payActionLine(current)}</div><div className="small muted mt-1" style={{ display: 'flex', gap: '8px', alignItems: 'flex-start' }}><span style={{ flex: 1, minWidth: 0 }}>{t('checkout.flowUsdtChainNote')}</span></div></div></details>
        {note}
        <UsdtPayBlock
          quote={current}
          expired={expired}
         
          onLaunchRequested={() => {
            // Stablecoin payments are self-wallet sends; keep polling.
          }}
        />

      </div>
    );
  }

  if (!invoice || expired || !isQuotePayable(current)) {
    // This screen has FOUR very different reasons to exist, and lumping them
    // together is what used to strand buyers ("controls paused / do not start
    // another payment") in front of a dead end:
    //
    //  1. MONEY SEEN — the supplier or the shop has a payment and only
    //     settlement is left. There is nothing to fix and nothing to start;
    //     say so calmly instead of shouting "paused".
    //  2. WINDOW JUST LAPSED — the invoice can no longer be paid, but the
    //     shop is still inside its verification buffer, so "nothing was
    //     charged" is not yet proven. Truthful copy, no button, auto-refresh.
    //  3. WINDOW OVER, NOTHING CHARGED — a fresh invoice is safe and must be
    //     one tap away. The decision is read from the QUOTE's own deadline
    //     (`canRenewQuote`), not from this page's countdown: a buyer who opens
    //     the page after the deadline never ran the timer, and that was the
    //     exact case with no button at all.
    //  4. ANYTHING ELSE (hold, review, an order still being created) — report
    //     it truthfully, in words, with no raw supplier enum.
    const seen = paymentInFlight(current);
    const renewSafe = !seen && canRenewQuote(current, Date.now());
    // Owner (2026-10-04): the "window is over" lecture misfired on fresh
    // invoices and read like a block — the whole headed/status/body screen is
    // gone. What remains is actions: one tap for a fresh invoice, or open the
    // order. Only the MONEY-SEEN state keeps words, because there the words
    // are the safety ("we see your money, wait for settlement").
    // Zero-tap: a dead invoice (window over, nothing charged) is replaced
    // silently. The button-only screen below appears once the budget is
    // spent — no lecture, no status line, just actions.
    if (renewSafe && (((window as any).__autoRenewBudget as number) ?? 0) > 0) {
      return (
        <AutoRenewOnce
          key={`${quoteIdOf(current)}:${renewTick}`}
          budgetKey="__autoRenewBudget"
          fire={() => {
            setRenewTick((x) => x + 1);
            finish(false, 'renew');
          }}
          label={t('checkout.flowRenewing')}
        />
      );
    }
    if (seen) {
      return (
        <div className="center" style={{ padding: '26px 10px', textAlign: 'center' }} role="status">
          <div className="strong">{t('checkout.flowPaymentSeen')}</div>
          <div className="small mt-1">{t('checkout.flowWaitForSettlement')}</div>
          <a className="btn btn-gold btn-block mt-2" href={pagePath('/order?type=quote&id=' + encodeURIComponent(quoteIdOf(current)))}>{t('checkout.flowOpenOrder')}</a>
          </div>
      );
    }
    return (
      <div className="center" style={{ padding: '26px 10px', textAlign: 'center' }} role="status">
        <button type="button" className="btn btn-gold btn-block" onClick={() => finish(false, 'renew')}>
          <Icon name="bolt" size={14} /> {t('checkout.flowRenewInvoice')}
        </button>
        <a className="btn btn-outline btn-block mt-2" href={pagePath('/order?type=quote&id=' + encodeURIComponent(quoteIdOf(current)))}>{t('checkout.flowOpenOrder')}</a>
      </div>
    );
  }

  const dest = deliveryLine(current);
  const cbExclusive = cashbackExclusiveNote({ source: quote?.cashback_source, code: quote?.cashback_code });
  const youGet = youGetLabel || youGetText(quote);
  const nimBase = nimAmountText(current);
  // The Nimiq Pay network fee is added on top of the converted amount, so the
  // recap always says "+ fee" instead of pretending the figure is final.
  const nimText = nimBase ? t('checkout.flowFeeSuffix', { amount: nimBase }) : null;
  const localFiat = (() => { try { const { label } = quoteFaceValue(current); return label || ''; } catch { return ''; } })();

  return (
    <div>
      {stepNote && <div className="xs faint mb-1">{stepNote}</div>}

      <div className="center mt-1">
        <PaymentCountdown
          expiresAt={current.payment_expires_at || current.payment_expiry}
          onExpire={() => setExpired(true)}
        />
      </div>
      <div className="pay-wait small muted mt-1 center" style={{ display: 'flex', gap: 8, alignItems: 'center', justifyContent: 'center' }}>
        <div className="spinner" style={{ width: 14, height: 14 }} />
        <span>{t('checkout.flowWaiting')}</span>
      </div>

      <LightningRailPills />
      <div className="pay-hero mt-2">
        <div className="pay-hero-label">{t('checkout.flowDirect')}</div>
        <div className="pay-hero-amt">
          {nimText ? (
            <>
              <img className="pay-nim-ico" src={asset("/img/nimiq-hexagon.png?v=40")} alt="NIM" draggable={false} width={22} height={22} style={{ pointerEvents: "none", borderRadius: 5 }} />
              <span className="big-nim">{nimText}</span>
            </>
          ) : (
            <span className="big-nim">{t('checkout.flowAmountInNimiqPay')}</span>
          )}
        </div>
        {/* NO BALANCE, NO WARNING, ON THIS CARD.
            Owner (2026-10-06): "ödeme kartında yetersiz bakiye şeyini kaldır, o
            da NIM miktarım falan yazmasın lütfen". The payment card is the amount
            due and how to pay it — nothing else. The buyer's own NIM used to sit
            here as a second strip and a short-balance box below it; both are
            gone. The shortfall is still handled, one step later and as a POPUP
            with options: pressing either pay button asks the same question
            (continue / refresh / cancel) without crowding this card. */}
        <div className="pay-hero-youget">
          <span className="xs faint">{t('checkout.flowYouGet')}</span>
          <span className="strong pay-you-get">{youGet || t('checkout.instantDelivery')}</span>
        </div>
        {cbNimLabel && (
          <div className="small strong" style={{ marginTop: 6 }}>{t('checkout.flowCashbackLine', { nim: cbNimLabel })}</div>
        )}
        {dest && (
          <div className="pay-hero-del small">
            <Icon name={dest.icon as any} size={14} /> {dest.text}
          </div>
        )}
        {localFiat && <div className="small muted" style={{ marginTop: 6, fontWeight: 700 }}>{localFiat}</div>}
        {/* Owner (2026-10-04): inside Nimiq Pay the pay button lives INSIDE the
            hero card, right under the amount — not one card further down. */}
        {invoice && (insidePay ? (
          <div style={{ marginTop: 12 }}>
            <NimiqPayPayButton
              invoice={invoice}
              amountNim={Number(nimAmountFor(current, cachedNimRate())) || 0}
              onBeforePay={askBeforePaying}
              className="btn btn-gold btn-block btn-lg"
            />
          </div>
        ) : uri ? (
          <button
            type="button"
            className="btn btn-gold btn-block btn-lg"
            style={{ marginTop: 12 }}
            onClick={async () => {
              // The popup answers first; "continue anyway" proceeds right here.
              if (!(await askBeforePaying())) return;
              rememberLightningPayment(invoice, { kind: 'quote', ref: quoteIdOf(current) });
              void authorizePaymentLaunch(quoteIdOf(current)).catch(() => {});
              // Outside Nimiq Pay the button must DO something: desktop gets
              // the toast at once (QR hint + download links), mobile tries the
              // lightning: URI and toasts when nothing opened.
              if (!detectMobilePlatform()) {
                notifyHeroMissing();
                return;
              }
              launchLightningUri(uri, notifyHeroMissing);
            }}
          >
            {t('checkout.flowPayWithNim')}
          </button>
        ) : null)}      </div>
      <details className="checkout-details-min"><summary>{t('checkout.flowDetailsSummary')}</summary><div style={{ marginTop: 8 }}><div className="small" style={{ display: 'flex', justifyContent: 'space-between', gap: 8, marginBottom: 8 }}><span className="muted">{t('orderPage.finalTotal')}</span><span className="mono strong">{coinAmountLabelFor(current, 'BTC') || t('orderPage.amountNote')}</span></div><div className="small muted">{t('checkout.flowNimEstimateNote')}</div><CashbackFeeNotice example="nim" /><StakerCashbackLine quote={current} />{cbExclusive && (<div className="small muted mt-1" style={{ textAlign: 'center' }}><Icon name="lock" size={13} /> {cbExclusive}</div>)}<div className="alert info mt-1" style={{ marginBottom: 0, display: 'flex', gap: '8px', alignItems: 'center' }}><Icon name="bolt" size={18} /><div className="small">{payRail(current).note}</div></div><div className="small muted mt-1">{payActionLine(current)}</div></div></details>
      {note}
      {uri ? <LightningPayBlock quoteId={quoteIdOf(current)} invoice={invoice} uri={uri} onLaunch={() => rememberLightningPayment(invoice, { kind: 'quote', ref: quoteIdOf(current) })} hidePayButton /> : null}
    </div>
  );
}

function BatchPayPhase({ quote, items, onResult }: { quote: any; items: CartItem[]; onResult: (ok: boolean) => void }) {
  const { t } = useT();
  const note = (
    <div className="alert info mt-1" style={{ marginBottom: 0, textAlign: 'left', display: 'flex', gap: '8px', alignItems: 'center' }}>
      <Icon name="bag" size={18} />
      <div className="small">
        {t('checkout.flowSingleInvoice', {
          items: items.map((it) => `${it.name}${(it.qty || 1) > 1 ? ' ×' + it.qty : ''}`).join(', '),
        })}
      </div>
    </div>
  );
  return (
    <PayScreen
      quote={quote}
      name={t('checkout.flowWholeCart', { n: items.length })}
      stepNote={t('checkout.flowOneCart')}
      note={note}
      youGetLabel={items.map((it) => `${it.name}${(it.qty || 1) > 1 ? ' ×' + (it.qty || 1) : ''}`).join(', ')}
      onResult={onResult}
    />
  );
}

function PerItemPayPhase({ quote, name, qty = 1, index, total, paidCount, onResult }: { quote: any; name: string; qty?: number; index: number; total: number; paidCount: number; onResult: (ok: boolean) => void }) {
  const { t } = useT();
  const stepNote = total > 1
    ? paidCount
      ? t('checkout.flowCheckoutOfPaid', { index: index + 1, total, paid: paidCount })
      : t('checkout.flowCheckoutOf', { index: index + 1, total })
    : '';
  return <PayScreen quote={quote} name={name} stepNote={stepNote} youGetLabel={`${name} ×${qty}`} onResult={onResult} />;
}

function FailurePrompt({ issues = [], message, detail = '', activeCheckout = false, items = [], onChoice, onBackToCart }: { issues?: SupplierIssue[]; message: string; detail?: string; activeCheckout?: boolean; items?: CartItem[]; onChoice: (c: 'retry' | 'onebyone' | 'stop' | 'force') => void; onBackToCart: () => void }) {
  const { t } = useT();
  const productLabel = items.length === 1 ? items[0].name : items.length > 1 ? t('checkout.flowTheseItems') : t('checkout.flowYourCart');
  const displayMessage = activeCheckout
    ? t('checkout.flowActiveCheckoutPrice', { name: productLabel })
    : message;
  return (
    <div className="center" style={{ padding: '26px 10px', textAlign: 'center' }}>
      <div style={{ width: 52, height: 52, margin: '0 auto 12px', borderRadius: '50%', background: 'var(--stamp-soft, #f6e3dc)', display: 'grid', placeItems: 'center', color: 'var(--stamp, #c7481d)' }}>
        <Icon name="alert" size={28} />
      </div>
      <div className="strong">{t('checkout.flowCheckoutPaused')}</div>
      <SupplierProblemNotice issues={issues} />
      {!issues.length && !activeCheckout && <div className="small muted mt-1" style={{ maxWidth: 380, margin: '6px auto 0' }}>{displayMessage}</div>}
      {!issues.length && <ErrorDetail detail={detail} className="center" />}
      {activeCheckout && (
        <div className="alert info mt-2" style={{ maxWidth: 380, marginLeft: 'auto', marginRight: 'auto', marginBottom: 0, textAlign: 'left', display: 'flex', gap: '8px', alignItems: 'flex-start' }}>
          <div className="small">{t('checkout.flowUnpaidOrderHold')}</div>
        </div>
      )}
      <div style={{ maxWidth: 330, margin: '16px auto 0', display: 'flex', flexDirection: 'column', gap: '8px' }}>
        {activeCheckout && (
          <button type="button" className="btn btn-gold btn-block" onClick={() => onChoice('force')}>
            <Icon name="bolt" size={14} /> {t('checkout.flowForceContinue')}
          </button>
        )}
        {activeCheckout && (
          <a className="btn btn-outline btn-block mt-1" href={pagePath("/orders")}>
            {t('checkout.flowOpenOrdersPayExisting')}
          </a>
        )}
        <button className="btn btn-outline btn-block" onClick={onBackToCart}>
          {t('checkout.flowBackToCartItem')}
        </button>
        <button className="btn btn-ghost btn-block" onClick={() => onChoice('stop')}>
          {t('checkout.flowFinishCheckout')}
        </button>
      </div>
      <div className="xs faint mt-2">{activeCheckout ? t('checkout.flowUnfinishedStay') : t('checkout.flowNotInferred')}</div>
    </div>
  );
}

function BlockedPrompt({ issues = [], blocked, items, onChoice }: { issues?: SupplierIssue[]; blocked: any[]; items: CartItem[]; onChoice: (c: 'details' | 'remove' | 'retry' | 'stop' | 'onebyone') => void }) {
  const { t } = useT();
  const anyFixable = blocked.some((b: any) => b.fixable);
  const remaining = items.length - blocked.length;
  const nameOf = (b: any) => (items[b.index] && items[b.index].name) || b.product_id;
  return (
    <div className="center" style={{ padding: '26px 10px', textAlign: 'center' }}>
      <div style={{ width: 52, height: 52, margin: '0 auto 12px', borderRadius: '50%', background: 'var(--stamp-soft, #f6e3dc)', display: 'grid', placeItems: 'center', color: 'var(--stamp, #c7481d)' }}>
        <Icon name="alert" size={28} />
      </div>
      <div className="strong">
        {anyFixable
          ? blocked.length === 1
            ? t('checkout.flowOneNeedsFix')
            : t('checkout.flowManyNeedFix', { n: blocked.length })
          : blocked.length === 1
          ? t('checkout.flowOneCantOrder')
          : t('checkout.flowManyCantOrder', { n: blocked.length })}
      </div>
      <SupplierProblemNotice issues={issues} />
      <div style={{ maxWidth: 360, margin: '12px auto 0', display: 'grid', gap: '8px', textAlign: 'left' }}>
        {blocked.map((b) => (
          <div key={b.product_id + b.index} className="alert error" style={{ marginBottom: 0, display: 'flex', gap: '6px', alignItems: 'center' }}>
            <Icon name="x" size={16} />
            <div className="small">
              <span className="strong">{nameOf(b)}</span> — {b.reason || t('checkout.flowRefused')}
            </div>
          </div>
        ))}
      </div>
      <div className="small muted mt-2" style={{ maxWidth: 360, margin: '12px auto 0' }}>
        {remaining > 0
          ? t('checkout.flowRemoveRecheck', { count: remaining })
          : t('checkout.flowNothingCharged')}
      </div>
      <div style={{ maxWidth: 330, margin: '16px auto 0', display: 'flex', flexDirection: 'column', gap: '8px' }}>
        {anyFixable && (
          <button className="btn btn-gold btn-block" onClick={() => onChoice('details')}>
            {t('checkout.flowFixDelivery')}
          </button>
        )}
        <button className={anyFixable ? 'btn btn-outline btn-block' : 'btn btn-gold btn-block'} onClick={() => onChoice('remove')}>
          {blocked.length === 1
            ? t('checkout.flowRemoveOneContinue', { name: nameOf(blocked[0]) })
            : t('checkout.flowRemoveManyContinue', { n: blocked.length })}
        </button>
        <button className="btn btn-outline btn-block" onClick={() => onChoice('retry')}>
          {t('checkout.flowRetryOneOrder')}
        </button>
        <button className="btn btn-ghost btn-block" onClick={() => onChoice('stop')}>
          {t('checkout.flowBackToCart')}
        </button>
      </div>
      <div className="xs faint mt-2">{t('checkout.flowCartUntouched')}</div>
    </div>
  );
}

function isActiveCheckoutMessage(reason: string, msg: string): boolean {
  return reason === 'active-checkout' || (reason === 'quote' && /unpaid order|unpaid checkout|existing payment|existing order|safety hold/i.test(msg));
}

/**
 * takeLastPayDetail — the exact failure the wallet reported at the pay step.
 *
 * Owner (2026-10-05): "o kırmızı yerde tam hataları söyleyebilirdi". The pay
 * button writes the wallet's own type/code/message to a module-level handoff
 * (window.__lastPayDetail) when a payment fails; the prompt that follows reads
 * it once and clears it, so a later unrelated failure never inherits it.
 */
function takeLastPayDetail(): string {
  const w = window as any;
  const detail = String(w.__lastPayDetail || '');
  w.__lastPayDetail = '';
  return detail;
}

function whyFor(reason: string, name: string, msg: string): string {
  if (isActiveCheckoutMessage(reason, msg)) {
    return tr('checkout.flowActiveCheckoutPrice', { name });
  }
  // The backend's own sentence belongs on EVERY failure screen, not just the
  // pricing one: a buyer staring at "the checkout paused" needs to know whether
  // it was the wallet, the supplier or the price.
  const suffix = msg ? ' — ' + msg : '';
  const map: Record<string, string> = {
    quote: tr('checkout.flowWhyQuote', { name, msg: suffix }),
    expired: tr('checkout.flowWhyExpired', { name }),
    timeout: tr('checkout.flowWhyTimeout', { name }),
    failed: tr('checkout.flowWhyFailed', { name }),
    manual_review: tr('checkout.flowWhyManual', { name }),
    cancel: tr('checkout.flowWhyCancel', { name }),
    limit: tr('checkout.flowWhyLimit', { name }),
    invalid: tr('checkout.flowWhyInvalid', { name }),
    // The wallet refused the spend for want of NIM. "The checkout paused" is
    // true and useless; this says what actually happened, and the wallet's own
    // sentence is appended below it.
    insufficient: tr('orderPage.nimiqPay.insufficient'),
    updateRequired: tr('wallet.updatePayToast'),
  };
  const base = map[reason] || tr('checkout.flowWhyGeneric', { name });
  if (reason === 'quote' || !msg) return base;
  return base + suffix;
}

/**
 * DailyLimitPrompt — the daily/monthly purchase budget refused this order.
 *
 * This is neither a rate limit nor a pricing failure, so the old wording for
 * both was actively misleading: "Could not lock a live price … Too many
 * requests — wait a few seconds and try again" told the buyer to retry
 * something that cannot succeed for hours. The screen instead names the limit,
 * shows used / left against both ceilings, says when the window slides, and
 * never offers "skip to the next item" (the same ceiling refuses it too).
 */
function DailyLimitPrompt({
  limit,
  subject,
  paidCount,
  restCount,
  onChoice,
  onBackToCart,
}: {
  limit: DailyLimit;
  subject: string;
  paidCount: number;
  restCount: number;
  onChoice: (c: 'retry' | 'cart') => void;
  onBackToCart: () => void;
}) {
  const { t } = useT();
  // The countdown has to move on its own: the buyer may sit on this screen
  // deciding whether to wait the window out.
  const [, tick] = useState(0);
  useEffect(() => {
    const timer = setInterval(() => tick((n) => n + 1), 30_000);
    return () => clearInterval(timer);
  }, []);

  const ordersPct = limit.maxOrders > 0 ? Math.min(100, (limit.usedOrders / limit.maxOrders) * 100) : 0;
  const usdPct = limit.maxUSD > 0 ? Math.min(100, (limit.usedUSD / limit.maxUSD) * 100) : 0;

  return (
    <div className="center" style={{ padding: '26px 10px', textAlign: 'center' }}>
      <div style={{ width: 52, height: 52, margin: '0 auto 12px', borderRadius: '50%', background: 'var(--gold-soft)', display: 'grid', placeItems: 'center', color: 'var(--gold)' }}>
        <Icon name="clock" size={28} />
      </div>
      <div className="strong">{limitHeadline(limit)}</div>
      <div className="small muted mt-1" style={{ maxWidth: 380, margin: '6px auto 0' }}>{limitSentence(limit, subject)}</div>

      <div className="card" style={{ maxWidth: 380, margin: '14px auto 0', textAlign: 'left' }}>
        <div className="row between" style={{ alignItems: 'baseline' }}>
          <div className="small strong">{limit.period === 'monthly' ? t('checkout.flowMonthlyLimit') : t('checkout.flowDailyLimit')}</div>
          <div className="xs faint">{limit.period === 'monthly' ? t('checkout.flowUtcMonth') : t('checkout.flowRolling24')}</div>
        </div>
        {limit.maxOrders > 0 && (
          <>
            <div className="row between mt-2 mb-1" style={{ alignItems: 'baseline' }}>
              <div className="xs muted">{t('checkout.flowOrdersLabel')}</div>
              <div className="xs strong">{Math.min(limit.usedOrders, limit.maxOrders)} / {limit.maxOrders}</div>
            </div>
            <LimitBar pct={ordersPct} />
          </>
        )}
        {limit.maxUSD > 0 && (
          <>
            <div className="row between mt-2 mb-1" style={{ alignItems: 'baseline' }}>
              <div className="xs muted">{t('checkout.flowSpendingLabel')}</div>
              <div className="xs strong">{fmtUSD(Math.min(limit.usedUSD, limit.maxUSD))} / {fmtUSD(limit.maxUSD)}</div>
            </div>
            <LimitBar pct={usdPct} />
          </>
        )}
        <div className="xs muted mt-2" style={{ display: 'flex', gap: '6px', alignItems: 'center' }}>
          <Icon name="clock" size={13} /> <span>{limitResetLine(limit)}</span>
        </div>
        <div className="xs faint mt-1">{limitHint(limit)}</div>
      </div>

      {paidCount > 0 && (
        <div className="small mt-2" style={{ color: 'var(--green, #2F5540)', fontWeight: 700 }}>
          {t('checkout.flowPaidItems', { count: paidCount })}
        </div>
      )}

      <div style={{ maxWidth: 330, margin: '16px auto 0', display: 'flex', flexDirection: 'column', gap: '8px' }}>
        <button className="btn btn-gold btn-block" onClick={onBackToCart}>
          {restCount > 1 ? t('checkout.flowBackToCartAmount') : t('checkout.flowBackToCartItem')}
        </button>
        <button className="btn btn-outline btn-block" onClick={() => onChoice('retry')}>
          {t('checkout.flowTryAgainOrder')}
        </button>
      </div>
      <div className="xs faint mt-2">{t('checkout.flowNothingChargedRest', { count: restCount })}</div>
    </div>
  );
}

/** One filled track for the limit card (same visual as the profile limits). */
function LimitBar({ pct }: { pct: number }) {
  return (
    <div style={{ width: '100%', display: 'block', height: '8px', background: 'var(--surface-3)', borderRadius: '99px', overflow: 'hidden' }}>
      <div style={{ width: Math.max(0, Math.min(100, pct)) + '%', height: '100%', background: 'var(--gold-grad)' }} />
    </div>
  );
}

function ItemFailedPrompt({ issues = [], it, reason, msg, detail = '', index, total, paidCount, onChoice, onBackToCart }: { issues?: SupplierIssue[]; it: CartItem; reason: string; msg: string; detail?: string; index: number; total: number; paidCount: number; onChoice: (c: 'retry' | 'skip' | 'stop' | 'force') => void; onBackToCart: () => void }) {
  const { t } = useT();
  const why = whyFor(reason, it.name, msg);
  const activeCheckout = isActiveCheckoutMessage(reason, msg);
  const remaining = total - index - 1;
  return (
    <div className="center" style={{ padding: '26px 10px', textAlign: 'center' }}>
      <div style={{ width: 52, height: 52, margin: '0 auto 12px', borderRadius: '50%', background: 'var(--stamp-soft, #f6e3dc)', display: 'grid', placeItems: 'center', color: 'var(--stamp, #c7481d)' }}>
        <Icon name="alert" size={28} />
      </div>
      <div className="strong">{t('checkout.flowCheckoutPaused')}</div>
      <SupplierProblemNotice issues={issues} />
      {!issues.length && !activeCheckout && <div className="small muted mt-1" style={{ maxWidth: 380, margin: '6px auto 0' }}>{why}</div>}
      {!issues.length && !activeCheckout && <ErrorDetail detail={detail} className="center" />}
      {activeCheckout && (
        <div className="alert info mt-2" style={{ marginBottom: 0, textAlign: 'left', display: 'flex', gap: '8px', alignItems: 'flex-start' }}>
          <div className="small">{t('checkout.flowUnpaidOrderNote')}</div>
        </div>
      )}
      {paidCount > 0 && (
        <div className="small mt-2" style={{ color: 'var(--green, #2F5540)', fontWeight: 700 }}>
          {t('checkout.flowPaidItems', { count: paidCount })}
        </div>
      )}
      {reason === 'timeout' && (
        <div className="alert error mt-2" style={{ marginBottom: 0, textAlign: 'left', display: 'flex', gap: '8px', alignItems: 'center' }}>
          <Icon name="alert" size={16} />
          <div className="small">{t('checkout.flowMayStillLand')}</div>
        </div>
      )}
      <div style={{ maxWidth: 320, margin: '16px auto 0', display: 'flex', flexDirection: 'column', gap: '8px' }}>
        {activeCheckout ? (
          <button type="button" className="btn btn-gold btn-block" onClick={() => onChoice('force')}>
            <Icon name="bolt" size={14} /> {t('checkout.flowForceContinue')}
          </button>
        ) : !issues.length ? (
          <button className="btn btn-gold btn-block" onClick={() => onChoice('retry')}>
            {t('checkout.flowReopenCheckout', { name: it.name })}
          </button>
        ) : null}
        {activeCheckout && (
          <a className="btn btn-outline btn-block" href={pagePath("/orders")}>
            {t('checkout.flowOpenOrdersPayExisting')}
          </a>
        )}
        {!activeCheckout && !issues.length && remaining > 0 && (
          <button className="btn btn-outline btn-block" onClick={() => onChoice('skip')}>
            {t('checkout.flowSkipNextItem', { n: remaining })}
          </button>
        )}
        <button className="btn btn-outline btn-block" onClick={onBackToCart}>
          {t('checkout.flowBackToCartRemoveChange')}
        </button>
        <button className="btn btn-ghost btn-block" onClick={() => onChoice('stop')}>
          {t('checkout.flowFinishCheckout')}
        </button>
      </div>
      <div className="xs faint mt-2">{t('checkout.flowUnfinishedStay')}</div>
    </div>
  );
}

/**
 * AlreadyPaidPhase — the order the safety hold pointed at turns out to be paid.
 *
 * This screen exists because the alternative is worse in both directions: a
 * second payment would charge a shopper for an order they already own, and a
 * bare "completed" beat (what happened, owner report 2026-10-06) tells them
 * nothing and drops them on the orders list wondering what they just bought.
 * So the shop states the fact, names the order, and offers the two things that
 * make sense: open THAT order, or go back to the cart.
 */
function AlreadyPaidPhase({ quoteId, status, onOpenOrders, onBackToCart }: { quoteId: string; status: string; onOpenOrders: () => void; onBackToCart: () => void }) {
  const { t } = useT();
  return (
    <div className="center" style={{ padding: '26px 10px', textAlign: 'center' }}>
      <div className="pay-check" style={{ margin: '0 auto 12px' }}>
        <Icon name="check" size={30} />
      </div>
      <div className="strong">{t('checkout.flowAlreadyPaid')}</div>
      <div className="small muted mt-1" style={{ maxWidth: 380, margin: '6px auto 0' }}>
        {t('checkout.flowAlreadyPaidBody')}
      </div>
      <div style={{ maxWidth: 330, margin: '16px auto 0', display: 'flex', flexDirection: 'column', gap: '8px' }}>
        {quoteId ? (
          <a className="btn btn-gold btn-block" href={pagePath('/order?type=quote&id=' + encodeURIComponent(quoteId))}>
            {t('checkout.flowOpenThatOrder')}
          </a>
        ) : null}
        <button type="button" className="btn btn-outline btn-block" onClick={onOpenOrders}>
          {t('checkout.flowSeeAllOrders')}
        </button>
        <button type="button" className="btn btn-ghost btn-block" onClick={onBackToCart}>
          {t('checkout.flowBackToCartItem')}
        </button>
      </div>
      {status ? <div className="xs faint mt-2">{status}</div> : null}
    </div>
  );
}

function SuccessBeat({ paidCount, delivered = [], onDone }: { paidCount: number; delivered?: CartItem[]; onDone: () => void }) {
  const { t } = useT();
  const done = useRef(false);
  const finish = useCallback(() => {
    if (done.current) return;
    done.current = true;
    onDone();
  }, [onDone]);

  useEffect(() => {
    // The success state is only a short confirmation beat; Orders is the
    // destination. Keep the explicit button as a fallback for slow/blocked
    // client-side navigation, but the normal path redirects automatically.
    const t = setTimeout(finish, 900);
    return () => clearTimeout(t);
  }, [finish]);
  // Channel-accurate confirmation: a phone-only cart has no code and no email,
  // so the old "your code is delivered to the address" line was wrong for it.
  const del = deliverySummary(delivered);
  const what =
    del.channel === 'phone'
      ? t('checkout.flowDestPhone')
      : del.channel === 'both'
        ? t('checkout.flowDestBoth')
        : del.hasEsim
          ? t('checkout.flowDestEsim')
          : t('checkout.flowDestCode');
  return (
    <div style={{ padding: '30px 12px 26px', textAlign: 'center' }}>
      <div className="pay-check">
        <Icon name="check" size={34} />
      </div>
      <div className="strong" style={{ fontSize: '1.3rem', marginTop: '14px', color: 'var(--ink-on-green)' }}>
        {paidCount === 1 ? t('checkout.flowPaymentReceived') : t('checkout.flowAllPaymentsReceived')}
      </div>
      <div className="small muted mt-1">
        {paidCount === 1
          ? t('checkout.flowSettled', { what })
          : t('checkout.flowSettledMany', { n: paidCount, what })}
      </div>
      <div className="small faint mt-3" style={{ display: 'flex', gap: '8px', alignItems: 'center', justifyContent: 'center' }}>
        <div className="spinner" style={{ width: 14, height: 14 }} />
        <span>{t('checkout.flowOpeningOrders')}</span>
      </div>
      <button className="btn btn-gold mt-2" type="button" onClick={finish}>
        <Icon name="receipt" size={16} />
        <span>{t('checkout.flowOpenOrdersNow')}</span>
      </button>
    </div>
  );
}

function Summary({ paid, kept, stoppedEarly, onTrack, onBackToCart }: { paid: CartItem[]; kept: CartItem[]; stoppedEarly: boolean; onTrack: () => void; onBackToCart: () => void }) {
  const { t } = useT();
  const nonePaid = paid.length === 0;
  return (
    <div style={{ padding: '16px 4px 8px' }}>
      <div className="center" style={{ textAlign: 'center' }}>
        {nonePaid ? (
          <>
            <div className="pay-check idle">
              <Icon name="bag" size={32} />
            </div>
            <div className="strong" style={{ fontSize: '1.25rem', marginTop: '12px' }}>
              {t('checkout.flowNothingPaid')}
            </div>
            <div className="small muted mt-1" style={{ maxWidth: 380, margin: '6px auto 0' }}>
              {kept.length === 1 ? t('checkout.flowStillInCartOne') : t('checkout.flowStillInCartMany')}
            </div>
          </>
        ) : (
          <>
            <div className="pay-check">
              <Icon name="check" size={34} />
            </div>
            <div className="strong" style={{ fontSize: '1.25rem', marginTop: '12px', color: 'var(--ink-on-green)' }}>
              {t('checkout.flowOrdersPaidBadge', { count: paid.length })}
            </div>
            <div className="small muted mt-1" style={{ maxWidth: 360, margin: '6px auto 0' }}>
              {t('checkout.flowPaidGoTo')}
            </div>
          </>
        )}
      </div>
      {kept.length > 0 && !nonePaid && (
        <div className="alert info mt-2" style={{ marginBottom: 0, textAlign: 'left', display: 'flex', gap: '8px', alignItems: 'center' }}>
          <Icon name="bag" size={18} />
          <div className="small">
            {stoppedEarly ? t('checkout.stoppedEarly') + ' ' : ''}
            {t('checkout.flowNotCompleted', { n: kept.length, names: kept.map((k) => k.name).join(', ') })}
          </div>
        </div>
      )}
      {nonePaid ? (
        <>
          <button className="btn btn-gold btn-block mt-2" onClick={onBackToCart}>
            {t('checkout.flowBackToCart')}
          </button>
          <button className="btn btn-outline btn-block mt-1" onClick={onTrack}>
            {t('checkout.flowTrackOrders')}
          </button>
        </>
      ) : (
        <>
          <button className="btn btn-gold btn-block mt-2" onClick={onTrack}>
            {t('checkout.flowTrackOrders')}
          </button>
          <button className="btn btn-outline btn-block mt-1" onClick={onBackToCart}>
            {t('checkout.flowBackToCart')}
          </button>
        </>
      )}
    </div>
  );
}
