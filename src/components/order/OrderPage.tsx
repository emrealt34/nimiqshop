import { CashbackFeeNotice } from '../checkout/CashbackFeeNotice';
import { productMoney } from '../../lib/productMoney';
/**
 * OrderPage.tsx — React port of pages/order.js: single purchase detail with
 * live tracking timeline, delivery contents, summary, refund + rating cards,
 * inline support thread, and a reload-safe "pay now" block for quotes.
 * Handles legacy order rows and direct CryptoRefills-Lightning quotes.
 */
import { useCallback, useEffect, useRef, useState, type ReactNode } from 'react';
import { Icon } from '../ui/Icon';
import { UnifiedThumb, BrandThumbStack } from '../ui/UnifiedThumb';
import { FlagMark } from '../ui/FlagMark';
import { AppRoot } from '../AppRoot';
import { openLoginSheet } from '../shell/SiteShell';
import { friendlyApiMessage, getOrder, refreshOrder, getQuote, refreshQuote, rateOrder, rateQuote, getProduct, allowNewPurchase, createQuote } from '../../lib/api';
import { isAuthed, getAddress } from '../../lib/session';
import { useSession } from '../../lib/useSession';
import { quoteStages, isTerminalStatus, shouldAskRating, ratingDismissedKey } from '../../lib/orderTrack';
import { brandMetaForTitle } from '../../lib/catalogMeta';
import { mapKind } from '../../lib/catalog';
import { lightningPaymentURI, rememberLightningPayment } from '../../lib/hub';
import { isQuotePayable, quoteBolt11, canRenewQuote, paymentInFlight, paymentWindowVerifying, renewItemFromQuote, renewInfoFromQuote, canRebuildRequest } from '../../lib/pay';
import { supplierStatusLabel } from '../../lib/supplierStatus';
import { buildOrderRequest } from '../../lib/delivery';
import { uuid } from '../../lib/format';
import { inNimiqPay } from '../../lib/miniapp';
import {
  fmtNum,
  fmtDate,
  queryParam,
  shortAddr,
  countryName,
  stripHtml,
  cleanFamilyName,
  cleanProductLabel,
  cleanBatchProductLabels,
  cleanSupplierTerms,
  stripCryptoCopy,
  quoteFaceValue,
  safeRichHTML,
  MAX_QTY,
} from '../../lib/format';
import { siteName } from '../../lib/config';
import { useInterval } from '../../lib/useInterval';
import { useToast, useSheet } from '../AppProviders';
import { itemKey, saveCart, type CartItem } from '../../lib/cartStore';
import { openCartSheet } from '../cart/CartSheet';
import {
  StatusBadge,
  StageTimeline,
  CopyButton,
  Kv,
  KvCard,
  EmptyState,
  ErrorState,
  SkeletonLines,
  kindMeta,
  StarsDisplay,
  StarPicker,
  NimAmount,
  NimWalletLabel,
} from '../ui/uiKit';
import { LightningPayBlock } from '../checkout/LightningPayBlock';
import { NimiqPayPayButton } from '../checkout/NimiqPayPayButton';
import { SimulatedPayBlock } from '../checkout/SimulatedPayBlock';
import { isTestMode } from '../../lib/config';
import { UsdtPayBlock } from '../checkout/UsdtPayBlock';
import { PaymentCountdown } from '../checkout/PaymentCountdown';
import { deliverySummary, payRail, payActionLine, linesOf , coinAmountLabel, coinAmountLabelFor } from '../../lib/deliveryCopy';
import { hasLockedNim, nimAmountText } from '../../lib/nim';
import { StakerCashbackLine } from '../staker/StakerCashback';
import { asset } from '../../lib/asset';
import { useT, t as i18nT, type Translator } from '../../i18n';
import { pagePath } from '../../lib/asset';

/** Redeem instructions as i18n keys (resolved with t() at render, so they
 *  follow the language switcher). */
const ORDER_REDEEM_STEPS: Record<string, string[]> = {
  gift_card: ['orderPage.redeemGift1', 'orderPage.redeemGift2', 'orderPage.redeemGift3'],
  topup: ['orderPage.redeemTopup1', 'orderPage.redeemTopup2', 'orderPage.redeemTopup3'],
  esim: ['orderPage.redeemEsim1', 'orderPage.redeemEsim2', 'orderPage.redeemEsim3'],
  // A cart holding BOTH an email-delivered product and a phone top-up: the two
  // halves are redeemed differently, so say so instead of picking one.
  mixed: ['orderPage.redeemMixed1', 'orderPage.redeemMixed2', 'orderPage.redeemMixed3'],
};

/**
 * Body of the "Rate your delivery" sheet.
 *
 * A component rather than a pre-built element: the sheet outlives the render
 * pass that opened it, so it must subscribe to the translator itself. That way
 * a rating prompt that opens before this visitor's dictionary chunk arrives
 * (or a language switch while it is open) re-renders in the right language
 * instead of freezing in English.
 */
function RateDeliveryPrompt({ onRate, onLater }: { onRate: (stars: number) => void; onLater: () => void }) {
  const { t } = useT();
  return (
    <div className="center" style={{ padding: '4px 2px 2px' }}>
      <div className="strong" style={{ fontSize: '1.05rem' }}>{t('orderPage.howWasDelivery')}</div>
      <div className="small muted mt-1">{t('orderPage.howWasIt')}</div>
      <div className="mt-2" style={{ display: 'flex', justifyContent: 'center' }}>
        <StarPicker size={34} onSelect={onRate} />
      </div>
      <button className="btn btn-ghost btn-block mt-2" onClick={onLater}>
        <Icon name="clock" size={16} />
        <span className="btn-label">{t('orderPage.rateLater')}</span>
      </button>
      <div className="xs faint mt-1">{t('orderPage.rateNoPressure')}</div>
    </div>
  );
}

function giftChannelLabel(ch: string) {
  // "email" is the only note channel there is — the shop has no SMS sender.
  return ch === 'email' ? i18nT('orderPage.byEmail') : String(ch || '');
}

/* ---------------- How to redeem + terms (on the order itself) ---------------- */
function RedeemInfoCard({ family, country, kind, deliveredNote = '', title = '' }: { family: string; country: string; kind: string; deliveredNote?: string; title?: string }) {
  const { t } = useT();
  const [rich, setRich] = useState<any>(null);
  useEffect(() => {
    let alive = true;
    (async () => {
      try {
        if (!family || !country || String(country).length !== 2) return;
        const data = await getProduct(family, country);
        const families: any[] = Array.isArray(data) ? data : data && data.products ? [data] : [];
        const fam = families.length ? ({ products: families.flatMap((f: any) => Array.isArray(f.products) ? f.products : []), family: families[0]?.family || families[0]?.brand || family, _families: families } as any) : null;
        if (fam && alive) setRich(fam.rich_description || {});
      } catch {
        /* family unreachable → generic steps stay */
      }
    })();
    return () => {
      alive = false;
    };
  }, [family, country]);

  const genericSteps = ORDER_REDEEM_STEPS[kind] || ORDER_REDEEM_STEPS.gift_card;
  const howTo = rich && rich.how_to_redeem ? (
    <div className="rich-html howto-rich" dangerouslySetInnerHTML={{ __html: safeRichHTML(rich.how_to_redeem) }} />
  ) : (
    <ol className="howto-steps">
      {genericSteps.map((st, i) => (
        <li key={i}>{t(st)}</li>
      ))}
    </ol>
  );
  const geoBanner =
    rich && rich.redeem_geo ? (
      <div className="alert info mt-1" style={{ marginBottom: 0 }}>
        <Icon name="info" size={16} />
        <div className="small">{t('orderPage.mayOnlyRedeem', { geo: stripHtml(rich.redeem_geo) })}</div>
      </div>
    ) : null;
  const termsHtml = stripCryptoCopy((rich && rich.term_and_conditions) || '', { scrubNames: false });
  const termsText = cleanSupplierTerms('');

  return (
    <div className="card">
      <div className="card-title">
        <Icon name="gift" size={16} />
        <span>{title ? t('orderPage.howToRedeemOf', { title }) : t('orderPage.howToRedeem')}</span>
      </div>
      {geoBanner}
      {howTo}
      {deliveredNote ? (
        <div className="howto-note small">
          <Icon name="info" size={15} />
          <span>{deliveredNote}</span>
        </div>
      ) : null}
      {termsHtml || termsText ? (
        <details className="howto-terms mt-1">
          <summary className="xs">{termsHtml ? t('productPage.termsFromSupplier') : t('productPage.supplierTerms')}</summary>
          {termsHtml ? (
            <div className="xs muted rich-html" dangerouslySetInnerHTML={{ __html: safeRichHTML(termsHtml) }} />
          ) : (
            <div className="xs muted">{termsText}</div>
          )}
        </details>
      ) : null}
    </div>
  );
}

