/**
 * QuickStake — the one-tap "stake right here, right now" widget.
 *
 * The buyer meets it at the two moments the shop has their full attention
 * and the USDT discount is on the table: the USDT cashback popup at the
 * payment picker, and the USDT pay screen. Inside Nimiq Pay the provider
 * signs with a single confirmation dialog, so the whole flow is: tap a
 * preset → tap Stake. The widget handles the same walls the Cashback page
 * already knows about:
 *
 *   - a wallet already staking with ANOTHER validator → offer the MOVE flow
 *     (sendUpdateStakerTransaction + reactivateAllStake), never a doomed
 *     sendNewStakerTransaction;
 *   - a browser outside Nimiq Pay → no signing here, link the Cashback page
 *     (which carries the wallet guide);
 *   - signed out → say so, keep it one line.
 *
 * On success stakeWithUs() already announces the stake to the shop (the
 * loyalty bridge over the pool's index latency) and the parent can refresh
 * whatever numbers it is showing via onStaked().
 */
import { useEffect, useState } from 'react';
import { useSession } from '../../lib/useSession';
import { useInNimiqPay } from '../../lib/miniapp';
import {
  loadStakerProgram,
  loadMyStake,
  refreshMyStake,
  fmtStakeNIM,
  pctLabel,
  type MyStake,
  type StakerProgram,
} from '../../lib/stakerCashback';
import {
  MIN_STAKE_NIM,
  POOL_VALIDATOR_BADGE,
  POOL_VALIDATOR_NAME,
  StakeCancelledError,
  StakeInvalidError,
  StakeUnsupportedError,
  moveStakeToPool,
  stakeWithUs,
  walletStakeGuide,
} from '../../lib/stake';
import { useToast } from '../AppProviders';
import { useT, rich } from '../../i18n';
import { Icon } from '../ui/Icon';
import { pagePath } from '../../lib/asset';

const chipBtn = {
  border: '1.5px solid var(--line-strong,#4E3D28)',
  borderRadius: 999,
  background: 'var(--panel-2)',
  padding: '6px 12px',
  fontSize: 13,
  fontWeight: 700,
  cursor: 'pointer',
  lineHeight: 1.2,
} as const;

