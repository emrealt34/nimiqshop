/**
 * AdminSystemPanel.tsx — the Overview cards that describe the MACHINE and
 * WHERE the customers are.
 *
 * WHY THESE LIVE IN THE CONSOLE. The rest of the operator page answers "what is
 * the shop doing?" (players, orders, cashback). These two answer the questions
 * that come right before it when something feels wrong:
 *
 *   • "Is it us or is it the supplier?" — CPU, memory, disk and network load on
 *     the host running the API, in the same little cards the Players row uses.
 *     Until now that meant opening a hosting dashboard that knows nothing about
 *     the shop.
 *   • "Where are my customers?" — the countries the shop has actually been
 *     visited from, ranked (Türkiye 3, Almanya 6 …), with how many of those
 *     people were here today and how many have bought something.
 *
 * Both come from the admin dashboard payload the Players card already fetches
 * (countries and host are fields of it), so opening the Overview costs no extra
 * round trip on first paint. The host numbers then refresh on their own cadence
 * — they are the only live part, and the endpoint samples /proc behind a short
 * cache, so polling them is cheap.
 *
 * WHAT THE CARDS REFUSE TO DO. No invented numbers: when the host cannot answer
 * (no /proc, a denied read), the card says why instead of drawing zeroes that
 * look like a healthy machine; when a country is not known, the row still shows
 * the code rather than an empty flag.
 */
import { useCallback, useEffect, useState } from 'react';
import { Icon } from '../ui/Icon';
import { AlertBox } from '../ui/uiKit';
import { adminDashboard } from '../../lib/api';
import { countryName, flag } from '../../lib/format';
import { useInterval } from '../../lib/useInterval';

/* ---------------- formatting ---------------- */

/** Bytes as a short human figure. Binary units, because that is what `df` and
 *  the kernel mean, with one decimal only while the number is small. */
export function fmtBytes(bytes?: number | null): string {
  const n = Number(bytes);
  if (!isFinite(n) || n <= 0) return '0 B';
  const units = ['B', 'KB', 'MB', 'GB', 'TB', 'PB'];
  let v = n;
  let u = 0;
  while (v >= 1024 && u < units.length - 1) {
    v /= 1024;
    u++;
  }
  const digits = v < 10 && u > 0 ? 1 : 0;
  return `${v.toFixed(digits)} ${units[u]}`;
}

/** A rate, e.g. "412 KB/s". Below 1 KB/s the honest answer is "idle". */
export function fmtRate(bytesPerSec?: number | null): string {
  const n = Number(bytesPerSec);
  if (!isFinite(n) || n <= 0) return '0 B/s';
  if (n < 1024) return `${n.toFixed(0)} B/s`;
  return `${fmtBytes(n)}/s`;
}

/** Seconds as "3g 4h" / "5h 12m" / "18m". */
export function fmtUptime(seconds?: number | null): string {
  const s = Math.max(0, Math.floor(Number(seconds) || 0));
  if (!s) return '—';
  const d = Math.floor(s / 86400);
  const h = Math.floor((s % 86400) / 3600);
  const m = Math.floor((s % 3600) / 60);
  if (d) return `${d}d ${h}h`;
  if (h) return `${h}h ${m}m`;
  return `${m}m`;
}

/** A meter's tone from its fill: comfortable, worth watching, act now. */
export function meterTone(pct: number): 'good' | 'warn' | 'bad' {
  if (pct >= 90) return 'bad';
  if (pct >= 75) return 'warn';
  return 'good';
}

/* ---------------- shared mini-card ---------------- */

/**
 * MetricTile is the Players row's mini card (`.astat`) plus the one thing a
 * percentage needs to be readable at a glance: a meter. The card grows a bar
 * only when there IS a percentage — network and uptime have no ceiling, and a
 * fake one would be a lie about what the number means.
 */
