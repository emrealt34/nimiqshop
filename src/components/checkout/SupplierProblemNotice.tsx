import type { SupplierIssue } from '../../lib/supplierProblems';
import { lightningLimitPeriod } from '../../lib/supplierProblems';
import { useT } from '../../i18n';
import { pagePath } from '../../lib/asset';

/** Supplier codes worth printing verbatim under the sentence: the real
 *  machine reason, so nobody has to guess what the partner answered. The
 *  shop's own placeholders are not supplier reasons. */
const shopOnlyCodes = new Set(['UNKNOWN_SUPPLIER_PROBLEM', 'SUPPLIER_COOLDOWN']);

/** Persistent, accessible list: multiple supplier problems must not collapse
 * into the first error, a temporary toast or a generic price failure. */
export function SupplierProblemNotice({ issues, actions = true }: { issues: SupplierIssue[]; /** Hide the support links when the screen offers better actions (pay the pending order, change the email). */ actions?: boolean }) {
  const { t } = useT();
  if (!issues.length) return null;
  const account = issues.some(p => p.scope === 'account');
  // Owner (2026-10-20): a Lightning-limit refusal opens with the plain
  // guidance — "Cryptorefills did not approve this payment: you may have an
  // open order or be above the limit; check Orders first, or lower the
  // amount" — and the partner's real reason follows, numbers and code.
  const limit = lightningLimitPeriod(issues) !== null;
  const codes = Array.from(new Set(issues.map(p => p.code).filter(c => c && !shopOnlyCodes.has(c))));
  return (
    <div role="alert" aria-label={t('checkout.spnAria')} className="alert error mt-2" style={{ textAlign: 'left', overflowWrap: 'anywhere' }}>
      <div>
        <strong>{t(limit ? 'checkout.spnLimitLead' : 'checkout.spnHeading')}</strong>
        <ul style={{ margin: '8px 0', paddingLeft: 20 }}>
          {issues.map((issue, index) => <li key={`${issue.code}-${index}`} style={{ marginBottom: 8, whiteSpace: 'pre-line' }}>{issue.message}</li>)}
        </ul>
        {codes.length > 0 && (
          <div className="xs faint" data-testid="supplier-reason-code" style={{ fontFamily: 'ui-monospace, SFMono-Regular, Menlo, monospace', marginBottom: 6 }}>
            {t('checkout.spnSupplierCode', { code: codes.join(', ') })}
          </div>
        )}
        {/* Owner (2026-10-04): the two actions rendered as one glued blob —
            they are separate buttons and need air between them. */}
        {actions && (
          <div style={{ display: 'flex', flexWrap: 'wrap', gap: 8, marginTop: 4, justifyContent: 'center' }}>
            <a className="btn btn-outline btn-sm" href={pagePath("/support")}>{t('checkout.spnContactSupport')}</a>
            {account && <a className="btn btn-outline btn-sm" href="https://www.cryptorefills.com/en" target="_blank" rel="noopener noreferrer">{t('checkout.spnOpenCryptorefills')}</a>}
          </div>
        )}
      </div>
    </div>
  );
}
