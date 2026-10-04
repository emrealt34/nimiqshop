/**
 * stake.ts — every Nimiq staking operation a buyer can perform from the shop,
 * through the provider Nimiq Pay injects into mini apps.
 *
 * The six provider methods (all user-confirmed by a native dialog, keys never
 * leave the wallet):
 *
 *   sendNewStakerTransaction({delegation, value})       first-time delegation
 *   sendStakeTransaction({value})                       add to an existing stake
 *   sendSetActiveStakeTransaction({newActiveBalance})   raise/lower active stake
 *   sendUpdateStakerTransaction({newDelegation, …})     move to another validator
 *   sendRetireStakeTransaction({retireStake})           retire stake (cooling off)
 *   sendRemoveStakeTransaction({value})                 withdraw retired stake
 *
 * Everything is expressed in NIM at this boundary and converted to Luna here —
 * a 1000× mistake in the UI would otherwise be invisible until the chain
 * rejected it. All values are validated before the dialog is ever shown.
 *
 * Nothing here reads stake: the shop asks the operator's own pool
 * (GET /api/poolstake/me), never the chain. See lib/stakerCashback.ts.
 */
import { getNimiqProvider, inNimiqPay, initNimiqMiniApp } from './miniapp';
import { t as tr } from '../i18n';
import { asset } from './asset';

export const LUNA_PER_NIM = 100_000;

/**
 * The pool's minimum stake: 100 NIM on every lane, the same rule the pool
 * site enforces. nimToLuna() already rejects zero/negative/non-numeric
 * amounts; this is the floor on top of it.
 */
export const MIN_STAKE_NIM = 100;

/** Where a browser user is sent when the shop is not running inside Nimiq Pay. */
export const NIMIQ_WALLET_URL = 'https://wallet.nimiq.com/';

/**
 * Our pool's name as it appears in the Nimiq Wallet's validator list. The
 * wallet guide always names it, even when POOL_VALIDATOR_ADDRESS is not
 * configured — a staker must never have to "ask the shop".
 */
export const POOL_VALIDATOR_NAME = 'Nimiq Base Staking';
/**
 * The pool's badge (orange flame in the hexagon), served from public/. Shown
 * next to the validator name in the staking guide so a user picking a validator
 * in the Nimiq wallet can match it by sight and never delegates to a stranger.
 */
export const POOL_VALIDATOR_BADGE = asset('/img/validator-nimiq-base-staking.png');

export class StakeCancelledError extends Error {
  constructor(message = tr('stake.cancelled')) {
    super(message);
    this.name = 'StakeCancelledError';
  }
}

export class StakeUnsupportedError extends Error {
  constructor(message: string) {
    super(message);
    this.name = 'StakeUnsupportedError';
  }
}

/**
 * The chain/wallet rejected the transaction as INVALID (InvalidTransactionError
 * in the provider API) — e.g. a staker already exists for this address, the
 * active balance is impossible, or retired stake is still cooling off. The UI
 * shows the message and, for newStaker, points at the "move" flow: a wallet
 * that already stakes with another validator can never send a NEW staker.
 */
export class StakeInvalidError extends Error {
  constructor(message: string) {
    super(message);
    this.name = 'StakeInvalidError';
  }
}

/** The six operations, as one discriminated union the UI can drive. */
export type StakeOp =
  | { kind: 'newStaker'; delegation: string; amountNIM: number }
  | { kind: 'addStake'; amountNIM: number }
  | { kind: 'setActiveStake'; activeNIM: number }
  | { kind: 'changeDelegation'; delegation: string; reactivateAll?: boolean }
  | { kind: 'retireStake'; amountNIM: number }
  | { kind: 'removeStake'; amountNIM: number };

/** Provider method backing each operation — one place to keep them in sync. */
export const OP_METHOD: Record<StakeOp['kind'], string> = {
  newStaker: 'sendNewStakerTransaction',
  addStake: 'sendStakeTransaction',
  setActiveStake: 'sendSetActiveStakeTransaction',
  changeDelegation: 'sendUpdateStakerTransaction',
  retireStake: 'sendRetireStakeTransaction',
  removeStake: 'sendRemoveStakeTransaction',
};

