/**
 * ratingComment.ts — the rating comment rules, as the buyer types them.
 *
 * The backend (internal/ratings) is the authority; this file mirrors its rules
 * so the form can explain a refusal before the buyer is asked to sign anything.
 * The comment goes on-chain in the memo, so the rules are strict: letters a-z
 * and A-Z, digits 0-9 and single spaces only. No links, no punctuation, no
 * Turkish letters. The memo is "<stars>" or "<stars> <comment>".
 */

/** Longest comment that still fits the 64-byte memo after "<stars> ". */
export const MAX_COMMENT = 62;

export type CommentProblem = 'chars' | 'long' | null;

/** Trims, collapses runs of spaces to one. Returns the cleaned text. */
export function cleanComment(raw: string): string {
  return String(raw || '').split(/\s+/).filter(Boolean).join(' ');
}

/** Returns the first problem with a comment, or null when it is acceptable. */
export function commentProblem(raw: string): CommentProblem {
  const s = cleanComment(raw);
  if (!s) return null;
  if (!/^[A-Za-z0-9 ]+$/.test(s)) return 'chars';
  if (s.length > MAX_COMMENT) return 'long';
  return null;
}

/** The memo line the wallet will sign, for display only. */
export function memoPreview(stars: number, comment: string): string {
  const c = cleanComment(comment);
  return c ? `${stars} ${c}` : String(stars);
}
