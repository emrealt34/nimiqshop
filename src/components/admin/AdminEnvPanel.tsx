import { useCallback, useEffect, useMemo, useState } from 'react';
import { Icon } from '../ui/Icon';
import { AlertBox, LoadingRows } from '../ui/uiKit';
import { useToast } from '../AppProviders';
import { adminEnv, adminRemoveEnv, adminSetEnv } from '../../lib/api';

/**
 * AdminEnvPanel.tsx — environment variables, managed from the console.
 *
 * Owner (2026-10-19): "env anahtarlarını admin sayfasından ekleyip
 * çıkarabilmeliyim. öyle her seferinde birşey ekliyeceksek kod değiştirelim
 * gerek yok. mesela partner key gibi." — adding a key must not mean a code
 * change, and the partner key is the example that matters, so it is applied
 * to the running supplier client the instant it is saved.
 *
 * Three things the panel never does:
 *
 *   • never prints a secret back — the list carries a masked value, enough to
 *     recognise which key is loaded and never enough to copy it;
 *   • never pretends a value is live when it is not — each row says "applied
 *     now" or "from the next start on";
 *   • never leaves a hole — REMOVE falls back to the host's own value (or to
 *     unset) and says which of the two will happen before it does it.
 */
type EnvEntry = {
  key: string;
  label: string;
  hint: string;
  secret: boolean;
  live: boolean;
  known: boolean;
  source: 'console' | 'host' | 'unset';
  value?: string;
  host_value?: string;
  updated_at?: string;
  updated_by?: string;
};

const SOURCE_LABEL: Record<string, string> = { console: 'Console', host: 'Host', unset: 'Unset' };

/** Keys that hold a credential: never shown whole in the form either. */
function looksSecret(key: string): boolean {
  return /KEY|SECRET|TOKEN|PASSWORD|PASSWD|CREDENTIAL/.test(key);
}

function describeError(e: unknown): string {
  const raw = (e as any)?.message;
  return typeof raw === 'string' && raw.trim() ? raw : 'Request failed';
}

