/**
 * NimiqPayPayButton.tsx — one-tap Nimiq Pay payment inside a Mini App.
 *
 * Rendered in the pay-now card ONLY when the shop is running inside Nimiq Pay and
 * the quote carries a Lightning invoice. Tapping it hands the invoice to Nimiq
 * Pay via the Mini App SDK; Nimiq Pay lets the buyer choose NIM or USDT, shows
 * the swap amount + fees, and asks for final approval.
 *
 * A submitted payment does NOT mean the merchant has been paid in BTC yet, so
 * the order stays pending — the order page's poll flips it to "paid" once the
 * backend confirms Lightning settlement, which also removes this whole card.
 *
 * The SDK forbids resubmitting the same invoice after DUPLICATE_PAYMENT or
 * TRANSACTION_OUTCOME_UNKNOWN, so after any terminal outcome the button locks
 * itself and asks the buyer to check status instead of retrying.
 *
 * Copy is fully localised via i18n under the `orderPage.nimiqPay.*` keys, with a
 * built-in English fallback so the button reads correctly even before a locale
 * ships those keys. Callers may still override any label with the `labels` prop.
 */
import { useCallback, useState } from 'react';
import { useT } from '../../i18n';
import {
  payLightningInvoice,
  isTerminalOutcome,
  type PayLightningOutcome,
} from '../../lib/nimiqPay';

export type NimiqPayPayLabels = {
  idle: string;
  paying: string;
  submitted: string;
  duplicate: string;
  unknown: string;
  declined: string;
  invalid: string;
  unavailable: string;
  error: string;
  waiting: string;
};

// English fallback — used when a locale has not (yet) defined the key.
const DEFAULT_LABELS: NimiqPayPayLabels = {
  idle: 'Pay with Nimiq Pay',
  paying: 'Opening Nimiq Pay…',
  submitted: 'Payment submitted — waiting for settlement.',
  duplicate: 'This invoice was already submitted. Check your payment status before trying again.',
  unknown: 'We could not confirm the result. Do not pay again — check your payment status first.',
  declined: 'Payment was not approved. You can try again when ready.',
  invalid: 'This payment request is invalid or expired. Refresh the order and try again.',
  unavailable: 'Open this shop inside Nimiq Pay to pay with NIM or USDT.',
  error: 'Something went wrong starting the payment. Please try again.',
  waiting: 'Keep this page open — your order updates automatically once the merchant is paid.',
};

type Tone = 'success' | 'warn' | 'error' | 'info';
function toneFor(o: PayLightningOutcome): Tone {
  switch (o.status) {
    case 'submitted': return 'success';
    case 'duplicate':
    case 'unknown': return 'warn';
    case 'declined':
    case 'unavailable': return 'info';
    default: return 'error';
  }
}
const TONE_COLOR: Record<Tone, string> = {
  success: 'var(--ok, #2f8f4e)',
  warn: 'var(--stamp, #c7481d)',
  error: 'var(--stamp, #c7481d)',
  info: 'var(--ink, #4E3D28)',
};

export function NimiqPayPayButton({
  invoice,
  onSubmitted,
  labels,
  className = 'btn btn-gold btn-block',
}: {
  invoice: string;
  /** Called once the spend was submitted (submitted / duplicate / unknown). */
  onSubmitted?: (outcome: PayLightningOutcome) => void;
  labels?: Partial<NimiqPayPayLabels>;
  className?: string;
}) {
  const { t } = useT();
  // Label priority: explicit prop → i18n key → built-in English fallback.
  const label = (k: keyof NimiqPayPayLabels): string => {
    if (labels && labels[k] != null) return labels[k] as string;
    const key = `orderPage.nimiqPay.${k}`;
    const v = t(key);
    return v && v !== key ? v : DEFAULT_LABELS[k];
  };

  const [busy, setBusy] = useState(false);
  const [outcome, setOutcome] = useState<PayLightningOutcome | null>(null);

  const locked = isTerminalOutcome(outcome); // never resubmit the same invoice

  const handleClick = useCallback(async () => {
    if (busy || locked || !invoice) return;
    setBusy(true);
    const res = await payLightningInvoice(invoice);
    setOutcome(res);
    setBusy(false);
    if (res.status === 'submitted' || res.status === 'duplicate' || res.status === 'unknown') {
      onSubmitted?.(res);
    }
  }, [busy, locked, invoice, onSubmitted]);

  const message = outcome ? label(outcome.status) : '';
  const tone = outcome ? toneFor(outcome) : 'info';
  const hash = outcome && 'hash' in outcome ? outcome.hash : undefined;
  const swapId = outcome && 'swapId' in outcome ? outcome.swapId : undefined;

  return (
    <div className="nimiq-pay-trigger mt-2">
      <button
        type="button"
        className={className}
        onClick={handleClick}
        disabled={busy || locked || !invoice}
        aria-busy={busy}
      >
        <span className="btn-label" style={{ display: 'inline-flex', alignItems: 'center', gap: '8px' }}>
          <svg width="16" height="16" viewBox="0 0 24 24" aria-hidden="true" style={{ flex: '0 0 auto' }}>
            <path d="M13 2 4 14h6l-1 8 9-12h-6l1-8Z" fill="currentColor" />
          </svg>
          {busy ? label('paying') : locked ? label('submitted') : label('idle')}
        </span>
      </button>

      {message ? (
        <p className="xs mt-1" role="status" aria-live="polite" style={{ color: TONE_COLOR[tone], margin: '6px 2px 0' }}>
          {message}
        </p>
      ) : null}

      {outcome?.status === 'submitted' ? (
        <p className="xs faint mt-1" style={{ margin: '4px 2px 0' }}>{label('waiting')}</p>
      ) : null}

      {/* Identifiers help support trace a duplicate / uncertain payment. */}
      {hash ? (
        <p className="xs faint mono" style={{ margin: '4px 2px 0', wordBreak: 'break-all' }}>
          tx: {hash}{swapId ? ` · swap: ${swapId}` : ''}
        </p>
      ) : null}
    </div>
  );
}
