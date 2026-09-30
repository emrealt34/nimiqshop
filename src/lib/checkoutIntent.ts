import { t as tr } from '../i18n';
/** Persistent checkout identity, NOT payment authorization. No email, phone,
 * wallet token or cart body is written to localStorage: only a SHA-256 key,
 * random idempotency key and the opaque local quote ID. */
export const INTENT_PREFIX = 'nimshop.checkout.v2:';
export type CheckoutIntent = { key: string; quoteId?: string; createdAt: number };

export function canonicalIntent(value: unknown): string {
  if (Array.isArray(value)) return '[' + value.map(canonicalIntent).sort().join(',') + ']';
  if (value && typeof value === 'object') {
    const object = value as Record<string, unknown>;
    return '{' + Object.keys(object).filter((k) => object[k] !== undefined).sort()
      .map((k) => JSON.stringify(k) + ':' + canonicalIntent(object[k])).join(',') + '}';
  }
  return JSON.stringify(value) ?? 'null';
}

export async function digestIntent(value: string): Promise<string> {
  if (!globalThis.crypto?.subtle) throw new Error(tr('intent.needsCrypto'));
  const digest = await crypto.subtle.digest('SHA-256', new TextEncoder().encode(value));
  return [...new Uint8Array(digest)].map((x) => x.toString(16).padStart(2, '0')).join('');
}

export function ownerFromToken(token: string): string {
  try {
    const raw = token.split('.')[1].replace(/-/g, '+').replace(/_/g, '/');
    const claims = JSON.parse(atob(raw));
    return String(claims.uid || claims.sub || claims.user_id || token);
  } catch { return token; } // only HASHED before storage; never an auth decision
}

export async function loadOrCreateIntent(storage: Storage, scope: string, proposedKey: string): Promise<{ storageKey: string; intent: CheckoutIntent }> {
  const storageKey = INTENT_PREFIX + await digestIntent(scope);
  const readOrCreate = () => {
    const raw = storage.getItem(storageKey);
    if (raw) {
      const intent: CheckoutIntent = JSON.parse(raw);
      if (!intent.key || intent.key.length < 16) throw new Error(tr('intent.invalid'));
      return { storageKey, intent };
    }
    const intent: CheckoutIntent = { key: proposedKey, createdAt: Date.now() };
    // Must succeed BEFORE the HTTP POST; otherwise a reload would lose the key.
    storage.setItem(storageKey, JSON.stringify(intent));
    return { storageKey, intent };
  };
  if (typeof navigator !== 'undefined' && navigator.locks) {
    return navigator.locks.request(storageKey, readOrCreate);
  }
  // Older browsers: backend transaction + durable aliases provide the final
  // cross-tab guard even if two tabs race while picking a fresh UUID here.
  return readOrCreate();
}

export function saveIntentQuote(storage: Storage, key: string, intent: CheckoutIntent, quoteId: string) {
  storage.setItem(key, JSON.stringify({ ...intent, quoteId }));
}

/** Only call following an explicit user action AND a verified terminal order. */
export function releaseIntentForQuote(storage: Storage, quoteId: string) {
  for (let i = storage.length - 1; i >= 0; i--) {
    const key = storage.key(i);
    if (!key?.startsWith(INTENT_PREFIX)) continue;
    const intent: CheckoutIntent = JSON.parse(storage.getItem(key) || '{}');
    if (intent.quoteId === quoteId) storage.removeItem(key);
  }
}

/** Cosmetics/rewards must not turn an uncertain purchase into a fresh intent.
 * The server still binds the FULL body and rejects incompatible key reuse. */
export function purchaseIntentPayload(value: unknown): unknown {
  if (Array.isArray(value)) return value.map(purchaseIntentPayload);
  if (value && typeof value === 'object') return Object.fromEntries(Object.entries(value as Record<string,unknown>)
    .filter(([key]) => !['cashback_code','gift_channel','gift_message','gifter_identicon','coin','network'].includes(key))
    .map(([key,v]) => [key,purchaseIntentPayload(v)]));
  return value;
}
