import type { SupplierIssue } from '../../lib/supplierProblems';
import { useT } from '../../i18n';
import { pagePath } from '../../lib/asset';

/** Persistent, accessible list: multiple supplier problems must not collapse
 * into the first error, a temporary toast or a generic price failure. */
export function SupplierProblemNotice({ issues }: { issues: SupplierIssue[] }) {
  const { t } = useT();
  if (!issues.length) return null;
  const account = issues.some(p => p.scope === 'account');
  return (
    <div role="alert" aria-label={t('checkout.spnAria')} className="alert error mt-2" style={{ textAlign: 'left', overflowWrap: 'anywhere' }}>
      <div>
        <strong>{t('checkout.spnHeading')}</strong>
        <ul style={{ margin: '8px 0', paddingLeft: 20 }}>
          {issues.map((issue, index) => <li key={`${issue.code}-${index}`} style={{ marginBottom: 8 }}>{issue.message}</li>)}
        </ul>
        {/* Owner (2026-10-04): the two actions rendered as one glued blob —
            they are separate buttons and need air between them. */}
        <div style={{ display: 'flex', flexWrap: 'wrap', gap: 8, marginTop: 4 }}>
          <a className="btn btn-outline btn-sm" href={pagePath("/support")}>{t('checkout.spnContactSupport')}</a>
          {account && <a className="btn btn-outline btn-sm" href="https://www.cryptorefills.com/en" target="_blank" rel="noopener noreferrer">{t('checkout.spnOpenCryptorefills')}</a>}
        </div>
      </div>
    </div>
  );
}
