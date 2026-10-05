/**
 * ActivityPage.tsx — React port of pages/activity.js. A live, transparent feed
 * of everything bought: stat tiles, community rating card with clickable
 * distribution filter, feed cards with real brand thumb + buyer local amount +
 * USD + NIM, wallet chip, stars and status. 15s poll + presence heartbeat.
 */
import { useCallback, useEffect, useState } from 'react';
import { BrandThumb, BrandThumbStack } from '../ui/UnifiedThumb';
import { Icon } from '../ui/Icon';
import { FlagMark } from '../ui/FlagMark';
import { Identicon } from '../ui/Identicon';
import { AppRoot } from '../AppRoot';
import { getActivity, cachedNimRate, friendlyApiMessage } from '../../lib/api';
import { cleanProductLabel, cleanBatchProductLabels, fmtNIM, timeAgo, countryName, shortAddr, fmtDuration } from '../../lib/format';
import { Clipboard } from '../../lib/clipboard';
import { useInterval } from '../../lib/useInterval';
import { useToast } from '../AppProviders';
import { StatusBadge, StarsDisplay, EmptyState, ErrorState, SkeletonLines, NimMark } from '../ui/uiKit';
import { Pager } from '../ui/Pager';
import { useT, t as i18nT } from '../../i18n';
import { pagePath } from '../../lib/asset';

/** Rows per page in the live feed (the site's default pagination). */
const FEED_PER_PAGE = 10;

/* ---------------- Meta thumb (real product photo) ---------------- */
function FeedThumb({ title, country }: { title: string; country: string }) {
  return (
    <div className="feed-thumb">
      <BrandThumb title={title} country={country} />
    </div>
  );
}

function FeedBatchThumb({ titles, country }: { titles: string[]; country: string }) {
  return (
    <div className="feed-thumb">
      <BrandThumbStack titles={titles} country={country} />
    </div>
  );
}

/* ---------------- normalization ---------------- */
function splitCleanBatchTitles(values: any): string[] {
  const source = Array.isArray(values) ? values : [values];
  return source.flatMap((value) =>
    cleanProductLabel(value)
      .split(/\s*\+\s*/)
      .map((part) => cleanProductLabel(part))
      .filter(Boolean)
  );
}

function batchTitles(it: any): string[] {
  const fromLines = Array.isArray(it?.lines) ? splitCleanBatchTitles(it.lines) : [];
  if (fromLines.length > 1) return fromLines.slice(0, 3);

  const raw = it?.batch_summary_items ?? it?.batch_summary;
  const fromSummary = raw ? splitCleanBatchTitles(Array.isArray(raw) ? raw : String(raw).split(',')) : [];
  if (fromSummary.length > 1) return fromSummary.slice(0, 3);

  // Old public rows stored a joined ProductID. The shared cleaner removes
  // Markdown links, denominations, quantities, and duplicate brand fragments.
  const joined = String(it?.product_id || it?.product || it?.title || it?.display_title || '').trim();
  const fromProduct = joined ? cleanBatchProductLabels(joined) : [];
  if (fromProduct.length > 1) return fromProduct.slice(0, 3);

  const count = Math.max(0, Number(it?.batch_items) || 0);
  return count > 1 ? Array.from({ length: Math.min(count, 3) }, (_, i) => i18nT('common.cartItem', { n: i + 1 })) : [];
}

function normItem(it: any) {
  const suppliedBatchItems = Math.max(0, Number(it.batch_items) || 0);
  const batchSummaryItems = batchTitles(it);
  const batchItems = Math.max(suppliedBatchItems, batchSummaryItems.length > 1 ? batchSummaryItems.length : 0);
  const batchSummary = batchSummaryItems.join(', ');
  const cleanTitle = cleanProductLabel(it.display_title || it.title || it.product || it.product_name || '');
  return {
    ...it,
    type: it.type || it.kind || 'purchase',
    title: batchSummaryItems.length > 1 ? batchSummaryItems.join(' + ') : cleanTitle || i18nT('common.purchase'),
    time: it.time || it.created_at || it.updated_at || '',
    address: it.address || '',
    anonymous: !!it.anonymous,
    usd: Number(it.usd) > 0 ? Number(it.usd) : 0,
    nim: Number(it.nim) > 0 ? Number(it.nim) : Number(it.nim_estimate) > 0 ? Number(it.nim_estimate) : 0,
    country: it.country || (Array.isArray(it.lines) && it.lines[0]?.country) || '',
    rating: Number(it.rating) || 0,
    batch_items: batchItems,
    batch_summary: batchSummary,
    batch_summary_items: batchSummaryItems,
  };
}

