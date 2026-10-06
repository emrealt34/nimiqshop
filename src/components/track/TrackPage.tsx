/**
 * TrackPage.tsx — React port of pages/track.js. PUBLIC live order tracking:
 * anyone with an order id can see lifecycle stage + summary + public txs,
 * but never delivery codes. Anonymous purchases also hide wallet identity and
 * transaction details. 15s auto-poll.
 */
import React, { useCallback, useEffect, useRef, useState } from 'react';
import { Icon } from '../ui/Icon';
import { BrandThumbStack } from '../ui/UnifiedThumb';
import { FlagMark } from '../ui/FlagMark';
import { AppRoot } from '../AppRoot';
import { openLoginSheet } from '../shell/SiteShell';
import { friendlyApiMessage, trackOrder, errorDetailLine } from '../../lib/api';
import { useSession } from '../../lib/useSession';
import { cleanProductLabel, cleanBatchProductLabels, fmtUSD, fmtNIM, fmtDate, queryParam, countryName, shortAddr } from '../../lib/format';
import { useInterval } from '../../lib/useInterval';
import { useRouter } from '../../lib/router';
import { useSheet, useToast } from '../AppProviders';
import { deliverySummary, payRail } from '../../lib/deliveryCopy';
import { KvSkeleton } from '../ui/uiKit';
import { StatusBadge, StageTimeline, EmptyState, ErrorState, AlertBox, CopyButton, KvCard } from '../ui/uiKit';
import { useT } from '../../i18n';
import { pagePath } from '../../lib/asset';

type TrackData = any;

function TrackingSearch() {
  const { t } = useT();
  const [value, setValue] = useState('');
  const { navigate } = useRouter();
  return <form className="tracking-search card" onSubmit={(event) => {
    event.preventDefault();
    const id = value.trim();
    if (id) navigate('/track?order=' + encodeURIComponent(id));
  }} style={{ display: 'flex', flexDirection: 'column', gap: '14px', alignItems: 'stretch' }}>
    <label className="field" style={{ marginBottom: 0, width: '100%' }}>
      <span className="strong">{t('trackPage.orderId')}</span>
      <input className="input" required maxLength={128} value={value} autoComplete="off"
        placeholder={t('trackPage.orderIdPlaceholder')} onChange={(event) => setValue(event.target.value)} style={{ marginTop: 6 }} />
      <span className="xs muted">{t('trackPage.orderIdHint')}</span>
    </label>
    <button type="submit" className="btn btn-gold" disabled={!value.trim()} style={{ alignSelf: 'center', minWidth: 180, justifyContent: 'center' }}><Icon name="search" size={18} /> {t('order.track')}</button>
  </form>;
}

function TrackThumbs({ titles, country }: { titles: string[]; country: string }) {
  return (
    <div style={{ width: 'clamp(96px, 40vw, 140px)', flex: 'none' }}>
      <BrandThumbStack titles={titles} country={country} />
    </div>
  );
}

