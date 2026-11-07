import { useEffect, useState } from 'react';
import { ApiError, friendlyApiMessage, getStakeLinkInviteInfo, type StakeInviteInfo } from '../../lib/api';
import { claimStakingWalletInvite } from '../../lib/hub';
import { openInNimiqPay, useInNimiqPay } from '../../lib/miniapp';
import { fmtStakeNIM } from '../../lib/stakerCashback';
import { POOL_VALIDATOR_NAME } from '../../lib/stake';
import { formatWalletAddress } from '../../lib/format';
import { identiconPngDataUrl } from '../../lib/identicon';
import { buildStakeInviteUrl, clearStakeInviteToken } from '../../lib/stakeLinkIntent';
import { useT } from '../../i18n';
import { Icon } from '../ui/Icon';
import { useNimiqPayInstallSheet } from '../ui/NimiqPayInstallDialog';
import { useToast } from '../AppProviders';

interface ClaimedResult {
  account_address: string;
  stake_address: string;
  stake_nim: number;
}

export function StakeInviteClaimCard({
  inviteToken,
  onDismiss,
  onLinked,
}: {
  inviteToken: string;
  onDismiss: () => void;
  onLinked?: () => void;
}) {
  const { t } = useT();
  const { toast } = useToast();
  const inPay = useInNimiqPay();
  const showPayInstall = useNimiqPayInstallSheet();

  const [info, setInfo] = useState<StakeInviteInfo | null>(null);
  const [loading, setLoading] = useState(true);
  const [expired, setExpired] = useState(false);
  const [busy, setBusy] = useState<'pay' | 'hub' | null>(null);
  const [err, setErr] = useState('');
  const [avatar, setAvatar] = useState('');
  const [claimed, setClaimed] = useState<ClaimedResult | null>(null);

  const pretty = (address: string) => formatWalletAddress(address);

  useEffect(() => {
    let live = true;
    setLoading(true);
    setExpired(false);
    setErr('');
    getStakeLinkInviteInfo(inviteToken)
      .then((res) => {
        if (!live) return;
        setInfo(res);
        setLoading(false);
        if (res.account_address) {
          void identiconPngDataUrl(res.account_address)
            .then((url) => { if (live) setAvatar(url); })
            .catch(() => {});
        }
      })
      .catch((e) => {
        if (!live) return;
        setLoading(false);
        const status = e instanceof ApiError ? e.status : 0;
        if (status === 400 || status === 401) {
          setExpired(true);
        } else {
          setErr(friendlyApiMessage(e, t('stakeLink.errGeneric')));
        }
      });
    return () => { live = false; };
  }, [inviteToken, t]);

  useEffect(() => {
    const onHubLinked = (event: Event) => {
      const detail = ((event as CustomEvent<Record<string, unknown>>).detail || {}) as Record<string, unknown>;
      if (!detail.via_invite) return;
      const stakeAddr = String(detail.stake_address || '');
      const accountAddr = String(detail.account_address || info?.account_address || '');
      const stakeNim = Number(detail.stake_nim) || 0;
      setClaimed({ account_address: accountAddr, stake_address: stakeAddr, stake_nim: stakeNim });
      setBusy(null);
      if (stakeAddr) toast(t('stakeLink.linked', { address: pretty(stakeAddr) }), 'success');
      onLinked?.();
    };
    const onHubErr = (event: Event) => {
      const detail = (event as CustomEvent<{ message?: string; error?: unknown }>).detail;
      setBusy(null);
      setErr(explain(detail?.error || detail?.message));
    };
    window.addEventListener('nimshop:stake-linked', onHubLinked);
    window.addEventListener('nimshop:hub-error', onHubErr);
    return () => {
      window.removeEventListener('nimshop:stake-linked', onHubLinked);
      window.removeEventListener('nimshop:hub-error', onHubErr);
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [info?.account_address, onLinked, t, toast]);

  function explain(e: unknown): string {
    const status = e instanceof ApiError ? e.status : 0;
    const msg = e instanceof ApiError ? String(e.message || '') : '';
    if (status === 422) {
      const other = String((e as ApiError).data?.other_validator || '');
      return other ? t('stakeLink.errElsewhere', { validator: POOL_VALIDATOR_NAME }) : t('stakeLink.errNoStake');
    }
    if (status === 409) {
      if (/maximum|limit/i.test(msg)) return t('stakeLink.errLimit', { max: info?.linked_wallet_limit || 5 });
      if (/another account/i.test(msg)) return t('stakeLink.errTaken');
      return t('stakeLink.errOwnAddress');
    }
    if (status === 401) {
      if (/invite/i.test(msg)) return t('stakeLink.claimExpired');
      return t('stakeLink.errSignature');
    }
    return friendlyApiMessage(e, t('stakeLink.errGeneric'));
  }

  async function claim(via: 'pay' | 'hub') {
    if (busy) return;
    setErr('');
    setBusy(via);
    try {
      const res = await claimStakingWalletInvite(inviteToken, via);
      if ('redirecting' in res) {
        toast(t('stakeLink.redirect'), 'info');
        return;
      }
      const stakeAddr = String(res?.stake_address || '');
      const accountAddr = String(res?.account_address || info?.account_address || '');
      const stakeNim = Number(res?.stake_nim) || 0;
      setClaimed({ account_address: accountAddr, stake_address: stakeAddr, stake_nim: stakeNim });
      toast(t('stakeLink.linked', { address: pretty(stakeAddr) }), 'success');
      onLinked?.();
    } catch (e) {
      setErr(explain(e));
    } finally {
      setBusy(null);
    }
  }

  function dismiss() {
    clearStakeInviteToken();
    onDismiss();
  }

  function openInviteInPay() {
    const shareUrl = typeof window !== 'undefined'
      ? buildStakeInviteUrl(window.location.origin + window.location.pathname, inviteToken)
      : '';
    openInNimiqPay(() => showPayInstall(), shareUrl || undefined);
  }

  return (
    <section className="card mt-2 fade-in" data-testid="stake-invite-claim-card" style={{ border: '1px solid var(--gold)' }}>
      <div className="row between" style={{ alignItems: 'center', gap: 8 }}>
        <div className="card-title" style={{ marginBottom: 0 }}>
          <Icon name="lock" size={16} /> {t('stakeLink.claimTitle')}
        </div>
        <button type="button" className="btn btn-sm btn-ghost" onClick={dismiss} aria-label={t('stakeLink.cancel')}>
          <Icon name="x" size={14} /> {t('stakeLink.cancel')}
        </button>
      </div>

      {claimed ? (
        <div className="alert success mt-2" role="status" data-testid="stake-invite-claimed" style={{ marginBottom: 0 }}>
          <Icon name="check" size={18} />
          <div className="small">
            {t('stakeLink.claimSuccess', {
              stake: pretty(claimed.stake_address),
              nim: fmtStakeNIM(claimed.stake_nim),
              account: pretty(claimed.account_address),
            })}
          </div>
        </div>
      ) : loading ? (
        <p className="small muted mt-2" style={{ marginBottom: 0 }}>{t('actions.loading')}</p>
      ) : expired || !info ? (
        <div className="alert warn mt-2" role="alert" data-testid="stake-invite-expired" style={{ marginBottom: 0 }}>
          <Icon name="alert" size={18} />
          <div className="small">{err || t('stakeLink.claimExpired')}</div>
        </div>
      ) : info.at_limit ? (
        <div className="alert warn mt-2" role="alert" style={{ marginBottom: 0 }}>
          <Icon name="alert" size={18} />
          <div className="small">{t('stakeLink.limitReached', { max: info.linked_wallet_limit || 5 })}</div>
        </div>
      ) : (
        <div className="mt-2">
          <div className="row" style={{ gap: 10, alignItems: 'center', marginBottom: 10 }}>
            {avatar
              ? <img className="cb-linked-avatar" src={avatar} alt="" />
              : <span className="cb-linked-avatar cb-linked-avatar-fallback" aria-hidden="true">◈</span>}
            <div className="small mono strong" data-testid="stake-invite-target">{pretty(info.account_address)}</div>
          </div>
          <p className="small muted" style={{ marginTop: 0 }}>
            {t('stakeLink.claimBody', { address: pretty(info.account_address) })}
          </p>

          {inPay ? (
            <button
              type="button"
              className="btn btn-gold btn-block"
              disabled={!!busy}
              onClick={() => { void claim('pay'); }}
              data-testid="stake-invite-claim-pay"
            >
              <Icon name="nimiq" size={16} />
              <span>{busy === 'pay' ? t('stakeLink.busy') : t('stakeLink.claimWithPay')}</span>
            </button>
          ) : (
            <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
              <button
                type="button"
                className="btn btn-gold btn-block"
                disabled={!!busy}
                onClick={() => { void claim('hub'); }}
                data-testid="stake-invite-claim-hub"
              >
                <Icon name="lock" size={15} />
                <span>{busy === 'hub' ? t('stakeLink.busy') : t('stakeLink.claimWithHub')}</span>
              </button>
              <button
                type="button"
                className="btn btn-outline btn-block"
                disabled={!!busy}
                onClick={openInviteInPay}
                data-testid="stake-invite-open-pay"
              >
                <Icon name="nimiq" size={15} />
                <span>{t('stakeLink.claimOpenPay')}</span>
              </button>
            </div>
          )}

          <p className="xs faint mt-1" style={{ marginBottom: 0 }}>
            {inPay ? t('stakeLink.signHintPay') : t('stakeLink.signHint')}
          </p>

          {err && (
            <div className="alert error mt-2" role="alert" data-testid="stake-invite-error" style={{ marginBottom: 0 }}>
              <Icon name="alert" size={18} />
              <div className="small">{err}</div>
            </div>
          )}
        </div>
      )}
    </section>
  );
}
