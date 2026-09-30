/**
 * QR.tsx — renders a Lightning invoice as an SVG QR using the vendored
 * qrcode-generator (public/vendor/qrcode.js), loaded lazily. Ported from
 * ui.js qrSvgNode().
 */
import { useEffect, useState } from 'react';
import { ensureLib } from '../../lib/vendorLoad';
import { useT } from '../../i18n';

export function QR({
  text,
  size = 29,
  /** Absolute pixel size. When given it wins over the module-count `size`. */
  px,
  /** Foreground (module) colour — the stablecoin rail brands its QR. */
  fg = '#042133',
  /** Centre mark (the shop's Nimiq hexagon) embedded in the quiet zone.
   *  With a centre mark the error correction rises M → Q so scanners still
   *  read the code through the overlay. */
  center,
}: {
  text: string;
  size?: number;
  px?: number;
  fg?: string;
  center?: string;
}) {
  const { t } = useT();
  const [path, setPath] = useState<string | null>(null);
  const [count, setCount] = useState(0);
  const [failed, setFailed] = useState(false);

  useEffect(() => {
    let alive = true;
    (async () => {
      try {
        await ensureLib('qrcode');
      } catch {
        return;
      }
      const lib = (window as any).qrcode;
      if (!lib || !alive) return;
      try {
        const qr = lib(0, center ? 'Q' : 'M');
        qr.addData(String(text));
        qr.make();
        const c = qr.getModuleCount();
        let d = '';
        for (let r = 0; r < c; r++) {
          for (let col = 0; col < c; col++) {
            if (qr.isDark(r, col)) d += `M${col} ${r}h1v1h-1z`;
          }
        }
        if (!alive) return;
        setPath(d || 'M0 0');
        setCount(c);
      } catch {
        if (alive) setFailed(true);
      }
    })();
    return () => {
      alive = false;
    };
  }, [text, center]);

  if (failed) return <div className="small muted">{t('checkout.qrUnavailable')}</div>;
  if (!path) return <div className="small muted">{t('checkout.qrLoading')}</div>;

  return (
    <svg
      viewBox={`0 0 ${count} ${count}`}
      width={px || size * 6}
      height={px || size * 6}
      shapeRendering="crispEdges"
      aria-hidden="true"
      style={{ display: 'block' }}
    >
      <rect width={count} height={count} fill="#ffffff" />
      <path d={path || 'M0 0'} fill={fg} />
      {center && count > 0 && (
        <>
          {/* white quiet pad behind the mark so modules never touch it */}
          <rect
            x={count / 2 - count * 0.14}
            y={count / 2 - count * 0.14}
            width={count * 0.28}
            height={count * 0.28}
            rx={count * 0.045}
            fill="#ffffff"
          />
          <image
            href={center}
            x={count / 2 - count * 0.11}
            y={count / 2 - count * 0.11}
            width={count * 0.22}
            height={count * 0.22}
            preserveAspectRatio="xMidYMid meet"
          />
        </>
      )}
    </svg>
  );
}