/** Human label for a confirmation line / receipt. Translated at CALL time so a
 *  language switch mid-session is honoured (the cashback page shows the same
 *  six labels under cashback.op*). */
const OP_KEY: Record<StakeOp['kind'], string> = {
  newStaker: 'cashback.opNewStaker',
  addStake: 'cashback.opAddStake',
  setActiveStake: 'cashback.opSetActiveStake',
  changeDelegation: 'cashback.opChangeDelegation',
  retireStake: 'cashback.opRetireStake',
  removeStake: 'cashback.opRemoveStake',
};

export function opLabel(kind: StakeOp['kind']): string {
  return tr(OP_KEY[kind] || OP_KEY.newStaker);
}

/* ------------------------------------------------------------------ values */

/** NIM → Luna, or null when the input is not a usable positive amount. */
export function nimToLuna(nim: number): number | null {
  if (!Number.isFinite(nim) || nim <= 0) return null;
  const luna = Math.round(nim * LUNA_PER_NIM);
  return Number.isFinite(luna) && luna >= 1 ? luna : null;
}

/** Luna → NIM for display. */
export function lunaToNIM(luna: number): number {
  return Number.isFinite(luna) ? luna / LUNA_PER_NIM : 0;
}

/** Compact form of a Nimiq address: no spaces, upper case. */
export function compactAddress(addr: string): string {
  return String(addr || '').replace(/\s+/g, '').toUpperCase();
}

function mod97(digits: string): number {
  let rem = 0;
  for (let i = 0; i < digits.length; i++) rem = (rem * 10 + digits.charCodeAt(i) - 48) % 97;
  return rem;
}

/**
 * ISO 7064 mod 97-10 checksum, the same one the backend uses
 * (internal/nimiq/address.go): move the first four characters to the end,
 * map letters to digits, mod 97, 98 − remainder.
 */
export function iso7064Check(rearranged: string): string {
  const moved = rearranged.slice(4) + rearranged.slice(0, 4);
  let numeric = '';
  for (const c of moved) {
    const code = c.charCodeAt(0);
    if (code >= 48 && code <= 57) numeric += c;
    else if (code >= 65 && code <= 90) numeric += String(code - 65 + 10);
  }
  return String(98 - mod97(numeric)).padStart(2, '0');
}

/**
 * True when the string is a valid Nimiq user-friendly address: 36 characters
 * compact, "NQ" prefix, correct checksum. Mirrors the backend validator so the
 * two sides can never disagree about what is sendable.
 */
export function isValidNimiqAddress(addr: string): boolean {
  const norm = compactAddress(addr);
  if (norm.length !== 36 || !norm.startsWith('NQ')) return false;
  if (!/^[0-9A-Z]+$/.test(norm)) return false;
  return iso7064Check('NQ00' + norm.slice(4)) === norm.slice(2, 4);
}

/**
 * The address to delegate to, or '' when it is not a real address.
 *
 * This gate exists because the delegation target is the one field a staking
 * transaction cannot recover from: send it somewhere wrong and the stake is
 * delegated to a stranger. Garbage in, nothing sent.
 */
export function normalizeValidator(addr: string): string {
  const norm = compactAddress(addr);
  return isValidNimiqAddress(norm) ? norm : '';
}

/* --------------------------------------------------------------- providers */

function provider(): any {
  return getNimiqProvider();
}

/** True when the shop runs inside Nimiq Pay and a provider is present. */
export function inNimiqPayWithProvider(): boolean {
  return inNimiqPay() && !!provider();
}

/**
 * True when this specific operation can be sent right now. Before `init()`
 * resolves, Pay may know the host but not have exposed the returned provider
 * yet. Stay optimistic in that one state so the button is not permanently
 * disabled; runStakeOp awaits the official SDK provider and gives a precise
 * unsupported error if the host truly lacks the method.
 */
