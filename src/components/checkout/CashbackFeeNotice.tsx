/**
 * Always-visible disclosure beside prices and cashback — never a tooltip or
 * dependent on login, programme availability, cashback rate or destination.
 * "Fee" includes Nimiq Pay's NIM-to-BTC swap cost ADDED ON TOP of our price,
 * not a supplier invoice repricing or a separately inferred catalogue amount.
 */
import { useT } from '../../i18n';
import en from '../../i18n/locales/en';

/**
 * The English source strings stay exported (tests and external assertions read
 * them), but they are read FROM the dictionary instead of being a second copy
 * of the copy: the rendered component translates by key, so the disclosure
 * follows the language switcher while these constants can never drift.
 */
export const CASHBACK_FEE_TITLE = en.cashbackFee.title;
export const CASHBACK_FEE_RULE = en.cashbackFee.rule;
export const CASHBACK_FEE_NIM_EXAMPLE = en.cashbackFee.nimExample;

export function CashbackFeeNotice({ example = false }: { example?: 'nim' | false }) {
  const { t } = useT();
  return (
    <aside className="cashback-fee-notice mt-1" role="note" aria-label={t('cashbackFee.aria')}>
      <strong className="cashback-fee-notice-title">{t('cashbackFee.title')}</strong>
      <p>{t('cashbackFee.rule')}</p>
      {example && (
        <p className="cashback-fee-notice-example">
          {t('cashbackFee.nimExample')}
        </p>
      )}
    </aside>
  );
}
