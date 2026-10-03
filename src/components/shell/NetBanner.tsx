import { useEffect, useState } from 'react';
import { Icon } from '../ui/Icon';
import { useT } from '../../i18n';

/** Offline / restored-connection strip — failures explained, never a freeze. */
export function NetBanner() {
  const { t } = useT();
  // Starts "online" even on a device that is offline: the server render (and
  // therefore the HTML React hydrates) always assumes online, so reading
  // navigator.onLine during the FIRST render would make the banner appear
  // against server markup that does not contain it — a hydration mismatch.
  // The effect below adopts the real value immediately after hydration and the
  // online/offline listeners keep it current from then on.
  const [online, setOnline] = useState(true);
  const [flash, setFlash] = useState(false);

  useEffect(() => {
    setOnline(navigator.onLine);
    const on = () => {
      setOnline(true);
      setFlash(true);
      // DS172411 (setTimeout): closure only, never a string — no untrusted data is evaluated.
      setTimeout(() => setFlash(false), 2200);
    };
    const off = () => setOnline(false);
    window.addEventListener('online', on);
    window.addEventListener('offline', off);
    return () => {
      window.removeEventListener('online', on);
      window.removeEventListener('offline', off);
    };
  }, []);

  if (online && !flash) return null;
  return (
    <div
      role="status"
      className="net-banner"
      style={{
        position: 'sticky',
        top: 0,
        zIndex: 80,
        padding: '8px 14px',
        textAlign: 'center',
        fontSize: '0.85rem',
        fontWeight: 700,
        // ROOT FIX 2026-09-17: these were hardcoded pastels, so the strip stayed
        // light-green/pink in the dark theme (--wash-net-* invert with the theme).
        background: online ? 'var(--wash-net-ok)' : 'var(--wash-net-bad)',
        color: online ? 'var(--ink-on-green)' : 'var(--stamp-ink)',
        borderBottom: '2px solid currentColor',
      }}
    >
      <span style={{ display: 'inline-flex', alignItems: 'center', gap: 6 }}>
        <Icon name={online ? 'check' : 'alert'} size={14} />
        {online ? t('netBanner.online') : t('netBanner.offline')}
      </span>
    </div>
  );
}