/* ---------------- Order thumb ---------------- */
function OrderThumb({ family, country, image = '', bgColor = '' }: any) {
  const { t } = useT();
  const [resolved, setResolved] = useState(image ? { logo: image, bg: 'rgb(255, 255, 255)' } : null);
  useEffect(() => {
    if (image) {
      setResolved({ logo: image, bg: 'rgb(255, 255, 255)' });
      return;
    }
    let alive = true;
    const clean = cleanProductLabel(family) || cleanFamilyName(family);
    if (clean) {
      brandMetaForTitle(clean, country).then((m) => {
        if (alive && m && m.logo) setResolved({ logo: m.logo, bg: m.bg || 'rgb(255, 255, 255)' });
      });
    }
    return () => {
      alive = false;
    };
  }, [family, country, image, bgColor]);

  // Unified thumb — same structure as homepage, per-brand bg so dark logos stay visible
  return (
    <div style={{ width: 160, flex: 'none' }}>
      <UnifiedThumb src={resolved?.logo || ''} alt={family || t('orderPage.productFallback')} bg={resolved?.bg || bgColor || 'rgb(255,255,255)'} />
    </div>
  );
}

function ProductThumbStack({ titles, country }: { titles: string[]; country?: string }) {
  const cleanTitles = titles
    .map((title) => cleanProductLabel(title))
    .filter(Boolean)
    .slice(0, 3);
  return (
    <div style={{ width: 160, flex: 'none' }}>
      <BrandThumbStack titles={cleanTitles} country={country} />
    </div>
  );
}


function detailBatchTitles(record: any, fallback: string): string[] {
  const scopes = [record, record?.quote, record?.payload].filter((scope) => scope && typeof scope === 'object');

  for (const scope of scopes) {
    for (const key of ['lines', 'items', 'products']) {
      const raw = scope[key];
      if (!Array.isArray(raw)) continue;
      const values = raw.map((item: any) =>
        typeof item === 'string'
          ? item
          : item?.product_id || item?.id || item?.product_name || item?.brand_name || item?.family || item?.name || item?.title || ''
      );
      const titles = cleanBatchProductLabels(values);
      if (titles.length > 1) return titles.slice(0, 3);
    }
  }

  for (const scope of scopes) {
    for (const key of ['batch_summary_items', 'batch_summary', 'display_title', 'title', 'product_name', 'product_id']) {
      const raw = scope[key];
      if (raw == null || raw === '') continue;
      const values = Array.isArray(raw) ? raw : String(raw).split(/\s*,\s*/);
      const titles = cleanBatchProductLabels(values);
      if (titles.length > 1) return titles.slice(0, 3);
    }
  }

  const batchCount = Math.max(
    0,
    ...scopes.map((scope) => Number(scope.batch_items) || 0)
  );
  const isBatch = scopes.some((scope) => scope.is_batch === true);
  if (batchCount > 1 || (isBatch && batchCount === 0)) {
    const count = batchCount > 1 ? batchCount : 2;
    return Array.from({ length: Math.min(count, 3) }, (_, index) => `Cart item ${index + 1}`);
  }

  return cleanBatchProductLabels(fallback).slice(0, 1);
}

function selectedAmountLabel(q: any) {
  const qty = Number(q.quantity) || 1;
  const { value: perUnit, currency: ccy } = quoteFaceValue(q);
  if (!(perUnit > 0) || !ccy) return '';
  return qty > 1 ? `${fmtNum(perUnit * qty)} ${ccy} (${fmtNum(perUnit)} × ${qty})` : `${fmtNum(perUnit)} ${ccy}`;
}

function copyIdNode(text: string) {
  return (
    <span className="row" style={{ gap: '6px', justifyContent: 'flex-end' }}>
      <span className="mono small">{shortAddr(text, 10, 8)}</span>
      <CopyButton getText={text} label="" />
    </span>
  );
}

function refundCardForOrder(refund: any, t: Translator) {
  if (!refund) return null;
  const amount = refund.amount !== undefined && refund.amount !== null && refund.amount !== '' ? `${refund.amount} ${refund.currency || ''}`.trim() : t('orderPage.recordedBySupplier');
  return (
    <div className="card">
      <div className="card-title">{t('orderPage.supplierRefund')}</div>
      <Kv
        rows={[
          [t('orderPage.rowAmount'), amount],
          refund.method ? [t('orderPage.rowMethod'), String(refund.method)] : null,
          refund.address
            ? [
                t('orderPage.rowRefundAddress'),
                <span key="a" className="row" style={{ gap: '6px', justifyContent: 'flex-end' }}>
                  <span className="mono small">{shortAddr(refund.address, 10, 8)}</span>
                  <CopyButton getText={refund.address} label={t('orderPage.labelAddress')} />
                </span>,
              ]
            : null,
        ].filter(Boolean) as any}
      />
      <div className="small muted mt-1">{t('orderPage.supplierRefundNote')}</div>
    </div>
  );
}

function refundCardForQuote(refund: any, t: Translator) {
  const s = String(refund.status || '');
  if (s === 'refunding') {
    return (
      <div className="card">
        <div className="card-title">{t('orderPage.yourRefund')}</div>
        <div className="row" style={{ gap: '10px', alignItems: 'center' }}>
          <div className="spinner" style={{ width: '20px', height: '20px' }} />
          <div className="strong">{t('orderPage.refundProgress')}</div>
        </div>
        <div className="small muted mt-1">{refund.detail || t('orderPage.refundProgressDesc')}</div>
        <div className="alert info mt-1" style={{ marginBottom: 0 }}>
          <Icon name="info" size={16} />
          <div className="small">{t('orderPage.refundAutoNote')}</div>
        </div>
      </div>
    );
  }
  return (
    <div className="card">
      <div className="card-title">{t('orderPage.yourRefund')}</div>
      <Kv
        rows={[
          [t('orderPage.rowAmount'), `${refund.amount_nim} NIM`],
          refund.refund_address
            ? [
                t('orderPage.backToYourWallet'),
                <span key="w" className="row" style={{ gap: '6px', justifyContent: 'flex-end' }}>
                  <span className="mono small">{shortAddr(refund.refund_address, 12, 10)}</span>
                  <CopyButton getText={refund.refund_address} label={t('orderPage.labelWallet')} />
                </span>,
              ]
            : null,
          refund.tx_hash
            ? [
                t('orderPage.refundTx'),
                <span key="t" className="row" style={{ gap: '6px', justifyContent: 'flex-end' }}>
                  <span className="mono small">{shortAddr(refund.tx_hash, 10, 8)}</span>
                  <CopyButton getText={refund.tx_hash} label={t('orderPage.labelTxHash')} />
                </span>,
              ]
            : null,
        ].filter(Boolean) as any}
      />
      <div className="small muted mt-1">{refund.detail || t('orderPage.refundDoneDesc')}</div>
    </div>
  );
}