export function supportsOp(op: StakeOp): boolean {
  if (!inNimiqPay()) return false;
  const p = provider();
  return !p || typeof p[OP_METHOD[op.kind]] === 'function';
}

async function ensureConnected(p: any): Promise<void> {
  if (p.connected === false && typeof p.connect === 'function') await p.connect();
}

/* -------------------------------------------------------------- validation */

/**
 * Validates an operation and returns the exact provider arguments. Pure, so
 * it is unit-testable and so an invalid operation is rejected BEFORE the user
 * is shown a wallet dialog.
 */
export function buildArgs(op: StakeOp): { method: string; args: Record<string, unknown> } {
  switch (op.kind) {
    case 'newStaker': {
      const delegation = normalizeValidator(op.delegation);
      if (!delegation) throw new Error(tr('stake.noPoolValidator'));
      const value = nimToLuna(op.amountNIM);
      if (value === null) throw new Error(tr('stake.enterStakeAmount'));
      if (op.amountNIM < MIN_STAKE_NIM) throw new Error(tr('stake.minimumStake', { nim: String(MIN_STAKE_NIM) }));
      return { method: OP_METHOD.newStaker, args: { delegation, value } };
    }
    case 'addStake': {
      const value = nimToLuna(op.amountNIM);
      if (value === null) throw new Error(tr('stake.enterAmount'));
      return { method: OP_METHOD.addStake, args: { value } };
    }
    case 'setActiveStake': {
      // 0 is meaningful here: it deactivates the whole stake.
      if (!Number.isFinite(op.activeNIM) || op.activeNIM < 0) throw new Error(tr('stake.enterActiveStake'));
      return { method: OP_METHOD.setActiveStake, args: { newActiveBalance: Math.round(op.activeNIM * LUNA_PER_NIM) } };
    }
    case 'changeDelegation': {
      const delegation = normalizeValidator(op.delegation);
      if (!delegation) throw new Error(tr('stake.enterValidatorAddress'));
      const args: Record<string, unknown> = { newDelegation: delegation };
      if (op.reactivateAll) args.reactivateAllStake = true;
      return { method: OP_METHOD.changeDelegation, args };
    }
    case 'retireStake': {
      const retireStake = nimToLuna(op.amountNIM);
      if (retireStake === null) throw new Error(tr('stake.enterAmount'));
      return { method: OP_METHOD.retireStake, args: { retireStake } };
    }
    case 'removeStake': {
      const value = nimToLuna(op.amountNIM);
      if (value === null) throw new Error(tr('stake.enterAmount'));
      return { method: OP_METHOD.removeStake, args: { value } };
    }
    default: {
      // Exhaustiveness: adding a new op without a case is a compile error.
      const never: never = op;
      throw new Error(tr('stake.unknownOp', { op: JSON.stringify(never) }));
    }
  }
}

function isCancellation(e: unknown): boolean {
  const msg = String((e as Error)?.message || e || '');
  return /cancel|rejected|denied|abort/i.test(msg);
}

/**
 * Maps a provider failure (error type + message — resolved object, thrown
 * Error, either) to the error the UI should show. Pure, so every branch is
 * unit-testable and both runStakeOp paths stay in lockstep.
 *
 *   PermissionDeniedError → StakeCancelledError  (the user said no — not an error)
 *   InvalidTransactionError → StakeInvalidError  (the chain refused — with the
 *     move-to-our-pool guidance on a failed first delegation, the one case a
 *     user cannot solve by retrying the same button)
 *   anything else → the provider's own message
 */
export function classifyStakeError(type: string, message: string, opKind?: StakeOp['kind']): Error {
  const t = String(type || '');
  const m = String(message || '');
  if (/PermissionDenied/i.test(t) || isCancellation(t + ' ' + m)) return new StakeCancelledError();
  if (/InvalidTransaction/i.test(t)) {
    if (opKind === 'newStaker') {
      return new StakeInvalidError(tr('stake.alreadyStaker', { detail: m ? ` (${m})` : '' }));
    }
    return new StakeInvalidError(tr('stake.rejectedInvalid', { detail: m ? `: ${m}` : '.' }));
  }
  return new Error(m || t || tr('stake.errorGeneric'));
}

