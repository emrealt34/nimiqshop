/**
 * OrdersPage.tsx — React port of pages/orders.js: purchase history (legacy
 * order rows + direct CryptoRefills-Lightning quotes), filters, live local
 * status refresh, inline star rating.
 */
import { useCallback, useEffect, useMemo, useState } from 'react';
import { Icon } from '../ui/Icon';
import { UnifiedThumb, BrandThumbStack } from '../ui/UnifiedThumb';
import { FlagMark } from '../ui/FlagMark';
import { AppRoot } from '../AppRoot';
import { openLoginSheet } from '../shell/SiteShell';
import { listOrders, listQuotes } from '../../lib/api';
import { brandMetaFor } from '../../lib/catalogMeta';
import { cleanProductLabel, cleanBatchProductLabels, fmtDate, fmtDuration, fmtNum, quoteFaceValue, countryName } from '../../lib/format';
import { friendlyApiMessage, errorDetailLine } from '../../lib/api';
import { isAuthed } from '../../lib/session';
import { useSession } from '../../lib/useSession';
import { quoteStages, isTerminalStatus, isDeliveredStatus, isIssueStatus } from '../../lib/orderTrack';
import { deliverySummary, payRail } from '../../lib/deliveryCopy';
import { useInterval } from '../../lib/useInterval';
import { useToast, useSheet } from '../AppProviders';
import { StatusBadge, MiniProgress, StarsDisplay, EmptyState, ErrorState, LockedSignInCard, SkeletonCards, NimAmount, OnChainProof, ClockTime } from '../ui/uiKit';
import { Pager } from '../ui/Pager';
import { useT, t as i18nT } from '../../i18n';
import { pagePath } from '../../lib/asset';

/** Rows per page in the order list. */
const ORDERS_PER_PAGE = 5;


function batchTotalsLabel(q: any): string {
  const raw = q?.face_value_totals;
  if (Array.isArray(raw)) return raw.filter(Boolean).join(' + ');
  if (raw && typeof raw === 'object') {
    return Object.entries(raw)
      .filter(([, v]) => Number(v) > 0)
      .map(([ccy, amount]) => `${fmtNum(Number(amount))} ${String(ccy).toUpperCase()}`)
      .join(' + ');
  }
  return '';
}

function splitCleanBatchTitles(values: any): string[] {
  const source = Array.isArray(values) ? values : [values];
  return source.flatMap((value) =>
    cleanProductLabel(value)
      .split(/\s*\+\s*/)
      .map((part) => cleanProductLabel(part))
      .filter(Boolean)
  );
}

function batchLineTitles(q: any): string[] {
  const fromLines = Array.isArray(q?.lines) ? splitCleanBatchTitles(q.lines) : [];
  if (fromLines.length > 1) return fromLines.slice(0, 3);

  const raw = q?.batch_summary_items ?? q?.batch_summary;
  const fromSummary = raw ? splitCleanBatchTitles(Array.isArray(raw) ? raw : String(raw).split(',')) : [];
  if (fromSummary.length > 1) return fromSummary.slice(0, 3);

  // Older batch quotes did not expose `lines`, but ProductID is stored as the
  // joined family list ("Steam + Netflix"). Clean the whole legacy string,
  // remove duplicate denominations, and de-duplicate the resulting brands.
  const joined = String(q?.product_id || q?.title || q?.display_title || '').trim();
  const fromProduct = joined ? cleanBatchProductLabels(joined) : [];
  if (fromProduct.length > 1) return fromProduct.slice(0, 3);

  // Last-resort visual fallback: the count is still more honest than a
  // single-item thumbnail when the API has no per-line names.
  const count = Math.max(0, Number(q?.batch_items) || 0);
  return count > 1 ? Array.from({ length: Math.min(count, 3) }, (_, i) => i18nT('common.cartItem', { n: i + 1 })) : [];
}

function batchSummaryLabel(q: any): string {
  return batchLineTitles(q).join(', ');
}

function batchSummaryItems(q: any): string[] {
  return batchLineTitles(q);
}


/** Filter chips — module-level, so they carry an i18n key instead of a label. */
const FILTERS = [
  { key: 'all', labelKey: 'ordersPage.filterAll' },
  { key: 'active', labelKey: 'ordersPage.filterActive' },
  { key: 'delivered', labelKey: 'ordersPage.filterDelivered' },
  { key: 'issues', labelKey: 'ordersPage.filterIssues' },
];

