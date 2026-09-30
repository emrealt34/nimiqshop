import { t as tr } from '../i18n';
/**
 * usdt.ts — paying USDT on Polygon from inside Nimiq Pay, through the EVM
 * provider Nimiq Pay injects (`window.ethereum`, EIP-1193).
 *
 * The model (per the official "Using EVM Tokens in Mini Apps" reference):
 *
 *   provider   window.ethereum            — injected by Nimiq Pay
 *   chain      Polygon, chain ID 0x89     — wallet_switchEthereumChain
 *   token      USDT (ERC-20, 6 decimals)  — 0xc2132D05D31c914a87C6611C10748AEb04B58e8F
 *
 * Every write (accounts, chain switch, transfer) triggers a native Nimiq Pay
 * confirmation dialog; keys never leave the wallet. Reads (eth_call) are
 * silent.
 *
 * Amounts are integer 6-decimal units held in BigInt end to end — never
 * floats. USDT uses 6 decimals (NOT 18): 1 USDT = 1_000_000 units. A
 * decimals mistake is a 10^12× error, so parsing is strict and unit-tested:
 * anything with a non-zero digit beyond the 6th decimal is rejected rather
 * than silently rounded (a payment must be EXACT — the supplier matches the
 * on-chain amount against coin_amount).
 *
 * The ERC-20 calldata is hand-encoded (selector + 32-byte padded args). It is
 * deterministic, dependency-free and pinned by unit tests against known
 * vectors — the same job viem's encodeFunctionData does in the docs, without
 * adding a dependency to the shop.
 */

/** Polygon mainnet chain ID, decimal and EIP-3085 hex forms. */
export const POLYGON_CHAIN_ID = 137;
export const POLYGON_CHAIN_ID_HEX = '0x89';

/** USDT (Tether) on Polygon mainnet. */
export const USDT_CONTRACT = '0xc2132D05D31c914a87C6611C10748AEb04B58e8F';
/** USDT uses 6 decimals — NOT the 18 most ERC-20s use. */
export const USDT_DECIMALS = 6;
/** 1 USDT in base units, as a string for display. */
export const USDT_UNITS_PER_TOKEN = 1_000_000;

/** Chain metadata for wallet_addEthereumChain (only used if the wallet does
 * not know Polygon yet — Nimiq Pay ships with Polygon in its list). */
export const POLYGON_CHAIN_PARAMS = {
  chainId: POLYGON_CHAIN_ID_HEX,
  chainName: 'Polygon',
  nativeCurrency: { name: 'POL', symbol: 'POL', decimals: 18 },
  rpcUrls: ['https://polygon-rpc.com'],
  blockExplorerUrls: ['https://polygonscan.com'],
} as const;

/** ERC-20 function selectors (keccak4 of the canonical signatures). */
const SELECTOR_BALANCE_OF = '0x70a08231'; // balanceOf(address)
const SELECTOR_TRANSFER = '0xa9059cbb'; // transfer(address,uint256)

/** The user closed the Nimiq Pay dialog — a normal outcome, not a failure. */
export class UsdtCancelledError extends Error {
  constructor(message = tr('usdt.cancelled')) {
    super(message);
    this.name = 'UsdtCancelledError';
  }
}

/* --------------------------------------------------------------- provider */

/** Nimiq Pay's injected EVM provider, or null outside Nimiq Pay / old builds. */
export function getEvmProvider(): unknown | null {
  if (typeof window === 'undefined') return null;
  return (window as unknown as { ethereum?: unknown }).ethereum || null;
}

/** True when the shop can pay USDT in-app right now. */
export function canPayUsdtInApp(): boolean {
  const p = getEvmProvider();
  return !!p && typeof (p as { request?: unknown }).request === 'function';
}

async function evmRequest<T = unknown>(p: unknown, method: string, params?: unknown[]): Promise<T> {
  const r = (p as { request: (a: { method: string; params?: unknown[] }) => Promise<T> }).request({
    method,
    params,
  });
  return await r;
}

/** EIP-1193 user-rejection: code 4001, or a wrapped message saying the same. */
export function isEvmRejection(e: unknown): boolean {
  const err = e as { code?: number | string; message?: string; data?: unknown };
  if (Number(err?.code) === 4001) return true;
  const msg = String(err?.message || e || '');
  return /user (rejected|denied)|cancelled|canceled|denied by user/i.test(msg);
}

/* ------------------------------------------------------------ amount math */

/**
 * Exact USDT amount → integer base units. Strict: accepts "10", "10.5",
 * "10.39000000" (trailing zeros beyond 6 dp are fine — suppliers send
 * fixed-point columns), but rejects "10.0000001" (real precision loss),
 * zero, negatives and junk. Never rounds a payment.
 */
export function parseUsdtUnits(amount: string | number): bigint {
  const s = String(amount ?? '').trim();
  const m = /^(\d+)(?:\.(\d+))?$/.exec(s);
  if (!m) throw new Error(tr('usdt.invalidAmount', { value: s }));
  const whole = m[1];
  let frac = m[2] || '';
  if (frac.length > USDT_DECIMALS) {
    if (/[1-9]/.test(frac.slice(USDT_DECIMALS))) {
      throw new Error(tr('usdt.tooManyDecimals', { decimals: String(USDT_DECIMALS) }));
    }
    frac = frac.slice(0, USDT_DECIMALS);
  }
  const units = BigInt(whole + frac.padEnd(USDT_DECIMALS, '0'));
  if (units <= 0n) throw new Error(tr('usdt.amountZero'));
  return units;
}