/* ---------------- Help card (replaces the inline support thread) --------- */
function HelpCard() {
  const { t } = useT();
  return (
    <a
      className="card"
      href={pagePath("/support")}
      style={{ display: 'block', textDecoration: 'none', color: 'inherit' }}
    >
      <div className="row between" style={{ alignItems: 'center', gap: 10 }}>
        <div>
          <div className="card-title" style={{ margin: 0 }}>
            <Icon name="headset" size={16} /> {t('orderPage.helpTitle')}
          </div>
          <div className="xs faint mt-1">{t('orderPage.helpHint')}</div>
        </div>
        <Icon name="chevron" size={18} />
      </div>
    </a>
  );
}

function RatingCard({ status, rating, onRate }: { status: string; rating: number; onRate: (r: number) => Promise<unknown> }) {
  const { t } = useT();
  const { toast } = useToast();
  if (!isTerminalStatus(status)) return null;
  if (['failed', 'refunded', 'expired', 'denied', 'blocked'].includes(String(status).toLowerCase())) return null;
  if (rating && rating > 0) {
    return (
      <div className="card">
        <div className="card-title">{t('orderPage.ratePurchase')}</div>
        <div>
          <div className="small muted mb-1">{t('orderPage.yourPublicRating')}</div>
          <div className="row" style={{ gap: '10px', alignItems: 'center' }}>
            <StarsDisplay rating={rating} size={26} />
            <span className="small faint">{t('orderPage.thanks')}</span>
          </div>
        </div>
      </div>
    );
  }
  return (
    <div className="card">
      <div className="card-title">{t('orderPage.ratePurchase')}</div>
      <div className="small muted mb-1">{t('orderPage.howWasIt')}</div>
      <StarPicker
        size={26}
        onSelect={async (r) => {
          try {
            await onRate(r);
            toast(t('orderPage.ratedThanks'), 'success');
          } catch (err) {
            toast(friendlyApiMessage(err, t('orderPage.rateError')), 'error');
          }
        }}
      />
    </div>
  );
}

/**
 * QuoteDeliveryCard — the delivered contents of a quote/batch order.
 *
 * The supplier returns ONE fulfillment entry per delivered unit, so a cart of
 * "steam ×1 + airbnb ×2" comes back as three entries. This used to be rendered
 * nowhere for quotes (only legacy orders had a delivery card), which meant a
 * paid batch order showed the buyer no codes at all. Entries are grouped by
 * brand so a ×2 line shows both of its codes under one heading.
 */
function QuoteDeliveryCard({ q, fulfillment }: { q: any; fulfillment: any }) {
  const { t } = useT();
  const entries: any[] = Array.isArray(fulfillment)
    ? fulfillment
    : fulfillment && typeof fulfillment === 'object'
      ? [fulfillment]
      : [];
  // A delivery counts as confirmed if the supplier returned anything for it.
  const usable = entries.filter((f) => f && (f.code || f.pin || f.link || f.barcode_value || f.instructions));
  if (!usable.length) return null;

  const del = deliverySummary(q);
  const lines = linesOf(q);

  // Group by brand + denomination so a 4× cart reads as its real items.
  const groups = new Map<string, { title: string; kind: string; count: number }>();
  for (const f of usable) {
    const brand = cleanProductLabel(f.brand_name || f.family || q.product_id) || t('orderPage.itemFallback');
    const denom = String(f.denomination || '');
    const key = brand + '|' + denom;
    const match = lines.find((l) => String(l.product_id || '').toLowerCase() === brand.toLowerCase());
    const title = brand;
    if (!groups.has(key)) groups.set(key, { title, kind: String(match?.kind || f.kind || 'gift_card'), count: 0 });
    groups.get(key)!.count += 1;
  }

  // Where each kind of item actually landed — never a code, only the route.
  const destFor = (kind: string) => {
    if (kind === 'topup' || kind === 'phone_refill' || kind === 'mobile_recharge') {
      // Only numbers that carry product value may be named here.
      const credited = del.creditPhones.length ? del.creditPhones : del.phones;
      return t('orderPage.creditAppliedTo', { target: credited.length ? credited.join(', ') : i18nT('orderPage.yourNumber') });
    }
    const to = String(q.customer_email || q.email || '');
    return t('orderPage.sentTo', { target: to || i18nT('orderPage.yourDeliveryAddress') });
  };

  return (
    <div className="card">
      <div className="card-title">{t('orderPage.deliveryConfirmed')}</div>
      <div className="small muted mb-1">
        {usable.length === 1
          ? t('orderPage.deliveredHeadline')
          : t('orderPage.deliveriesInOrder', { count: usable.length })}{' '}
        {del.channel === 'phone'
          ? t('orderPage.topupNothingRedeem')
          : del.channel === 'both'
            ? t('orderPage.codesEmailTopupNumber')
            : t('orderPage.codesEmailOnly')}
      </div>

      {Array.from(groups.values()).map((g, gi) => (
        <div
          key={gi}
          className="row between"
          style={{ gap: 10, padding: '10px 0', borderTop: gi ? '1px dashed var(--line-hairline, rgba(0,0,0,.12))' : 'none', alignItems: 'flex-start' }}
        >
          <div style={{ minWidth: 0, flex: 1 }}>
            <div className="strong small">
              {g.title}
              {g.count > 1 ? ` · ${g.count}×` : ''}
            </div>
            <div className="xs faint" style={{ marginTop: 2 }}>
              {destFor(g.kind)}
            </div>
          </div>
          <span className="chip xs" style={{ whiteSpace: 'nowrap', color: 'var(--green, #1f7a4d)' }}>
            <Icon name="check" size={12} /> {t('orderPage.delivered')}
          </span>
        </div>
      ))}

      <div className="note-box info mt-2" style={{ fontSize: '0.78rem' }}>
        <Icon name="info" size={14} />{' '}
        {del.channel === 'phone'
          ? t('orderPage.notOnNumberSupport')
          : del.channel === 'both'
            ? t('orderPage.nothingArrivedEmail')
            : t('orderPage.nothingArrivedResend')}
      </div>
    </div>
  );
}

/**
 * QuoteItemsCard — what is in this order, line by line, with each line's own
 * delivery destination. A mixed cart must never be summarised by a single
 * "delivered to your email" sentence.
 */
function QuoteItemsCard({ q }: { q: any }) {
  const { t } = useT();
  const lines = linesOf(q);
  if (lines.length < 2) return null;
  return (
    <div className="card">
      <div className="card-title">{t('orderPage.itemsInOrder')}</div>
      {lines.map((l, i) => {
        const isPhone = String(l.delivery_channel) === 'phone';
        return (
          <div key={i} className="row between" style={{ gap: 10, padding: '8px 0', borderTop: i ? '1px dashed var(--line-hairline, rgba(0,0,0,.12))' : 'none' }}>
            <div style={{ minWidth: 0, flex: 1 }}>
              <div className="strong small">
                {cleanProductLabel(l.product_id) || l.product_id}
                {Number(l.quantity) > 1 ? ` ×${l.quantity}` : ''}
              </div>
              <div className="xs faint">
                {l.face_label && l.face_label !== 'range' ? l.face_label : l.denomination || ''}
              </div>
            </div>
            <div className="xs" style={{ textAlign: 'right', maxWidth: '55%' }}>
              <span className="row" style={{ gap: 5, justifyContent: 'flex-end', alignItems: 'center' }}>
                <Icon name={isPhone ? 'phone' : 'mail'} size={13} />
                <span>{isPhone ? t('orderPage.toPhone') : l.kind === 'esim' ? t('orderPage.esimByEmail') : t('orderPage.byEmail')}</span>
              </span>
              {l.delivery_target ? <div className="mono faint" style={{ wordBreak: 'break-all' }}>{l.delivery_target}</div> : null}
            </div>
          </div>
        );
      })}
    </div>
  );
}

