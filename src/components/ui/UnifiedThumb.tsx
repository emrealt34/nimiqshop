/**
 * UnifiedThumb.tsx — Global, canonical product thumbnail.
 * Exactly the same markup + inline styles as the homepage product card,
 * used everywhere (homepage, cart, orders list, order detail, checkout preview, activity).
 * User request: <div class="thumb thumb-gc" style="background: rgb(255, 255, 255);"><img class="product-img" src="..." style="background: rgb(255, 255, 255); object-fit: contain;"></div>
 * This component guarantees pixel-identical rendering on every page.
 *
 * NO fallback image files, ever. When a row has no logo, or the remote logo
 * cannot load (CDN unreachable / hotlink blocked), the tile shows a designed
 * MONOGRAM state — the brand's initial letter in the tile's serif face on the
 * brand background. Pure text + CSS: nothing to 404, never a white void.
 */
import React, { useEffect, useState } from 'react';
import { Icon } from './Icon';
import { brandMetaForTitle } from '../../lib/catalogMeta';
import { asset } from '../../lib/asset';

/** First alphanumeric char of a label, uppercased — the monogram letter. */
export function monogramLetter(label: string): string {
  const m = String(label || '').replace(/[^a-zA-Z0-9çğıöşüÇĞİÖŞÜ]/g, '').charAt(0);
  return m ? m.toLocaleUpperCase('tr-TR') : '•';
}

/** Relative-luminance check so the placeholder icon stays readable on dark
 * brand backgrounds (Koton #181818, IKEA #055ba9) as well as white ones. */