export function AdminEnvPanel() {
  const { toast } = useToast();
  const [entries, setEntries] = useState<EnvEntry[] | null>(null);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState('');
  const [formKey, setFormKey] = useState('');
  const [formValue, setFormValue] = useState('');

  const load = useCallback(async () => {
    try {
      const out = await adminEnv();
      setEntries(Array.isArray(out.keys) ? out.keys : []);
    } catch (e) {
      setErr(describeError(e));
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  const known = useMemo(
    () => (entries || []).filter((entry) => entry.known).map((entry) => entry.key),
    [entries],
  );

  const editing = (entries || []).find((entry) => entry.key === formKey.trim().toUpperCase());
  const secret = editing?.secret ?? looksSecret(formKey);

  const save = async () => {
    const key = formKey.trim().toUpperCase();
    if (!key || busy) return;
    setBusy(true);
    setErr('');
    try {
      await adminSetEnv(key, formValue.trim());
      setFormValue('');
      toast(`${key} saved.${editing?.live ? ' Applied to the running server.' : ' In force from the next start on.'}`, 'success');
      await load();
    } catch (e) {
      setErr(describeError(e));
    } finally {
      setBusy(false);
    }
  };

  const remove = async (entry: EnvEntry) => {
    const back = entry.host_value ? `the host value (${entry.host_value})` : 'unset';
    if (!window.confirm(`Remove the console value of ${entry.key}?\n\nThe server falls back to ${back}.`)) return;
    setBusy(true);
    setErr('');
    try {
      await adminRemoveEnv(entry.key);
      toast(`${entry.key} removed — now using ${back}.`, 'success');
      if (formKey.trim().toUpperCase() === entry.key) setFormValue('');
      await load();
    } catch (e) {
      setErr(describeError(e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="card mt-2">
      <div className="card-title">
        <Icon name="lock" size={14} />
        <span>Environment</span>
      </div>
      <div className="small muted mb-2">
        Add, change and remove environment variables here — no code change, no redeploy. A value saved
        in the console is stored in the shop&apos;s own database, so it survives a redeploy, and it is
        applied on every start before anything reads it. The host always wins: a variable the host
        already sets keeps its value, and removing a console value falls back to it. Secrets are
        write-only — the table shows just enough to tell which one is loaded.
      </div>

      {err ? (
        <div className="mb-2">
          <AlertBox type="error">{err}</AlertBox>
        </div>
      ) : null}

      {entries === null && !err ? <LoadingRows rows={4} /> : null}

      {entries && entries.length > 0 ? (
        <table className="table small">
          <thead>
            <tr>
              <th>Variable</th>
              <th>Value</th>
              <th>Source</th>
              <th>Effect</th>
              <th />
            </tr>
          </thead>
          <tbody>
            {entries.map((entry) => (
              <tr key={entry.key}>
                <td>
                  <div className="strong">{entry.label || entry.key}</div>
                  <div className="mono xs faint">{entry.key}</div>
                  {entry.hint ? <div className="xs faint mt-1">{entry.hint}</div> : null}
                  {!entry.known ? (
                    <div className="xs mt-1" style={{ color: 'var(--warn, #b45309)' }}>
                      Saved from the console — the shop cannot check this name.
                    </div>
                  ) : null}
                </td>
                <td className="mono">
                  {entry.value || '—'}
                  {entry.source === 'console' && entry.host_value ? (
                    <div className="xs faint">host: {entry.host_value}</div>
                  ) : null}
                </td>
                <td>
                  <span
                    className="chip xs"
                    style={
                      entry.source === 'console'
                        ? { borderStyle: 'solid', borderColor: 'var(--line-strong)', color: 'var(--ink)' }
                        : undefined
                    }
                  >
                    {SOURCE_LABEL[entry.source] || entry.source}
                  </span>
                </td>
                <td className="xs muted">
                  {entry.live ? 'Applied the moment it is saved' : 'In force from the next start on'}
                  {entry.updated_at ? (
                    <div className="faint">{new Date(entry.updated_at).toLocaleString()}</div>
                  ) : null}
                </td>
                <td>
                  <div className="row" style={{ gap: 6, justifyContent: 'flex-end', flexWrap: 'wrap' }}>
                    <button
                      type="button"
                      className="btn"
                      disabled={busy}
                      onClick={() => {
                        setFormKey(entry.key);
                        setFormValue('');
                        setErr('');
                      }}
                    >
                      {entry.source === 'console' ? 'Replace' : 'Set'}
                    </button>
                    {entry.source === 'console' ? (
                      <button type="button" className="btn" disabled={busy} onClick={() => { void remove(entry); }}>
                        Remove
                      </button>
                    ) : null}
                  </div>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      ) : null}

      <div className="row mt-2" style={{ gap: 8, flexWrap: 'wrap' }}>
        <div className="field" style={{ flex: '2 1 260px' }}>
          <label className="small muted">Variable</label>
          <input
            className="input"
            type="text"
            list="admin-env-known-keys"
            placeholder="CRYPTOREFILLS_PARTNER_KEY"
            value={formKey}
            onChange={(e) => setFormKey(e.target.value.toUpperCase())}
            spellCheck={false}
            autoComplete="off"
          />
          <datalist id="admin-env-known-keys">
            {known.map((key) => (
              <option key={key} value={key} />
            ))}
          </datalist>
        </div>
        <div className="field" style={{ flex: '2 1 240px' }}>
          <label className="small muted">Value</label>
          <input
            className="input"
            type={secret ? 'password' : 'text'}
            placeholder={editing?.value ? `new value (now ${editing.value})` : 'value'}
            value={formValue}
            onChange={(e) => setFormValue(e.target.value)}
            spellCheck={false}
            autoComplete="off"
          />
        </div>
        <div className="field" style={{ alignSelf: 'flex-end' }}>
          <button type="button" className="btn" disabled={busy || !formKey.trim() || !formValue.trim()} onClick={() => { void save(); }}>
            Save value
          </button>
        </div>
      </div>
      <div className="xs faint mt-1">
        {secret
          ? 'This value is a secret: it is kept in the shop database on this host — never in the repository, never in the audit log (the log names the variable, not the value) and never shown back in full here.'
          : 'Any SCREAMING_SNAKE variable can be typed in — the list above is the curated set, not a cage.'}
      </div>
    </div>
  );
}