/* ---------------- Pay-now card for quotes ---------------- */
function PayNowCard({ q }: { q: any }) {
  const { t } = useT();
  const [expired, setExpired] = useState(false);
  const [renewBusy, setRenewBusy] = useState(false);
  const [renewErr, setRenewErr] = useState('');
  useEffect(() => { setExpired(false); setRenewErr(''); }, [q.id]);
  const rail = payRail(q);
  const del = deliverySummary(q);
  const invoice = quoteBolt11(q);
  // NOT payable any more — but never silently vanish. The order page used to
  // render nothing at all here, which left the buyer staring at a raw supplier
  // state with no button: the exact dead end reported on 2026-10-04. Three
  // states matter, and each gets its own card:
  //   • money was seen (or is in flight) → say so calmly; nothing to do;
  //   • window just lapsed, verification buffer running → "checking", no CTA;
  //   • window over with nothing charged → one-tap FRESH INVOICE.
  if (expired || !isQuotePayable(q)) {
    if (paymentInFlight(q)) {
      return (
        <div className="card" style={{ borderColor: 'var(--line-strong)', borderWidth: '2px' }}>
          <div className="card-title" style={{ display: 'flex', alignItems: 'center', gap: '8px' }}>
            <Icon name="check" size={15} />
            <span>{t('orderPage.paySeenTitle')}</span>
          </div>
          <div className="small muted">{t('orderPage.paySeenBody')}</div>
          <div className="xs faint mt-1">{t('orderPage.supplierState', { state: supplierStatusLabel(q.supplier_status) })}</div>
        </div>
      );
    }
    if (!canRenewQuote(q, Date.now())) {
      // Either the shop is still proving whether money arrived (the grace
      // buffer after the deadline) or a human has this order. Both are
      // "wait, do not pay again" — a timer is never proof of failure.
      if (!paymentWindowVerifying(q, Date.now())) return null;
      return (
        <div className="card" style={{ borderColor: 'var(--line-strong)', borderWidth: '2px' }}>
          <div className="card-title" style={{ display: 'flex', alignItems: 'center', gap: '8px' }}>
            <Icon name="clock" size={15} />
            <span>{t('orderPage.verifyTitle')}</span>
          </div>
          <div className="small muted">{t('orderPage.verifyBody')}</div>
        </div>
      );
    }
    const renew = async () => {
      setRenewBusy(true);
      setRenewErr('');
      try {
        const req = buildOrderRequest(renewItemFromQuote(q), renewInfoFromQuote(q));
        const out: any = await createQuote(req, uuid());
        const next = (out && (out.quote || out)) || {};
        const id = next.quote_id || next.id;
        if (!id) throw new Error(t('orderPage.renewFailed'));
        window.location.href = pagePath('/order?type=quote&id=' + encodeURIComponent(id));
      } catch (e) {
        // ACTIVE_CHECKOUT here means the shop is still holding this buyer to
        // the old order (the verification buffer, or a human review). That is
        // not a failure of the renewal — say what is actually happening.
        if ((e as { code?: string } | null)?.code === 'ACTIVE_CHECKOUT') {
          setRenewErr(t('orderPage.verifyBody'));
        } else {
          setRenewErr(friendlyApiMessage(e, t('orderPage.renewFailed')));
        }
        setRenewBusy(false);
      }
    };
    // A quote that cannot be re-quoted (no product/country in the payload) must
    // not offer a button whose only possible outcome is a backend refusal.
    const rebuildable = canRebuildRequest(q);
    return (
      <div className="card" style={{ borderColor: 'var(--stamp)', borderWidth: '2px' }}>
        <div className="card-title" style={{ display: 'flex', alignItems: 'center', gap: '8px' }}>
          <Icon name="clock" size={15} />
          <span>{t('orderPage.windowOverTitle')}</span>
        </div>
        <div className="small muted">{t('orderPage.windowOverBody')}</div>
        {rebuildable && (
        <button type="button" className="btn btn-gold btn-block mt-2" disabled={renewBusy} onClick={() => { void renew(); }}>
          <Icon name="bolt" size={14} /> <span className="btn-label">{renewBusy ? t('orderPage.renewBusy') : t('orderPage.renewCta')}</span>
        </button>
        )}
        {renewErr && <div className="small mt-1" style={{ color: 'var(--stamp)' }}>{renewErr}</div>}
        <div className="xs faint mt-1">{t('checkout.flowRenewWhy')}</div>
      </div>
    );
  }
  if (!rail.isUsdt && !invoice) return null;
  let payURI = '';
  try {
    if (invoice) payURI = lightningPaymentURI(invoice);
  } catch {}
  // TEST MODE: NO separate simulated screen — this exact real pay-now card
  // (recap · countdown · real QR/wallet block) gains ONE extra button, the
  // simulated pay button, right at the payment spot. The page's 12s poll
  // hides this card once the simulated payment settles.
  // Backend verdict first, static flag only as a fallback — see isTestMode().
  const testMode = isTestMode(q as Record<string, unknown>);
  // Mirror of the checkout pay screen: same hero (big NIM + fee, you-get,
  // delivery, local fiat), same collapsed summary — the order page must not
  // invent a second, different recap (owner, 2026-10-04).
  const nimBase = nimAmountText(q);
  const localFiat = (() => { try { const { label } = quoteFaceValue(q); return label || ''; } catch { return ''; } })();
  return (
    <div className="card" style={{ borderColor: 'var(--stamp)', borderWidth: '2px', boxShadow: '3px 3px 0 rgba(199, 72, 29, 0.25)' }}>
      <div className="center mt-1">
        <PaymentCountdown
          expiresAt={q.payment_expiry || q.payment_expires_at}
          onExpire={() => setExpired(true)}
        />
      </div>
      <div className="pay-wait small muted mt-1 center" style={{ display: 'flex', gap: 8, alignItems: 'center', justifyContent: 'center' }}>
        <div className="spinner" style={{ width: 14, height: 14 }} />
        <span>{t('checkout.flowWaiting')}</span>
      </div>
      <div className="pay-hero mt-2">
        <div className="pay-hero-label">{t('checkout.flowDirect')}</div>
        <div className="pay-hero-amt">
          {rail.isUsdt ? (
            <span className="big-nim">{coinAmountLabel(q) || t('orderPage.amountShownBelow')}</span>
          ) : nimBase ? (
            <>
              <img className="pay-nim-ico" src={asset("/img/nimiq-hexagon.png?v=40")} alt="NIM" width={22} height={22} style={{ borderRadius: 5 }} />
              <span className="big-nim">{t('checkout.flowFeeSuffix', { amount: nimBase })}</span>
            </>
          ) : (
            <span className="big-nim">{t('checkout.flowAmountInNimiqPay')}</span>
          )}
        </div>
        <div className="pay-hero-youget">
          <span className="xs faint">{t('checkout.flowYouGet')}</span>
          <span className="strong pay-you-get">{selectedAmountLabel(q) || cleanProductLabel(q.product_id) || t('orderPage.instantDelivery')}</span>
        </div>
        {del && (
          <div className="pay-hero-del small">
            <Icon name={del.icon as any} size={14} /> {del.sentence}
          </div>
        )}
        {localFiat && <div className="small muted" style={{ marginTop: 6, fontWeight: 700 }}>{localFiat}</div>}
      </div>
      {/* The full recap lives in the summary, exactly like the pay screen:
          the visible card is the hero, the table opens on "See details". */}
      <details className="checkout-details-min">
        <summary>{t('checkout.flowDetailsSummary')}</summary>
        <div style={{ marginTop: 8 }}>
          <dl className="pay-recap">
            <div>
              <dt>{t('orderPage.youPay')}</dt>
              <dd>
                {rail.isUsdt ? (
                  coinAmountLabel(q) || t('orderPage.amountShownBelow')
                ) : (
                  <NimAmount q={q} fallback={t('orderPage.amountInNimiqPay')} />
                )}
              </dd>
            </div>
            <div><dt>{t('orderPage.paymentMethod')}</dt><dd>{rail.label}</dd></div>
            <div>
              <dt>{t('orderPage.finalTotal')}</dt>
              <dd>{rail.isUsdt ? t('orderPage.finalTotalUsdt', { coin: rail.short }) : (coinAmountLabelFor(q, 'BTC') || t('orderPage.amountNote'))}</dd>
            </div>
            <div><dt>{t('orderPage.rowTo')}</dt><dd>{t('orderPage.toSupplier')}</dd></div>
            <div><dt>{t('orderPage.youGet')}</dt><dd>{selectedAmountLabel(q) || cleanProductLabel(q.product_id) || t('orderPage.instantDelivery')}</dd></div>
            <div><dt>{t('orderPage.rowDelivery')}</dt><dd>{del.sentence}</dd></div>
            <div><dt>{t('orderPage.nextStep')}</dt><dd>{payActionLine(q, del)}</dd></div>
          </dl>
          <div className="small muted mt-1">{t('checkout.flowNimEstimateNote')}</div>
          <CashbackFeeNotice example={rail.isUsdt ? 'usdt' : 'nim'} />
          <StakerCashbackLine quote={q} />
          <div className="alert info mt-1" style={{ marginBottom: 0, display: 'flex', gap: '8px', alignItems: 'center' }}>
            <Icon name="bolt" size={18} />
            <div className="small">{rail.note}</div>
          </div>
        </div>
      </details>
      {rail.isUsdt ? (
        <UsdtPayBlock quote={q} expired={expired} />
      ) : payURI ? (
        <>
          {/* Inside Nimiq Pay: one-tap native payment (choose NIM/USDT, approve).
              Outside it: the QR / wallet hand-off below stays the way to pay. */}
          <LightningPayBlock
            quoteId={String(q.id || q.quote_id)}
            invoice={invoice}
            uri={payURI}
            onLaunch={() => rememberLightningPayment(invoice, { kind: 'quote', ref: q.id })}
            avatarAddress={getAddress()}
          />
        </>
      ) : null}
      {testMode ? <SimulatedPayBlock quoteId={String(q.id || q.quote_id)} /> : null}
      <div className="xs faint mt-1">{t('orderPage.reloadSafe')}</div>
    </div>
  );
}