/**
 * The provider's wallet methods are typed `Promise<string | ErrorResponse>`
 * (see @nimiq/mini-app-sdk provider.d.ts): a Nimiq Pay build may resolve with
 * `{ error: { type, message } }` instead of throwing — the SDK's own
 * `listAccounts()` does exactly that. Left unchecked, a user rejection would
 * flow through as a "successful" stake whose transaction hash is the string
 * "[object Object]" and trigger the post-stake announce for a tx that never
 * happened. Any resolved object with a truthy `error` is a failure.
 */
function isProviderErrorResponse(
  r: unknown
): r is { error: { type?: string; message?: string } } {
  return !!r && typeof r === 'object' && !!(r as { error?: unknown }).error;
}

/* --------------------------------------------------------------- execution */

/**
 * Sends one staking operation through Nimiq Pay and resolves with the
 * transaction hash once it has been broadcast.
 *
 * Rejections are a normal outcome, not a failure: they surface as
 * StakeCancelledError so the UI can say "you cancelled" instead of showing a
 * scary error. A missing provider method surfaces as StakeUnsupportedError.
 */
export async function runStakeOp(op: StakeOp): Promise<string> {
  const { method, args } = buildArgs(op); // throws before any dialog
  // The SDK contract is `const nimiq = await init()`. Use that resolved
  // provider as well as the legacy window.nimiq mirror; otherwise a host that
  // injects the provider only through init() would make every stake button
  // report "unsupported" even though the wallet supports the method.
  const direct = provider();
  const p: any = direct || (await initNimiqMiniApp()) || provider();
  if (!p) {
    throw new StakeUnsupportedError(tr('stake.openInPay'));
  }
  if (typeof (p as any)[method] !== 'function') {
    throw new StakeUnsupportedError(
      tr('stake.unsupportedOp', { op: opLabel(op.kind) })
    );
  }
  await ensureConnected(p);
  try {
    const res = await p[method](args);
    // Resolved error object (not a thrown one): PermissionDeniedError is
    // still just the user saying no, anything else is a real failure.
    if (isProviderErrorResponse(res)) {
      const errType = String(res.error?.type || '');
      const errMsg = String(res.error?.message || errType || tr('stake.errorGeneric'));
      throw classifyStakeError(errType, errMsg, op.kind);
    }
    if (!res) throw new Error(tr('stake.noTxHash'));
    return String(res);
  } catch (e) {
    if (e instanceof StakeCancelledError || e instanceof StakeInvalidError) throw e; // already classified above
    // Thrown path: the SDK/older hosts reject instead of resolving an error
    // object — the error NAME often carries the provider type.
    const name = String((e as Error)?.name || '');
    const msg = String((e as Error)?.message || e || '');
    if (/PermissionDenied|InvalidTransaction/i.test(name) || isCancellation(msg)) {
      throw classifyStakeError(name || msg, msg, op.kind);
    }
    throw e;
  }
}

/* Named wrappers — the call sites read better than an inline op object. */
export const newStaker = (delegation: string, amountNIM: number) =>
  runStakeOp({ kind: 'newStaker', delegation, amountNIM });
export const addStake = (amountNIM: number) => runStakeOp({ kind: 'addStake', amountNIM });
export const setActiveStake = (activeNIM: number) => runStakeOp({ kind: 'setActiveStake', activeNIM });
export const changeDelegation = (delegation: string, reactivateAll = false) =>
  runStakeOp({ kind: 'changeDelegation', delegation, reactivateAll });
export const retireStake = (amountNIM: number) => runStakeOp({ kind: 'retireStake', amountNIM });
export const removeStake = (amountNIM: number) => runStakeOp({ kind: 'removeStake', amountNIM });

