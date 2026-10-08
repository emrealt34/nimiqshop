/**
 * RatingFlow — the star rating with an optional comment, paid by the buyer.
 *
 * Flow: pick stars and an optional comment → the shop returns the memo the
 * wallet must sign → the buyer's wallet pays 1 Luna plus the network fee →
 * the shop confirms the transaction on the chain and saves the rating.
 * A repeat of the saved rating is refused before any payment is asked for.
 */
import { useEffect, useRef, useState } from 'react';
import { useT, type Translator } from '../../i18n';
import { ApiError, getRatingComments, getRatingConfig, ratingIntent, type RatingKind } from '../../lib/api';
import {
  awaitRatingSaved,
  clearPendingRating,
  payRatingIntent,
  RatingError,
  readPendingRating,
  rememberPendingRating,
  type RatingIntent,
} from '../../lib/ratingPay';
import { cleanComment, commentProblem, MAX_COMMENT, memoPreview } from '../../lib/ratingComment';
import { StarPicker, StarsDisplay } from '../ui/uiKit';

export interface RatingCurrent {
  rated: boolean;
  stars: number;
  comment: string;
  edits: number;
}

interface RatingConfig {
  enabled: boolean;
  comments_enabled: boolean;
  max_ratings: number;
  max_comment: number;
}

let configCache: Promise<RatingConfig> | null = null;
function loadConfig(): Promise<RatingConfig> {
  if (!configCache) {
    configCache = getRatingConfig()
      .then((c: any) => ({
        enabled: !!c.enabled,
        comments_enabled: c.comments_enabled !== false,
        max_ratings: Number(c.max_ratings) || 5,
        max_comment: Number(c.max_comment) || MAX_COMMENT,
      }))
      .catch(() => {
        configCache = null; // retry on the next mount
        return { enabled: false, comments_enabled: false, max_ratings: 5, max_comment: MAX_COMMENT };
      });
  }
  return configCache;
}

