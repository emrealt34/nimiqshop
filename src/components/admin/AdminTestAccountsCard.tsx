import { LoadingRows } from '../ui/uiKit';
import { useEffect, useState } from 'react';
import { Icon } from '../ui/Icon';
import { useToast } from '../AppProviders';
import { adminAddTestAccount, adminRemoveTestAccount, adminTestAccounts } from '../../lib/api';

type TestAccount = { address: string; label: string; added_at: string };

const fmtAddress = (a: string) => a.replace(/(.{4})/g, '$1 ').trim();

function describeError(e: unknown): string {
  return (e as any)?.message || 'Request failed';
}

/**
 * Test accounts: wallets the operator lets test the checkout end to end.
 *
 * A listed wallet buys like anyone else — real supplier order, real payable
 * invoice — and additionally gets a "simulate payment" button on its own
 * orders: pressing it settles the order through the real state machine with no
 * money moving, which is the one simulated step. Its orders stay out of the
 * public feed and out of the daily budget, and its cashback is paid with a
 * simulated hash. Everyone else is unaffected.
 */
export function AdminTestAccountsCard() {
  const { toast } = useToast();
  const [accounts, setAccounts] = useState<TestAccount[] | null>(null);
  const [address, setAddress] = useState('');
  const [label, setLabel] = useState('');
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState('');

  const load = async () => {
    try {
      const out = await adminTestAccounts();
      setAccounts(Array.isArray(out.accounts) ? out.accounts : []);
    } catch (e) {
      setErr(describeError(e));
    }
  };

  useEffect(() => {
    void load();
  }, []);

  const add = async () => {
    const compact = address.replace(/\s+/g, '').toUpperCase();
    if (!compact) return;
    setBusy(true);
    setErr('');
    try {
      const out = await adminAddTestAccount(compact, label.trim());
      setAccounts(Array.isArray(out.accounts) ? out.accounts : []);
      setAddress('');
      setLabel('');
      toast('Test account added.', 'success');
    } catch (e) {
      setErr(describeError(e));
    } finally {
      setBusy(false);
    }
  };

  const remove = async (a: TestAccount) => {
    if (!window.confirm(`Remove test account ${fmtAddress(a.address)}?`)) return;
    setBusy(true);
    setErr('');
    try {
      const out = await adminRemoveTestAccount(a.address);
      setAccounts(Array.isArray(out.accounts) ? out.accounts : []);
      toast('Test account removed.', 'success');
    } catch (e) {
      setErr(describeError(e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="card mt-2">
      <div className="card-title">
        <Icon name="user" size={14} />
        <span>Test accounts</span>
      </div>
      <div className="small muted mb-2">
        Listed wallets buy for real — a real supplier order with a real, payable invoice — and get a
        simulate-payment button on their own orders for phone testing. Pressing it is the only simulated step:
        the order settles with no money moving, so the code it delivers is a placeholder. These orders stay out
        of the public feed and the daily budget. Everyone else is unaffected.
      </div>

      {accounts === null && !err ? <LoadingRows /> : null}
      {accounts && accounts.length === 0 ? <div className="small muted">No test accounts yet.</div> : null}
      {accounts && accounts.length > 0 ? (
        <table className="table small">
          <thead>
            <tr>
              <th>Wallet</th>
              <th>Label</th>
              <th>Added</th>
              <th />
            </tr>
          </thead>
          <tbody>
            {accounts.map((a) => (
              <tr key={a.address}>
                <td className="mono">{fmtAddress(a.address)}</td>
                <td>{a.label || '—'}</td>
                <td>{a.added_at ? new Date(a.added_at).toLocaleDateString() : '—'}</td>
                <td>
                  <button type="button" className="btn" disabled={busy} onClick={() => { void remove(a); }}>
                    Remove
                  </button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      ) : null}

      <div className="row mt-2" style={{ gap: 8, flexWrap: 'wrap' }}>
        <div className="field" style={{ flex: '2 1 260px' }}>
          <label className="small muted">Nimiq address</label>
          <input
            className="input"
            type="text"
            placeholder="NQ00 0000 0000 0000 0000 0000 0000 0000 0000"
            value={address}
            onChange={(e) => setAddress(e.target.value)}
          />
        </div>
        <div className="field" style={{ flex: '1 1 160px' }}>
          <label className="small muted">Label (optional)</label>
          <input className="input" type="text" maxLength={60} value={label} onChange={(e) => setLabel(e.target.value)} />
        </div>
        <div className="field" style={{ alignSelf: 'flex-end' }}>
          <button type="button" className="btn" disabled={busy || !address.trim()} onClick={() => { void add(); }}>
            Add test account
          </button>
        </div>
      </div>
      {err ? <div className="small mt-1" style={{ color: 'var(--stamp)' }}>{err}</div> : null}
    </div>
  );
}
