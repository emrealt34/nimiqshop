/**
 * AdminRatingsPanel — operator moderation of buyer star-rating comments.
 *
 * Two controls: switch buyer comments off for everybody (stars keep working),
 * and hide or show a single comment. Hiding removes the words from the public
 * site and from the buyer's own order page; the stars still count, and the
 * memo stays on the Nimiq chain (on-chain transactions cannot be deleted).
 */
import { useCallback, useEffect, useState } from 'react';
import { useT } from '../../i18n';
import { adminHideRatingComment, adminListRatings, adminSetRatingComments } from '../../lib/api';
import { fmtDate } from '../../lib/format';
import { AlertBox, StarsDisplay } from '../ui/uiKit';

interface AdminRating {
  kind: 'order' | 'quote';
  id: string;
  stars: number;
  comment: string;
  at: string;
  hidden: boolean;
}

export function AdminRatingsPanel() {
  const { t } = useT();
  const [items, setItems] = useState<AdminRating[]>([]);
  const [enabled, setEnabled] = useState(true);
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);

  const load = useCallback(async () => {
    setLoading(true);
    try {
      const res: any = await adminListRatings();
      setItems(Array.isArray(res?.items) ? res.items : []);
      setEnabled(res?.comments_enabled !== false);
      setError(null);
    } catch (e: any) {
      setError(String(e?.message || e));
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    load();
  }, [load]);

  async function toggleComments() {
    setBusy('switch');
    setNotice(null);
    try {
      const res: any = await adminSetRatingComments(!enabled);
      setEnabled(res?.comments_enabled !== false);
      setNotice(t('rating.adminSaved'));
    } catch (e: any) {
      setError(String(e?.message || e));
    } finally {
      setBusy(null);
    }
  }

  async function toggleHidden(r: AdminRating) {
    setBusy(r.id);
    setNotice(null);
    try {
      await adminHideRatingComment(r.kind, r.id, !r.hidden);
      setItems((list) => list.map((x) => (x.id === r.id && x.kind === r.kind ? { ...x, hidden: !r.hidden } : x)));
      setNotice(t('rating.adminSaved'));
    } catch (e: any) {
      setError(String(e?.message || e));
    } finally {
      setBusy(null);
    }
  }

  return (
    <div className="card">
      <div className="card-title">{t('rating.adminTitle')}</div>
      <div className="small muted mb-2">{t('rating.adminHint')}</div>

      <div className="row" style={{ gap: '10px', alignItems: 'center', flexWrap: 'wrap' }}>
        <span className="strong">{enabled ? t('rating.adminCommentsOn') : t('rating.adminCommentsOff')}</span>
        <button className="btn btn-ghost btn-sm" type="button" disabled={busy !== null || loading} onClick={toggleComments}>
          {enabled ? t('rating.adminTurnOff') : t('rating.adminTurnOn')}
        </button>
      </div>

      {error ? <div className="mt-2"><AlertBox type="error">{error}</AlertBox></div> : null}
      {notice ? <div className="mt-2"><AlertBox type="success">{notice}</AlertBox></div> : null}

      {loading ? (
        <div className="small muted mt-2">…</div>
      ) : items.length === 0 ? (
        <div className="small muted mt-2">{t('rating.adminEmpty')}</div>
      ) : (
        <ul style={{ listStyle: 'none', padding: 0, margin: '12px 0 0' }}>
          {items.map((r) => (
            <li
              key={`${r.kind}:${r.id}`}
              className="row"
              style={{ gap: '10px', alignItems: 'flex-start', padding: '8px 0', borderTop: '1px solid var(--line, #eee)', opacity: r.hidden ? 0.6 : 1 }}
            >
              <div style={{ flex: 1, minWidth: 0 }}>
                <div className="row" style={{ gap: '8px', alignItems: 'center', flexWrap: 'wrap' }}>
                  <StarsDisplay rating={r.stars} size={14} />
                  <span className="xs faint">{fmtDate(r.at)}</span>
                  <span className="xs mono faint">{t('rating.adminPurchase')} {r.id.slice(0, 8)}</span>
                  {r.hidden ? <span className="xs strong">{t('rating.adminHidden')}</span> : null}
                </div>
                {r.comment ? (
                  <div className="small mt-1" style={{ wordBreak: 'break-word' }}>{r.comment}</div>
                ) : null}
              </div>
              {r.comment ? (
                <button
                  className="btn btn-ghost btn-sm"
                  type="button"
                  disabled={busy !== null}
                  onClick={() => toggleHidden(r)}
                >
                  {r.hidden ? t('rating.adminShow') : t('rating.adminHide')}
                </button>
              ) : null}
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}