/* ---------------- Main view ---------------- */
export function OrderView() {
  const { t } = useT();
  const id = queryParam('id');
  const isQuote = queryParam('type') === 'quote';
  // Query parameters are unavailable during Astro's server render. Hold the
  // empty-state until the browser has hydrated so order links do not briefly
  // flash "No order id provided" before loading the real order.
  const [queryReady, setQueryReady] = useState(false);
  const [data, setData] = useState<any>(null);
  const [err, setErr] = useState('');
  const [loading, setLoading] = useState(true);
  const { toast } = useToast();
  const { openSheet, closeSheet } = useSheet();
  // null = not decided yet (SSR / first paint) → neutral skeleton, never the
  // signed-out card. See lib/useSession.ts.
  const authed = useSession();

  useEffect(() => {
    setQueryReady(true);
  }, []);

  const load = useCallback(
    async (manual = false) => {
      if (!queryReady) return;
      if (!id) {
        setErr('no-id');
        setLoading(false);
        return;
      }
      if (!isAuthed()) {
        setLoading(false);
        setData(null);
        return;
      }
      try {
        const res = isQuote ? (manual ? await refreshQuote(id) : await getQuote(id)) : manual ? await refreshOrder(id) : await getOrder(id);
        setData(isQuote ? res : res);
        setErr('');
      } catch (e: any) {
        setData(null);
        if (e.status === 404) setErr('404');
        else setErr(friendlyApiMessage(e, t('orderPage.loadError')));
      } finally {
        setLoading(false);
      }
    },
    [id, isQuote, queryReady, t]
  );

  useEffect(() => {
    load();
  }, [load, authed]);

  const terminal = data ? ['fulfilled','refunded','delivered','complete'].includes(data.status || data.quote?.status) : false;
  useInterval(() => { if (data && !terminal) load(false); }, 12000, [data, terminal]);

  // ---- Rate-your-delivery popup -----------------------------------------
  // The moment the live tracking poll sees the order FULFILLED (or the buyer
  // opens a freshly delivered order), ask ONCE for a rating. "Rate later"
  // remembers the dismissal per order; the rating itself stays available on
  // this page (Rate your purchase card) and on the Orders page row.
  const rateAskedFor = useRef('');
  useEffect(() => {
    if (!data) return;
    const row = isQuote ? data.quote || data : data;
    const rowId = String(row?.id || row?.quote_id || '');
    if (!rowId || rateAskedFor.current === rowId) return;
    if (!shouldAskRating(row, isQuote ? 'quote' : 'order', {
      isDismissed: (k) => {
        try { return !!localStorage.getItem(k); } catch { return false; }
      },
    })) return;
    rateAskedFor.current = rowId;
    const laterKey = ratingDismissedKey(isQuote ? 'quote' : 'order', rowId);
    const rate = async (stars: number) => {
      try {
        await (isQuote ? rateQuote(rowId, stars) : rateOrder(rowId, stars));
        toast(t('orderPage.ratedThanks'), 'success');
      } catch (e) {
        toast(friendlyApiMessage(e, t('orderPage.rateError')), 'error');
      }
      load(false);
    };
    openSheet({
      // Both the heading and the body translate at RENDER time. The prompt
      // opens the moment the order payload lands, which can beat this
      // visitor's dictionary chunk; a t() captured here would leave the whole
      // prompt (and the dialog's accessible name) in the fallback language
      // until the sheet was closed and reopened.
      title: (tr) => tr('orderPage.rateTitle'),
      render: (close) => (
        <RateDeliveryPrompt
          onRate={(stars) => {
            rate(stars);
            close();
          }}
          onLater={() => {
            try { localStorage.setItem(laterKey, '1'); } catch {}
            close();
          }}
        />
      ),
    });
  }, [data, isQuote, openSheet, toast, load, t]);

  if (queryReady && (!id || err === 'no-id')) {
    return (
      <div className="container">
        <BackRow onRefresh={() => load(true)} />
        <div className="mt-2">
          <EmptyState iconName="receipt" title={t('orderPage.chooseOrder')} text={t('orderPage.chooseOrderText')}
            action={<a href={pagePath("/orders")} className="btn btn-gold">{t('orderPage.viewMyOrders')}</a>} />
        </div>
      </div>
    );
  }

  if (authed === null) {
    // Auth not decided yet (server HTML / first paint): neutral skeleton only.
    return (
      <div className="container">
        <BackRow onRefresh={() => load(true)} />
        <div className="mt-2">
          <div className="card">
            <SkeletonLines n={5} />
          </div>
        </div>
      </div>
    );
  }

  if (!authed) {
    return (
      <div className="container">
        <BackRow onRefresh={() => load(true)} />
        <div className="mt-2">
          <div className="card locked fade-in">
            <div className="lock-ico">
              <Icon name="lock" size={34} />
            </div>
            <h2>{t('orderPage.signInToView')}</h2>
            <p>{t('orderPage.ordersPrivate')}</p>
            <button className="btn btn-gold" onClick={() => openLoginSheet({ openSheet, closeSheet, toast })}>
              <Icon name="nimiq" size={19} />
              <span className="btn-label">{t('ui.connectWallet')}</span>
            </button>
          </div>
        </div>
      </div>
    );
  }

  if (loading) {
    return (
      <div className="container">
        <BackRow onRefresh={() => load(true)} />
        <div className="mt-2">
          <div className="card">
            <SkeletonLines n={5} />
          </div>
        </div>
      </div>
    );
  }

  if (err === '404') {
    return (
      <div className="container">
        <BackRow onRefresh={() => load(true)} />
        <div className="mt-2">
          <EmptyState iconName="search" title={t('orderPage.notFound')} text={t('orderPage.notFoundText')} />
        </div>
      </div>
    );
  }
  if (err || !data) {
    return (
      <div className="container">
        <BackRow onRefresh={() => load(true)} />
        <div className="mt-2">
          <ErrorState message={err} retry={() => load()} />
        </div>
      </div>
    );
  }

  // Unwrap quote
  const q = isQuote ? data.quote || data : null;
  const o = isQuote ? null : data;

  if (isQuote && q) {
    return <QuoteContent q={q} refund={data.refund} fulfillment={data.fulfillment} />;
  }
  if (o) {
    return <OrderContent o={o} />;
  }
  return null;
}