export function TrackView() {
  const { t } = useT();
  const id = queryParam('order');
  const [queryReady, setQueryReady] = useState(false);
  const [data, setData] = useState<TrackData | null>(null);
  const [err, setErr] = useState('');
  /* The exact failure behind `err` (code · status · backend message). */
  const [errDetail, setErrDetail] = useState('');
  const [loading, setLoading] = useState(true);
  const inFlight = useRef(false);
  const alive = useRef(true);
  const { openSheet, closeSheet } = useSheet();
  const { toast } = useToast();
  // null = auth not decided yet (SSR / first paint). Render nothing for the
  // owner bar instead of the signed-out variant — see lib/useSession.ts.
  const authed = useSession();

  useEffect(() => { alive.current = true; setQueryReady(true); return () => { alive.current = false; }; }, []);

  const load = useCallback(async () => {
    if (!queryReady || inFlight.current) return;
    if (!id) {
      setLoading(false);
      setErr('');
      setData(null);
      return;
    }
    inFlight.current = true;
    try {
      const res = await trackOrder(id);
      if (!alive.current) return;
      if (!res || typeof res !== 'object' || !res.id) throw new Error(t('trackPage.invalidResponse'));
      setData(res);
      setErr('');
    } catch (err: any) {
      if (!alive.current) return;
      setData(null);
      if (err.status === 404) { setErr('__404__'); setErrDetail(''); }
      else {
        setErr(friendlyApiMessage(err, t('errors.loadOrder')));
        setErrDetail(errorDetailLine(err));
      }
    } finally {
      inFlight.current = false;
      if (alive.current) setLoading(false);
    }
  }, [id, queryReady, t]);

  useEffect(() => {
    load();
  }, [load]);
  useInterval(() => { if (id && document.visibilityState === 'visible') load(); }, 15000, [id]);

  if (queryReady && !id) {
    return (
      <div className="container">
        <BackRow />
        <Header />
        <div className="mt-2">
          <TrackingSearch />
          <p className="small muted mt-2">{t('trackPage.publicNote')}</p>
          <a className="btn btn-ghost" href={pagePath("/activity")}>{t('trackPage.seeLivePayments')}</a>
        </div>
      </div>
    );
  }

  if (loading) {
    return (
      <div className="container">
        <BackRow />
        <Header />
        <div className="mt-2">
          <div className="card">
            <KvSkeleton n={6} />
          </div>
        </div>
      </div>
    );
  }

  if (err === '__404__') {
    return (
      <div className="container">
        <BackRow />
        <Header />
        <div className="mt-2">
          <EmptyState iconName="search" title={t('trackPage.notFoundTitle')} text={t('trackPage.notFoundText')} />
          <TrackingSearch />
        </div>
      </div>
    );
  }
  if (err) {
    return (
      <div className="container">
        <BackRow />
        <Header />
        <div className="mt-2">
          <ErrorState message={err} detail={errDetail} retry={load} />
        </div>
      </div>
    );
  }

  const trk = data;
  const quoteTracking = ['direct_payment','cryptorefills_purchase'].includes(trk.type);
  const titleParts = cleanBatchProductLabels(trk.batch_summary_items || trk.batch_summary || trk.title || '');
  const title = titleParts.length > 1
    ? titleParts.join(' + ')
    : cleanProductLabel(trk.title) || (quoteTracking ? t('trackPage.btcLightningPurchase') : t('common.purchase'));
  // Rail and channel come from the public payload; a USDT order must not be
  // labelled a "Nimiq Pay purchase", and a top-up must not promise a code.
  const tRail = payRail(trk);
  const tDel = deliverySummary(trk);
  const rows: Array<[string, any]> = [
    [t('trackPage.rowStatus'), <StatusBadge key="s" status={trk.status} />],
    [t('trackPage.rowType'), quoteTracking ? (tRail.isUsdt ? t('trackPage.usdtPurchase') : t('trackPage.btcLightningPurchase')) : t('common.purchase')],
    [t('trackPage.rowAmount'), Number(trk.usd) > 0 ? fmtUSD(trk.usd) : '—'],
  ];
  if (tDel.label && tDel.label !== '—') rows.push([t('trackPage.rowDelivery'), tDel.label]);
  if (trk.nim > 0 && !tRail.isUsdt) rows.push([t('trackPage.rowNimEstimate'), '≈ ' + fmtNIM(Math.round(Number(trk.nim)||0), 0) + ' NIM']);
  if (tRail.isUsdt) rows.push([t('trackPage.rowNetwork'), t('trackPage.polygonChain')]);
  if (trk.country)
    rows.push([
      t('trackPage.rowCountry'),
      <span key="c" className="row" style={{ gap: '6px', alignItems: 'center' }}>
        <FlagMark country={trk.country} size={18} /> <span>{countryName(trk.country)}</span>
      </span>,
    ]);
  if (trk.quantity && trk.quantity > 1) rows.push([t('trackPage.rowQuantity'), '×' + trk.quantity]);
  if (trk.created_at) rows.push([t('trackPage.rowCreated'), fmtDate(trk.created_at)]);
  if (trk.tx)
    rows.push([
      t('trackPage.rowTx'),
      <span key="tx" className="mono small" style={{ wordBreak: 'break-all' }}>{String(trk.tx)}</span>,
    ]);
  if (trk.updated_at) rows.push([t('trackPage.rowUpdated'), fmtDate(trk.updated_at)]);

  // Anonymous activity remains visible, but the buyer's payment transaction
  // details stay private alongside the wallet identity.
  const txPairs: Array<[string, any]> = (trk.anonymous ? [] : (trk.transactions || [])).filter((tx: any) => !/lightning|bitcoin|\bBTC\b|bolt.?11/i.test(String(tx.network || '') + ' ' + String(tx.hash || ''))).map((tx: any) => [
    tx.label + ' · ' + tx.network,
    <span key="tx" className="row" style={{ gap: '6px', justifyContent: 'flex-end' }}>
      <span className="mono small" title={tx.network + ' · ' + tx.hash}>{shortAddr(tx.hash, 8, 6)}</span>
      <CopyButton getText={tx.hash} label="" />
    </span>,
  ]);
  const txCard = txPairs.length ? (
    <div className="mt-2">
      <KvCard title={t('trackPage.txTitle')} rows={txPairs} />
    </div>
  ) : null;

  const ownerBar = authed === null ? null : authed ? (
    <a className="btn btn-gold btn-block" href={pagePath(quoteTracking ? `/order?type=quote&id=${encodeURIComponent(trk.id)}` : `/order?id=${encodeURIComponent(trk.id)}`)}>
      <Icon name="eye" size={18} />
      <span className="btn-label">{t('trackPage.openMyOrder')}</span>
    </a>
  ) : (
    <button className="btn btn-outline btn-block track-owner-btn" onClick={() => openLoginSheet({ openSheet, closeSheet, toast })}>
      <Icon name="nimiq" size={18} />
      <span className="btn-label">{t('trackPage.connectYours')}</span>
    </button>
  );

  return (
    <div className="container">
      <BackRow />
      <Header />
      <div className="fade-in">
        <div className="row between mb-2" style={{ flexWrap: 'wrap', gap: '12px', alignItems: 'center' }}>
          <div className="row" style={{ gap: 12, alignItems: 'center' }}>
            {titleParts.length > 1 ? <TrackThumbs titles={titleParts} country={String(trk.country||'')} /> : <TrackThumbs titles={[title]} country={String(trk.country||'')} />}
            <h2 style={{ margin: 0 }}>{title}</h2>
          </div>
          <StatusBadge status={trk.status} />
        </div>
        <div className="detail-grid cols">
          <div className="card">
            <div className="card-title">{t('trackPage.liveTracking')}</div>
            <StageTimeline stages={trk.stages || []} channel={tDel.channel} usdt={tRail.isUsdt} />
          </div>
          <div className="col">
            <KvCard title={t('trackPage.summaryTitle')} rows={rows} />
            <PublicItemsCard lines={Array.isArray(trk.lines) ? trk.lines : []} />
            {txCard}
            <div className="mt-2">{ownerBar}</div>
          </div>
        </div>
        <div className="mt-2">
          <AlertBox type="info">
            {t('trackPage.publicAlert')}{trk.anonymous ? t('trackPage.publicAlertAnon') : ''}
          </AlertBox>
        </div>
      </div>
    </div>
  );
}

