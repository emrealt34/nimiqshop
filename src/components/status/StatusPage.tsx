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
    const load = async () => {
      setNow(Date.now());
      try {
        const r = await fetch(asset('/data/market/refresh.json'), { cache: 'no-store', signal: controller.signal });
        if (!r.ok) throw Error('unavailable');
        const value = await r.json();
        if (!value?.catalog || !value?.market) throw Error('invalid');
        setData(value); setError(false);
      } catch { if (!controller.signal.aborted) setError(true); }
    };
    void load(); const timer = setInterval(load, 60_000);
    return () => { controller.abort(); clearInterval(timer); };
  }, []);
  const date = (value: unknown) => typeof value === 'string' && Number.isFinite(Date.parse(value)) ? fmtDate(value) : '—';
  const recent = (value: unknown) => typeof value === 'string' && now - Date.parse(value) < 90 * 60_000 && now >= Date.parse(value);
  const fresh = !!data && recent(data.catalog.generated_at) && recent(data.market.generated_at)
    && recent(data.market.nim?.observed_at) && recent(data.market.btc?.observed_at);
  const run = /^\d+$/.test(data?.run_id || '') ? `https://github.com/emrealt34/nimiqshop/actions/runs/${data.run_id}` : 'https://github.com/emrealt34/nimiqshop/actions/workflows/catalog-refresh.yml';
  return <section className="container section refresh-status">
    <h1>{t('refreshStatus.title')}</h1>
    <p className="muted">{t('refreshStatus.intro')}</p>
    <div className={`alert ${error || (data && !fresh) ? 'warn' : 'info'}`} role="status">{error ? t('refreshStatus.error') : !data ? t('refreshStatus.loading') : fresh ? t('refreshStatus.fresh') : t('refreshStatus.stale')}</div>
    {data ? <div className="card pad mt-2">
      <dl className="refresh-status-list">
        <dt>{t('refreshStatus.catalog')}</dt><dd>{date(data.catalog.generated_at)}</dd>
        <dt>{t('refreshStatus.market')}</dt><dd>{date(data.market.generated_at)}</dd>
        <dt>{t('refreshStatus.countries')}</dt><dd>{data.catalog.countries} · {t('refreshStatus.retained')}: {data.catalog.retained_countries?.join(', ') || '—'}</dd>
        <dt>{t('refreshStatus.stock')}</dt><dd>{t('refreshStatus.stockCounts', { checked: data.catalog.stock?.checked || 0, pending: data.catalog.stock?.skipped || 0 })}</dd>
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