function BackRow({ onRefresh }: { onRefresh?: () => void }) {
  const { t } = useT();
  return (
    <div className="row mb-2" style={{ gap: '10px' }}>
      <a className="btn btn-ghost btn-sm" href={pagePath("/orders")}>
        <Icon name="back" size={16} />
        <span className="btn-label">{t('orderPage.allOrders')}</span>
      </a>
      {onRefresh ? (
        <button className="btn btn-ghost btn-sm" onClick={onRefresh}>
          <Icon name="refresh" size={16} />
          <span className="btn-label">{t('orderPage.refreshStatus')}</span>
        </button>
      ) : null}
    </div>
  );
}

/** Human labels for the buyer's checkout choices, mirrored on the order review. */
function payMethodLabel(q: any): string {
  const rail = payRail(q);
  if (rail.isUsdt) return rail.label; // "USDT · Polygon"
  const m = String(q?.payment_method || '').toLowerCase();
  const coin = String(q?.coin || '').toUpperCase();
  if (m === 'nimiq_pay') return i18nT('productPage.payWithNimChip');
  return m ? m.replace(/_/g, ' ') : coin === 'NIM' ? i18nT('productPage.payWithNimChip') : '—';
}
function cashbackLabel(q: any): ReactNode {
  const d = String(q?.cashback_destination || '').toLowerCase();
  if (d === 'burn') return i18nT('orderPage.burnNim');
  if (d === 'cashback') return <NimWalletLabel size={16} />;
  return null;
}

function OrderContent({ o }: { o: any }) {
  const { t } = useT();
  const meta = kindMeta(o.kind);
  const payload = o.payload || {};
  const f = o.fulfillment;
  const productTitle = cleanProductLabel(payload.product_name || o.product_id) || o.product_id || 'Product';
  const detailThumbTitles = detailBatchTitles(o, productTitle);
  const detailTitle = detailThumbTitles.length > 1 ? detailThumbTitles.join(' + ') : productTitle;

  const statusRow = (
    <div className="row between mb-2" style={{ flexWrap: 'wrap', gap: '12px' }}>
      <div>
        <div className="row" style={{ gap: '10px' }}>
          {detailThumbTitles.length > 1 ? (
            <ProductThumbStack
              titles={detailThumbTitles}
              country={payload.country || o.country || ''}
            />
          ) : (
            <OrderThumb
              family={productTitle}
              country={payload.country || ''}
              image={payload.product_image || payload.logo_url || ''}
              bgColor={payload.product_bg || ''}
              iconName={meta.icon}
            />
          )}
          <div>
            <h2 style={{ margin: 0 }}>{detailTitle}</h2>
            <div className="xs faint">
              {t(meta.labelKey)} · placed {fmtDate(o.created_at)}
            </div>
          </div>
        </div>
      </div>
      <StatusBadge status={o.status} />
    </div>
  );

  const left = (
    <div style={{ display: 'grid', gap: 14 }}>
      <div className="card">
        <div className="card-title">{t('orderPage.liveTracking')}</div>
        <StageTimeline stages={o.stages || []} channel={deliverySummary(o).channel} usdt={payRail(o).isUsdt} />
      </div>
      {detailThumbTitles.length > 1 ? (
        detailThumbTitles.map((t) => (
          <RedeemInfoCard
            key={t}
            family={t}
            title={t}
            country={payload.country || ''}
            kind={mapKind(t) || o.kind}
            deliveredNote={(f && (f.how_to_redeem || f.instructions)) || ''}
          />
        ))
      ) : (
        <RedeemInfoCard
          family={productTitle}
          country={payload.country || ''}
          kind={o.kind}
          deliveredNote={(f && (f.how_to_redeem || f.instructions)) || ''}
        />
      )}
    </div>
  );

  const rightItems: React.ReactNode[] = [];

  if (f && (f.code || f.link || f.pin || f.instructions || f.barcode_value)) {
    // Codes/PINs are NEVER rendered here. They are delivered to the customer's
    // email or phone; showing them in the app duplicates the secret in a second
    // place and contradicts what the checkout promised.
    const oDel = deliverySummary(o);
    const oCredited = oDel.creditPhones.length ? oDel.creditPhones : oDel.phones;
    const oTo = oDel.channel === 'phone'
      ? (oCredited.length ? oCredited.join(', ') : t('orderPage.yourNumber'))
      : String(o.customer_email || o.email || payload.email || t('orderPage.yourDeliveryAddress'));
    const deliv = (
      <div className="card" key="deliv">
        <div className="card-title">{t('orderPage.deliveryConfirmed')}</div>
        <div className="row between" style={{ gap: 10, alignItems: 'flex-start' }}>
          <div style={{ minWidth: 0, flex: 1 }}>
            <div className="strong small">{productTitle}</div>
            <div className="xs faint" style={{ marginTop: 2 }}>
              {oDel.channel === 'phone' ? t('orderPage.creditAppliedTo', { target: oTo }) : t('orderPage.sentTo', { target: oTo })}
            </div>
          </div>
          <span className="chip xs" style={{ whiteSpace: 'nowrap', color: 'var(--green, #1f7a4d)' }}>
            <Icon name="check" size={12} /> {t('orderPage.delivered')}
          </span>
        </div>
        <div className="note-box info mt-2" style={{ fontSize: '0.78rem' }}>
          <Icon name="info" size={14} />{' '}
          {oDel.channel === 'phone'
            ? t('orderPage.topupDeliveredNote')
            : t('orderPage.emailDeliveredNote')}
        </div>
      </div>
    );
    rightItems.push(deliv);
  }

  // RedeemInfoCard now lives under Live tracking (left column) per global requirement — always visible for every product.
  // No duplicate on right column.

  const summaryRows: Array<[string, any]> = [
    [t('orderPage.rowProduct'), productTitle],
    Number(payload.value) > 0 && payload.currency
      ? [t('orderPage.selectedAmount'), <span key="sa" className="strong">{`${fmtNum(Number(payload.value) * (Number(o.quantity) || 1))} ${payload.currency}`}</span>]
      : null,
    [t('orderPage.rowType'), t(meta.labelKey)],
    payload.country
      ? [
          t('orderPage.rowCountry'),
          <span key="c" className="row" style={{ gap: '6px', alignItems: 'center' }}>
            <FlagMark country={payload.country} size={18} /> <span>{countryName(payload.country)}</span>
          </span>,
        ]
      : null,
    o.gift_channel || payload.gift_channel || o.gift_message || payload.gift_message
      ? [t('orderPage.rowGift'), o.gift_channel || payload.gift_channel ? t('orderPage.yesWithChannel', { channel: giftChannelLabel(o.gift_channel || payload.gift_channel) }) : t('orderPage.yes')]
      : [t('orderPage.rowGift'), t('orderPage.no')],
    (o.customer_email || payload.customer_email || (String(payload.beneficiary || '').includes('@') ? payload.beneficiary : ''))
      ? [t('orderPage.deliveryEmail'), <span key="e" className="mono small">{o.customer_email || payload.customer_email || payload.beneficiary}</span>]
      : null,
    (payload.phone_number || o.phone_number || (String(payload.beneficiary || '').startsWith('+') ? payload.beneficiary : ''))
      ? [t('orderPage.phoneNumber'), <span key="p" className="mono small">{payload.phone_number || o.phone_number || payload.beneficiary}</span>]
      : null,
    o.gift_channel || payload.gift_channel ? [t('orderPage.giftNotification'), giftChannelLabel(o.gift_channel || payload.gift_channel)] : null,
    o.gift_message || payload.gift_message
      ? [t('orderPage.giftMessage'), <span key="gm" className="small">{String(o.gift_message || payload.gift_message).slice(0, 140)}</span>]
      : null,
    [t('orderPage.rowQuantity'), String(o.quantity)],
    (() => {
      const micros = Math.round(Number(o.price_usd) * 1e6);
      const snapQ = {
        product_usd: micros,
        estimated_nim: Number(o.estimated_nim) > 0 ? Number(o.estimated_nim) : undefined,
        nim_usd_rate: Number(o.nim_usd_rate) > 0 ? Number(o.nim_usd_rate) : undefined,
      };
      const pv = Number(payload && payload.value);
      const face = pv > 0 && payload.currency ? `${pv} ${payload.currency}` : '';
      return [
        t('orderPage.rowTotal'),
        micros > 0 && hasLockedNim(snapQ) ? (
          <NimAmount key="t" q={snapQ} fallback="—" />
        ) : face ? (
          face
        ) : (
          '—'
        ),
      ];
    })(),
    [t('orderPage.orderId'), copyIdNode(o.id)],
    o.supplier_order_id ? [t('orderPage.supplierOrder'), <span key="so" className="mono small">{shortAddr(o.supplier_order_id, 10, 8)}</span>] : null,
    [t('orderPage.rowUpdated'), fmtDate(o.updated_at)],
  ].filter(Boolean) as any;

  rightItems.push(<KvCard key="summary" title={t('trackPage.summaryTitle')} rows={summaryRows} />);

  const refundCard = refundCardForOrder(o.refund, t);
  if (refundCard) rightItems.push(refundCard);

  const rateCard = (
    <RatingCard
      status={o.status}
      rating={o.rating || 0}
      onRate={(r) => rateOrder(o.id, r)}
    />
  );
  if (rateCard) rightItems.push(rateCard);

  rightItems.push(<HelpCard key="support" />);

  return (
    <div className="container">
      <BackRow onRefresh={() => undefined} />
      <div className="fade-in">
        {statusRow}
        <div className="detail-grid cols">
          {left}
          <div className="col">{rightItems}</div>
        </div>
      </div>
    </div>
  );
}