export function OrdersView() {
  const { t, lang } = useT();
  const [rows, setRows] = useState<any[]>([]);
  const [filter, setFilter] = useState('all');
  const [orderPage, setOrderPage] = useState(0);
  const [loading, setLoading] = useState(true);
  const [err, setErr] = useState('');
  /* The exact failure behind `err` (code · status · backend message), shown in
     small print so a failed list is actionable instead of just "went wrong". */
  const [errDetail, setErrDetail] = useState('');
  const [lastRefresh, setLastRefresh] = useState('');
  const [awaitingCount, setAwaitingCount] = useState(0);
  const { toast } = useToast();
  const { openSheet, closeSheet } = useSheet();
  // null = not decided yet (SSR / first paint) → render the neutral skeleton,
  // never the "Connect wallet" card. See lib/useSession.ts.
  const authed = useSession();

  const countAwaiting = (r: any[]) => r.filter(awaitingPay).length;

  const normalizeOrders = useCallback(async (orders: any) => {
    const out: any[] = [];
    for (const o of (orders || []) as any[]) {
      const family = cleanProductLabel((o.payload && (o.payload.product_name || o.product_id)) || o.product_id);
      const meta = await brandMetaFor(family, o.payload && o.payload.country);
      out.push({
        rowKind: 'order',
        id: o.id,
        kind: o.kind,
        name: cleanProductLabel((o.payload && o.payload.product_name) || o.product_id) || o.product_id,
        country: o.payload && o.payload.country,
        qty: o.quantity,
        priceLabel: Number(o.price_usd) > 0 ? t('ordersPage.nimAtCheckout') : '—',
        paidUnit:
          Number(o.payload && o.payload.value) > 0 && o.payload.currency
            ? `${fmtNum(Number(o.payload.value) * (Number(o.quantity) || 1))} ${o.payload.currency}`
            : null,
        // NIM shop: the card price is "≈ X NIM" + the product's own face value.
        // A "$" conversion belonged to the old CryptoRefills UI — removed.
        // History must never drift with the market: the ≈ NIM figure is derived
        // from the rate locked onto the order at purchase time (nim_usd_rate /
        // estimated_nim), never from today's quote.
        usdLabel: '',
        nimUsd: Number(o.price_usd) > 0 ? Number(o.price_usd) : 0,
        nimUsdRate: Number(o.nim_usd_rate) > 0 ? Number(o.nim_usd_rate) : 0,
        estimatedNim: Number(o.estimated_nim) > 0 ? Number(o.estimated_nim) : 0,
        deliveryLabel: deliverySummary(o).label === '—' ? '' : deliverySummary(o).label,
        deliveryIcon: deliverySummary(o).icon,
        railLabel: payRail(o).isUsdt ? payRail(o).label : '',
        status: o.status,
        created_at: o.created_at,
        updated_at: o.updated_at,
        stages: o.stages,
        current_stage: o.current_stage,
        rating: o.rating || 0,
        ratingTx: o.rating_tx || '',
        hasTicket: !!o.has_ticket,
        ticketStatus: o.ticket_status,
        image: (o.payload && (o.payload.product_image || o.payload.logo_url || o.payload.image)) || meta.logo,
        bgColor: (o.payload && (o.payload.product_bg || o.payload.bg_color)) || meta.bg,
      });
    }
    return out;
  }, [t]);

  const quotePrice = useCallback((q: any) => {
    const micros = Number(q.product_usd);
    const usd = isFinite(micros) && micros > 0 ? micros / 1e6 : 0;
    const { value: localVal, currency: ccy, label } = quoteFaceValue(q);
    const local = localVal > 0 && ccy ? `${fmtNum(localVal)} ${ccy}` : label || '';
    const paidUnit = ccy && local ? local : null;
    // no "$" conversion on the cards — the NIM amount is the price, the face
    // value is the product's own denomination (5 USD stays "5 USD", that IS
    // the product; nothing gets converted to dollars any more).
    const usdLabel = '';
    return { priceLabel: local, paidUnit, usdLabel, usd };
  }, []);

  const normalizeQuotes = useCallback(
    async (quotes: any) => {
      const out: any[] = [];
      for (const q of (quotes || []) as any[]) {
        const stages = quoteStages(q);
        const totalsLabel = batchTotalsLabel(q);
        const summaryItems = batchSummaryItems(q);
        const summaryLabel = batchSummaryLabel(q);
        const suppliedBatchCount = Math.max(0, Number(q.batch_items) || 0);
        const batchItems = Math.max(suppliedBatchCount, summaryItems.length > 1 ? summaryItems.length : 0);
        const isBatch = !!q.is_batch || batchItems > 1 || !!totalsLabel || Array.isArray(q._rows) || summaryItems.length > 1;
        const displayTitle = cleanProductLabel(String(q.display_title || q.title || '').trim());
        const family = cleanProductLabel(summaryItems[0] || q.product_id) || i18nT('trackPage.productFallback');
        const quoteCountry = q.product_country || q.country || (Array.isArray(q.lines) && q.lines[0]?.country) || '';
        const priced = quotePrice(q);
        const meta = await brandMetaFor(family, quoteCountry);
        const hasAmount = Number(q.product_usd) > 0 || parseFloat(q.coin_amount || q.lightning_amount_btc || '') > 0;
        out.push({
          rowKind: 'quote',
          id: q.id || q.quote_id || q.ID,
          kind: 'quote',
          // A batch title is the clean brand list. Keep the count in the
          // subline below, not inside a malformed supplier/denomination label.
          name: isBatch
            ? (summaryItems.length > 1 ? summaryItems.join(' + ') : displayTitle || (batchItems > 0 ? i18nT('ordersPage.cartOrderItems', { count: batchItems }) : i18nT('ordersPage.cartOrder')))
            : (displayTitle || family),
          country: quoteCountry,
          qty: isBatch ? 0 : q.quantity,
          batchItems,
          batchSummary: summaryLabel,
          batchSummaryItems: summaryItems,
          // A BATCH must never fall back to quoteFaceValue(): that parses the
          // JOINED denomination label ("steam (50 USD) + airbnb (30) + …") and
          // returns only the FIRST item's value, so a 4-item cart rendered
          // "50 USD" underneath an invoice covering all of them. When the
          // server cannot give a complete per-currency total, show the item
          // count instead of a number that is simply wrong.
          priceLabel: isBatch ? totalsLabel || '' : priced.priceLabel,
          paidUnit: isBatch
            ? totalsLabel || (batchItems > 1 ? i18nT('ordersPage.itemsCount', { count: batchItems }) : '')
            : priced.paidUnit,
          usdLabel: priced.usdLabel,
          nimUsd: priced.usd,
          nimUsdRate: Number(q.nim_usd_rate) > 0 ? Number(q.nim_usd_rate) : 0,
          estimatedNim: Number(q.estimated_nim) > 0 ? Number(q.estimated_nim) : 0,
          hasAmount,
          deliveryLabel: deliverySummary(q).label === '—' ? '' : deliverySummary(q).label,
          deliveryIcon: deliverySummary(q).icon,
          railLabel: payRail(q).isUsdt ? payRail(q).label : '',
          status: q.status,
          created_at: q.created_at,
          updated_at: q.updated_at || q.created_at,
          stages,
          current_stage: stages.findIndex((s) => s.status === 'in_progress'),
          rating: q.rating || 0,
          ratingTx: q.rating_tx || '',
          hasTicket: false,
          image: meta.logo,
          bgColor: meta.bg,
        });
      }
      return out;
    },
    [quotePrice]
  );

  const load = useCallback(
    async (_manual = false) => {
      if (!isAuthed()) {
        setLoading(false);
        setErr('');
        return;
      }
      setLoading(true);
      try {
        const [orders, quotes] = await Promise.all([listOrders(), listQuotes().catch(() => [])]);
        const r = [...(await normalizeOrders(orders)), ...(await normalizeQuotes(quotes))];
        r.sort((a, b) => {
          const aWait = awaitingPay(a) ? 0 : 1;
          const bWait = awaitingPay(b) ? 0 : 1;
          if (aWait !== bWait) return aWait - bWait;
          return new Date(b.created_at).getTime() - new Date(a.created_at).getTime();
        });
        setRows(r);
        setAwaitingCount(countAwaiting(r));
        setLastRefresh(t('ordersPage.updated', { time: new Date().toLocaleTimeString(lang === 'en' ? 'en-US' : lang, { hour: '2-digit', minute: '2-digit', second: '2-digit' }) }));
        setErr('');
        setErrDetail('');
      } catch (e) {
        setErr(friendlyApiMessage(e, t('ordersPage.loadError')));
        setErrDetail(errorDetailLine(e));
      } finally {
        setLoading(false);
      }
    },
    [normalizeOrders, normalizeQuotes, t, lang]
  );

  useEffect(() => {
    load();
  }, [load]);

  const needsAutoRefresh = useMemo(() => rows.some((r) => !isTerminalStatus(r.status)), [rows]);
  useInterval(() => { if (needsAutoRefresh) load(false); }, needsAutoRefresh ? 20000 : null, [needsAutoRefresh]);

  const filtered = rows.filter((r) => {
    if (filter === 'active') return !isTerminalStatus(r.status);
    if (filter === 'delivered') return isDeliveredStatus(r.status);
    if (filter === 'issues') return isIssueStatus(r.status);
    return true;
  });
  // The list auto-refreshes (new statuses arrive), so the current page is
  // clamped on every render — you never land on a page past the last one.
  const orderPageCount = Math.max(1, Math.ceil(filtered.length / ORDERS_PER_PAGE));
  const orderPageSafe = Math.min(orderPage, orderPageCount - 1);
  const orderRows = filtered.slice(orderPageSafe * ORDERS_PER_PAGE, orderPageSafe * ORDERS_PER_PAGE + ORDERS_PER_PAGE);

  if (authed === null) {
    // Auth not decided yet (server HTML / first paint): neutral skeleton only.
    return (
      <div className="container orders-page">
        <SectionHead awaitingCount={awaitingCount} lastRefresh={lastRefresh} onRefresh={() => load(true)} />
        <div className="seg mb-2" />
        <div className="grid products">
          <SkeletonCards n={6} />
        </div>
      </div>
    );
  }

  if (!authed) {
    return (
      <div className="container orders-page">
        <SectionHead awaitingCount={awaitingCount} lastRefresh={lastRefresh} onRefresh={() => load(true)} />
        <div className="seg mb-2" />
        <LockedSignInCard
          title={t('ordersPage.lockedTitle')}
          text={t('ordersPage.lockedText')}
          onConnect={() => openLoginSheet({ openSheet, closeSheet, toast })}
          lg
          iconSize={20}
        />
      </div>
    );
  }

  return (
    <div className="container orders-page">
      <SectionHead awaitingCount={awaitingCount} lastRefresh={lastRefresh} onRefresh={() => load(true)} />
      <div className="seg mb-2">
        {FILTERS.map((f) => (
          <button
            key={f.key}
            className={filter === f.key ? 'active' : ''}
            onClick={() => {
              setFilter(f.key);
              setOrderPage(0); // a new view starts on page 1
            }}
          >
            {t(f.labelKey)}
          </button>
        ))}
      </div>
      <div id="list" className="orders-surface">
        {err ? (
          <ErrorState message={err} detail={errDetail} retry={() => load()} />
        ) : loading && !rows.length ? (
          <div className="grid products">
            <SkeletonCards n={6} />
          </div>
        ) : !rows.length ? (
          <EmptyState
            iconName="bag"
            title={t('ordersPage.emptyTitle')}
            text={t('ordersPage.emptyText')}
            action={
              <a className="btn btn-gold" href={pagePath("/")}>
                <Icon name="bag" size={18} />
                <span>{t('cartSheet.browseShop')}</span>
              </a>
            }
          />
        ) : !filtered.length ? (
          <EmptyState iconName="search" title={t('ordersPage.emptyFilterTitle')} text={t('ordersPage.emptyFilterText')} />
        ) : (
          <div className="fade-in">
            <div className="order-list">
              {orderRows.map((r, i) => (
                <OrderRow key={i} r={r} />
              ))}
            </div>
            <Pager page={orderPageSafe} pageCount={orderPageCount} onPage={setOrderPage} />
          </div>
        )}
      </div>
    </div>
  );
}