export function isDarkBg(bg: string): boolean {
  const m = String(bg || '').trim();
  let r = 0, g = 0, b = 0;
  const hex = m.match(/^#([0-9a-f]{3}|[0-9a-f]{6})$/i);
  if (hex) {
    const h = hex[1].length === 3 ? hex[1].split('').map((c) => c + c).join('') : hex[1];
    r = parseInt(h.slice(0, 2), 16); g = parseInt(h.slice(2, 4), 16); b = parseInt(h.slice(4, 6), 16);
  } else {
    const rgb = m.match(/rgba?\(\s*([\d.]+)[,\s]+([\d.]+)[,\s]+([\d.]+)/i);
    if (rgb) { r = +rgb[1]; g = +rgb[2]; b = +rgb[3]; } else return false;
  }
  return (0.2126 * r + 0.7152 * g + 0.0722 * b) / 255 < 0.5;
}

/** The ONE global no-logo / logo-failed tile content: the bag icon on the
 * brand background — the same look the homepage tile shows, everywhere. */
export function ThumbPh({ bg }: { bg?: string }) {
  return (
    <span className={`thumb-ph${isDarkBg(bg || '') ? ' on-dark' : ''}`} aria-hidden="true">
      <Icon name="bag" size={24} />
    </span>
  );
}

/** The ONE tile backdrop everywhere: the shop's brand-icon.png artwork behind
 * the product logo, veiled per background luminance so the logo always stays
 * legible (light veil on light brand backgrounds, dark veil on dark ones). */
export function tileBackground(bgColor: string): string {
  const veil = isDarkBg(bgColor) ? 'rgba(24, 24, 24, 0.45)' : 'rgba(255, 255, 255, 0.45)';
  // half the tile width (2× smaller than the old cover fill) so the artwork
  // reads as a centred motif behind the logo, not a wallpaper.
  return `linear-gradient(${veil}, ${veil}), url(${asset('/img/brand-icon.png')}) center / 50% no-repeat ${bgColor}`;
}

/** One product img that degrades to the global placeholder instead of a broken/hidden img. */
export function ThumbImg({ src, alt = '', bg }: { src: string; alt?: string; bg?: string }) {
  const [failed, setFailed] = useState(false);
  useEffect(() => { setFailed(false); }, [src]);
  const bgColor = (bg && String(bg).trim()) ? String(bg).trim() : 'rgb(255, 255, 255)';
  if (!String(src || '').trim() || failed) {
    return <ThumbPh bg={bgColor} />;
  }
  return (
    <img
      className="product-img"
      src={src}
      alt={alt}
      loading="lazy"
      decoding="async"
      referrerPolicy="no-referrer"
      style={{ background: 'transparent', objectFit: 'contain' } as React.CSSProperties}
      onError={() => setFailed(true)}
    />
  );
}

type UnifiedThumbProps = {
  src?: string | null;
  alt?: string;
  bg?: string; // defaults to rgb(255, 255, 255) per request — overrides are ignored to keep 100% identical
};

export function UnifiedThumb({ src, alt = '', bg = 'rgb(255, 255, 255)' }: UnifiedThumbProps) {
  // Per-brand background: use provided bg (from catalog) so dark logos (Koton #181818,
  // IKEA #055ba9, Razer #000) remain visible; default to white (Hepsiburada) when
  // no brand color is known. This keeps the *structure* identical everywhere
  // while the *color* stays truthful per brand.
  const bgColor = (bg && String(bg).trim()) ? String(bg).trim() : 'rgb(255, 255, 255)';
  const logo = (src && String(src).trim()) || '';
  return (
    <div className="thumb thumb-gc" style={{ background: tileBackground(bgColor) }} role="img" aria-label={String(alt || '')}>
      {logo ? <ThumbImg src={logo} alt={alt} bg={bgColor} /> : <ThumbPh bg={bgColor} />}
    </div>
  );
}

/**
 * Stack variant — used for batch orders with 2-3 items.
 * Keeps the same thumb markup per layer, just wrapped in the existing multi-thumb structure
 * but each layer is visually the canonical thumb.
 */
export function UnifiedThumbStack({ logos, alts, bgs }: { logos: (string | null | undefined)[]; alts?: string[]; bgs?: (string | null | undefined)[] }) {
  const items = logos.slice(0, 3);
  if (!items.length) return null;
  if (items.length === 1) return <UnifiedThumb src={items[0]} alt={alts?.[0] || ''} bg={bgs?.[0] || 'rgb(255,255,255)'} />;
  return (
    <div className="thumb thumb-gc thumb-stack-shell" style={{ background: tileBackground('rgb(255, 255, 255)') }}>
      <div className="multi-thumb" data-count={items.length} aria-hidden="true">
        {items.map((logo, idx) => (
          <div key={(logo || '') + idx} className="multi-thumb-card" data-layer={idx + 1} aria-hidden="true">
            <ThumbImg
              src={(logo && String(logo).trim()) || ''}
              alt={alts?.[idx] || ''}
              bg={bgs?.[idx] || 'rgb(255,255,255)'}
            />
          </div>
        ))}
      </div>
    </div>
  );
}

/** ONE global resolver+renderer for title-based rows (activity, cashback,
 * orders, track): resolves the brand meta, then renders the canonical
 * UnifiedThumb. No page ever builds its own tile markup again. */
export function BrandThumb({ title, country = '' }: { title: string; country?: string }) {
  const [meta, setMeta] = useState<{ logo?: string; bg?: string } | null>(null);
  useEffect(() => {
    let alive = true;
    setMeta(null);
    brandMetaForTitle(title, country)
      .then((m) => { if (alive) setMeta(m || null); })
      .catch(() => { if (alive) setMeta(null); });
    return () => { alive = false; };
  }, [title, country]);
  return <UnifiedThumb src={meta?.logo || ''} alt={title} bg={meta?.bg || undefined} />;
}

/** Stack twin of BrandThumb: 2-3 titles -> UnifiedThumbStack, same global markup. */
export function BrandThumbStack({ titles, country = '' }: { titles: string[]; country?: string }) {
  const clean = (titles || []).map((t) => String(t || '').trim()).filter(Boolean).slice(0, 3);
  const key = clean.join('\u0001');
  const [metas, setMetas] = useState<Array<{ logo?: string; bg?: string } | null>>([]);
  useEffect(() => {
    let alive = true;
    setMetas([]);
    Promise.all(
      clean.map((t) => brandMetaForTitle(t, country).catch(() => null))
    ).then((arr) => { if (alive) setMetas(arr); });
    return () => { alive = false; };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [key, country]);
  if (clean.length === 0) return null;
  if (clean.length === 1) return <BrandThumb title={clean[0]} country={country} />;
  return (
    <UnifiedThumbStack
      logos={clean.map((_, i) => metas[i]?.logo || '')}
      alts={clean}
      bgs={clean.map((_, i) => metas[i]?.bg || undefined)}
    />
  );
}
