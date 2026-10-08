/**
 * ElsewhereStake — the stake card when the buyer's NIM sits with ANOTHER
 * validator. It shows that validator (name and logo from Nimiq's validator
 * registry, through our backend) and the amount, then one button that moves
 * the stake to the shop's pool. The amount and preset form is hidden in this
 * case: a new delegation would only create a second staker.
 */
import { useEffect, useState } from 'react';
import { getValidatorInfo } from '../../lib/api';
import { fmtStakeNIM } from '../../lib/stakerCashback';
import { POOL_VALIDATOR_NAME } from '../../lib/stake';
import { shortAddr } from '../../lib/format';
import { useT } from '../../i18n';
import { Icon } from '../ui/Icon';

type ValidatorInfo = {
  found?: boolean;
  name?: string;
  logo?: string;
  accent_color?: string;
};

export function ElsewhereStake({
  address,
  stakeNim,
  canMove,
  busy,
  onMove,
}: {
  address: string;
  stakeNim: number;
  canMove: boolean;
  busy: boolean;
  onMove: () => void;
}) {
  const { t } = useT();
  const [info, setInfo] = useState<ValidatorInfo | null>(null);

  useEffect(() => {
    let live = true;
    getValidatorInfo(address)
      .then((r: ValidatorInfo) => {
        if (live) setInfo(r);
      })
      .catch(() => {
        /* no name or logo: the address alone is still correct */
      });
    return () => {
      live = false;
    };
  }, [address]);

  const named = !!(info?.found && info.name);
  const name = named ? String(info!.name) : shortAddr(address);
  const accent = info?.accent_color || 'var(--gold)';

  return (
    <div>
      <div style={{ display: 'flex', alignItems: 'center', gap: 12, marginBottom: 12 }}>
        {info?.found && info.logo ? (
          <img
            src={info.logo}
            alt=""
            width={44}
            height={44}
            style={{ width: 44, height: 44, borderRadius: 10, objectFit: 'contain', background: 'var(--panel-2)', padding: 4, flex: '0 0 auto' }}
          />
        ) : (
          <div
            aria-hidden="true"
            style={{
              width: 44,
              height: 44,
              borderRadius: 10,
              background: accent,
              color: '#fff',
              display: 'grid',
              placeItems: 'center',
              fontWeight: 800,
              fontSize: 18,
              flex: '0 0 auto',
            }}
          >
            {name.charAt(0).toUpperCase()}
          </div>
        )}
        <div style={{ minWidth: 0, flex: 1 }}>
          <div className="strong" style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
            {name}
          </div>
          <div className="xs faint mono">{fmtStakeNIM(stakeNim)} NIM</div>
        </div>
      </div>

      <div className="xs faint" style={{ marginBottom: 10 }}>
        {t('cashback.elsewhereHint', { validator: POOL_VALIDATOR_NAME })}
      </div>

      <button className="btn btn-gold btn-block" onClick={onMove} disabled={!canMove || busy} aria-busy={busy}>
        <Icon name="chevron" size={14} /> {t('cashback.elsewhereChange')}
      </button>
    </div>
  );
}
