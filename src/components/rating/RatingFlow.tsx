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
import { ApiError, cancelRatingIntent, getRatingComments, getRatingConfig, ratingIntent, type RatingKind } from '../../lib/api';
import {
  awaitRatingSaved,
  clearPendingRating,
  payRatingIntent,
  RatingError,
  readPendingRating,
  rememberPendingRating,
  markPendingRatingSubmitted,
  type RatingIntent,
} from '../../lib/ratingPay';
import { cleanComment, commentProblem, MAX_COMMENT, memoPreview } from '../../lib/ratingComment';
import { ClockTime, OnChainRating, StarPicker, StarsDisplay } from '../ui/uiKit';
import { Identicon } from '../ui/Identicon';
import { shortAddr } from '../../lib/format';

/** One buyer rating, as the public comment list and the track page return
 *  it. Every field but the stars is optional: a rating is just stars, and a
 *  comment the shop cannot match to a buyer has no face. */
export interface RatingCommentItem {
  stars: number;
  comment?: string;
  at?: string;
  /** The buyer's wallet. Absent whenever the shop cannot match the purchase. */
  address?: string;
  /** Public product label of the purchase the rating belongs to. */
  product?: string;
  /** An admin test-center purchase, visible only while test orders are shown. */
  test_mode?: boolean;
  /** The rating's own on-chain transaction, when the anchor landed. */
  tx?: string;
}
import { ratingStars } from '../../lib/ratingStars';

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
  const [stars, setStars] = useState(ratingStars(current.stars) ?? 0);
  const [comment, setComment] = useState(current.comment || '');
  const [phase, setPhase] = useState<'idle' | 'signing' | 'waiting' | 'saved'>('idle');
  const [pendingSubmission, setPendingSubmission] = useState(false);
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
    setPendingSubmission(!!pending.submitted);
    setStars(pending.stars);
    setComment(pending.comment);
    setPhase('waiting');
    awaitRatingSaved(kind, id, { stars: pending.stars, comment: pending.comment, tx_hash: pending.hash })
      .then((res) => {
        clearPendingRating();
        if (!mounted.current) return;
        setPendingSubmission(false);
        setPhase('saved');
        onSaved?.(res);
      })
      .catch((err) => {
        if (!mounted.current) return;
        if (err instanceof RatingError && err.code === 'pending') {
          // A redirect may finish before the wallet can return its hash.
          // Retain that uncertain submission and only retry chain verification.
          markPendingRatingSubmitted(kind, id);
          setPendingSubmission(true);
        } else if (!readPendingRating(kind, id)?.submitted) clearPendingRating();
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
    const pending = readPendingRating(kind, id);
    if (pending?.submitted) {
      setPhase('waiting');
      try {
        const result = await awaitRatingSaved(kind, id, { stars: pending.stars, comment: pending.comment, tx_hash: pending.hash });
        clearPendingRating(); setPendingSubmission(false); setPhase('saved'); onSaved?.(result);
      } catch (err) { setPhase('idle'); setError(messageFor(err, t)); }
      return;
    }
    if (ratingStars(stars) === null) return setError(t('rating.pickStars'));
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
      markPendingRatingSubmitted(kind, id, paid.hash);
      setPendingSubmission(true);
      if (!mounted.current) return;
      setPhase('waiting');
      const res = await awaitRatingSaved(kind, id, { stars, comment: c, tx_hash: paid.hash });
      clearPendingRating();
      if (!mounted.current) return;
      setPendingSubmission(false);
      setPhase('saved');
      onSaved?.(res);
    } catch (err) {
      if (err instanceof RatingError && (err.code === 'cancelled' || err.code === 'wrongWallet')) {
        // Only an explicit wallet refusal (or Hub refusing to pay from the
        // signed-in address) releases the durable reservation: nothing was sent.
        await cancelRatingIntent(kind, id, { stars, comment: c }).catch(() => undefined);
      }
      if (err instanceof RatingError && err.code === 'pending') {
        // An uncertain wallet outcome must only retry verification, never payment.
        markPendingRatingSubmitted(kind, id, err.hash);
        setPendingSubmission(true);
        if (mounted.current) {
          setPhase('idle');
          setError(t('rating.pendingTimeout'));
        }
        return;
      }
      if (!readPendingRating(kind, id)?.submitted) clearPendingRating();
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
        <StarPicker size={26} value={stars} disabled={busy || pendingSubmission} onSelect={(n) => { setStars(n); setError(null); }} />
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
        disabled={busy || pendingSubmission || (cfg !== null && !cfg.comments_enabled)}
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
        disabled={busy || (!pendingSubmission && (ratingStars(stars) === null || !!hint || unchanged || limitReached)) || !cfg?.enabled}
        onClick={submit}
      >
        <span className="btn-label">
          {phase === 'signing' ? t('rating.signing')
            : pendingSubmission ? t('rating.checkStatus')
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
    if (err.code === 'pending') return t('rating.pendingTimeout');
    if (err.code === 'unavailable') return t('rating.updateNimiqPay');
    if (err.code === 'wrongWallet') return t('rating.wrongWallet');
    return t('rating.failed');
  }
  if (err instanceof ApiError) {
    const msg = err.message || '';
    if (err.code === 'INVALID_RATING_STARS' || /(?:stars?|rating) must be.*(?:integer|1.*5)/i.test(msg)) return t('rating.pickStars');
    if (err.status === 401 || /sign in/i.test(msg)) return t('rating.notSignedIn');
    if (/finish the pending rating for your other purchase/i.test(msg)) return t('rating.pendingOtherPurchase');
    if (/already saved/i.test(msg)) return t('rating.alreadySaved');
    if (/maximum number/i.test(msg)) return t('rating.limit');
    if (/switched off/i.test(msg)) return t('rating.commentsOff');
    if (err.status === 503) return t('rating.unavailable');
    if (err.status === 400 || err.status === 409) return msg;
  }
  return t('rating.failed');
}

/**
 * Public list of the newest buyer comments, on the Activity page.
 *
 * A comment is a person's words, so it is shown the way the feed shows a
 * purchase: the buyer's identicon and wallet beside their stars, when they
 * said it, what they bought, and the comment under it. The wallet is only
 * ever the one the feed already publishes for that purchase — an anonymous
 * buyer stays a "Private buyer" chip, and a comment the shop could not match
 * to a buyer (written before the face was captured) simply has no face.
 *
 * Renders nothing when there are no comments.
 */
/**
 * One buyer rating: the buyer's face, the stars, when it was written, the
 * words and the on-chain rating itself. Shared by the Activity comment list
 * and the track page, so the same rating looks the same in both places.
 */
export function RatingCommentCard({ c, plain = false, showProduct = true }: { c: RatingCommentItem; plain?: boolean; showProduct?: boolean }) {
  const words = String(c.comment || '').trim();
  return (
    <div className={plain ? 'rating-comment rating-comment-plain' : 'rating-comment'}>
      <div className="rating-comment-head">
        {/* Not a copy button and not a tooltip carrying the whole wallet:
            the feed row beside the purchase is where an address is meant
            to be read and copied. */}
        {c.address ? (
          <span className="feed-wallet chip">
            <Identicon address={c.address} className="feed-id" size={22} />
            <span className="mono">{shortAddr(c.address)}</span>
          </span>
        ) : null}
        <StarsDisplay rating={c.stars} size={13} />
        <ClockTime ts={c.at} className="xs faint" />
        {c.test_mode ? <span className="chip xs">TEST</span> : null}
        <OnChainRating tx={c.tx} />
      </div>
      {words ? <p className="rating-comment-body">{words}</p> : null}
      {showProduct && c.product ? <div className="xs faint">{c.product}</div> : null}
    </div>
  );
}

export function RecentRatingComments({ seed }: { seed?: RatingCommentItem[] } = {}) {
  const { t } = useT();
  // `seed` is the seam for the preview and the tests: the page itself always
  // lets the component load the list.
  const [items, setItems] = useState<RatingCommentItem[]>(seed || []);
  useEffect(() => {
    if (seed) return;
    let alive = true;
    getRatingComments()
      .then((r: any) => alive && setItems(Array.isArray(r?.items) ? r.items : []))
      .catch(() => alive && setItems([]));
    return () => {
      alive = false;
    };
  }, [seed]);
  if (!items.length) return null;
  return (
    // rs-comments spans the whole card: the summary above it is a two-column
    // grid, and a comment list squeezed into one of those columns is what
    // made the block look broken on the Activity page.
    <div className="rs-comments mt-2">
      <div className="small strong mb-1">{t('rating.recentTitle')}</div>
      <ul className="rating-comments">
        {items.slice(0, 8).map((c, i) => (
          <li key={`${c.at}-${i}`}>
            <RatingCommentCard c={c} />
          </li>
        ))}
      </ul>
    </div>
  );
}