function compactNIM(n: number) {
  if (!(n > 0)) return '—';
  if (n >= 1e9) return (n / 1e9).toFixed(1) + 'B';
  if (n >= 1e6) return (n / 1e6).toFixed(1) + 'M';
  if (n >= 1e4) return Math.round(n / 1e3) + 'k';
  return fmtNIM(n, 0);
}

/* presence id + heartbeat */
function presenceId() {
  try {
    let id = localStorage.getItem('nimshop_pid');
    if (!id) {
      id = 'p-' + Math.random().toString(36).slice(2) + Date.now().toString(36);
      localStorage.setItem('nimshop_pid', id);
    }
    return id;
  } catch {
    return '';
  }
}
function heartbeat() {
  try {
    fetch(String((window as any).APP_CONFIG?.API_BASE || '/api').replace(/\/$/, '') + '/presence', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ id: presenceId() }),
      keepalive: true,
    }).catch(() => { /* offline / API down: presence is best-effort */ });
  } catch {
    /* ignore — try/catch only covers a synchronous throw, hence the .catch above */
  }
}

export function ActivityView() {
  const { t } = useT();
  const [allItems, setAllItems] = useState<any[]>([]);
  const [summary, setSummary] = useState<any>({});
  const [, setLastSummary] = useState<any>({});
  const [starFilter, setStarFilter] = useState(0);
  const [feedPage, setFeedPage] = useState(0);
  const [err, setErr] = useState('');
  const [loaded, setLoaded] = useState(false);
  const { toast } = useToast();

  const renderSummary = useCallback((s: any, items: any[]) => {
    if (!s || (!s.count && !items.length)) {
      const rated = items.filter((it) => it.rating > 0);
      const dist: Record<string, number> = {};
      for (const it of rated) dist[String(it.rating)] = (Number(dist[String(it.rating)]) || 0) + 1;
      s = { count: rated.length, average: rated.length ? rated.reduce((a: number, b: any) => a + b.rating, 0) / rated.length : 0, dist, active_users: s?.active_users || 0 };
    }
    setLastSummary(s);
    setSummary(s);
  }, []);

  const load = useCallback(async () => {
    try {
      const res = await getActivity(50);
      const raw = res.items || res.activity || res || [];
      const items = (Array.isArray(raw) ? raw : []).map(normItem).sort((a, b) => new Date(b.time).getTime() - new Date(a.time).getTime());
      setAllItems(items);
      renderSummary(res.summary || {}, items);
      setErr('');
    } catch (e) {
      setErr(friendlyApiMessage(e, t('errors.loadActivity')));
    } finally {
      setLoaded(true);
    }
  }, [renderSummary, t]);

  useEffect(() => {
    load();
    heartbeat();
  }, [load]);
  useInterval(load, 15000);
  useInterval(heartbeat, 30000);

  const s = summary;
  const activeNow = Number(s.active_users) || 0;
  const avgDeliv = Number(s.avg_delivery_seconds) || 0;
  const volUSD = allItems.reduce((a, it) => a + (it.usd > 0 ? it.usd : 0), 0);
  // A missing/zero rate made this divide by zero and render "InfinityB NIM".
  // Only convert when the cached rate is actually usable.
  const nimRate = Number(cachedNimRate()?.usd_per_nim) || 0;
  const volNIM =
    allItems.reduce((a, it) => a + (it.nim > 0 ? it.nim : 0), 0) ||
    (volUSD > 0 && nimRate > 0 ? volUSD / nimRate : 0);

  const tile = (ico: string, label: string, value: string, sub?: string, cls = '') => (
    // NOTE: join classes with a SPACE. The port used '.'+cls, which produced
    // class="act-tile.hot" — one class name — so the CSS rule `.act-tile.hot`
    // (green "shopping now" tile) never matched.
    <div className={'act-tile' + (cls ? ' ' + cls : '')}>
      <div className="act-tile-ico">
        {ico === 'nimiq' ? <NimMark className="act-tile-nim-mark" size={22} /> : <Icon name={ico} size={18} />}
      </div>
      <div className="act-tile-body">
        <div className="act-tile-num">{value}</div>
        <div className="act-tile-label">{label}</div>
        {sub ? <div className="act-tile-sub xs faint">{sub}</div> : null}
      </div>
    </div>
  );

  const count = s.count || 0;
  const avg = Number(s.average) || 0;
  const dist = s.dist || {};

  const filtered = starFilter > 0 ? allItems.filter((it) => it.rating === starFilter) : allItems;
  // The feed polls every 15s and items shift around, so the current page is
  // clamped on every render — you never land on a page past the last one.
  const pageCount = Math.max(1, Math.ceil(filtered.length / FEED_PER_PAGE));
  const pageSafe = Math.min(feedPage, pageCount - 1);
  const pageRows = filtered.slice(pageSafe * FEED_PER_PAGE, pageSafe * FEED_PER_PAGE + FEED_PER_PAGE);

  return (
    <div className="container activity-page">
      <div className="act-hero">
        <div>
          <h1 className="act-title">
            <Icon name="pulse" size={26} /> {t('activityPage.title')}
          </h1>
          <div className="xs faint mt-1">{t('activityPage.lede')}</div>
        </div>
        <div className="activity-actions">
          <button className="btn btn-ghost btn-sm" onClick={() => { toast(t('activityPage.refreshing'), 'info'); load(); }}>
            <Icon name="refresh" size={16} />
            <span className="btn-label">{t('common.refresh')}</span>
          </button>
        </div>
      </div>

      {/* The stat tiles go from a one-line skeleton to a 3-tile grid once the
          feed answers; reserving the grid's height keeps everything below it
          (#summary and the footer) from being pushed down — that push measured
          0.10 CLS before this. */}
      <div id="stats" className="act-stats mt-2" style={{ minHeight: 132 }}>
        {!loaded ? (
          <div className="card" style={{ padding: '18px' }}>
            <SkeletonLines n={1} />
          </div>
        ) : (
          <div className="act-stats-grid fade-in">
            {tile('bag', t('activityPage.tilePayments'), String(allItems.length))}
            {/* Fall back to USD volume when no NIM figure can be derived —
                an empty/〝—〞NIM tile next to real payments reads as broken. */}
            {volNIM > 0
              ? tile('nimiq', t('activityPage.tileNimVolume'), compactNIM(volNIM), '')
              : tile('nimiq', t('activityPage.tileVolume'), volUSD > 0 ? `${Math.round(volUSD)} USD` : '—', '')}
            {tile('user', t('activityPage.tileShoppingNow'), String(activeNow), t('activityPage.liveVisitor', { count: activeNow }), activeNow > 0 ? 'hot' : '')}
            {tile('clock', t('activityPage.tileAvgDelivery'), avgDeliv > 0 ? fmtDuration(avgDeliv) : '—', t('activityPage.paidToDelivered'))}
          </div>
        )}
      </div>

      <div id="summary" className="mt-2" style={{ minHeight: 300 }}>
        {!loaded ? (
          <div className="card">
            <SkeletonLines n={3} />
          </div>
        ) : (
          <div className="card rating-summary fade-in">
            <div className="rs-body">
              <div className="rs-left">
                <div className="rs-avg">
                  <StarsDisplay rating={avg} size={30} />
                  <span className="rs-num">{avg ? avg.toFixed(1) : '—'}</span>
                </div>
                <div className="small faint">{count ? t('activityPage.buyerRating', { count }) : t('activityPage.noRatingsYet')}</div>
              </div>
              <div className="rs-dist">
                {[5, 4, 3, 2, 1].map((star) => {
                  const c = Number(dist[String(star)] || 0);
                  const pct = count ? Math.round((c / count) * 100) : 0;
                  const active = starFilter === star;
                  return (
                    <button
                      key={star}
                      className={'dist-row' + (active ? ' active' : '')}
                      type="button"
                      title={c ? t('activityPage.distShow', { count: c, star }) : t('activityPage.distNone', { star })}
                      onClick={() => {
                        setStarFilter((prev) => (prev === star ? 0 : star));
                        setFeedPage(0); // a new view starts on page 1
                      }}
                    >
                      <span className="dist-star">
                        <StarsDisplay rating={star} size={13} />
                        <span className="xs faint">{star}</span>
                      </span>
                      <div className="dist-track">
                        <div className="dist-fill" style={{ width: pct + '%' }} />
                      </div>
                      <span className="dist-count xs mono faint">{c}</span>
                    </button>
                  );
                })}
              </div>
            </div>
          </div>
        )}
      </div>

      <div id="feed" className="mt-2">
        {/* The empty state below renders an <h3>; without a section heading in
            between, the document jumped h1 → h3 and Lighthouse failed the
            heading-order audit. The section is visually self-evident, so the
            heading stays for assistive tech only. */}
        <h2 className="sr-only">{t('activityPage.title')}</h2>
        {!loaded ? null : err ? (
          <ErrorState message={err} retry={load} />
        ) : !filtered.length ? (
          <EmptyState
            iconName={starFilter ? 'star' : 'pulse'}
            title={starFilter ? t('activityPage.emptyStarTitle', { star: starFilter }) : t('activityPage.noPaymentsYet')}
            text={starFilter ? t('activityPage.emptyStarText') : t('activityPage.emptyText')}
            action={
              starFilter ? undefined : (
                <a className="btn btn-gold" href={pagePath("/")}>
                  <Icon name="bag" size={18} />
                  <span>{t('activityPage.firstPurchase')}</span>
                </a>
              )
            }
          />
        ) : (
          <div className="fade-in">
            <div className="feed-list">
              {pageRows.map((it, i) => (
                <FeedItem key={i} it={it} />
              ))}
            </div>
            <Pager page={pageSafe} pageCount={pageCount} onPage={setFeedPage} />
          </div>
        )}
      </div>
    </div>
  );
}

