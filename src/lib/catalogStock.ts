import { cleanFamilyName } from './format';
import { familyAvailability, type Availability } from './catalogAvailability';
import type { Product } from './catalog';

const KEY = 'nimshop_stock:v1';
const TTL = 5 * 60_000;
type Observation = { state: Availability; at: number };
const observations = new Map<string, Observation>();
const listeners = new Set<() => void>();
let restored = false;
const keyOf = (family: string, country: string) => `${country.trim().toUpperCase()}|${cleanFamilyName(family).trim().toLowerCase()}`;

function restore(): void {
  if (restored || typeof sessionStorage === 'undefined') return;
  restored = true;
  try {
    for (const [key, rec] of JSON.parse(sessionStorage.getItem(KEY) || '[]')) {
      if (!observations.has(key) && ['available', 'unavailable'].includes(rec?.state)
        && rec.at > Date.now() - TTL && rec.at <= Date.now() + 60_000) observations.set(key, rec);
    }
  } catch { /* storage is optional */ }
}

export function observedAvailability(family: string, country: string): Availability {
  restore();
  const rec = observations.get(keyOf(family, country));
  return rec && rec.at > Date.now() - TTL ? rec.state : 'unknown';
}

export function recordAvailability(family: string, country: string, data: unknown, notFound = false): Availability {
  const state = notFound ? 'unavailable' : familyAvailability(data);
  if (state === 'unknown') return state;
  restore();
  const previous = observedAvailability(family, country);
  observations.set(keyOf(family, country), { state, at: Date.now() });
  for (const [key, rec] of observations) if (rec.at <= Date.now() - TTL) observations.delete(key);
  try { sessionStorage.setItem(KEY, JSON.stringify([...observations])); } catch { /* storage is optional */ }
  if (previous !== state) for (const listener of listeners) listener();
  return state;
}

export function onStockChange(listener: () => void): () => void {
  listeners.add(listener);
  return () => { listeners.delete(listener); };
}

export function applyObservedStock(products: Product[]): Product[] {
  return products.map((p) => {
    const state = observedAvailability(p.family || p.id, p.country);
    if (state === 'unknown') return p;
    // Checkout refusals remain authoritative until the server clears them.
    if (state === 'available' && p.reported_oos) return p;
    return { ...p, in_stock: state === 'available', is_out_of_stock: state === 'unavailable' };
  });
}
