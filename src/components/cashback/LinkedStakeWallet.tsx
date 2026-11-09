import { useEffect, useMemo, useRef, useState } from 'react';
import { ApiError, createStakeLinkInvite, friendlyApiMessage, unlinkStakeAddress } from '../../lib/api';
import { StakeLinkWalletNotInHostError, linkStakingWallet } from '../../lib/hub';
import { openInNimiqPay, useInNimiqPay } from '../../lib/miniapp';
import { type MyStake, type StakeWalletRow, fmtStakeNIM } from '../../lib/stakerCashback';
import { POOL_VALIDATOR_NAME } from '../../lib/stake';
import { formatWalletAddress } from '../../lib/format';
import { identiconPngDataUrl } from '../../lib/identicon';
import { buildStakeInviteUrl, clearStakeLinkIntent, normalizeStakeAddress } from '../../lib/stakeLinkIntent';
import { useT } from '../../i18n';
import { Icon } from '../ui/Icon';
import { QR } from '../checkout/QR';
import { useNimiqPayInstallSheet } from '../ui/NimiqPayInstallDialog';
import { useToast } from '../AppProviders';

const FALLBACK_WALLET_LIMIT = 5;

/**
 * Shows each earning wallet separately, sums their stake, and names the
 * signed-in payout wallet. Supports BOTH direct host signing (Nimiq Pay inside
 * the app, Nimiq Hub in a browser) AND cross-device pairing links
 * (?stake_link=<token>) so a buyer can seamlessly link a phone Nimiq Pay
 * wallet and a PC Nimiq Hub wallet without sharing session cookies.
 */
