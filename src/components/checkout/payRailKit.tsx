/**
 * payRailKit.tsx — the shared skeleton of BOTH pay-now rails.
 *
 * Nimiq Pay Lightning and USDT-on-Polygon must feel like ONE product: the
 * pay-now sheet renders the same sections in the same order on both rails —
 * status line, rail pills, explainer, primary action, copy action, QR
 * toggle, QR frame, warning note. Only the WORDS and the QR payload differ;
 * everything else comes from here so the two rails can never drift apart
 * visually again.
 */
import { useCallback, type ReactNode } from 'react';
import { Icon } from '../ui/Icon';
import { Clipboard } from '../../lib/clipboard';
import { useT } from '../../i18n';
import { asset } from '../../lib/asset';
import { useToast } from '../AppProviders';

/** Rail chip (NIM hexagon-blue / USDT teal / network purple…). The mark
 * slot shows a REAL icon when `img` is given — no invented glyphs. */
export function RailPill({ bg, mark, img, children }: { bg: string; mark?: string; img?: string; children: ReactNode }) {
  return (
    <span
      style={{
        display: 'inline-flex',
        alignItems: 'center',
        gap: 7,
        padding: '5px 11px',
        borderRadius: 999,
        background: bg,
        color: '#fff',
        fontWeight: 800,
        fontSize: '0.78rem',
        letterSpacing: '0.01em',
      }}
    >
      <span
        style={{
          display: 'inline-grid',
          placeItems: 'center',
          width: 18,
          height: 18,
          borderRadius: '50%',
          background: '#fff',
          color: bg,
          fontWeight: 900,
          fontSize: '0.68rem',
          overflow: 'hidden',
        }}
      >
        {img ? (
          <img src={img} alt="" width={16} height={16} style={{ width: 16, height: 16, objectFit: 'contain', display: 'inline-block' }} />
        ) : (
          mark
        )}
      </span>
      {children}
    </span>
  );
}

/** Centered pill row — rail + network, identical placement on both rails. */
export function RailPills({ pills }: { pills: Array<{ bg: string; mark?: string; img?: string; label: string }> }) {
  return (
    <div style={{ display: 'flex', gap: 8, alignItems: 'center', justifyContent: 'center', flexWrap: 'wrap', marginTop: 10 }}>
      {pills.map((p) => (
        <RailPill key={p.label} bg={p.bg} mark={p.mark} img={p.img}>
          {p.label}
        </RailPill>
      ))}
    </div>
  );
}

/** Owner (2026-10-05): the Lightning pills sit directly UNDER the
 *  "Lightning wallet waiting…" line on checkout AND order page — one shared
 *  component so the two screens can never place them differently again. */
export function LightningRailPills() {
  const { t } = useT();
  return (
    <RailPills
      pills={[
        { bg: '#0582CA', img: asset('/img/nimiq-hexagon.png?v=40'), label: t('checkout.lpPayWithNimOrUsdt') },
        { bg: '#0E6BA8', img: asset('/img/btc-lightning.png'), label: t('checkout.lpBtcNetwork') },
      ]}
    />
  );
}

/** Bottom warning note — same .note-box on both rails, rail-specific words. */
export function PayNote({ children }: { children: ReactNode }) {
  return (
    <div className="note-box mt-2">
      <Icon name="alert" size={14} />
      <span>{children}</span>
    </div>
  );
}

/** Copy-with-toast helper shared by both pay blocks. Uses the Nimiq Pay
 *  clipboard (Clipboard.copy) — synchronous, boolean, mobile-compatible. */
export function useCopyField() {
  const { t } = useT();
  const { toast } = useToast();
  return useCallback(
    (label: string, text: string) => {
      if (!text) return;
      const ok = Clipboard.copy(text);
      toast(ok ? t('clipboard.copiedLabel', { label }) : t('clipboard.copyFailed'), ok ? 'info' : 'warn');
    },
    [t, toast],
  );
}
