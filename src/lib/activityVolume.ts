/** Public product volume, expressed exclusively in NIM equivalents. */
export function activityVolumeNIM(items: readonly { nim?: unknown; nim_estimate?: unknown; usd?: unknown; test_mode?: boolean }[], usdPerNim: unknown): number | null {
  const positive = (value: unknown) => {
    const n = Number(value);
    return Number.isFinite(n) && n > 0 ? n : 0;
  };
  const rate = positive(usdPerNim);
  let total = 0;
  for (const item of items) {
    if (item.test_mode === true) continue;
    // Preserve the purchase-time equivalent when supplied by the backend.
    const nim = positive(item.nim) || positive(item.nim_estimate);
    const usd = positive(item.usd);
    if (!nim && usd && !rate) return null; // Do not report a partial total.
    total += nim || (usd ? usd / rate : 0);
    if (!Number.isFinite(total)) return null;
  }
  return total;
}
