/**
 * LinkedStakeWallet — the answer to "which wallets actually earn my cashback?"
 *
 * A shop account IS an address: signing in creates one account per wallet. A
 * buyer who stakes from his regular wallets but checks out through Nimiq Pay is
 * therefore signed in as a DIFFERENT account that has never staked, and he earns
 * the base rate while the stake he is thinking of sits one wallet over. Buyers
 * also split a stake across several wallets — a hardware wallet, a phone, an old
 * address — so an account can link MORE THAN ONE, and this card does three jobs:
 *
 *  1. It always says which wallets earn and which wallet is paid, so the two can
 *     never be confused again.
 *  2. It shows the total those wallets add up to, and says plainly that the
 *     staker base is counted once however many wallets are linked: linking more
 *     wallets adds their stake, it does not multiply the rate.
 *  3. It lets him link and remove wallets after proving ownership of each with a
 *     Hub signature — the same proof login uses, on a challenge the backend
 *     binds to this purpose so a login signature cannot be replayed here.
 *
 * It also states plainly that cashback does not depend on the payment method:
 * the shop's home page invites buyers to "open in Nimiq Pay", and customers read
 * that as an eligibility rule. It never was — the rate comes from the stake
 * behind the earning wallets, whatever rail the order is paid on.
 */
import { useState } from 'react';
import { ApiError, friendlyApiMessage, unlinkStakeAddress } from '../../lib/api';
import { linkStakingWallet } from '../../lib/hub';
import { type MyStake, type StakeWalletRow, fmtStakeNIM } from '../../lib/stakerCashback';
import { POOL_VALIDATOR_NAME } from '../../lib/stake';
import { formatWalletAddress } from '../../lib/format';
import { useT } from '../../i18n';
import { Icon } from '../ui/Icon';
import { useToast } from '../AppProviders';

/** The server publishes its cap; this is only the fallback for a cached view
 *  that predates the field, so the UI never offers a link it cannot perform. */
const FALLBACK_WALLET_LIMIT = 5;