function FeedItem({ it }: { it: any }) {
  const { t } = useT();
  const { toast } = useToast();
  const privateBuyer = !!it.anonymous;
  const addr = privateBuyer ? '' : String(it.address || '');
  const sub = [
    it.batch_items > 1 ? t('ordersPage.itemsCount', { count: it.batch_items }) : it.quantity && it.quantity > 1 ? `×${it.quantity}` : null,
    it.country ? countryName(it.country) : null,
  ].filter(Boolean).join(' · ');
  const local = it.local_amount && it.local_currency ? `${it.local_amount} ${it.local_currency}` : '';
  // Batch rows carry no local_amount/nim, which left the feed showing the
  // placeholder "amount in wallet" for real, delivered orders. USD is known.
  const main =
    local ||
    (it.nim > 0 ? '≈ ' + compactNIM(it.nim) + ' NIM' : '') ||
    (it.usd > 0 ? `${Math.round(it.usd * 100) / 100} USD` : '');
  const chips: string[] = [];
  if (local && it.nim > 0) chips.push('≈ ' + compactNIM(it.nim) + ' NIM');

  return (
    <a className="feed-item" href={pagePath('/track?order=' + encodeURIComponent(it.id))}>
      {it.batch_summary_items?.length > 1 ? <FeedBatchThumb titles={it.batch_summary_items} country={it.country} /> : <FeedThumb title={it.title} country={it.country} />}
      <div className="feed-main">
        <div className="feed-row-1">
          <div className="feed-title-wrap">
            <span className="feed-flag" title={it.country ? countryName(it.country) : ''}>
              {it.country ? <FlagMark country={it.country} size={16} /> : '🌐'}
            </span>
            <span className="strong truncate feed-title">{it.title}</span>
          </div>
          <div className="feed-side">
            {it.status ? <StatusBadge status={it.status} /> : null}
            <span className="xs faint feed-time">{timeAgo(it.time)}</span>
          </div>
        </div>
        <div className="feed-row-2">
          <div className="feed-amounts">
            {main ? <span className="feed-amt-main">{main}</span> : <span className="feed-amt-main muted">{t('activityPage.amountInWallet')}</span>}
            {it.tx ? (
              <span className="xs mono faint" title={String(it.tx)} style={{ wordBreak: 'break-all' }}>
                {String(it.tx).slice(0, 10)}…{String(it.tx).slice(-8)}
              </span>
            ) : null}
            {chips.length ? (
              <span className="feed-amt-chips">
                {chips.map((c, i) => (
                  <span key={i} className="chip xs">
                    {c}
                  </span>
                ))}
              </span>
            ) : null}
          </div>
        </div>
        <div className="feed-row-3">
          {privateBuyer ? (
            <span className="feed-wallet chip feed-wallet-private" title={t('activityPage.privateBuyerTitle')}>
              <Icon name="eye-off" size={14} />
              <span>{t('activityPage.privateBuyer')}</span>
            </span>
          ) : addr ? (
            <button
              className="feed-wallet chip"
              type="button"
              title={t('activityPage.copyTitle', { addr })}
              onClick={(e) => {
                e.preventDefault();
                // Boolean copy: the toast must not claim success on failure
                // (the old promise chain always toasted "copied").
                const copiedOk = Clipboard.copy(addr);
                toast(copiedOk ? t('activityPage.walletCopied') : t('activityPage.copyFailed'), copiedOk ? 'success' : 'warn');
              }}
            >
              <IdenticonChip address={addr} />
              <span className="mono">{shortAddr(addr)}</span>
              <Icon name="copy" size={12} />
            </button>
          ) : (
            <span className="feed-wallet chip feed-wallet-private" title={t('activityPage.walletHiddenTitle')}>
              <Icon name="eye-off" size={14} />
              <span>{t('activityPage.walletHidden')}</span>
            </span>
          )}
          {sub ? <span className="xs faint">{sub}</span> : null}
          {it.rating > 0 ? (
            <span className="cell-rate">
              <StarsDisplay rating={it.rating} size={14} />
            </span>
          ) : null}
        </div>
      </div>
    </a>
  );
}

function IdenticonChip({ address }: { address: string }) {
  return <Identicon address={address} className="feed-id" />;
}

export function ActivityPage() {
  return (
    <AppRoot activeKey="activity">
      <ActivityView />
    </AppRoot>
  );
}
