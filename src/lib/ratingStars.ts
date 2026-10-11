/** Accept a selected whole star value without rounding or choosing for the buyer. */
export function ratingStars(value: unknown): number | null {
  if (typeof value === 'string' && /^[1-5]$/.test(value.trim())) return Number(value.trim());
  return typeof value === 'number' && Number.isInteger(value) && value >= 1 && value <= 5 ? value : null;
}
