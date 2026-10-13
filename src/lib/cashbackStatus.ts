/** A submitted hash is not proof of payment. The persisted worker status
 * determines settlement; old labels must never override it. */
export function cashbackPaymentStatus(cb: { status?: string; tx_hash?: string; paid_at?: string | null; test_mode?: boolean }): 'paid' | 'pending' | 'confirming' | 'skipped' | 'simulated' {
  const status = String(cb.status || '').trim().toLowerCase();
  if (cb.test_mode || String(cb.tx_hash || '').startsWith('TEST')) return 'simulated';
  if (status === 'skipped') return 'skipped';
  if (status === 'broadcast' || status === 'confirming') return 'confirming';
  if (status === 'paid' && /^[a-f0-9]{64}$/i.test(cb.tx_hash || '') && cb.paid_at && Number.isFinite(Date.parse(cb.paid_at))) return 'paid';
  return 'pending';
}
