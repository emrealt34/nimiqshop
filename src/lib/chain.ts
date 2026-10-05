/**
 * chain.ts — public, read-only links into the Nimiq chain.
 *
 * The shop anchors every star rating on-chain (1 Luna + a memo naming the stars
 * and the order), so the UI must be able to point at that transaction. The
 * explorer is nimiq.watch: its URL is the bare 64-hex hash behind `#`.
 * Anything that is not a hash or an NQ address returns null, so a truncated or
 * hand-typed value can never become a link (and never a `javascript:` URL).
 */

/** tx hash (with or without 0x) or an NQ address → explorer URL, else null. */
export function explorerUrl(hashOrAddress: string | null | undefined, network = 'main'): string | null {
  const v = String(hashOrAddress || '').trim();
  if (!v) return null;
  const host = network === 'test' ? 'https://test.nimiq.watch/#' : 'https://nimiq.watch/#';
  if (/^(0x)?[0-9a-f]{64}$/i.test(v)) return host + v.replace(/^0x/i, '').toLowerCase();
  const a = v.replace(/\s+/g, '').toUpperCase();
  if (/^NQ[0-9]{2}[0-9A-HJ-NP-VXY]{32}$/.test(a)) {
    const groups = a.match(/.{4}/g);
    return groups ? host + groups.join('+') : null;
  }
  return null;
}

/** Short "ab12cd34…" form of a tx hash for a chip label. */
export function shortTx(hash: string): string {
  const h = String(hash || '').replace(/^0x/i, '');
  return h.length > 12 ? `${h.slice(0, 8)}…${h.slice(-4)}` : h;
}
