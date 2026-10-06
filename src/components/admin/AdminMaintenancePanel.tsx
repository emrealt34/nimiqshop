/**
 * AdminMaintenancePanel.tsx — the console's Maintenance section: the one place
 * that can START THE SHOP OVER, plus the live memory story.
 *
 * WHY IT IS A SEPARATE SECTION. This is the only destructive action in the
 * whole console, and the owner's request was "delete the registered users
 * completely, right now, a fresh install". An action like that must not sit
 * next to a "refresh" button on the page an operator opens all day: it gets a
 * tab of its own, a preview that has to load, a typed phrase, and a receipt
 * afterwards. The tab is also the natural home for the memory envelope, since
 * both answers are "what is in this box right now".
 *
 * WHAT THE RESET KEEPS — and says so on screen, before the button: the operator
 * accounts (so pressing it cannot lock anyone out of the console they are
 * standing in), the site settings, and the catalog snapshots. Everything that
 * belongs to a customer goes: accounts and their presence, orders, quotes,
 * support threads, stake watch/ledger rows, cashback rows and codes, the public
 * activity feed and the per-customer checkout locks.
 */
import { useCallback, useEffect, useState } from 'react';
import { Icon } from '../ui/Icon';
import { AlertBox } from '../ui/uiKit';
import { adminDashboard, adminReset, adminResetPreview } from '../../lib/api';

/** Formats the count table the preview and the receipt share. */
function CountTable({ rows }: { rows: Array<{ label: string; count: number }> }) {
  if (!rows.length) return <div className="small muted">Nothing to delete.</div>;
  return (
    <table className="amt-table">
      <tbody>
        {rows.map((r) => (
          <tr key={r.label}>
            <td>{r.label}</td>
            <td className="amt-count">{r.count.toLocaleString()}</td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}

export function MaintenancePanel() {
  const [preview, setPreview] = useState<any | null>(null);
  const [err, setErr] = useState('');
  const [phrase, setPhrase] = useState('');
  const [busy, setBusy] = useState(false);
  const [result, setResult] = useState<any | null>(null);
  const [mem, setMem] = useState<any | null>(null);
  const [proc, setProc] = useState<any | null>(null);

  const load = useCallback(() => {
    adminResetPreview()
      .then((d) => {
        setPreview(d);
        setErr('');
      })
      .catch((e) => setErr((e as Error).message || 'Could not read what a reset would delete'));
    // The numbers the process is really running with: the database reports the
    // caches it opened with, and the host sampler reports this process's heap.
    adminDashboard()
      .then((d) => {
        setMem(d?.memory || null);
        setProc(d?.host?.process || null);
      })
      .catch(() => {});
  }, []);

  useEffect(load, [load]);

  const expected = String(preview?.phrase || 'RESET ALL DATA');
  const armed = phrase.trim().toUpperCase() === expected;

  const doReset = async () => {
    if (!armed) return;
    setBusy(true);
    setErr('');
    setResult(null);
    try {
      const r = await adminReset(expected);
      setResult(r);
      setPhrase('');
      load();
    } catch (e) {
      setErr((e as Error).message || 'The reset failed');
    } finally {
      setBusy(false);
    }
  };

  const namespaces: Array<{ label: string; count: number }> = preview?.namespaces || [];
  const total = Number(preview?.total) || 0;

  return (
    <>
      <div className="card mt-2">
        <div className="card-title">
          <Icon name="pulse" size={18} />
          <span>Memory</span>
          {mem?.budget_mb ? (
            <span className="chip xs" style={{ marginLeft: 'auto' }}>
              {mem.budget_mb} MB budget
            </span>
          ) : null}
        </div>
        {mem ? (
          <div className="xs faint">
            {mem.budget_mb
              ? `This process is allowed about ${mem.budget_mb} MB (${mem.scaled ? 'derived from the ' + (mem.budget_source || 'detected') + ' limit' : 'configured'}). `
              : 'No memory limit was detectable on this host, so the runtime and the database keep their own defaults. '}
            The database opens with a {mem.block_cache_mb ?? '—'} MB block cache, a {mem.index_cache_mb ?? '—'} MB index
            cache and {mem.num_memtables ?? '—'} × {mem.memtable_mb ?? '—'} MB memtables
            {mem.total_cache_mb ? ` (${mem.total_cache_mb} MB total)` : ''}; the Go heap ceiling sits three quarters of the
            way up the budget, held connections take at most a third of it, and idle sockets hold no buffers.
            {proc ? ` Heap now ${Math.round((Number(proc.heap_bytes) || 0) / 1048576)} MB, ${Number(proc.goroutines) || 0} goroutines.` : ''}{' '}
            Override with <code>GO_MEMORY_LIMIT_MB</code>, <code>MAX_CONCURRENT_CONNS</code> or <code>BADGER_*_MB</code>.
          </div>
        ) : (
          <div className="small muted">Memory envelope not reported by this API build.</div>
        )}
      </div>

      <div className="card mt-2 amt-danger">
        <div className="card-title">
          <Icon name="alert" size={18} />
          <span>Start the shop over</span>
        </div>
        <div className="small muted">
          Deletes every CUSTOMER record — accounts and their presence, orders, quotes, support threads, stake watch and
          ledger rows, cashback rows and claim codes, wallet-notification preferences and their send ledger, the public
          activity feed and checkout locks. Kept:{' '}
          <b>operator accounts and sessions</b>, site settings and catalog snapshots, so the login you are using now keeps
          working. This cannot be undone.
        </div>

        <div className="mt-2">
          {preview ? (
            <>
              <div className="xs faint" style={{ marginBottom: 6 }}>
                Right now this would delete {total.toLocaleString()} records:
              </div>
              <CountTable rows={namespaces} />
            </>
          ) : (
            <div className="small muted">Reading the database…</div>
          )}
        </div>

        <div className="field mt-2">
          <label>
            Type <code>{expected}</code> to confirm
          </label>
          <input
            className="input"
            type="text"
            value={phrase}
            placeholder={expected}
            autoComplete="off"
            spellCheck={false}
            onChange={(e) => setPhrase(e.target.value)}
          />
        </div>
        {err ? (
          <div className="alert error mt-1" style={{ marginBottom: 0 }}>
            <Icon name="alert" size={16} />
            <div className="small">{err}</div>
          </div>
        ) : null}
        <button className="btn btn-block btn-lg mt-2 amt-btn" disabled={!armed || busy || total === 0} onClick={doReset}>
          <Icon name="trash" size={16} />
          <span className="btn-label">{busy ? 'Deleting…' : 'Delete all customer data'}</span>
        </button>
        {!armed ? <div className="xs faint mt-1">The button unlocks when the phrase matches exactly.</div> : null}

        {result ? (
          <div className="mt-2">
            <AlertBox type="success">
              Deleted {Number(result.total || 0).toLocaleString()} records in {Number(result.duration_ms || 0)} ms.
            </AlertBox>
            <CountTable rows={result.deleted || []} />
          </div>
        ) : null}
      </div>
    </>
  );
}
