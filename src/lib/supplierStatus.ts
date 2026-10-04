/**
 * supplierStatus.ts — the supplier's own order states, said in the buyer's
 * language.
 *
 * CryptoRefills answers with machine states (`WaitingForPayment`,
 * `PaymentSetupFailed`, …). Those used to reach the screen verbatim —
 * "Tedarikçi durumu: WaitingForPayment" — which tells a buyer nothing and looks
 * like a bug. The mapping below is the full documented vocabulary
 * (backend/internal/cryptorefills/status.go); anything unrecognised falls back
 * to a plain "in progress" line instead of leaking an enum.
 */
import { t as tr } from '../i18n';

const KEYS: Record<string, string> = {
  created: 'supplierState.created',
  waitingforpayment: 'supplierState.waitingForPayment',
  paymentstarted: 'supplierState.paymentStarted',
  partialpaymentstarted: 'supplierState.partialPaymentStarted',
  paymentreceived: 'supplierState.paymentReceived',
  waitingfordelivery: 'supplierState.waitingForDelivery',
  waitingformanualaction: 'supplierState.waitingForManualAction',
  done: 'supplierState.done',
  expired: 'supplierState.expired',
  paymentfailed: 'supplierState.paymentFailed',
  paymentsetupfailed: 'supplierState.paymentSetupFailed',
  refunded: 'supplierState.refunded',
};

/** Buyer-facing label for a supplier state; never the raw enum. */
export function supplierStatusLabel(status: unknown): string {
  const key = String(status == null ? '' : status).trim().toLowerCase();
  if (!key) return tr('supplierState.unknown');
  const i18nKey = KEYS[key];
  if (i18nKey) return tr(i18nKey);
  return tr('supplierState.unknown');
}
