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
import { useWalletBalance } from '../wallet/WalletBalance';

/** NIM with enough precision to be recognisable (see WalletBalance.nimText). */
function nim(n: number): string {
  const abs = Math.abs(n);
  const decimals = abs >= 1000 ? 2 : abs >= 10 ? 2 : abs >= 1 ? 3 : 5;
  return abs.toLocaleString('en-US', { maximumFractionDigits: decimals });
}

export type NimiqPayPayLabels = {
  idle: string;
  paying: string;
  submitted: string;
  duplicate: string;
  unknown: string;
  declined: string;
  network: string;
  noProvider: string;
  invalid: string;
  unavailable: string;
  error: string;
  waiting: string;
  /** The wallet refused the spend and named the balance as the reason. */
  insufficient: string;
  /** "You need about X NIM — your wallet has Y NIM." */
  needHave: string;
  /** "Nimiq Pay said: …" — the wallet's own words, never paraphrased. */
  said: string;
};

// English fallback — used when a locale has not (yet) defined the key.
const DEFAULT_LABELS: NimiqPayPayLabels = {
  idle: 'Pay with Nimiq Pay',
  paying: 'Opening Nimiq Pay…',
  submitted: 'Payment submitted — waiting for settlement.',
  duplicate: 'This invoice was already submitted. Check your payment status before trying again.',
  unknown: 'We could not confirm the result. Do not pay again — check your payment status first.',
  declined: 'Payment was not approved. You can try again when ready.',
  network: 'Nimiq Pay reported a network problem and nothing was charged. Check your connection and try again.',
  noProvider: 'Could not reach Nimiq Pay. Close and reopen the app, then try again — nothing was charged.',
  invalid: 'This payment request is invalid or expired. Refresh the order and try again.',
  unavailable: 'Open this shop inside Nimiq Pay to pay with NIM or USDT.',
  error: 'The payment could not be started.',
  waiting: 'Keep this page open — your order updates automatically once the merchant is paid.',
  insufficient: 'The wallet refused this payment: not enough NIM for the amount plus the network fee.',
  needHave: 'You need about {{need}} NIM — your wallet has {{have}} NIM.',
  said: 'Nimiq Pay said: {{message}}',
};

type Tone = 'success' | 'warn' | 'error' | 'info';
function toneFor(o: PayLightningOutcome): Tone {
  switch (o.status) {
    case 'submitted': return 'success';
    case 'duplicate':
    case 'unknown': return 'warn';
    case 'network':
    case 'noProvider': return 'warn';
    case 'insufficient': return 'error';
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
  amountNim = 0,
  className = 'btn btn-gold btn-block',
}: {
  invoice: string;
  /** Called once the spend was submitted (submitted / duplicate / unknown). */
  onSubmitted?: (outcome: PayLightningOutcome) => void;
  labels?: Partial<NimiqPayPayLabels>;
  /** The NIM this payment costs (amount + the host's fee is added on top). */
  amountNim?: number;
  className?: string;
}) {
  const { t } = useT();
  // The balance is already on screen; a refusal can therefore say HOW short the
  // wallet is instead of only that something failed. Shared reading: no extra
  // network call.
  const { state: wallet, refresh: refreshWallet } = useWalletBalance();
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
    } else {
      // A refusal: hand the exact failure to whatever prompt comes next
      // (type · code · the wallet's own message), so the red screen that
      // follows names the real reason instead of "something went wrong".
      const w = window as any;
      const facts = 'wallet' in res && res.wallet ? res.wallet : undefined;
      w.__lastPayReason = res.status;
      w.__lastPayDetail = [facts?.label, facts?.message].filter(Boolean).join(' · ') || res.message || '';
    }
    // Any outcome may have moved money (or been refused for want of it), so the
    // balance shown above the button is re-read instead of waiting out its TTL.
    refreshWallet();
  }, [busy, locked, invoice, onSubmitted, refreshWallet]);

  const message = outcome ? label(outcome.status as keyof NimiqPayPayLabels) : '';
  const tone = outcome ? toneFor(outcome) : 'info';
  const hash = outcome && 'hash' in outcome ? outcome.hash : undefined;
  const swapId = outcome && 'swapId' in outcome ? outcome.swapId : undefined;

  // The wallet's own account of the failure, shown verbatim. Owner
  // (2026-10-05): "o kırmızı yerde tam hataları söyleyebilirdi" — our sentence
  // explains, the wallet's sentence is the evidence, and support can trace it.
  const walletErr = outcome && 'wallet' in outcome ? outcome.wallet : undefined;
  const shortfall = (() => {
    if (outcome?.status !== 'insufficient' || !(amountNim > 0)) return 0;
    if (wallet.status !== 'ready') return 0;
    return Math.max(0, amountNim - wallet.availableNim);
  })();
  const needHave = outcome?.status === 'insufficient' && amountNim > 0 && wallet.status === 'ready';

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
          {busy ? label('paying') : locked ? label('submitted') : label('idle')}
        </span>
      </button>

      {message ? (
        <p className="xs mt-1" role="status" aria-live="polite" style={{ color: TONE_COLOR[tone], margin: '6px 2px 0' }}>
          {message}
        </p>
      ) : null}

      {needHave ? (
        <p className="xs" style={{ color: TONE_COLOR.error, margin: '4px 2px 0', fontWeight: 700 }}>
          {t('orderPage.nimiqPay.needHave', { need: nim(amountNim), have: nim(wallet.availableNim) })}
          {shortfall > 0 ? ' ' + t('wallet.short', { nim: nim(shortfall) }) : ''}
        </p>
      ) : null}

      {walletErr?.message ? (
        <p className="xs faint" style={{ margin: '4px 2px 0', wordBreak: 'break-word' }}>
          {t('orderPage.nimiqPay.said', { message: walletErr.message })}
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