function PublicItemsCard({ lines }: { lines: any[] }) {
  const { t } = useT();
  const visible = lines.filter((line) => line && line.product_id);
  if (visible.length < 2) return null;
  return (
    <div className="card mt-2">
      <div className="card-title">{t('trackPage.itemsInOrder')}</div>
      {visible.map((line, index) => {
        const channel = String(line.delivery_channel || '').toLowerCase();
        const isPhone = channel === 'phone';
        const isEsim = String(line.kind || '').toLowerCase() === 'esim';
        const face = String(line.face_label || line.denomination || '').trim();
        return (
          <div
            key={String(line.product_id) + ':' + index}
            className="row between"
            style={{ gap: 10, padding: '8px 0', borderTop: index ? '1px dashed var(--line-hairline, rgba(0,0,0,.12))' : 'none', alignItems: 'flex-start' }}
          >
            <div style={{ minWidth: 0, flex: 1 }}>
              <div className="strong small">
                {cleanProductLabel(line.product_id) || t('trackPage.productFallback')}
                {Number(line.quantity) > 1 ? ` ×${line.quantity}` : ''}
              </div>
              {face && face.toLowerCase() !== 'range' ? <div className="xs faint">{face}</div> : null}
            </div>
            <div className="xs" style={{ textAlign: 'right', maxWidth: '55%' }}>
              <span className="row" style={{ gap: 5, justifyContent: 'flex-end', alignItems: 'center' }}>
                <Icon name={isPhone ? 'phone' : 'mail'} size={13} />
                <span>{isPhone ? t('trackPage.toPhone') : isEsim ? t('trackPage.esimByEmail') : t('trackPage.byEmail')}</span>
              </span>
              <div className="mono faint">{isPhone ? t('trackPage.phoneHidden') : t('trackPage.emailHidden')}</div>
            </div>
          </div>
        );
      })}
    </div>
  );
}

function BackRow() {
  const { t } = useT();
  return (
    <div className="row mb-2" style={{ gap: '10px' }}>
      <a className="btn btn-ghost btn-sm" href={pagePath("/activity")}>
        <Icon name="back" size={16} />
        <span className="btn-label">{t('nav.activity')}</span>
      </a>
    </div>
  );
}

function Header() {
  const { t } = useT();
  return (
    <>
      <h1 style={{ display: 'flex', alignItems: 'center', gap: '10px', margin: '4px 0 2px' }}>
        <Icon name="pulse" size={24} /> {t('trackPage.headerTitle')}
      </h1>
      <p className="lede">{t('trackPage.headerLede')}</p>
    </>
  );
}

export function TrackPage() {
  return (
    <AppRoot activeKey="track">
      <TrackView />
    </AppRoot>
  );
}