/** Base units → human string, trailing zeros trimmed ("10.5", "12", "0.000001"). */
export function formatUsdtUnits(units: bigint | string | number): string {
  let u: bigint;
  try {
    u = BigInt(units);
  } catch {
    return '0';
  }
  const neg = u < 0n;
  if (neg) u = -u;
  const whole = u / BigInt(USDT_UNITS_PER_TOKEN);
  const frac = (u % BigInt(USDT_UNITS_PER_TOKEN)).toString().padStart(USDT_DECIMALS, '0').replace(/0+$/, '');
  return (neg ? '-' : '') + whole.toString() + (frac ? '.' + frac : '');
}

/* -------------------------------------------------------- ERC-20 encoding */

/** Left-pad a 20-byte hex address to a 32-byte argument word. */
function padAddress(addr: string): string {
  const clean = String(addr).trim().replace(/^0x/i, '').toLowerCase();
  if (!/^[0-9a-f]{40}$/.test(clean)) throw new Error(tr('usdt.invalidAddress', { address: addr }));
  return clean.padStart(64, '0');
}

/** calldata for balanceOf(address) — a read, no confirmation dialog. */
export function encodeBalanceOf(account: string): string {
  return SELECTOR_BALANCE_OF + padAddress(account);
}

/** calldata for transfer(address to, uint256 amount) — moves the tokens. */
export function encodeTransfer(to: string, units: bigint): string {
  const amountHex = units.toString(16).padStart(64, '0');
  return SELECTOR_TRANSFER + padAddress(to) + amountHex;
}

/* ------------------------------------------------------------- chain ops */

/** Is the wallet already on Polygon? (eth_chainId — silent, no dialog.) */
export async function isOnPolygon(p: unknown): Promise<boolean> {
  try {
    const id = await evmRequest<string>(p, 'eth_chainId');
    return String(id).toLowerCase() === POLYGON_CHAIN_ID_HEX;
  } catch {
    return false;
  }
}

/**
 * Make sure the active chain is Polygon. Skips the dialog when already there;
 * on error 4902 (chain unknown) adds Polygon via wallet_addEthereumChain and
 * retries the switch once. Both dialogs are user-confirmed by Nimiq Pay.
 */
export async function ensurePolygonChain(p: unknown): Promise<void> {
  if (await isOnPolygon(p)) return;
  let switchErr: unknown = null;
  try {
    await evmRequest(p, 'wallet_switchEthereumChain', [{ chainId: POLYGON_CHAIN_ID_HEX }]);
    return;
  } catch (e) {
    switchErr = e;
  }
  const code = Number((switchErr as { code?: number | string })?.code);
  const msg = String((switchErr as Error)?.message || switchErr || '');
  const unrecognized = code === 4902 || /4902|unrecognized|unknown chain|not (been )?added|add.*chain/i.test(msg);
  if (!unrecognized) {
    if (isEvmRejection(switchErr)) throw new UsdtCancelledError(tr('usdt.switchCancelled'));
    throw new Error(tr('usdt.switchFailed', { reason: msg || 'unknown error' }));
  }
  await evmRequest(p, 'wallet_addEthereumChain', [{ ...POLYGON_CHAIN_PARAMS }]);
  await evmRequest(p, 'wallet_switchEthereumChain', [{ chainId: POLYGON_CHAIN_ID_HEX }]);
}

/* -------------------------------------------------------------- account */

/**
 * Ask Nimiq Pay for the user's EVM account. Triggers the permission dialog
 * the FIRST time only. The returned address is the same on every EVM chain —
 * there is no separate "Polygon address".
 */
export async function connectEvmAccount(p: unknown): Promise<string> {
  let accounts: string[];
  try {
    accounts = await evmRequest<string[]>(p, 'eth_requestAccounts');
  } catch (e) {
    // Denying the account permission IS a cancellation, not a failure.
    if (isEvmRejection(e)) throw new UsdtCancelledError();
    throw e;
  }
  const addr = Array.isArray(accounts) ? String(accounts[0] || '') : '';
  if (!/^0x[0-9a-fA-F]{40}$/.test(addr)) throw new Error(tr('usdt.noAccount'));
  return addr;
}

/* ------------------------------------------------------------- token ops */

/**
 * The account's USDT balance as a display string, or null when the read
 * fails — a failed read must never block the payment attempt (the wallet
 * itself will refuse an insufficient transfer anyway).
 */
export async function readUsdtBalance(p: unknown, account: string): Promise<string | null> {
  try {
    const raw = await evmRequest<string>(p, 'eth_call', [
      { to: USDT_CONTRACT, data: encodeBalanceOf(account) },
      'latest',
    ]);
    const units = BigInt(String(raw));
    if (units < 0n) return null;
    return formatUsdtUnits(units);
  } catch {
    return null;
  }
}

/**
 * Send an exact USDT amount to `to` (the order's one-time payment address)
 * through Nimiq Pay's native confirmation dialog. Resolves with the tx hash
 * once the wallet has broadcast it.
 *
 * The `to` of the EVM transaction is the TOKEN CONTRACT — the human recipient
 * is an argument inside the transfer calldata — and `value` is 0x0 because no
 * POL moves. Gas (in POL) is paid by the user's wallet per normal EVM rules.
 */
export async function sendUsdtTransfer(p: unknown, from: string, to: string, amount: string | number): Promise<string> {
  const units = parseUsdtUnits(amount);
  const data = encodeTransfer(to, units);
  try {
    const hash = await evmRequest<string>(p, 'eth_sendTransaction', [
      {
        from,
        to: USDT_CONTRACT,
        data,
        value: '0x0',
      },
    ]);
    if (!hash || typeof hash !== 'string') throw new Error(tr('usdt.noTxHash'));
    return hash;
  } catch (e) {
    if (isEvmRejection(e)) throw new UsdtCancelledError();
    throw e;
  }
}
