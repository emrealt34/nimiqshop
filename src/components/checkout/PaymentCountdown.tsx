/**
 * PaymentCountdown.tsx — ported from util.payCountdown(). Ticks every second
 * until the invoice expires; at zero it shows the expiry message and calls
 * onExpire once.
 */
import { useEffect, useState } from 'react';
import { Icon } from '../ui/Icon';
import { useT } from '../../i18n';

function fmt(ms: number): string {
  const s = Math.max(0, Math.ceil(ms / 1000));
  const m = Math.floor(s / 60);
  return `${m}m ${String(s % 60).padStart(2, '0')}s`;
}

export function PaymentCountdown({ expiresAt, onExpire }: { expiresAt?: string | null; onExpire?: () => void }) {
  const { t } = useT();
  const [now, setNow] = useState(() => Date.now());
  const [, setExpired] = useState(false);

  useEffect(() => {
    if (!expiresAt) return;
    const exp = new Date(expiresAt).getTime();
    if (!isFinite(exp)) return;
    setExpired(exp - Date.now() <= 0);
    const tick = setInterval(() => {
      const ms = exp - Date.now();
      setNow(Date.now());
      if (ms <= 0) {
        clearInterval(tick);
        setExpired(true);
        if (onExpire) {
          try {
            onExpire();
          } catch {}
        }
      }
    }, 1000);
    return () => clearInterval(tick);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [expiresAt]);

  if (!expiresAt) return null;
  const exp = new Date(expiresAt).getTime();
  if (!isFinite(exp)) return null;
  const ms = exp - now;

  if (ms <= 0) {
    return (
      <div className="strong pay-countdown" style={{ fontSize: '1.05rem', color: 'var(--red, #B4471C)', fontWeight: 800 }}>
        <Icon name="clock" size={20} style={{ verticalAlign: 'middle' }} />{' '}
        <span>{t('checkout.pcClosed')}</span>
      </div>
    );
  }

  return (
    <div className="strong pay-countdown" style={{ fontSize: '1.05rem' }}>
      <Icon name="clock" size={20} style={{ verticalAlign: 'middle' }} />{' '}
      <span>{t('checkout.pcTimeLeft', { time: fmt(ms) })}</span>
    </div>
  );
}
