import { useEffect, useState } from 'react';
import { friendlyApiMessage, getQuote, userTestPay } from '../../lib/api';
import { useT } from '../../i18n';

/**
 * TestPayButton — the simulated "pay" button for a listed test wallet.
 *
 * The server decides whether it applies: GET /api/quotes/{id} returns
 * `test_pay: true` only for a simulated order owned by a wallet on the admin's
 * test-account list. For everyone else the component renders nothing, so the
 * normal checkout is unchanged. Clicking it settles the simulated order (no
 * supplier order, no real payment); the surrounding payment poll picks up the
 * result.
 */
export function TestPayButton({ quoteId, onPaid }: { quoteId: string; onPaid?: () => void }) {
  const { t } = useT();
  const [allowed, setAllowed] = useState(false);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState('');

  useEffect(() => {
    let live = true;
    setAllowed(false);
    if (!quoteId) return undefined;
    getQuote(quoteId)
      .then((out) => {
        if (live && out?.test_pay === true) setAllowed(true);
      })
      .catch(() => {});
    return () => {
      live = false;
    };
  }, [quoteId]);

  if (!allowed) return null;

  const run = async () => {
    setBusy(true);
    setErr('');
    try {
      await userTestPay(quoteId);
      onPaid?.();
    } catch (e) {
      setErr(friendlyApiMessage(e, t('checkout.testPayFailed')));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="mt-1" style={{ border: '1px dashed var(--stamp)', borderRadius: 8, padding: 8 }}>
      <button type="button" className="btn btn-block" disabled={busy} onClick={() => { void run(); }}>
        <span className="btn-label">{busy ? t('checkout.testPaySimulating') : t('checkout.testPaySimulate')}</span>
      </button>
      <div className="xs faint mt-1">{t('checkout.testPayHint')}</div>
      {err ? <div className="small mt-1" style={{ color: 'var(--stamp)' }}>{err}</div> : null}
    </div>
  );
}
