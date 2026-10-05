/**
 * RecentCashbackList — THE one "Recent cashback" block. Used on the Cashback
 * page and on the Profile page (and anywhere else in the future) so the list
 * never looks different from place to place: product thumbnail · product
 * name + rate chip · status · +NIM · rate source / tx.
 */
import React from 'react';
import { Icon } from '../ui/Icon';
import { BrandThumbStack } from '../ui/UnifiedThumb';
import { fmtStakeNIM, pctLabel } from '../../lib/stakerCashback';
import { Pager } from '../ui/Pager';
import { SkeletonLines } from '../ui/uiKit';
import { t as tr } from '../../i18n';

export type RecentCashbackRow = {
  id?: string;
  quote_id?: string;
  product_id?: string;
  amount_nim?: number;
  bps?: number;
  status?: string;
  status_label?: string;
  cashback_source?: string;
  tx_hash?: string;
  /** The purchase's Lightning payment hash — the tx of what you bought. */
  purchase_tx?: string;
};

export function rateSourceLabel(cb: RecentCashbackRow): string {
  switch (cb.cashback_source) {
    case 'code': return tr('cashback.rateCode');
    case 'ledger': return tr('cashback.rateLedger');
    case 'staker': return tr('cashback.rateStaker');
    default: return (cb.bps || 0) > 0 ? tr('cashback.rateBaseRate') : tr('cashback.rateNoStake');
  }
}

export function NimUnitMark({ size = 11 }: { size?: number }) {
  return (
    <span style={{ display: 'inline-flex', alignItems: 'center', gap: 4, fontWeight: 800 }}>
      {/* The REAL Nimiq mark, the same one the rest of the site draws: the
          public flat-top gold hexagon (Icon name="nimiq" → the PNG). This used
          to be a hand-rolled inline hexagon filled with currentColor, which
          meant a pointy-top shape (rotated against the brand) painted white in
          the dark theme — the exact thing Icon.tsx already warns about. */}
      <Icon name="nimiq" size={size} />
      NIM
    </span>
  );
}

function CashbackThumb({ title }: { title: string }) {
  const parts = React.useMemo(() => String(title || '').split(' + ').map((s) => s.trim()).filter(Boolean).slice(0, 3), [title]);
  return (
    <div className="cb-thumb">
      <BrandThumbStack titles={parts} />
    </div>
  );
}

export function RecentCashbackRowView({ cb }: { cb: RecentCashbackRow }) {
  const name = cb.product_id || tr('cashback.rcOrderFallback');
  const bps = Number(cb.bps) || 0;
  return (
    <div className="cb-row">
      <CashbackThumb title={String(name)} />
      <div className="cb-row-main">
        <div className="small strong cb-row-title">
          <span className="cb-row-name" title={name}>{name}</span>
          <span className="chip cb-rate-chip" title={tr('cashback.rcRateTitle')} style={{ color: bps > 0 ? 'var(--green)' : undefined }}>
            {pctLabel(bps)}
          </span>
        </div>
        <div className="xs faint">{cb.status_label || cb.status || 'pending'}</div>
        {cb.purchase_tx ? (
          <div className="mono xs faint" title={cb.purchase_tx} style={{ wordBreak: 'break-all' }}>
            tx {String(cb.purchase_tx).slice(0, 10)}…{String(cb.purchase_tx).slice(-6)}
          </div>
        ) : null}
      </div>
      <div className="cb-row-right">
        <div className="small strong cb-row-amount">+{fmtStakeNIM(Number(cb.amount_nim) || 0)} <NimUnitMark /></div>
        {cb.tx_hash ? (
          <span className="mono xs faint" title={cb.tx_hash}>{String(cb.tx_hash).slice(0, 10)}…</span>
        ) : (
          <span className="xs faint">{rateSourceLabel(cb)}</span>
        )}
      </div>
    </div>
  );
}

export function RecentCashbackList({
  rows,
  loading = false,
  pageSize = 0,
  emptyText = tr('cashback.rcEmpty'),
  showCount = true,
  title = tr('cashback.recentTitle'),
}: {
  rows: RecentCashbackRow[] | null | undefined;
  loading?: boolean;
  /** 0 = no pagination (show all rows). */
  pageSize?: number;
  emptyText?: string;
  showCount?: boolean;
  title?: string;
}) {
  const list = rows || [];
  const [page, setPage] = React.useState(0); // 0-based, like <Pager>
  const pageCount = pageSize > 0 ? Math.max(1, Math.ceil(list.length / pageSize)) : 1;
  const pageSafe = Math.min(page, pageCount - 1);
  const visible = pageSize > 0 ? list.slice(pageSafe * pageSize, pageSafe * pageSize + pageSize) : list;
  return (
    <div className="card mt-2 cb-recent-card">
      <div className="card-title cb-payouts-head">
        <span><Icon name="gift" size={16} /> {title}</span>
        {showCount && list.length > 0 && (
          <span className="xs faint" style={{ letterSpacing: 0, textTransform: 'none' }}>
            {tr('cashback.rcOrdersCount', { count: list.length })}
          </span>
        )}
      </div>
      {loading && !rows ? (
        <SkeletonLines n={3} />
      ) : list.length === 0 ? (
        <div className="small muted">{emptyText}</div>
      ) : (
        <div>
          {visible.map((cb, i) => <RecentCashbackRowView key={cb.id || cb.quote_id || `${cb.product_id}-${i}`} cb={cb} />)}
          {pageSize > 0 && pageCount > 1 && <Pager page={pageSafe} pageCount={pageCount} onPage={setPage} />}
        </div>
      )}
    </div>
  );
}
