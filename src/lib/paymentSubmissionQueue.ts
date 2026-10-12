export type PaymentSubmission = { owner: string; id: string; hash: string; swapId?: string; recordedAt: number };
const PREFIX = 'nimshop.payment-submission.v1:';
const memory = new Map<string, PaymentSubmission>();
const keyFor = (owner: string, id: string) => PREFIX + encodeURIComponent(owner) + ':' + encodeURIComponent(id);

export function pendingSubmissions(owner: string): PaymentSubmission[] {
  const entries = new Map<string, PaymentSubmission>();
  for (const [key, value] of memory) if (value.owner === owner) entries.set(key, value);
  try {
    for (let index = 0; index < localStorage.length; index++) {
      const key = localStorage.key(index);
      if (!key?.startsWith(PREFIX + encodeURIComponent(owner) + ':')) continue;
      const value = JSON.parse(localStorage.getItem(key) || 'null');
      if (value?.owner === owner && typeof value.id === 'string' && key === keyFor(owner, value.id)
        && typeof value.hash === 'string' && (!value.hash || /^(0x)?[a-f0-9]{64}$/i.test(value.hash))
        && (!value.swapId || (typeof value.swapId === 'string' && value.swapId.length <= 256))) entries.set(key, value);
    }
  } catch { /* Memory still preserves this page's unsaved identifiers. */ }
  return [...entries.values()];
}

export function rememberSubmission(value: PaymentSubmission): void {
  if (!value.owner || !value.id || (!value.hash && !value.swapId)) return;
  const key = keyFor(value.owner, value.id);
  const previous = pendingSubmissions(value.owner).find(entry => entry.id === value.id);
  if (previous && ((previous.hash && value.hash && previous.hash !== value.hash) || (previous.swapId && value.swapId && previous.swapId !== value.swapId))) throw new Error('Payment identifiers conflict');
  const next = { ...value, hash: value.hash || previous?.hash || '', swapId: value.swapId || previous?.swapId, recordedAt: previous?.recordedAt || value.recordedAt };
  memory.set(key, next);
  try { localStorage.setItem(key, JSON.stringify(next)); } catch { /* Still attempt the server write when browser storage is unavailable. */ }
}

export function forgetSubmission(saved: PaymentSubmission): void {
  const current = pendingSubmissions(saved.owner).find(entry => entry.id === saved.id);
  // Another tab may have enriched the same entry while the request was in flight.
  if (!current || current.hash !== saved.hash || current.swapId !== saved.swapId) return;
  const key = keyFor(saved.owner, saved.id);
  memory.delete(key);
  try { localStorage.removeItem(key); } catch { /* Retrying an acknowledged metadata write is idempotent. */ }
}