export function LinkedStakeWallet({ mine, onChanged }: { mine: MyStake | null; onChanged: () => void }) {
  const { t } = useT();
  const { toast } = useToast();
  const [open, setOpen] = useState(false);
  const [address, setAddress] = useState('');
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState('');

  const account = String(mine?.account_address || mine?.address || '');
  const limit = Number(mine?.linked_wallet_limit) > 0 ? Number(mine?.linked_wallet_limit) : FALLBACK_WALLET_LIMIT;

  /** The earning set. A cached or older view may not carry the breakdown yet,
   *  so fall back to the two addresses every response has always had. */
  const rows: StakeWalletRow[] = mine?.stake_addresses?.length
    ? mine.stake_addresses
    : account
      ? [{ address: account, linked: false, staked: !!mine?.staked, stake_nim: Number(mine?.stake_nim) || 0 }]
      : [];
  const linkedRows = rows.filter(row => row.linked);
  const linked = linkedRows.length > 0;
  const atLimit = linkedRows.length >= limit;
  const totalNim = rows.reduce((sum, row) => sum + (Number(row.stake_nim) || 0), 0);
  const pretty = (addr: string) => formatWalletAddress(addr);

  /** Server refusals are specific; naming the real reason beats a generic
   *  "could not link" that sends the buyer back to support. */
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
    const target = address.trim();
    if (!target || busy) return;
    setErr('');
    setBusy(true);
    try {
      const res = await linkStakingWallet(target);
      if (res && 'redirecting' in res) {
        // Hub took over the page (a phone browser): the pending challenge is in
        // sessionStorage and initHubRedirectHandling finishes the link on return.
        toast(t('stakeLink.redirect'), 'info');
        return;
      }
      const linkedAddr = String(res?.stake_address || target);
      toast(t('stakeLink.linked', { address: pretty(linkedAddr) }), 'success');
      setOpen(false);
      setAddress('');
      onChanged();
    } catch (e) {
      setErr(explain(e));
    } finally {
      setBusy(false);
    }
  }

  /** Removing one wallet leaves the others earning; removing all of them puts
   *  the account back on its own wallet. Neither touches cashback already paid. */
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
      <div className="row between">
        <h3>{t('stakeLink.title')}</h3>
        {linkedRows.length > 1 && (
          <button
            className="btn btn-sm btn-ghost"
            onClick={() => {
              void unlink();
            }}
            disabled={busy}
            data-testid="stake-link-unlink-all"
          >
            <Icon name="x" size={14} />
            {t('stakeLink.unlinkAll')}
          </button>
        )}
      </div>

      {/* Which wallets earn, and which wallet is paid. Both, always. */}
      <p className="small" data-testid="stake-link-earning">
        {linked
          ? t('stakeLink.summaryLinked', { count: rows.length, total: fmtStakeNIM(totalNim) })
          : t('stakeLink.summarySelf', { address: pretty(account) })}
      </p>
      {linked && (
        <p className="small muted" data-testid="stake-link-paid-to">
          {t('stakeLink.paidTo', { address: pretty(account) })}
        </p>
      )}

      {/* The payment-method myth, answered where the buyer is looking at his rate. */}
      <div className="cb-link-note mt-1" data-testid="stake-link-rail">
        <Icon name="info" size={16} />
        <div className="small">{t('stakeLink.anyRail')}</div>
      </div>

      {rows.length > 0 && (
        <ul
          className="mt-1"
          data-testid="stake-link-wallets"
          style={{ listStyle: 'none', margin: 0, padding: 0, display: 'flex', flexDirection: 'column', gap: 6 }}
        >
          {rows.map(row => (
            <li
              key={row.address}
              data-testid="stake-link-wallet"
              style={{ display: 'flex', alignItems: 'center', gap: 8, flexWrap: 'wrap' }}
            >
              <Icon name={row.linked ? 'link' : 'user'} size={14} />
              <span className="small mono" style={{ overflowWrap: 'anywhere' }}>
                {pretty(row.address)}
              </span>
              <span className="xs faint">
                {row.linked ? t('stakeLink.linkedBadge') : t('stakeLink.ownWallet')}
              </span>
              <span className="xs muted" data-testid="stake-link-wallet-stake">
                {Number(row.stake_nim) > 0
                  ? t('stakeLink.walletStake', { nim: fmtStakeNIM(Number(row.stake_nim) || 0) })
                  : row.other_validator
                    ? t('stakeLink.walletElsewhere')
                    : t('stakeLink.walletNoStake')}
              </span>
              {row.linked && (
                <button
                  className="btn btn-sm btn-ghost"
                  style={{ marginLeft: 'auto' }}
                  onClick={() => {
                    void unlink(row.address);
                  }}
                  disabled={busy}
                  data-testid="stake-link-unlink"
                >
                  <Icon name="x" size={13} />
                  {t('stakeLink.unlink')}
                </button>
              )}
            </li>
          ))}
        </ul>
      )}

      {linked && (
        <>
          <p className="small strong mt-1" data-testid="stake-link-total">
            {t('stakeLink.totalStake', { nim: fmtStakeNIM(totalNim) })}
          </p>
          {/* The rule that keeps linking honest: more wallets add stake, they do
              not multiply the base rate. Stated here because a buyer who is
              told "link your wallets" will otherwise reasonably ask why. */}
          <p className="xs faint">{t('stakeLink.baseOnce')}</p>
          <p className="xs faint">{t('stakeLink.signedHint')}</p>
        </>
      )}

      <p className="xs muted mt-1" data-testid="stake-link-count">
        {t('stakeLink.count', { n: linkedRows.length, max: limit })}
      </p>

      {atLimit ? (
        <div className="alert warn mt-1" data-testid="stake-link-limit">
          <Icon name="alert" size={18} />
          <div className="small">{t('stakeLink.limitReached', { max: limit })}</div>
        </div>
      ) : open ? (
        <div className="mt-1">
          <label className="xs muted" htmlFor="stake-link-address">
            {t('stakeLink.addressLabel')}
          </label>
          <input
            id="stake-link-address"
            className="input mono"
            value={address}
            placeholder={t('stakeLink.addressPlaceholder')}
            autoComplete="off"
            spellCheck={false}
            onChange={event => setAddress(event.target.value)}
            data-testid="stake-link-address"
          />
          <p className="xs faint">{t('stakeLink.signHint')}</p>
          <div className="row" style={{ gap: 8 }}>
            <button
              className="btn btn-gold"
              onClick={() => {
                void link();
              }}
              disabled={busy || !address.trim()}
              data-testid="stake-link-confirm"
            >
              <Icon name="lock" size={14} />
              {busy ? t('stakeLink.busy') : t('stakeLink.confirm')}
            </button>
            <button
              className="btn btn-ghost"
              onClick={() => {
                setOpen(false);
                setErr('');
              }}
              disabled={busy}
            >
              {t('stakeLink.cancel')}
            </button>
          </div>
        </div>
      ) : (
        <div className="mt-1">
          <p className="small muted">{t('stakeLink.why')}</p>
          <button
            className="btn btn-outline btn-block"
            onClick={() => setOpen(true)}
            disabled={busy}
            data-testid="stake-link-open"
          >
            <Icon name="wallet" size={14} />
            {linked ? t('stakeLink.addAnother') : t('stakeLink.openLink')}
          </button>
        </div>
      )}

      {err && (
        <div className="alert error mt-1" role="alert" data-testid="stake-link-error">
          <Icon name="alert" size={18} />
          <div className="small">{err}</div>
        </div>
      )}
    </section>
  );
}