/**
 * True when Nimiq Pay has finished syncing with the network. Best-effort and
 * OPTIMISTIC: an old host without isConsensusEstablished(), or a failed check,
 * returns true — the note is informational and must never block a stake the
 * wallet itself is willing to queue.
 */
export async function consensusEstablished(): Promise<boolean> {
  const p = provider() as unknown as { isConsensusEstablished?: () => Promise<unknown> };
  if (!p || typeof p.isConsensusEstablished !== 'function') return true;
  try {
    return (await p.isConsensusEstablished()) === true;
  } catch {
    return true;
  }
}

/**
 * For a wallet that already stakes with ANOTHER validator: sendNewStakerTransaction
 * would be rejected (a staker exists), so the only correct way in is
 * sendUpdateStakerTransaction — move the delegation to our pool and reactivate
 * everything. The stake itself never leaves the wallet.
 *
 * No announcement to the shop is made (or accepted): a buyer's claim of
 * "I've staked" is not evidence. The pool is the single source — until it
 * indexes the moved delegation the buyer simply is not staked yet, and the
 * shop's post-fulfillment pool re-check upgrades any underpaid cashback
 * within minutes of the index catching up.
 */
export async function moveStakeToPool(validator: string): Promise<string> {
  return changeDelegation(validator, true);
}

/**
 * First delegation or top-up.
 *
 * No announcement to the shop is made (or accepted): buyer claims are not
 * evidence. The pool indexes the delegation on its next pass; the shop's
 * post-fulfillment pool re-check re-asks every minute for an hour, so a real
 * staker's cashback is upgraded trust-free within minutes.
 */
export async function stakeWithUs(validator: string, amountNIM: number, alreadyStaked: boolean): Promise<string> {
  try {
    return alreadyStaked ? await addStake(amountNIM) : await newStaker(validator, amountNIM);
  } catch (e) {
    // The CHAIN, not the pool index, is the authority on whether a staker
    // exists — `alreadyStaked` comes from the pool's latest pass and can be
    // stale. One direction can be fixed without changing what the buyer asked
    // for: the pool believed there was a stake, the chain says there is none,
    // so the same amount goes out as the FIRST delegation. This is what used
    // to read as "the button behaves randomly": a freshly staked wallet whose
    // pool index had not caught up got an add-stake op the chain rejected.
    // The reverse (a new staker rejected because one already exists) cannot be
    // fixed silently — the buyer asked to ADD, and moving an existing
    // delegation to our pool is a different, explicit action — so that one
    // keeps its guided error and the move flow on the cashback page.
    if (e instanceof StakeInvalidError && alreadyStaked) {
      return newStaker(validator, amountNIM);
    }
    throw e;
  }
}

/**
 * Short helper for the UI guard. A provider returned only by SDK `init()` may
 * not be visible synchronously yet, so Pay is enough to keep the action
 * enabled; runStakeOp performs the awaited provider/method check.
 */
export function canStakeInApp(validator: string): boolean {
  return !!normalizeValidator(validator) && inNimiqPay();
}

/* ------------------------------------------------- browser (non-Pay) route */

/**
 * What to show a browser user who is NOT inside Nimiq Pay. They cannot sign
 * here, so the shop points them at the official wallet and tells them exactly
 * which validator to pick — by NAME. The wallet's validator list is picked by
 * name, so the raw NQ address is deliberately NOT shown here (it is still
 * returned as `validator` for the in-app delegation flow).
 */
export function walletStakeGuide(validator: string): { url: string; validator: string; steps: string[] } {
  const v = normalizeValidator(validator);
  return {
    url: NIMIQ_WALLET_URL,
    validator: v,
    steps: [
      tr('cashback.guideStep1'),
      tr('cashback.guideStep2', { validator: POOL_VALIDATOR_NAME }),
      tr('cashback.guideStep3'),
      tr('cashback.guideStep4'),
    ],
  };
}
