/**
 * FlagMark.tsx — the shop's country flag, rendered inside the Nimiq hexagon.
 *
 * This used to be a plain 4:3 <img> with rounded corners from the flag CDN.
 * Every call site (home country chips, product header, orders, activity,
 * tracking) now gets the hexagon silhouette instead, so the row of countries
 * matches the identicons and hub art around it. The implementation lives in
 * FlagHex.tsx; this wrapper keeps the name and props the call sites already
 * use.
 *
 * `size` is the rendered HEIGHT in px, as before. The hexagon is 20:18, so the
 * width is ~1.11× the height — narrower than the old 1.5× rectangle, which is
 * what stops a long row of country chips from running out of room.
 */
import { FlagHex } from './FlagHex';

export function FlagMark({ country, size = 16 }: { country?: string | null; size?: number }) {
  return <FlagHex country={country} size={size} />;
}

export default FlagMark;
