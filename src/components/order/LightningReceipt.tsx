import { useState } from 'react';
import { useSheet } from '../AppProviders';
import { Icon } from '../ui/Icon';
import { CopyButton } from '../ui/uiKit';
import { useT } from '../../i18n';
import { fmtDate } from '../../lib/format';
import { LIGHTNING_VERIFIER_URL, paymentHash, verifyLightningPreimage, type LightningReceiptData } from '../../lib/lightningReceipt';

function ReceiptDetails({ receipt }: { receipt: LightningReceiptData }) {
  const { t } = useT();
  const [preimage, setPreimage] = useState('');
  const [result, setResult] = useState<'valid' | 'invalid' | 'error' | null>(null);
  const [busy, setBusy] = useState(false);
  const hash = paymentHash(receipt.payment_hash);
  return <div className="lightning-receipt-details">
    <div className="small strong"><Icon name="bolt" size={14} /> BTC Lightning</div>
    {receipt.amount_btc ? <div className="mono small">{receipt.amount_btc} BTC</div> : null}
    <div className="small muted">{receipt.confirmed ? t('lightningReceipt.confirmed') : t('lightningReceipt.pending')}
      {receipt.confirmed && receipt.confirmed_at ? ` · ${fmtDate(receipt.confirmed_at)}` : ''}
    </div>
    <div className="xs faint">{t('lightningReceipt.hash')}</div>
    <code className="lightning-receipt-hash">{hash}</code>
    <div className="lightning-receipt-actions">
      <CopyButton getText={hash} label={t('lightningReceipt.copyHash')} />
      <a className="btn btn-outline btn-sm" href={LIGHTNING_VERIFIER_URL} target="_blank" rel="noopener noreferrer" referrerPolicy="no-referrer">
        <Icon name="external" size={14} /> {t('lightningReceipt.external')}
      </a>
    </div>
    <p className="xs muted">{t('lightningReceipt.hint')}</p>
    <form className="lightning-receipt-verify" onSubmit={async event => {
      event.preventDefault();
      setBusy(true);
      try { setResult(await verifyLightningPreimage(hash, preimage) ? 'valid' : 'invalid'); }
      catch { setResult('error'); }
      finally { setBusy(false); }
    }}>
      <label className="field">
        <span>{t('lightningReceipt.preimage')}</span>
        <input className="input mono" value={preimage} onChange={event => { setPreimage(event.target.value); setResult(null); }} autoComplete="off" spellCheck={false} maxLength={64} placeholder={t('lightningReceipt.preimagePlaceholder')} />
      </label>
      <button className="btn btn-outline btn-sm" type="submit" disabled={busy || !paymentHash(preimage)}>{t('lightningReceipt.verify')}</button>
      {result ? <div className={`small ${result === 'valid' ? 'strong' : 'muted'}`} role="status">{t(`lightningReceipt.${result}`)}</div> : null}
    </form>
  </div>;
}

/** Public cards and private summaries share the same receipt and verifier. */
export function LightningReceipt({ receipt, compact = false }: { receipt?: LightningReceiptData | null; compact?: boolean }) {
  const { t } = useT();
  const { openSheet } = useSheet();
  if (!receipt || !paymentHash(receipt.payment_hash)) return null;
  if (!compact) return <div className="lightning-receipt" data-payment-hash={paymentHash(receipt.payment_hash)}>
    <span className="xs strong"><Icon name="bolt" size={12} /> BTC Lightning</span>
    <code className="lightning-receipt-hash">{paymentHash(receipt.payment_hash)}</code>
    <div className="lightning-receipt-actions">
      <button type="button" className="btn btn-outline btn-sm" onClick={() => openSheet({ title: t('lightningReceipt.title'), render: () => <ReceiptDetails receipt={receipt} /> })}>
        <Icon name="bolt" size={14} /> {t('lightningReceipt.details')}
      </button>
      <a className="btn btn-outline btn-sm" href={LIGHTNING_VERIFIER_URL} target="_blank" rel="noopener noreferrer" referrerPolicy="no-referrer">
        <Icon name="external" size={14} /> {t('lightningReceipt.external')}
      </a>
    </div>
  </div>;
  return <button type="button" className="lightning-receipt-chip chip xs" title={paymentHash(receipt.payment_hash)} onClick={() => openSheet({ title: t('lightningReceipt.title'), render: () => <ReceiptDetails receipt={receipt} /> })}>
    <Icon name="bolt" size={12} /> BTC Lightning · {paymentHash(receipt.payment_hash).slice(0, 8)}…
  </button>;
}
