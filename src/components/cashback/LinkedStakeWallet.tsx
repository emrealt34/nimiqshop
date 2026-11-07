import { useEffect, useMemo, useState } from 'react';
import { ApiError, friendlyApiMessage, unlinkStakeAddress } from '../../lib/api';
import { linkStakingWallet } from '../../lib/hub';
import { type MyStake, type StakeWalletRow, fmtStakeNIM } from '../../lib/stakerCashback';
import { POOL_VALIDATOR_NAME } from '../../lib/stake';
import { formatWalletAddress } from '../../lib/format';
import { identiconPngDataUrl } from '../../lib/identicon';
import { clearStakeLinkIntent, normalizeStakeAddress } from '../../lib/stakeLinkIntent';
import { useT } from '../../i18n';
import { Icon } from '../ui/Icon';
import { useToast } from '../AppProviders';

const FALLBACK_WALLET_LIMIT = 5;

/**
 * Shows each earning wallet separately, sums their stake, and names the
 * signed-in payout wallet. Linking is a single explicit action: Hub selects
 * the staking wallet and proves it with a signature; the shop never asks the
 * buyer to type or paste an address.
 */
export function LinkedStakeWallet({ mine, onChanged, intent, onIntentDone }: {
  mine: MyStake | null;
  onChanged: () => void;
  intent?: string;
  onIntentDone?: () => void;
}) {
  const { t } = useT();
  const { toast } = useToast();
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState('');
  const [avatars, setAvatars] = useState<Record<string, string>>({});
  const account = String(mine?.account_address || mine?.address || '');
  const limit = Number(mine?.linked_wallet_limit) > 0 ? Number(mine?.linked_wallet_limit) : FALLBACK_WALLET_LIMIT;
  const rows: StakeWalletRow[] = mine?.stake_addresses?.length
    ? mine.stake_addresses
    : account
      ? [{ address: account, linked: false, staked: !!mine?.staked, stake_nim: Number(mine?.stake_nim) || 0 }]
      : [];
  const linkedRows = rows.filter(row => row.linked);
  const linked = linkedRows.length > 0;
  const atLimit = linkedRows.length >= limit;
  const totalNim = rows.reduce((sum, row) => sum + (Number(row.stake_nim) || 0), 0);
  const expectedAddress = useMemo(() => normalizeStakeAddress(intent), [intent]);
  const pretty = (address: string) => formatWalletAddress(address);

  const avatarAddresses = rows.map(row => row.address).join('|');
  useEffect(() => {
    let live = true;
    const addresses = avatarAddresses ? avatarAddresses.split('|') : [];
    void Promise.all(addresses.map(async address => {
      try { return [address, await identiconPngDataUrl(address)] as const; }
      catch { return [address, ''] as const; }
    })).then(entries => { if (live) setAvatars(Object.fromEntries(entries)); });
    return () => { live = false; };
  }, [avatarAddresses]);

  // The URL address is an expectation for Hub's signer picker, never a form
  // value. Invalid/already-linked intent is simply cleared; signing still only
  // starts when the buyer taps the action below.
  useEffect(() => {
    if (!intent) return;
    const already = expectedAddress && linkedRows.some(row => normalizeStakeAddress(row.address) === expectedAddress);
    if (!expectedAddress || already) {
      clearStakeLinkIntent();
      onIntentDone?.();
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [intent, expectedAddress]);

  function finishIntent() {
    clearStakeLinkIntent();
    onIntentDone?.();
  }

  useEffect(() => {
    const onLinked = (event: Event) => {
      const detail = (event as CustomEvent<Record<string, unknown>>).detail || {};
      const address = String(detail.stake_address || expectedAddress || '');
      clearStakeLinkIntent();
      onIntentDone?.();
      if (address) toast(t('stakeLink.linked', { address: formatWalletAddress(address) }), 'success');
      onChanged();
    };
    const onHubError = (event: Event) => {
      const detail = (event as CustomEvent<{ message?: string }>).detail;
      setErr(String(detail?.message || t('stakeLink.errGeneric')));
    };
    window.addEventListener('nimshop:stake-linked', onLinked);
    window.addEventListener('nimshop:hub-error', onHubError);
    return () => {
      window.removeEventListener('nimshop:stake-linked', onLinked);
      window.removeEventListener('nimshop:hub-error', onHubError);
    };
  }, [onChanged, onIntentDone, expectedAddress, t, toast]);

  function explain(e: unknown): string {
    const status = e instanceof ApiError ? e.status : 0;
    const msg = e instanceof ApiError ? String(e.message || '') : '';
    if (status === 422) {
      const other = String((e as ApiError).data?.other_validator || '');
      return other ? t('stakeLink.errElsewhere', { validator: POOL_VALIDATOR_NAME }) : t('stakeLink.errNoStake');
    }
    if (status === 409) {
      if (/maximum|limit/i.test(msg)) return t('stakeLink.errLimit', { max: limit });
      if (/another account/i.test(msg)) return t('stakeLink.errTaken');
      if (/not linked/i.test(msg)) return t('stakeLink.errNotLinked');
      return t('stakeLink.errOwnAddress');
    }
    if (status === 401) return t('stakeLink.errSignature');
    return friendlyApiMessage(e, t('stakeLink.errGeneric'));
  }

  async function link() {
    if (busy || atLimit) return;
    setErr('');
    setBusy(true);
    try {
      const result = await linkStakingWallet(expectedAddress || undefined);
      if ('redirecting' in result) {
        toast(t('stakeLink.redirect'), 'info');
        return;
      }
      const address = String(result?.stake_address || expectedAddress || '');
      toast(t('stakeLink.linked', { address: pretty(address) }), 'success');
      finishIntent();
      onChanged();
    } catch (e) {
      setErr(explain(e));
    } finally {
      setBusy(false);
    }
  }

  async function unlink(target?: string) {
    if (busy) return;
    setErr('');
    setBusy(true);
    try {
      await unlinkStakeAddress(target);
      toast(target ? t('stakeLink.unlinked', { address: pretty(target) }) : t('stakeLink.unlinkedAll'), 'success');
      onChanged();
    } catch (e) {
      setErr(explain(e));
    } finally {
      setBusy(false);
    }
  }

  return (
    <section className="cb-linked-wallet" data-testid="linked-stake-wallet">
      <div className="cb-linked-heading">
        <div>
          <span className="eyebrow">NimShop · Cashback</span>
          <h3>{t('stakeLink.title')}</h3>
        </div>
        {linkedRows.length > 1 && <button className="btn btn-sm btn-ghost" onClick={() => { void unlink(); }} disabled={busy} data-testid="stake-link-unlink-all"><Icon name="x" size={14} />{t('stakeLink.unlinkAll')}</button>}
      </div>

      <p className="small" data-testid="stake-link-earning">
        {linked ? t('stakeLink.summaryLinked', { count: rows.length, total: fmtStakeNIM(totalNim) }) : t('stakeLink.summarySelf', { address: pretty(account) })}
      </p>
      <p className="small muted" data-testid="stake-link-paid-to">
        {t('stakeLink.paidTo', { address: pretty(account) })}
      </p>

      <div className="cb-link-note" data-testid="stake-link-rail"><Icon name="info" size={16} /><div className="small">{t('stakeLink.anyRail')}</div></div>

      <ul className="cb-linked-list" data-testid="stake-link-wallets">
        {rows.map(row => (
          <li key={row.address} className="cb-linked-row" data-testid="stake-link-wallet">
            {avatars[row.address]
              ? <img className="cb-linked-avatar" src={avatars[row.address]} alt="" />
              : <span className="cb-linked-avatar cb-linked-avatar-fallback" aria-hidden="true">◈</span>}
            <div className="cb-linked-wallet-meta">
              <span className="small mono cb-linked-address">{pretty(row.address)}</span>
              <span className="xs faint">{row.linked ? t('stakeLink.linkedBadge') : t('stakeLink.ownWallet')}</span>
              <span className="xs muted" data-testid="stake-link-wallet-stake">
                {Number(row.stake_nim) > 0 ? t('stakeLink.walletStake', { nim: fmtStakeNIM(Number(row.stake_nim) || 0) }) : row.other_validator ? t('stakeLink.walletElsewhere') : t('stakeLink.walletNoStake')}
              </span>
            </div>
            {row.linked && <button className="btn btn-sm btn-ghost cb-linked-remove" onClick={() => { void unlink(row.address); }} disabled={busy} data-testid="stake-link-unlink"><Icon name="x" size={13} />{t('stakeLink.unlink')}</button>}
          </li>
        ))}
      </ul>

      {linked && <>
        <p className="cb-linked-total" data-testid="stake-link-total">{t('stakeLink.totalStake', { nim: fmtStakeNIM(totalNim) })}</p>
        <p className="xs faint">{t('stakeLink.baseOnce')}</p>
        <p className="xs faint">{t('stakeLink.signedHint')}</p>
      </>}
      <p className="xs muted" data-testid="stake-link-count">{t('stakeLink.count', { n: linkedRows.length, max: limit })}</p>

      {intent && expectedAddress && !atLimit && <div className="cb-link-note mt-1" data-testid="stake-link-intent"><Icon name="info" size={16} /><div className="small">{t('stakeLink.intent', { address: pretty(expectedAddress) })}</div><button className="btn btn-sm btn-ghost" onClick={finishIntent}>{t('stakeLink.cancel')}</button></div>}
      {atLimit ? <div className="alert warn mt-1" data-testid="stake-link-limit"><Icon name="alert" size={18} /><div className="small">{t('stakeLink.limitReached', { max: limit })}</div></div> : (
        <div className="mt-1">
          {!linked && <p className="small muted">{t('stakeLink.why')}</p>}
          <button className="btn btn-gold btn-block" onClick={() => { void link(); }} disabled={busy} data-testid="stake-link-confirm">
            <Icon name="lock" size={14} />{busy ? t('stakeLink.busy') : linked ? t('stakeLink.addAnother') : t('stakeLink.confirm')}
          </button>
          <p className="xs faint">{t('stakeLink.signHint')}</p>
        </div>
      )}

      {err && <div className="alert error mt-1" role="alert" data-testid="stake-link-error"><Icon name="alert" size={18} /><div className="small">{err}</div></div>}
    </section>
  );
}
