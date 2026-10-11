import { useEffect, useState } from 'react';
import { AppRoot } from '../AppRoot';
import { useT } from '../../i18n';
import { asset } from '../../lib/asset';
import { fmtDate } from '../../lib/format';

export function StatusView() {
  const { t } = useT();
  const [data, setData] = useState<any>(null);
  const [error, setError] = useState(false);
  const [now, setNow] = useState(Date.now());
  useEffect(() => {
    const controller = new AbortController();
    const read = async (path: string) => {
      const r = await fetch(asset(path), { cache: 'no-store', signal: controller.signal });
      if (!r.ok) throw Error('unavailable');
      return r.json();
    };
    const load = async () => {
      setNow(Date.now());
      try {
        let value;
        try { value = await read('/data/market/refresh.json'); }
        catch {
          // Older deployed snapshots predate the report. Read their actual
          // served timestamps; never substitute the current time or a run ID.
          const [catalog, market] = await Promise.all([read('/data/catalog/manifest.json'), read('/data/market/manifest.json')]);
          value = { catalog: { generated_at: catalog.generated_at, completed_at: catalog.completed_at, complete: catalog.complete, countries: Object.keys(catalog.countries || {}).length,
            retained_countries: (catalog.failed || []).map((f: any) => f.country), stock: catalog.verified || {} }, market };
        }
        if (!value?.catalog || !value?.market) throw Error('invalid');
        setData(value); setError(false);
      } catch { if (!controller.signal.aborted) setError(true); }
    };
    void load(); const timer = setInterval(load, 60_000);
    return () => { controller.abort(); clearInterval(timer); };
  }, []);
  const date = (value: unknown) => typeof value === 'string' && Number.isFinite(Date.parse(value)) ? fmtDate(value) : '—';
  const recent = (value: unknown) => typeof value === 'string' && now - Date.parse(value) < 8 * 60 * 60_000 && now >= Date.parse(value);
  const fresh = !!data && recent(data.catalog.generated_at) && recent(data.market.generated_at)
    && recent(data.market.nim?.observed_at) && recent(data.market.btc?.observed_at);
  const complete = data?.catalog.complete === true && data.catalog.stock?.completed === true
    && data.catalog.stock.mode === 'full' && !(data.catalog.retained_countries?.length)
    && !data.catalog.stock.unresolved && data.complete !== false && data.rates_complete !== false;
  const run = /^\d+$/.test(data?.run_id || '') ? `https://github.com/emrealt34/nimiqshop/actions/runs/${data.run_id}` : 'https://github.com/emrealt34/nimiqshop/actions/workflows/catalog-refresh.yml';
  return <section className="container section refresh-status">
    <h1>{t('refreshStatus.title')}</h1>
    <p className="muted">{t('refreshStatus.intro')}</p>
    <div className={`alert ${error || (data && (!fresh || !complete)) ? 'warn' : 'info'}`} role="status">{error ? t('refreshStatus.error') : !data ? t('refreshStatus.loading') : !fresh ? t('refreshStatus.stale') : !complete ? t('refreshStatus.partial') : t('refreshStatus.fresh')}</div>
    {data ? <div className="card pad mt-2">
      <dl className="refresh-status-list">
        <dt>{t('refreshStatus.catalog')}</dt><dd>{date(data.catalog.generated_at)}</dd>
        <dt>{t('refreshStatus.finished')}</dt><dd>{date(data.catalog.completed_at)}</dd>
        <dt>{t('refreshStatus.market')}</dt><dd>{date(data.market.generated_at)}</dd>
        <dt>{t('refreshStatus.countries')}</dt><dd>{data.catalog.countries} · {t('refreshStatus.retained')}: {data.catalog.retained_countries?.join(', ') || '—'}</dd>
        <dt>{t('refreshStatus.stock')}</dt><dd>{t('refreshStatus.stockCounts', { checked: data.catalog.stock?.checked || 0, pending: data.catalog.stock?.skipped || 0, unresolved: data.catalog.stock?.unresolved || 0 })}</dd>
        <dt>NIM / USD</dt><dd>{data.market.nim?.usd_per_nim ?? '—'} · {date(data.market.nim?.observed_at)}</dd>
        <dt>BTC / USD</dt><dd>{data.market.btc?.usd_per_btc ?? '—'} · {date(data.market.btc?.observed_at)}</dd>
        <dt>{t('refreshStatus.fiat')}</dt><dd>{date(data.market.fx?.observed_at)} · {data.market.fx?.currencies ?? '—'}</dd>
      </dl>
      <p className="small muted">{t('refreshStatus.explanation')}</p>
    </div> : null}
    <a className="btn btn-outline mt-2" href={run} target="_blank" rel="noopener noreferrer">{t('refreshStatus.actions')}</a>
  </section>;
}
export function StatusPage() { return <AppRoot activeKey="none"><StatusView /></AppRoot>; }