export function QuickStake({
  onStaked,
  collapsed = false,
}: {
  /** Called with the refreshed standing after a successful stake/move. */
  onStaked?: (m: MyStake | null) => void;
  /** Pay screens start with a single "Stake NIM now" button; the full form
   *  reveals on tap so the payment UI stays uncluttered. */
  collapsed?: boolean;
}) {
  const authed = useSession();
  const { toast } = useToast();
  const { t } = useT();
  const inPay = useInNimiqPay();

  const [program, setProgram] = useState<StakerProgram | null>(null);
  const [mine, setMine] = useState<MyStake | null>(null);
  const [amount, setAmount] = useState('');
  const [busy, setBusy] = useState(false);
  const [moving, setMoving] = useState(false);
  const [err, setErr] = useState('');
  // Revealed when a first delegation is rejected because the wallet already
  // stakes elsewhere — the move flow is the way in, retrying is not.
  const [moveHint, setMoveHint] = useState(false);
  const [done, setDone] = useState(false);
  const [open, setOpen] = useState(!collapsed);

  useEffect(() => {
    let alive = true;
    loadStakerProgram().then((p) => {
      if (alive) setProgram(p);
    });
    return () => {
      alive = false;
    };
  }, []);

  useEffect(() => {
    let alive = true;
    if (!authed || !program?.enabled) return;
    loadMyStake().then((m) => {
      if (!alive) return;
      setMine(m);
      // Sensible defaults: a staked wallet tops up (+100), a fresh one goes
      // straight to a meaningful 1000 NIM stake.
      setAmount(m?.staked && (m.stake_luna || 0) > 0 ? '100' : '1000');
    });
    return () => {
      alive = false;
    };
  }, [authed, program?.enabled]);

  if (!program?.enabled) return null;
  // useSession() is null until mounted: render nothing rather than flashing
  // the signed-out branch on every navigation (same rule as StakerCashback).
  if (authed === null) return null;

  const validator = String(mine?.pool_validator_address || program.validator || '');
  const stakedHere = !!mine?.staked && (mine?.stake_luna || 0) > 0;
  const presets = stakedHere ? [100, 500, 1000, 5000] : [100, 500, 1000, 5000];
  const amountNIM = Number(amount);

  async function afterSuccess(hash: string) {
    toast(t('checkout.qsStakeSent', { hash: String(hash).slice(0, 10) }), 'success');
    setDone(true);
    const fresh = await refreshMyStake().catch(() => null);
    if (fresh) setMine(fresh);
    onStaked?.(fresh);
    if (fresh?.staked) toast(t('checkout.qsStakerActive', { pct: pctLabel(fresh.cashback_bps) }), 'success');
    else toast(t('checkout.qsPoolNextPass'), 'info');
  }

  async function doStake() {
    setErr('');
    setDone(false);
    if (!Number.isFinite(amountNIM) || !(amountNIM > 0)) {
      setErr(t('checkout.qsErrAmountZero'));
      return;
    }
    if (!stakedHere && amountNIM < MIN_STAKE_NIM) {
      setErr(t('cashback.errFirstStakeMin', { nim: String(MIN_STAKE_NIM) }));
      return;
    }
    setBusy(true);
    try {
      const hash = await stakeWithUs(validator, amountNIM, stakedHere);
      await afterSuccess(hash);
    } catch (e) {
      if (e instanceof StakeCancelledError) setErr(e.message);
      else if (e instanceof StakeUnsupportedError) setErr(e.message);
      else if (e instanceof StakeInvalidError) {
        setErr(e.message);
        // A rejected FIRST delegation = the wallet already has a staker
        // (usually with another validator): the move flow is the way in.
        if (!stakedHere) setMoveHint(true);
      } else setErr((e as Error).message || t('cashback.toastTxFailed'));
    } finally {
      setBusy(false);
    }
  }

  async function doMove() {
    setErr('');
    setDone(false);
    setMoving(true);
    try {
      const hash = await moveStakeToPool(validator);
      toast(t('checkout.qsMoveSent', { hash: String(hash).slice(0, 10) }), 'success');
      setDone(true);
      const fresh = await refreshMyStake().catch(() => null);
      if (fresh) setMine(fresh);
      onStaked?.(fresh);
      toast(t('checkout.qsPoolDelegation'), 'info');
    } catch (e) {
      if (e instanceof StakeCancelledError) setErr(e.message);
      else setErr((e as Error).message || t('checkout.qsMoveFailed'));
    } finally {
      setMoving(false);
    }
  }

  /* ------------------------------------------------------- signed out */
  if (!authed) {
    return (
      <div className="small muted" style={{ display: 'flex', gap: 8, alignItems: 'flex-start' }}>
        <Icon name="wallet" size={16} />
        <span>
          Connect your Nimiq wallet (top bar) first — staking inside Nimiq Pay is one tap after that.{' '}
          <a href={pagePath("/cashback")}>{t('checkout.qsHowStaking')}</a>
        </span>
      </div>
    );
  }

  /* ------------------------------------------------- browser, not Pay */
  if (!inPay) {
    // In a browser nothing can be signed here, so the push is the WALLET
    // route: the same steps, badge and pool name the Cashback page shows,
    // plus the claim below — the buyer stakes in the wallet and tells the
    // shop, so the pending-stake note bridges the pool's index latency and
    // their very first order still earns the staker rate.
    const guide = walletStakeGuide(validator);
    const body = (
      <>
        <ol className="small" style={{ margin: '6px 0 0', paddingLeft: 18 }}>
          {guide.steps.map((s) => (
            <li key={s}>{s}</li>
          ))}
        </ol>
        <div
          style={{
            display: 'flex',
            alignItems: 'center',
            gap: 10,
            margin: '10px 0',
            padding: '8px 10px',
            border: '1px solid var(--line, #e6e2d8)',
            borderRadius: 10,
            background: 'var(--panel-2)',
          }}
        >
          <img src={POOL_VALIDATOR_BADGE} alt={t('checkout.qsValidatorBadgeAlt')} width={34} height={34} style={{ borderRadius: 8, background: 'var(--white-card)', flex: '0 0 auto' }} />
          <div className="small">
            <strong>{POOL_VALIDATOR_NAME}</strong> {t('checkout.qsValidatorPick')}
          </div>
        </div>
        {/* No claims, no trust: a wallet stake is picked up from the shop's
            own pool automatically — every delivered order is re-checked for
            an hour and upgraded the moment the pool reports the stake. */}
        <div className="xs faint mt-2" style={{ display: 'flex', gap: 6, alignItems: 'flex-start' }}>
          <Icon name="check" size={13} />
          <span>
            {t('checkout.qsStakedNote')}
          </span>
        </div>
        <a className="btn btn-sm mt-2" href={guide.url} target="_blank" rel="noreferrer noopener">
          <Icon name="external" size={14} /> {t('checkout.qsOpenApp')}
        </a>
      </>
    );
    if (collapsed) {
      return (
        <details style={{ borderTop: '1px dashed var(--line-dash,#e0d7c2)', paddingTop: 8 }}>
          <summary className="small strong" style={{ cursor: 'pointer' }}>
            <Icon name="bolt" size={13} /> {t('checkout.qsSummaryStake', { validator: POOL_VALIDATOR_NAME })}
          </summary>
          <div className="mt-2">{body}</div>
        </details>
      );
    }
    return (
      <div className="small muted" style={{ display: 'flex', gap: 8, alignItems: 'flex-start' }}>
        <img src={POOL_VALIDATOR_BADGE} alt="" width={22} height={22} style={{ borderRadius: 5, background: 'var(--white-card)', flex: 'none' }} />
        <div style={{ flex: 1, minWidth: 0 }}>
          {t('checkout.qsBrowserIntro')}
          {body}
        </div>
      </div>
    );
  }

  /* ------------------------------------------- inside Nimiq Pay: form */
  if (!open) {
    return (
      <button type="button" className="btn btn-outline btn-block" onClick={() => setOpen(true)}>
        <Icon name="bolt" size={14} /> {t('checkout.qsStakeNow')}
      </button>
    );
  }

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
      {stakedHere && (
        <div className="xs faint" style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
          <Icon name="check" size={13} />
          <span>
            {rich(t('checkout.qsYouStake', {
              nim: fmtStakeNIM(mine?.stake_nim || 0),
              pct: pctLabel(mine?.cashback_bps || 0),
            }))}
          </span>
        </div>
      )}

      <div style={{ display: 'flex', gap: 6, flexWrap: 'wrap' }}>
        {presets.map((n) => (
          <button
            key={n}
            type="button"
            style={{ ...chipBtn, ...(amountNIM === n ? { background: 'var(--gold)', color: 'var(--on-gold)', borderColor: 'var(--gold,#c28a2a)' } : {}) }}
            onClick={() => {
              setAmount(String(n));
              setErr('');
            }}
          >
            {stakedHere ? `+${n.toLocaleString('en-US')}` : n.toLocaleString('en-US')}
          </button>
        ))}
      </div>

      <div className="cb-form-row quick-stake-form-row">
        <input
          className="input"
          type="number"
          inputMode="decimal"
          min={stakedHere ? 1 : MIN_STAKE_NIM}
          step="1"
          value={amount}
          onChange={(e) => setAmount(e.target.value)}
          aria-label={t('checkout.qsStakeAmountAria')}
          style={{ flex: 1, minWidth: 0 }}
        />
        <button className="btn btn-gold" style={{ flexShrink: 0 }} onClick={doStake} disabled={busy || moving || !validator}>
          <Icon name="bolt" size={14} />
          {busy
                        ? t('staker.waitingPay')
                        : stakedHere
                          ? t('staker.addStake')
                          : t('checkout.qsStakeValidator', { validator: POOL_VALIDATOR_NAME })}
        </button>
      </div>
      <div className="xs faint" style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
        <img src={POOL_VALIDATOR_BADGE} alt="" width={14} height={14} style={{ borderRadius: 3, background: 'var(--white-card)' }} />
        {stakedHere
                    ? t('cashback.formAddsExisting')
                    : t('checkout.qsDelegates', { validator: POOL_VALIDATOR_NAME, min: String(MIN_STAKE_NIM) })}
      </div>

      {moveHint && (
        <div style={{ borderTop: '1px dashed var(--line-dash,#e0d7c2)', paddingTop: 8 }}>
          <div className="xs faint">
            {t('checkout.qsMoveHint')}
          </div>
          <button type="button" className="btn btn-outline btn-block mt-1" onClick={doMove} disabled={busy || moving || !validator}>
            <Icon name="chevron" size={14} />
            {moving ? t('staker.waitingPay') : t('cashback.formMoveBtn', { validator: POOL_VALIDATOR_NAME })}
          </button>
        </div>
      )}

      {err && (
        <div className="alert error" style={{ marginBottom: 0 }}>
          <div className="small">{err}</div>
        </div>
      )}
      {done && !err && (
        <div className="small" style={{ color: 'var(--green,#2f5540)', display: 'flex', gap: 6, alignItems: 'center' }}>
          <Icon name="check" size={14} /> {t('checkout.qsStakedDone')}
        </div>
      )}
    </div>
  );
}