function QuoteContent({ q, refund, fulfillment }: { q: any; refund?: any; fulfillment?: any }) {
  const { t } = useT();
  const stages = quoteStages(q);
  // Channel + rail are needed by the timeline near the top and the summary
  // further down, so they are resolved once here.
  const qDel = deliverySummary(q);
  const qRail = payRail(q);
  const { toast } = useToast();
  const { openSheet } = useSheet();
  const [rebuyBusy, setRebuyBusy] = useState(false);
  const quoteTitleFallback = cleanProductLabel(q.product_id) || 'Purchase';
  const quoteTitleParts = detailBatchTitles(q, quoteTitleFallback);
  const quoteTitle = quoteTitleParts.length > 1
    ? quoteTitleParts.join(' + ')
    : quoteTitleFallback;

  const quoteCountry = q.product_country || q.country || (Array.isArray(q.lines) && q.lines[0]?.country) || '';

  // "Buy these items again": refills the cart with THIS order's items and
  // opens it. The old button only cleared a local checkout-intent flag — it
  // looked broken because nothing visible happened. Now the click ends with
  // the "Your cart" sheet open, filled with the purchased items, and nothing
  // paid until the buyer checks out.
  const rebuyIntoCart = async () => {
    if (rebuyBusy) return;
    setRebuyBusy(true);
    try {
      try {
        // Still validates that the order is resolved and clears the local
        // checkout-intent lock so a later checkout is not blocked by it.
        await allowNewPurchase(q.id);
      } catch (err) {
        toast(friendlyApiMessage(err), 'error');
        return;
      }
      const lines = (linesOf(q) || []).filter((l: any) => l && l.product_id);
      const next: CartItem[] = [];
      let unmatched = 0;
      for (const l of lines) {
        try {
          const raw: any = await getProduct(String(l.product_id), l.country ? String(l.country) : undefined);
          const families2: any[] = Array.isArray(raw) ? raw : raw && raw.products ? [raw] : [];
          const family = families2.length ? ({ products: families2.flatMap((f: any) => Array.isArray(f.products) ? f.products : []), family: families2[0]?.family || families2[0]?.brand || String(l.product_id), brand: families2[0]?.brand, logo_url: families2[0]?.logo_url, images: families2[0]?.images } as any) : null;
          const label = String(l.denomination || '').trim();
          const selected = family?.products?.find((p: any) => p.denomination === label);
          // Rebuy fixed SKUs from their current structured data, not old labels.
          if (label.toLowerCase() !== 'range' && !selected) { unmatched++; continue; }
          const parsed = productMoney(selected);
          if (label.toLowerCase() === 'range') {
            // Legacy range orders without numeric line data cannot be rebuilt
            // safely. Do not recover an amount by parsing face_label.
            const value = Number(l.product_value || (lines.length === 1 ? q.product_value : 0));
            if (!(value > 0) || !Number.isFinite(value)) { unmatched++; continue; }
            parsed.value = value;
            parsed.currency = family?.products?.find((p: any) => p.range)?.range?.currency || '';
          }
          const kind = String(l.kind || '').toLowerCase();
          const item: CartItem = {
            id: String(l.product_id),
            type: kind === 'topup' ? 'phone_refill' : kind === 'esim' ? 'esim' : 'gift_card',
            name: cleanFamilyName(family?.family || family?.brand) || String(l.product_id),
            image: family?.logo_url || (family?.images && (family.images.large || family.images.medium || family.images.small)) || '',
            bgColor: String((family as any)?.bg_color || (family as any)?.bg || ''),
            country: String(l.country || q.product_country || q.country || '').toUpperCase(),
            currency: parsed.currency,
            pkg: '',
            value: parsed.value > 0 ? parsed.value : 0,
            denomination: label,
            coinAmount: 0,
            unitUSD: parsed.value > 0 && parsed.currency === 'USD' ? parsed.value : 0,
            qty: Math.max(1, Math.min(MAX_QTY, Number(l.quantity) || 1)),
          };
          const k = itemKey(item);
          const existing = next.find((x) => itemKey(x) === k);
          if (existing) existing.qty = Math.min(MAX_QTY, existing.qty + item.qty);
          else next.push(item);
        } catch {
          unmatched++;
        }
      }
      if (!next.length) {
        toast(t('orderPage.rebuyFailed'), 'error');
        return;
      }
      saveCart(next);
      toast(
        unmatched
          ? t('orderPage.rebuyPartial')
          : t('orderPage.rebuyDone'),
        'success'
      );
      openCartSheet({ openSheet });
    } finally {
      setRebuyBusy(false);
    }
  };

  const statusRow = (
    <div className="row between mb-2" style={{ flexWrap: 'wrap', gap: '12px' }}>
      <div>
        <div className="row" style={{ gap: '10px' }}>
          {quoteTitleParts.length > 1 ? (
            <ProductThumbStack
              titles={quoteTitleParts}
              country={quoteCountry}
            />
          ) : (
            <OrderThumb family={quoteTitleParts[0] || quoteTitle} country={quoteCountry} />
          )}
          <div>
            <h2 style={{ margin: 0 }}>{quoteTitle}</h2>
            <div className="xs faint">
              {selectedAmountLabel(q) ? selectedAmountLabel(q) + ' · ' : ''}placed {fmtDate(q.created_at)}
            </div>
          </div>
        </div>
      </div>
      <StatusBadge status={q.status} />
    </div>
  );

  const left = (
    <div style={{ display: 'grid', gap: 14 }}>
      <div className="card">
        <div className="card-title">{t('orderPage.liveTracking')}</div>
        <StageTimeline stages={stages} channel={qDel.channel} usdt={qRail.isUsdt} />
        <div className="small muted mt-1">{t('orderPage.supplierState', { state: q.supplier_status ? supplierStatusLabel(q.supplier_status) : t('orderPage.notYetConfirmed') })}</div>
      {(['fulfilled','refunded'].includes(q.status) || (q.status === 'failed' && (!q.supplier_order_id || (q.supplier_status === 'PaymentSetupFailed' && !q.payment_observed)))) && (
        <div className="mt-2">
          <button className="btn btn-gold btn-block" disabled={rebuyBusy} onClick={rebuyIntoCart}>
            <Icon name="bag" size={16} />
            <span className="btn-label">{rebuyBusy ? t('orderPage.fillingCart') : t('orderPage.rebuy')}</span>
          </button>
          <div className="xs faint mt-1">{t('orderPage.rebuyHint')}</div>
        </div>
      )}

      {q.status === 'lightning_invoice_created' ? (
        <div className="alert info mt-2" style={{ marginBottom: 0 }}>
          <Icon name="bolt" size={18} />
          <div>
            {t('orderPage.nimiqPay.directNotice', { site: siteName() })}
            {/* Fallback pay entry point: only when the pay-now card is not already
                showing its button (so the buyer never sees two identical buttons). */}
            {inNimiqPay() && !isQuotePayable(q) && quoteBolt11(q) ? (
              <NimiqPayPayButton
                invoice={quoteBolt11(q)}
                onSubmitted={() => rememberLightningPayment(quoteBolt11(q), { kind: 'quote', ref: q.id })}
              />
            ) : null}
          </div>
        </div>
      ) : null}
      </div>
      {quoteTitleParts.length > 1 ? (
        quoteTitleParts.map((t) => {
          const lineKind = (q.lines || q._rows || []).find((l: any) => cleanProductLabel(l?.product_id || l?.id || '') === cleanProductLabel(t))?.kind || (q as any).kind;
          return (
            <RedeemInfoCard
              key={t}
              family={t}
              title={t}
              country={quoteCountry}
              kind={lineKind || mapKind(t) || q.kind || mapKind(q.product_id)}
              deliveredNote={(fulfillment && (fulfillment.how_to_redeem || fulfillment.instructions)) || (q as any).how_to_redeem || ''}
            />
          );
        })
      ) : (
        <RedeemInfoCard
          family={quoteTitleParts[0] || quoteTitle}
          country={quoteCountry}
          kind={q.kind || mapKind(q.product_id)}
          deliveredNote={(fulfillment && (fulfillment.how_to_redeem || fulfillment.instructions)) || (q as any).how_to_redeem || ''}
        />
      )}
    </div>
  );

  const rightItems: React.ReactNode[] = [];

  // No duplicate raw invoice link: all handoffs use the guarded block below.
  // Pay-now reload-safe block
  rightItems.unshift(<PayNowCard key="paynow" q={q} />);

  // Delivered contents (codes/PINs/links) — previously rendered only for
  // legacy orders, so batch buyers saw no codes at all after delivery.
  rightItems.push(<QuoteDeliveryCard key="delivery" q={q} fulfillment={fulfillment} />);
  // Per-line manifest with each item's own destination (mixed carts).
  rightItems.push(<QuoteItemsCard key="items" q={q} />);

  // Gift / recipient info — the summary must answer "is this a gift, and who
  // receives it?" without making the buyer open another card.
  const qGiftChannel = String(q.gift_channel || (q._rows || []).find((r: any) => r && r.gift_channel)?.gift_channel || '');
  const qGiftMessage = String(q.gift_message || (q._rows || []).find((r: any) => r && r.gift_message)?.gift_message || '');
  const qIsGift = !!(qGiftChannel || qGiftMessage);
  const qEmail = String(q.email || q.customer_email || '');

  const summaryRows: Array<[string, any]> = [
    [t('orderPage.rowPayment'), <span key="pm" className="strong">{payMethodLabel(q)}</span>],
    // On a USDT order the buyer must be able to confirm the chain and the
    // exact token amount from the order itself, not only from the pay sheet.
    qRail.isUsdt ? [t('orderPage.rowNetwork'), <span key="nw" className="strong">{t('orderPage.polygonChain')}</span>] : null,
    qRail.isUsdt && coinAmountLabel(q) ? [t('orderPage.usdtAmount'), <span key="ua" className="mono small">{coinAmountLabel(q)}</span>] : null,
    // Delivery channel, stated once and correctly for mixed carts.
    [t('orderPage.rowDelivery'), <span key="dl" className="strong">{qDel.label}</span>],
    cashbackLabel(q) ? [t('orderPage.rowCashback'), <span key="cd">{cashbackLabel(q)}</span>] : null,
    [t('orderPage.rowGift'), qIsGift ? (qGiftChannel ? t('orderPage.yesWithChannel', { channel: giftChannelLabel(qGiftChannel) }) : t('orderPage.yes')) : t('orderPage.no')],
    quoteCountry
      ? [
          t('orderPage.rowCountry'),
          <span key="c" className="row" style={{ gap: '6px', alignItems: 'center' }}>
            <FlagMark country={quoteCountry} size={18} /> <span>{countryName(quoteCountry)}</span>
          </span>,
        ]
      : null,
    selectedAmountLabel(q) ? [t('orderPage.selectedAmount'), <span key="sa" className="strong">{selectedAmountLabel(q)}</span>] : null,
    [t('orderPage.rowQuantity'), String(q.quantity || 1)],
    // Label each address row by what actually happens to it: a top-up number
    // receives credit, a gift-note address receives the note.
    qEmail && qDel.hasEmail ? [t('orderPage.deliveryEmail'), <span key="re" className="mono small">{qEmail}</span>] : null,
    // On a top-up gift cart nothing is *delivered* to the order's email, but the
    // note is sent to it — so the address has to appear, named for what it is.
    // Hiding it behind "the product needs no email" left the buyer with no way to
    // check the one address a gift note can arrive at.
    qEmail && !qDel.hasEmail && qGiftChannel ? [t('orderPage.giftNoteEmail'), <span key="gne" className="mono small">{qEmail}</span>] : null,
    qDel.hasTopUp && qDel.creditPhones.length
      ? [t('orderPage.topupNumber'), <span key="tp" className="mono small">{qDel.creditPhones.join(', ')}</span>]
      : null,
    qGiftMessage ? [t('orderPage.giftMessage'), <span key="gm" className="small">{qGiftMessage.slice(0, 140)}</span>] : null,
    [t('orderPage.rowTotal'), qRail.isUsdt && coinAmountLabel(q) ? coinAmountLabel(q) : <NimAmount key="t" q={q} fallback="—" />],
    [t('orderPage.orderId'), copyIdNode(q.id || '')],
    [t('orderPage.rowUpdated'), fmtDate(q.updated_at || q.created_at)],
  ].filter(Boolean) as any;
  rightItems.push(<KvCard key="summary" title={t('orderPage.summaryTitle')} rows={summaryRows} />);

  if (refund) rightItems.push(refundCardForQuote(refund, t));

  const rateCard = <RatingCard status={q.status} rating={q.rating || 0} onRate={(r) => rateQuote(q.id, r)} />;
  if (rateCard) rightItems.push(rateCard);

  rightItems.push(<HelpCard key="support" />);

  return (
    <div className="container">
      <BackRow />
      <div className="fade-in">
        {statusRow}
        <div className="detail-grid cols">
          {left}
          <div className="col">{rightItems}</div>
        </div>
      </div>
    </div>
  );
}

export function OrderPage() {
  return (
    <AppRoot activeKey="order">
      <OrderView />
    </AppRoot>
  );
}
