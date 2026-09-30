import { useEffect, useMemo, useState } from 'react';
import { Icon } from '../ui/Icon';
import { cashbackPercentLabel } from '../../lib/cashback';
import { useCart, rowUSD } from '../../lib/cartStore';
import { useToast } from '../AppProviders';
import { useT, rich } from '../../i18n';
import {
  applyCashbackCode,
  CASHBACK_CODE_EVENT,
  clearAppliedCashbackCode,
  normalizeCashbackCode,
  readAppliedCashbackCode,
  type AppliedCashbackCode,
} from '../../lib/cashbackCode';

/**
 * CashbackCodeField — the promo-code card, collapsed by default so it never
 * eats checkout/cart space. Tap the card to open the code entry; once a code
 * is applied the card folds itself back to a one-line "code applied" summary.
 *
 * before the buyer opens anything — the user-visible list only appears inside
 * the opened card.
 */
export function CashbackCodeField({ compact = false }: { compact?: boolean }) {
  const { toast } = useToast();
  const { t } = useT();
  const cart = useCart();
  const orderUSD = cart.items.reduce((sum, item) => sum + Math.max(0, rowUSD(item)), 0);
  const [open, setOpen] = useState(false);
  const [input, setInput] = useState('');
  const [applied, setApplied] = useState<AppliedCashbackCode | null>(() => readAppliedCashbackCode());
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');

  useEffect(() => {
    const sync = () => {
      const next = readAppliedCashbackCode();
      setApplied(next);
      setInput(next?.code || '');
    };
    sync();
    window.addEventListener(CASHBACK_CODE_EVENT, sync);
    return () => window.removeEventListener(CASHBACK_CODE_EVENT, sync);
  }, []);

  const normalizedInput = normalizeCashbackCode(input);
  const pct = cashbackPercentLabel(Number(applied && applied.cashback_bps));
  const sameAsApplied = !!applied && !!normalizedInput && normalizedInput === applied.code;

  async function submit() {
    setError('');
    const code = normalizeCashbackCode(input);
    if (!code) {
      setError(t('cashback.codeEnterErr'));
      return;
    }
    setBusy(true);
    try {
      const next = await applyCashbackCode(code, orderUSD);
      setApplied(next);
      toast(t('cashback.codeAppliedToast', { code: next.code, pct: Number(next.cashback_percent).toFixed(2) }), 'success');
      setInput(next.code);
      setOpen(false); // fold the card back to its compact "code applied" state
    } catch (e) {
      const message = (e as Error).message || t('cashback.codeNotAccepted');
      setError(message);
      toast(message, 'error');
      setOpen(true);
    } finally {
      setBusy(false);
    }
  }

  function removeCode() {
    clearAppliedCashbackCode();
    setApplied(null);
    setInput('');
    setError('');
  }

  const actionLabel = useMemo(() => {
    if (busy) return t('cashback.codeApplying');
    if (sameAsApplied) return t('cashback.codeAppliedOk');
    return t('cashback.codeApply');
  }, [busy, sameAsApplied, t]);

  const wrapStyle: React.CSSProperties = compact
    ? { marginTop: 12, padding: '10px 12px' }
    : { marginTop: 14, padding: '12px 14px' };

  return (
    <div className="card" style={wrapStyle}>
      <button
        type="button"
        className="promo-trigger"
        aria-expanded={open}
        aria-controls="cashback-code-body"
        onClick={() => {
          setOpen((v) => !v);
          setError('');
        }}
      >
        <span className="promo-ico" aria-hidden="true">
          <Icon name="gift" size={16} />
        </span>
        <span className="promo-main">
          <span className="promo-title">{t('cashback.codeTitle')}</span>
          {applied ? (
            <span className="promo-summary">
              <Icon name="check" size={12} />
              <span>{t('cashback.codeUsingNow', { pct })}</span>
            </span>
          ) : (
            <span className="promo-summary promo-hint">{t('cashback.codeHaveOne')}</span>
          )}
        </span>
        {applied ? (
          <span className="chip chip-on promo-chip promo-chip-code" title={applied.code}>
            {applied.code}
          </span>
        ) : (
          <span className="promo-plus" aria-hidden="true">
            <Icon name="plus" size={14} />
          </span>
        )}
        <span className="promo-caret" aria-hidden="true">
          <Icon name="chevron-down" size={16} />
        </span>
      </button>

      {/* The body is always mounted; shown/hidden with a smooth grid-row
          transition so opening AND folding both animate. Hidden content is
          logic below keeps running while the card is folded. */}
      <div id="cashback-code-body" className="promo-body" data-open={open ? 'true' : 'false'} aria-hidden={!open}>
        <div className="promo-inner">
          <p className="xs faint" style={{ margin: '0 0 8px' }}>
            {applied ? t('cashback.codeChangeRemove') : t('cashback.codeReplaces')}
          </p>

          <div style={{ display: 'grid', gridTemplateColumns: 'minmax(0, 1fr) auto', gap: 8 }}>
            <input
              className="input"
              type="text"
              autoCapitalize="characters"
              autoCorrect="off"
              spellCheck={false}
              placeholder={t('cashback.codePlaceholder')}
              aria-label={t('cashback.codeAria')}
              value={input}
              onChange={(e) => {
                setInput(e.target.value.toUpperCase());
                setError('');
              }}
              onKeyDown={(e) => {
                if (e.key === 'Enter') {
                  e.preventDefault();
                  if (!busy && !sameAsApplied) submit();
                }
              }}
            />
            <button className="btn btn-outline" type="button" disabled={busy || sameAsApplied} onClick={submit}>
              {actionLabel}
            </button>
          </div>

          {error ? (
            <div className="alert error mt-1" style={{ marginBottom: 0, display: 'flex', gap: 8, alignItems: 'center' }}>
              <Icon name="alert" size={16} />
              <div className="small">{error}</div>
            </div>
          ) : null}

          {applied ? (
            <div className="small" style={{ display: 'flex', gap: 8, alignItems: 'center', marginTop: 8 }}>
              <Icon name="check" size={16} />
              <span>{rich(t('cashback.codeGives', { code: applied.code, pct }))}</span>
            </div>
          ) : null}

          {applied ? (
            <button
              className="btn btn-ghost btn-sm"
              style={{ marginTop: 8 }}
              type="button"
              onClick={() => {
                removeCode();
                setOpen(true); // keep it open so a replacement code can be typed
              }}
            >
              {t('cashback.codeRemove')}
            </button>
          ) : null}

        </div>
      </div>
    </div>
  );
}