export function RatingFlow({
  kind,
  id,
  current,
  onSaved,
}: {
  kind: RatingKind;
  id: string;
  current: RatingCurrent;
  onSaved?: (res: any) => void;
}) {
  const { t } = useT();
  const [cfg, setCfg] = useState<RatingConfig | null>(null);
  const [stars, setStars] = useState(current.stars || 0);
  const [comment, setComment] = useState(current.comment || '');
  const [phase, setPhase] = useState<'idle' | 'signing' | 'waiting' | 'saved'>('idle');
  const [error, setError] = useState<string | null>(null);
  const mounted = useRef(true);

  useEffect(() => {
    mounted.current = true;
    loadConfig().then((c) => mounted.current && setCfg(c));
    return () => {
      mounted.current = false;
    };
  }, []);

  // Resume after a redirect (or a closed tab): a rating was being paid for
  // this purchase. The shop finds the transaction by its memo and saves it.
  useEffect(() => {
    const pending = readPendingRating(kind, id);
    if (!pending) return;
    setStars(pending.stars);
    setComment(pending.comment);
    setPhase('waiting');
    awaitRatingSaved(kind, id, { stars: pending.stars, comment: pending.comment })
      .then((res) => {
        clearPendingRating();
        if (!mounted.current) return;
        setPhase('saved');
        onSaved?.(res);
      })
      .catch((err) => {
        if (!mounted.current) return;
        clearPendingRating();
        setPhase('idle');
        setError(messageFor(err, t));
      });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [kind, id]);

  const c = cleanComment(comment);
  const problem = commentProblem(comment);
  const commentsBlocked = !!cfg && !cfg.comments_enabled && c !== '';
  const unchanged = current.rated && stars === current.stars && c === cleanComment(current.comment);
  const limitReached = current.edits >= (cfg?.max_ratings || 5);
  const busy = phase === 'signing' || phase === 'waiting';

  async function submit() {
    setError(null);
    if (stars < 1) return setError(t('rating.pickStars'));
    if (problem === 'chars') return setError(t('rating.commentChars'));
    if (problem === 'long') return setError(t('rating.commentLong', { max: cfg?.max_comment || MAX_COMMENT }));
    if (commentsBlocked) return setError(t('rating.commentsOff'));
    if (unchanged) return setError(t('rating.alreadySaved'));
    if (limitReached) return setError(t('rating.limit'));

    setPhase('signing');
    rememberPendingRating({ kind, id, stars, comment: c });
    let intent: RatingIntent;
    try {
      intent = (await ratingIntent(kind, id, { stars, comment: c })) as RatingIntent;
    } catch (err) {
      clearPendingRating();
      setPhase('idle');
      return setError(messageFor(err, t));
    }
    try {
      const paid = await payRatingIntent(intent);
      if (!mounted.current) return;
      setPhase('waiting');
      const res = await awaitRatingSaved(kind, id, { stars, comment: c, tx_hash: paid.hash });
      clearPendingRating();
      if (!mounted.current) return;
      setPhase('saved');
      onSaved?.(res);
    } catch (err) {
      if (err instanceof RatingError && err.code === 'pending') {
        // Keep the pending entry: the rating saves itself once the chain confirms.
        if (mounted.current) {
          setPhase('idle');
          setError(t('rating.pendingTimeout'));
        }
        return;
      }
      clearPendingRating();
      if (mounted.current) {
        setPhase('idle');
        setError(messageFor(err, t));
      }
    }
  }

  if (cfg && !cfg.enabled) {
    return <div className="small muted">{t('rating.unavailable')}</div>;
  }

  const hint =
    problem === 'chars' ? t('rating.commentChars')
      : problem === 'long' ? t('rating.commentLong', { max: cfg?.max_comment || MAX_COMMENT })
        : null;

  return (
    <div className="rating-flow">
      <div className="small muted mb-1">{t('rating.intro')}</div>
      <div className="row" style={{ gap: '10px', alignItems: 'center', flexWrap: 'wrap' }}>
        <StarPicker size={26} value={stars} onSelect={(n) => { setStars(n); setError(null); }} />
        {stars > 0 ? <StarsDisplay rating={stars} size={16} /> : null}
      </div>

      <label className="small strong mt-2" htmlFor={`rating-comment-${id}`} style={{ display: 'block' }}>
        {t('rating.commentLabel')}
      </label>
      <input
        id={`rating-comment-${id}`}
        className="input mt-1"
        type="text"
        autoComplete="off"
        spellCheck={false}
        value={comment}
        disabled={busy || (cfg !== null && !cfg.comments_enabled)}
        placeholder={t('rating.commentPlaceholder')}
        aria-invalid={problem ? true : undefined}
        aria-describedby={`rating-comment-help-${id}`}
        onChange={(e) => { setComment(e.target.value); setError(null); }}
      />
      <div id={`rating-comment-help-${id}`} className={`xs mt-1 ${hint ? 'danger' : 'faint'}`}>
        {hint || t('rating.commentHint', { max: cfg?.max_comment || MAX_COMMENT })}
        <span style={{ float: 'right' }}>{c.length}/{cfg?.max_comment || MAX_COMMENT}</span>
      </div>

      {stars > 0 ? (
        <div className="xs faint mt-1" style={{ wordBreak: 'break-word' }}>
          {t('rating.memoLine', { memo: memoPreview(stars, comment) })}
        </div>
      ) : null}

      <div className="xs faint mt-1">{t('rating.costNote')}</div>

      {error ? <div className="small danger mt-1" role="alert">{error}</div> : null}
      {phase === 'waiting' ? <div className="small muted mt-1" role="status">{t('rating.waiting')}</div> : null}
      {phase === 'saved' ? <div className="small mt-1" role="status">{t('rating.saved')}</div> : null}

      <button
        className="btn btn-gold btn-block mt-2"
        type="button"
        disabled={busy || stars < 1 || !!hint || unchanged || limitReached || !cfg?.enabled}
        onClick={submit}
      >
        <span className="btn-label">
          {phase === 'signing' ? t('rating.signing')
            : current.rated ? t('rating.submitChange') : t('rating.submit')}
        </span>
      </button>
      {current.rated && current.edits > 0 ? (
        <div className="xs faint mt-1">
          {t('rating.changesLeft', { count: Math.max(0, (cfg?.max_ratings || 5) - current.edits) })}
        </div>
      ) : null}
    </div>
  );
}

function messageFor(err: unknown, t: Translator): string {
  if (err instanceof RatingError) {
    if (err.code === 'cancelled') return t('rating.cancelled');
    if (err.code === 'unavailable') return t('rating.updateNimiqPay');
    return t('rating.failed');
  }
  if (err instanceof ApiError) {
    const msg = err.message || '';
    if (err.status === 401 || /sign in/i.test(msg)) return t('rating.notSignedIn');
    if (/already saved/i.test(msg)) return t('rating.alreadySaved');
    if (/maximum number/i.test(msg)) return t('rating.limit');
    if (/switched off/i.test(msg)) return t('rating.commentsOff');
    if (err.status === 503) return t('rating.unavailable');
    if (err.status === 400 || err.status === 409) return msg;
  }
  return t('rating.failed');
}

/** Public list of the newest buyer comments. Renders nothing when there are none. */
export function RecentRatingComments() {
  const { t } = useT();
  const [items, setItems] = useState<{ stars: number; comment: string; at: string }[]>([]);
  useEffect(() => {
    let alive = true;
    getRatingComments()
      .then((r: any) => alive && setItems(Array.isArray(r?.items) ? r.items : []))
      .catch(() => alive && setItems([]));
    return () => {
      alive = false;
    };
  }, []);
  if (!items.length) return null;
  return (
    <div className="mt-2">
      <div className="small strong mb-1">{t('rating.recentTitle')}</div>
      <ul className="rating-comments" style={{ listStyle: 'none', padding: 0, margin: 0 }}>
        {items.slice(0, 8).map((c, i) => (
          <li key={i} className="row" style={{ gap: '8px', alignItems: 'baseline', padding: '4px 0', borderTop: i ? '1px solid var(--line, #eee)' : 'none' }}>
            <StarsDisplay rating={c.stars} size={12} />
            <span className="small" style={{ wordBreak: 'break-word' }}>{c.comment}</span>
          </li>
        ))}
      </ul>
    </div>
  );
}