function MetricTile({
  label,
  value,
  hint,
  percent,
  tone,
}: {
  label: string;
  value: string;
  hint?: string;
  percent?: number | null;
  tone?: 'good' | 'warn' | 'bad' | 'plain';
}) {
  const color =
    tone === 'good' ? 'var(--ink)' : tone === 'warn' ? 'var(--stamp)' : tone === 'bad' ? 'var(--red)' : 'var(--ink)';
  const pct = typeof percent === 'number' && isFinite(percent) ? Math.max(0, Math.min(100, percent)) : null;
  const fill = pct === null ? '' : meterTone(pct);
  return (
    <div className="astat">
      <div className="astat-label">{label}</div>
      <div className="astat-value" style={{ color }}>
        {value}
      </div>
      {pct === null ? null : (
        <div className={`ameter ameter-${fill}`} role="img" aria-label={`${Math.round(pct)}%`}>
          <span className="ameter-fill" style={{ width: `${pct}%` }} />
        </div>
      )}
      {hint ? <div className="astat-hint">{hint}</div> : null}
    </div>
  );
}

/* ---------------- host ---------------- */

function HostCard({ host, sampledAt }: { host: any | null; sampledAt: string }) {
  if (!host) return null;

  if (!host.available) {
    return (
      <div className="card mt-2">
        <div className="card-title">
          <Icon name="pulse" size={18} />
          <span>Server</span>
        </div>
        <AlertBox type="info">
          Host metrics are not available on this deployment: {host.reason || 'the machine did not report them'}.
        </AlertBox>
      </div>
    );
  }

  const cpu = host.cpu || {};
  const mem = host.memory || {};
  const net = host.network || {};
  const proc = host.process || {};
  const disks: any[] = Array.isArray(host.disks) ? host.disks : [];
  const cores = Number(cpu.cores) || 0;
  const load = Number(cpu.load1);
  // Load per core is the honest way to read load average: 4.0 on 8 cores is
  // half a machine, on 2 cores it is double.
  const loadPerCore = cores > 0 && isFinite(load) ? load / cores : NaN;

  return (
    <div className="card mt-2">
      <div className="card-title">
        <Icon name="pulse" size={18} />
        <span>Server</span>
        <span className="chip xs" style={{ marginLeft: 'auto' }}>
          {host.sample_interval_seconds ? `last ${host.sample_interval_seconds}s` : 'since boot'}
        </span>
      </div>
      <div className="xs faint">
        CPU, memory, disk and network on the machine running the API{sampledAt ? ` · sampled ${sampledAt}` : ''}
        {host.uptime_seconds ? ` · up ${fmtUptime(host.uptime_seconds)}` : ''}
        {cpu.model ? ` · ${cpu.model}` : ''}
      </div>
      <div className="astat-grid">
        <MetricTile
          label="CPU"
          value={`${(Number(cpu.percent) || 0).toFixed(1)}%`}
          percent={cpu.percent}
          tone={meterTone(Number(cpu.percent) || 0)}
          hint={
            `${cores} core${cores === 1 ? '' : 's'}` +
            (isFinite(loadPerCore) ? ` · load ${load.toFixed(2)} (${loadPerCore.toFixed(2)}/core)` : '')
          }
        />
        <MetricTile
          label="Memory"
          value={`${(Number(mem.percent) || 0).toFixed(1)}%`}
          percent={mem.percent}
          tone={meterTone(Number(mem.percent) || 0)}
          hint={
            `${fmtBytes(mem.used_bytes)} of ${fmtBytes(mem.total_bytes)}` +
            (mem.swap_total_bytes ? ` · swap ${fmtBytes(mem.swap_used_bytes)}` : ' · no swap')
          }
        />
        {disks.map((d) => (
          <MetricTile
            key={d.path}
            label={d.label || d.path}
            value={`${(Number(d.percent) || 0).toFixed(1)}%`}
            percent={d.percent}
            tone={meterTone(Number(d.percent) || 0)}
            hint={`${fmtBytes(d.used_bytes)} of ${fmtBytes(d.total_bytes)} · ${d.path}`}
          />
        ))}
        <MetricTile
          label="Network in"
          value={fmtRate(net.rx_bytes_per_sec)}
          hint={`${fmtBytes(net.rx_bytes_total)} received in total`}
        />
        <MetricTile
          label="Network out"
          value={fmtRate(net.tx_bytes_per_sec)}
          hint={`${fmtBytes(net.tx_bytes_total)} sent in total`}
        />
        <MetricTile
          label="Process"
          value={`${Number(proc.goroutines) || 0}`}
          hint={`goroutines · heap ${fmtBytes(proc.heap_bytes)}`}
        />
      </div>
    </div>
  );
}

