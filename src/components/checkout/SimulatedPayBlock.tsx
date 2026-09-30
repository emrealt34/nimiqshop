/**
 * SimulatedPayBlock.tsx — the ONE extra button TEST MODE adds to the real pay
 * screen. There is no separate simulated pay screen anywhere: checkout and the
 * order page render their exact real payment UI (real invoice, real QR, real
 * countdown) and place THIS single button at the payment spot. Pressing it
 * makes the backend walk the order through the real state machine
 * (broadcast → confirm → deliver), so the pay screen's live polling, the
 * delivery code, the gift email and the cashback engine all behave exactly
 * like production. No real money moves.
 */
import { useState } from 'react';
import { Icon } from '../ui/Icon';
import { useToast } from '../AppProviders';
import { payQuoteSimulated } from '../../lib/api';
import { useT } from '../../i18n';

export function SimulatedPayBlock({ quoteId, onPaid }: { quoteId: string; onPaid?: (r: any) => void }) {
  const { t } = useT();
  const { toast } = useToast();
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState('');

  const pay = async () => {
    if (!quoteId || busy) return;
    setErr('');
    setBusy(true);
    try {
      const r = await payQuoteSimulated(quoteId, 'auto');
      toast(t('testPay.sent'), 'success');
      onPaid?.(r);
    } catch (e) {
      setErr((e as Error).message || t('testPay.failed'));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="mt-2">
      <button className="btn btn-gold btn-block btn-lg" disabled={busy} onClick={pay}>
        <Icon name="zap" size={18} />
        <span className="btn-label">{busy ? t('testPay.paying') : t('testPay.pay')}</span>
      </button>
      {err ? (
        <div className="alert error mt-1" style={{ marginBottom: 0 }}>
          <Icon name="alert" size={16} />
          <div className="small">{err}</div>
        </div>
      ) : null}
    </div>
  );
}