function SectionHead({ awaitingCount, lastRefresh, onRefresh }: { awaitingCount: number; lastRefresh: string; onRefresh: () => void }) {
  const { t } = useT();
  return (
    <div className="section-head">
      <h2>
        <Icon name="receipt" size={26} /> {t('order.listTitle')}
        {awaitingCount > 0 ? <span className="await-pill">{t('ordersPage.awaitingPill', { count: awaitingCount })}</span> : null}
        {/* Owner (2026-10-06): "siparişler sekmesinden de kaldır" — no wallet
            figure in this header. The orders list answers "what did I buy and
            where is it"; the balance belongs where a purchase is decided. */}
      </h2>
      <div className="row" style={{ gap: '8px', alignItems: 'center', marginLeft: 'auto', flexShrink: 0 }}>
        <span className="xs faint" id="lastRefresh">
          {lastRefresh}
        </span>
        <button className="btn btn-ghost btn-sm" onClick={onRefresh}>
          <Icon name="refresh" size={16} />
          <span className="btn-label">{t('common.refresh')}</span>
        </button>
      </div>
    </div>
  );
}

/** A row is genuinely "awaiting payment" only while its payment window is still
 *  open. Once the window has lapsed the single-use invoice can no longer be
 *  paid, so it must not be counted/shown as a payable "Pay now" row. */