/* ---------------- countries ---------------- */

/**
 * CountryRankCard is the ranking the owner asked for: the countries the shop
 * has been visited from, most customers first, with the bar scaled to the top
 * country so the shape of the list is readable without doing arithmetic.
 */
function CountryRankCard({ countries }: { countries: any | null }) {
  if (!countries) return null;
  const rows: any[] = Array.isArray(countries.rows) ? countries.rows : [];
  const top = rows.length ? Math.max(1, Number(rows[0].users) || 1) : 1;

  return (
    <div className="card mt-2">
      <div className="card-title">
        <Icon name="globe" size={18} />
        <span>Countries</span>
        <span className="chip xs" style={{ marginLeft: 'auto' }}>
          {Number(countries.countries) || 0} countries
        </span>
      </div>
      <div className="xs faint">
        Where customers were last seen, by the country the edge reported for their visit · {Number(countries.placed) || 0}{' '}
        placed
        {Number(countries.unknown) ? ` · ${countries.unknown} not observed` : ''}
      </div>
      {rows.length === 0 ? (
        <div className="small muted mt-2">
          No country recorded yet. It is written when a customer signs in, browses signed-in, checks out or sends a
          heartbeat — a visit through an edge that reports a country.
        </div>
      ) : (
        <ol className="acr-list">
          {rows.map((row, i) => {
            const users = Number(row.users) || 0;
            const share = Math.round((users / top) * 100);
            const code = String(row.code || '');
            return (
              <li className="acr-row" key={code || i}>
                <span className="acr-rank">{i + 1}</span>
                <span className="acr-flag" aria-hidden="true">
                  {flag(code) || '🏳️'}
                </span>
                <span className="acr-main">
                  <span className="acr-name">
                    {countryName(code) || code}
                    <span className="acr-code">{code}</span>
                  </span>
                  <span className="acr-bar" aria-hidden="true">
                    <span className="acr-fill" style={{ width: `${share}%` }} />
                  </span>
                </span>
                <span className="acr-metrics">
                  <span className="acr-metric">
                    <b>{users}</b>
                    <i>customers</i>
                  </span>
                  <span className="acr-metric">
                    <b>{Number(row.active_today) || 0}</b>
                    <i>today</i>
                  </span>
                  <span className="acr-metric">
                    <b>{Number(row.with_orders) || 0}</b>
                    <i>bought</i>
                  </span>
                </span>
              </li>
            );
          })}
        </ol>
      )}
    </div>
  );
}

/* ---------------- the panel ---------------- */

export function SystemPanel() {
  const [data, setData] = useState<any | null>(null);
  const [err, setErr] = useState('');
  const [loadedAt, setLoadedAt] = useState('');

  const load = useCallback(() => {
    adminDashboard()
      .then((d) => {
        setData(d);
        setErr('');
        setLoadedAt(new Date().toLocaleTimeString());
      })
      .catch((e) => setErr((e as Error).message || 'Could not load the server metrics'));
  }, []);

  useEffect(load, [load]);
  // The host numbers are the live part; the country ranking barely changes, and
  // both ride the same cached payload, so one poll refreshes both quietly.
  useInterval(load, 15000, []);

  if (err) {
    return (
      <div className="card mt-2">
        <div className="card-title">
          <Icon name="pulse" size={18} />
          <span>Server</span>
        </div>
        <AlertBox type="error">{err}</AlertBox>
      </div>
    );
  }

  return (
    <>
      <HostCard host={data?.host || null} sampledAt={loadedAt} />
      <CountryRankCard countries={data?.countries || null} />
    </>
  );
}
