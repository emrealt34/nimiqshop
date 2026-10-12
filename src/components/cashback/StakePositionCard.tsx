import { useEffect, useState } from 'react';
import { getValidatorInfo } from '../../lib/api';
import { type StakePosition } from '../../lib/stakePosition';
import { POOL_VALIDATOR_NAME } from '../../lib/stake';
import { explorerUrl } from '../../lib/chain';
import { useT } from '../../i18n';
import { Icon } from '../ui/Icon';
import { LoadingRows } from '../ui/uiKit';

export function StakePositionCard({ position, loading, error, pool, busy, onRefresh, onMove, onWallet }: {
  position: StakePosition | null; loading: boolean; error: string; pool: string; busy: boolean;
  onRefresh: () => void; onMove: () => void; onWallet: () => void;
}) {
  const { t, lang } = useT();
  const [identity, setIdentity] = useState<{ address: string; name?: string; logo?: string } | null>(null);
  const delegation = position?.delegation || '';
  useEffect(() => {
    let live = true;
    if (delegation) getValidatorInfo(delegation).then(info => {
      if (live) setIdentity({ address: delegation, name: info?.found ? info.name : '', logo: info?.found ? info.logo : '' });
    }).catch(() => {});
    return () => { live = false; };
  }, [delegation]);
  const info = identity?.address === delegation ? identity : null;
  const here = !!pool && delegation === pool;
  const link = explorerUrl(delegation);
  return <section className="cb-position" aria-busy={loading}>
    <div className="row between"><h3>{t('stakePosition.title')}</h3><button className="btn btn-sm btn-ghost" onClick={onRefresh} disabled={loading || busy}><Icon name="refresh" size={14} />{t('common.refresh')}</button></div>
    {loading && !position ? <LoadingRows rows={3} /> : !position ? <div role="status" className="small muted">{error ? t('stakePosition.unavailable') : t('common.loading')}</div> : <>
      <p className="small muted">{!position.exists ? t('stakePosition.none') : here ? t('stakePosition.here') : t('stakePosition.elsewhere')}</p>
      {position.exists && <>
        <div className="cb-position-validator">
          {info?.logo ? <img src={info.logo} alt="" width={40} height={40} onError={event => { event.currentTarget.hidden = true; }} /> : <Icon name="nimiq" size={32} />}
          <div><strong>{info?.name || (here ? POOL_VALIDATOR_NAME : t('stakePosition.validator'))}</strong>{delegation && <code>{delegation.match(/.{1,4}/g)?.join(' ')}</code>}</div>
          {link && <a className="btn btn-sm btn-outline" href={link} target="_blank" rel="noopener noreferrer"><Icon name="external" size={14} />{t('stakePosition.verify')}</a>}
        </div>
        <dl className="cb-position-balances">{(['active_nim', 'inactive_nim', 'retired_nim'] as const).map(key => <div key={key}><dt>{t(`stakePosition.${key}`)}</dt><dd>{position[key].toLocaleString(lang, { maximumFractionDigits: 5 })} NIM</dd></div>)}</dl>
        {(position.inactive_nim > 0 || position.retired_nim > 0) && <p className="xs muted">{t('stakePosition.cooldown')}</p>}
        {!here && pool && position.active_nim + position.inactive_nim > 0 && <button className="btn btn-gold btn-block" disabled={busy || loading} onClick={onMove}>{t('cashback.formMoveBtn', { validator: POOL_VALIDATOR_NAME })}</button>}
        {(position.retired_nim > 0 || !delegation) && <button className="btn btn-outline btn-block mt-1" onClick={onWallet}>{t('stakePosition.manageWallet')}</button>}
      </>}
      <p className="xs faint">{t('stakePosition.source')}</p>
    </>}
  </section>;
}