function awaitingPay(x: any): boolean {
  if (String(x?.status || '').toLowerCase() !== 'awaiting_payment') return false;
  const exp = String(x?.payment_expiry || x?.payment_expires_at || '');
  if (!exp) return true;
  const t = Date.parse(exp);
  return Number.isFinite(t) ? t > Date.now() : true;
}

function OrderRow({ r }: { r: any }) {
  const { t } = useT();
  const href = pagePath(r.rowKind === 'quote' ? `/order?type=quote&id=${encodeURIComponent(r.id)}` : `/order?id=${encodeURIComponent(r.id)}`);
  const [rated] = useState(r.rating > 0);
  // Only render an "≈ NIM" price when the row carries a LOCKED snapshot
  // (explicit amount or purchase-time rate). Without one, a live conversion
  // would make history drift every time NIM moves — show the product's own
  // face value instead and never fake a checkout-time number.
  const locked = r.estimatedNim > 0 || (r.nimUsd > 0 && r.nimUsdRate > 0);
  const nimNode =
    r.nimUsd > 0 && locked ? (
      <NimAmount
        q={{
          product_usd: Math.round(r.nimUsd * 1e6),
          estimated_nim: r.estimatedNim > 0 ? r.estimatedNim : undefined,
          nim_usd_rate: r.nimUsdRate > 0 ? r.nimUsdRate : undefined,
        }}
        fallback="—"
      />
    ) : null;

  let durationNode: any = null;
  if (isTerminalStatus(r.status) && r.updated_at && r.created_at) {
    const secs = Math.round((new Date(r.updated_at).getTime() - new Date(r.created_at).getTime()) / 1000);
    if (isFinite(secs) && secs >= 0) {
      durationNode = (
        <span className="xs faint" style={{ display: 'inline-flex', alignItems: 'center', gap: '5px', whiteSpace: 'nowrap', flexShrink: 0 }}>
          <Icon name="clock" size={12} /> {t('common.total', { value: fmtDuration(secs) })}
        </span>
      );
    }
  }

  let ratingNode: any = null;
  if (isDeliveredStatus(r.status)) {
    if (rated && r.rating > 0) {
      ratingNode = (
        <span className="row" style={{ gap: '6px', alignItems: 'center', flexWrap: 'wrap', flexShrink: 0 }}>
          <StarsDisplay rating={r.rating} size={14} />
          <span className="xs faint">{t('common.rated')}</span>
          {/* The rating is anchored on-chain as a 1-Luna memo; the chip links
              to the very transaction anyone can read on the explorer. */}
          <OnChainProof tx={r.ratingTx} />
        </span>
      );
    } else if (!rated) {
      ratingNode = (
        // Rating (stars + optional comment, paid by the buyer) happens on the
        // order page, so the row only points there.
        <span className="row ord-rate" style={{ gap: '6px', alignItems: 'center' }}>
          <span className="xs faint">{t('common.rate')}</span>
          <StarsDisplay rating={0} size={14} />
        </span>
      );
    }
  }

  // NOTE: 'thumb ' (space!) — 'thumb.' + x would create ONE class literally named "thumb.thumb-gc"
  // which matches no CSS rule, so the raw image width (≈294px) blew up the grid track.

  // A batch of the SAME brand (qty 2 of one card) is not a multi-brand stack:
  // the fanned layer cards collapse into one small off-centre card inside the
  // tile. Only DISTINCT brands get the stack; everything else shows the single
  // centred thumb at full tile size.
  const distinctTitles = Array.from(new Set((r.batchSummaryItems || []) as string[]));

  return (
    <a className="order-card" href={href}>
      <div className="o-thumb">
        {distinctTitles.length > 1 ? (
          <BrandThumbStack titles={distinctTitles} country={r.country} />
        ) : r.image ? (
          <UnifiedThumb src={r.image} alt={r.name || r.id} bg={r.bgColor || 'rgb(255,255,255)'} />
        ) : (
          <UnifiedThumb src={''} alt={r.name || r.id} bg={'rgb(255,255,255)'} />
        )}
      </div>
      {/* THE FEED'S OWN RHYTHM (owner, 2026-10-06: "orders sekmesini de
          activity'deki tarz yaparsan seviniriz, güzel olmuş da"). Same three
          rows as a public feed row — title + status + when, then the amount and
          its chips, then progress · how long it took · the rating. The side
          column is gone because it was the thing that made a phone squeeze the
          name into a ribbon. */}
      <div className="o-main">
        <div className="o-row-1">
          <span className="o-title-wrap">
            {r.country ? (
              <span className="o-flag" title={countryName(r.country)}>
                <FlagMark country={r.country} size={16} />
              </span>
            ) : null}
            <span className="strong truncate o-name">{r.name || r.id}</span>
          </span>
          <span className="o-side">
            <StatusBadge status={r.status} />
            <span className="xs faint o-time">{r.created_at ? <ClockTime ts={r.created_at} /> : null}</span>
          </span>
        </div>
        <div className="o-row-2">
          <span className="o-amounts">
            <span className="o-amt-main">
              {nimNode ? nimNode : r.paidUnit || (r.nimUsd > 0 ? '' : r.priceLabel) || ''}
            </span>
            {nimNode && (r.paidUnit || r.usdLabel) ? (
              <span className="xs faint">{[r.paidUnit, r.usdLabel].filter(Boolean).join(' · ')}</span>
            ) : null}
            <span className="o-chips">
              <span className="chip xs">{fmtDate(r.created_at)}</span>
              {r.batchItems > 1 ? <span className="chip xs">{t('ordersPage.itemsCount', { count: r.batchItems })}</span> : r.qty > 1 ? <span className="chip xs">× {r.qty}</span> : null}
              {r.country ? (
                <span className="chip xs">
                  <FlagMark country={r.country} size={13} /> {countryName(r.country)}
                </span>
              ) : null}
              {/* Delivery channel, straight from the server manifest: a cart with
                  a top-up in it must not read as an email-only order. */}
              {r.deliveryLabel ? (
                <span className="chip xs">
                  <Icon name={r.deliveryIcon} size={12} /> {r.deliveryLabel}
                </span>
              ) : null}
              {r.railLabel ? <span className="chip xs">{r.railLabel}</span> : null}
              {String(r.status).toLowerCase() === 'awaiting_payment' ? (
                awaitingPay(r) ? (
                  <span className="chip xs" style={{ fontWeight: 800, color: 'var(--stamp-ink)' }}>{t('order.payNow')}</span>
                ) : (
                  <span className="chip xs" style={{ color: 'var(--ink-dim)' }}>{t('ordersPage.paymentWindowPassed')}</span>
                )
              ) : null}
            </span>
          </span>
        </div>
        <div className="o-row-3">
          <span className="o-progress">
            <MiniProgress order={r} />
          </span>
          {durationNode}
          {ratingNode ? <span className="o-rate">{ratingNode}</span> : null}
        </div>
      </div>
    </a>
  );
}

export function OrdersPage() {
  return (
    <AppRoot activeKey="orders">
      <OrdersView />
    </AppRoot>
  );
}
