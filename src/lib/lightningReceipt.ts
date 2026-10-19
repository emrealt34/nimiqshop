/** A payment hash identifies an invoice; only a matching preimage is a receipt. */
export const LIGHTNING_VERIFIER_URL = 'https://dev.blink.sv/decode';

export type LightningReceiptData = {
  quote_id?: string;
  submitted_tx?: string;
  chain_proof?: LightningChainProof;
  payment_hash: string;
  amount_btc?: string;
  confirmed: boolean;
  confirmed_at?: string;
};
export type LightningChainProof = { funding_tx: string; contract: string; settlement_tx?: string; preimage?: string };

export function lightningReceiptOf(value: any, publicView = false): LightningReceiptData | null {
  // A test wallet's order is a REAL order: when the buyer really pays the
  // invoice, the receipt is real and is shown. A simulated test-pay settles
  // the order without any payment, so it carries no hash and yields no
  // receipt — the exclusion is the missing payment, not the test flag.
  if (!value || (publicView && value.anonymous)) return null;
  const receipt = value.lightning_receipt;
  const hash = paymentHash(receipt?.payment_hash || value.lightning_payment_hash);
  const lightning = receipt || (/lightning/i.test(String(value.network || '')) && /btc/i.test(String(value.coin || value.paid || 'BTC')));
  if (!hash || !lightning) return null;
  const timestamp = receipt?.confirmed_at || value.paid_at;
  const confirmedAt = typeof timestamp === 'string' && Number.isFinite(Date.parse(timestamp)) ? timestamp : undefined;
  return {
    quote_id: value.id,
    submitted_tx: fundingHash(receipt?.submitted_tx || value.lightning_funding_hint || receipt?.chain_proof?.funding_tx || value.lightning_chain_proof?.funding_tx),
    chain_proof: receipt?.chain_proof || value.lightning_chain_proof,
    payment_hash: hash,
    amount_btc: receipt?.amount_btc || value.coin_amount || (String(value.paid || '').match(/^([\d.]+)\s+BTC$/i)?.[1]),
    confirmed: receipt?.confirmed === true || value.payment_observed === true || !!confirmedAt || ['fulfilled', 'completed', 'delivered'].includes(value.status),
    confirmed_at: confirmedAt,
  };
}

export function paymentHash(value: unknown): string {
  const hash = typeof value === 'string' ? value.trim().toLowerCase() : '';
  return /^[0-9a-f]{64}$/.test(hash) ? hash : '';
}

export async function verifyLightningPreimage(hash: unknown, preimage: unknown): Promise<boolean> {
  const expected = paymentHash(hash);
  const secret = paymentHash(preimage);
  if (!expected || !secret) return false;
  const bytes = Uint8Array.from(secret.match(/../g)!, byte => parseInt(byte, 16));
  const digest = await crypto.subtle.digest('SHA-256', bytes);
  const actual = Array.from(new Uint8Array(digest), byte => byte.toString(16).padStart(2, '0')).join('');
  return actual === expected;
}

/** SDK submission identifier, distinct from the invoice payment hash. */
export function fundingHash(value: unknown): string {
  const hash = typeof value === 'string' ? value.trim().toLowerCase() : '';
  return /^(0x)?[0-9a-f]{64}$/.test(hash) ? hash : '';
}