export function LinkedStakeWallet({ mine, onChanged, intent, onIntentDone }: {
  mine: MyStake | null;
  onChanged: () => void;
  intent?: string;
  onIntentDone?: () => void;
}) {
  const { t } = useT();
  const { toast } = useToast();
  const inPay = useInNimiqPay();
  const showPayInstall = useNimiqPayInstallSheet();
  const [busy, setBusy] = useState(false);
  const [inviteBusy, setInviteBusy] = useState(false);
  const [inviteUrl, setInviteUrl] = useState('');
  const [err, setErr] = useState('');
  const [avatars, setAvatars] = useState<Record<string, string>>({});
  const account = String(mine?.account_address || mine?.address || '');
  // onChanged arrives as a fresh arrow from the parent every render; going
  // through a ref keeps the 3.5s pairing poll on a steady cadence instead of
  // resetting the timer on every render.
  const onChangedRef = useRef(onChanged);
  useEffect(() => { onChangedRef.current = onChanged; }, [onChanged]);
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
  const initialLinkedRef = useRef<number | null>(null);
  const canNativeShare = typeof navigator !== 'undefined' && typeof navigator.share === 'function';

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

  useEffect(() => {
    if (!intent) return;
    const already = expectedAddress && linkedRows.some(row => normalizeStakeAddress(row.address) === expectedAddress);
    if (!expectedAddress || already) {
      clearStakeLinkIntent();
      onIntentDone?.();
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [intent, expectedAddress]);

  // While a cross-device pairing link is open on Device 1, poll every 3.5s so
  // the newly attached wallet appears automatically the moment Device 2 signs.
  useEffect(() => {
    if (!inviteUrl) {
      initialLinkedRef.current = null;
      return;
    }
    if (initialLinkedRef.current === null) {
      initialLinkedRef.current = linkedRows.length;
    } else if (linkedRows.length > initialLinkedRef.current) {
      const newest = linkedRows[linkedRows.length - 1];
      setInviteUrl('');
      initialLinkedRef.current = null;
      if (newest?.address) toast(t('stakeLink.linked', { address: pretty(newest.address) }), 'success');
      return;
    }
    const timer = window.setInterval(() => { onChangedRef.current(); }, 3500);
    return () => { window.clearInterval(timer); };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [inviteUrl, linkedRows.length]);

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
    if (e instanceof StakeLinkWalletNotInHostError) {
      return t('stakeLink.errNotInHost', { address: pretty(e.expected) });
    }
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

  async function generateInvite() {
    if (inviteBusy || atLimit) return;
    setErr('');
    setInviteBusy(true);
    try {
      const res = await createStakeLinkInvite();
      const base = typeof window !== 'undefined' ? window.location.origin + window.location.pathname : '';
      const url = buildStakeInviteUrl(base, res.invite_token);
      initialLinkedRef.current = linkedRows.length;
      setInviteUrl(url);
    } catch (e) {
      setErr(explain(e));
    } finally {
      setInviteBusy(false);
    }
  }

  async function copyInvite() {
    if (!inviteUrl) return;
    try {
      await navigator.clipboard.writeText(inviteUrl);
      toast(t('toast.copied'), 'success');
    } catch {
      toast(t('toast.copyFailed'), 'error');
    }
  }

  async function shareInvite() {
    if (!inviteUrl || !canNativeShare) return;
    try {
      await navigator.share({ title: t('stakeLink.claimTitle'), url: inviteUrl });
    } catch {
      /* user dismissed native share sheet */
    }
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
      const status = e instanceof ApiError ? e.status : 0;
      const msg = e instanceof ApiError ? String(e.message || '') : '';
      // Inside Nimiq Pay, Pay holds only the wallet already signed in, so a
      // direct sign answers 409 own-wallet. Automatically open the cross-device
      // pairing link so the buyer can attach their PC/other wallet immediately.
      if (inPay && (e instanceof StakeLinkWalletNotInHostError || (status === 409 && !/maximum|limit|another account|not linked/i.test(msg)))) {
        setErr(t('stakeLink.ownWalletLinkHint'));
        if (!inviteUrl) void generateInvite();
      } else {
        setErr(explain(e));
      }
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
        <div className="mt-1" style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
          {!linked && <p className="small muted" style={{ marginBottom: 0 }}>{t('stakeLink.why')}</p>}
          <button className="btn btn-gold btn-block" onClick={() => { void link(); }} disabled={busy || inviteBusy} data-testid="stake-link-confirm">
            <Icon name="lock" size={14} />{busy ? t('stakeLink.busy') : linked ? t('stakeLink.addAnother') : t('stakeLink.confirm')}
          </button>
          <button
            type="button"
            className="btn btn-outline btn-block"
            onClick={() => { void generateInvite(); }}
            disabled={busy || inviteBusy}
            data-testid="stake-link-create-invite"
          >
            <Icon name="external" size={14} />
            <span>{inviteBusy ? t('actions.loading') : t('stakeLink.createLink')}</span>
          </button>
          <p className="xs faint" style={{ marginBottom: 0 }}>{inPay ? t('stakeLink.signHintPay') : t('stakeLink.signHint')}</p>

          {inviteUrl && (
            <div className="card mt-1" data-testid="stake-link-invite-box" style={{ background: 'var(--surface-2)', padding: 12 }}>
              <div className="row between" style={{ alignItems: 'center', gap: 8 }}>
                <div className="small strong">{t('stakeLink.inviteReadyTitle', { address: pretty(account) })}</div>
                <button type="button" className="btn btn-sm btn-ghost" onClick={() => setInviteUrl('')}>
                  <Icon name="x" size={13} />
                </button>
              </div>
              <p className="xs muted mt-1" style={{ marginBottom: 10 }}>
                {t('stakeLink.inviteReadyHint')}
              </p>
              <div style={{ display: 'flex', justifyContent: 'center', padding: 8, background: '#fff', borderRadius: 10, width: 'fit-content', margin: '0 auto 10px' }}>
                <QR text={inviteUrl} px={148} />
              </div>
              <div className="row" style={{ gap: 6, flexWrap: 'wrap' }}>
                <button type="button" className="btn btn-sm btn-gold" style={{ flex: 1 }} onClick={() => { void copyInvite(); }} data-testid="stake-link-invite-copy">
                  <Icon name="copy" size={13} /> {t('stakeLink.inviteCopy')}
                </button>
                {canNativeShare && (
                  <button type="button" className="btn btn-sm btn-outline" style={{ flex: 1 }} onClick={() => { void shareInvite(); }}>
                    <Icon name="external" size={13} /> {t('stakeLink.inviteShare')}
                  </button>
                )}
                {!inPay && (
                  <button
                    type="button"
                    className="btn btn-sm btn-outline"
                    style={{ flex: 1 }}
                    onClick={() => { openInNimiqPay(() => showPayInstall(), inviteUrl); }}
                  >
                    <Icon name="nimiq" size={13} /> {t('stakeLink.inviteOpenPay')}
                  </button>
                )}
              </div>
              <p className="xs faint center mt-1" style={{ marginBottom: 0 }}>{t('stakeLink.inviteWaiting')}</p>
            </div>
          )}
        </div>
      )}

      {err && <div className="alert error mt-1" role="alert" data-testid="stake-link-error"><Icon name="alert" size={18} /><div className="small">{err}</div></div>}
    </section>
  );
}
