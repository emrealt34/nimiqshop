import { useCallback, useEffect, useRef, useState } from 'react';
import { friendlyApiMessage, getStakePosition } from './api';
import { getAddress, getUid, subscribeSession } from './session';
import { normalizeValidator } from './stake';

export type StakePosition = {
  address: string; exists: boolean; delegation: string;
  active_nim: number; inactive_nim: number; retired_nim: number;
  inactive_from: number | null; checked_at: string; from_cache: boolean;
};
const scope = () => `${getUid()}:${getAddress().replace(/\s/g, '').toUpperCase()}`;
export function parseStakePosition(raw: any): StakePosition {
  const address = String(raw?.address || '').replace(/\s/g, '').toUpperCase();
  if (!address || address !== getAddress().replace(/\s/g, '').toUpperCase() || typeof raw?.exists !== 'boolean') throw Error('Invalid staking position');
  for (const key of ['active_nim', 'inactive_nim', 'retired_nim']) {
    if (typeof raw[key] !== 'number' || !Number.isFinite(raw[key]) || raw[key] < 0) throw Error('Invalid staking balance');
  }
  const delegation = raw.delegation ? normalizeValidator(raw.delegation) : '';
  if (raw.delegation && !delegation) throw Error('Invalid validator address');
  if (!raw.exists && (delegation || raw.active_nim + raw.inactive_nim + raw.retired_nim > 0)) throw Error('Inconsistent staking position');
  return { ...raw, address, delegation };
}

/** No background polling: load for this wallet, recheck on explicit actions. */
export function useStakePosition(authed: boolean | null) {
  const [owner, setOwner] = useState('');
  const [snapshot, setSnapshot] = useState<{ owner: string; position: StakePosition | null; error: string }>({ owner: '', position: null, error: '' });
  const [loading, setLoading] = useState(false);
  const revision = useRef(0);
  useEffect(() => {
    setOwner(scope());
    return subscribeSession(() => setOwner(scope()));
  }, []);
  const refresh = useCallback(async (force = true) => {
    const current = scope();
    const version = ++revision.current;
    setLoading(true);
    try {
      const result = parseStakePosition(await getStakePosition(force));
      if (version !== revision.current || scope() !== current) return null;
      setSnapshot({ owner: current, position: result, error: '' });
      return result;
    } catch (cause) {
      if (version === revision.current && scope() === current) setSnapshot({ owner: current, position: null, error: friendlyApiMessage(cause) });
      return null;
    } finally { if (version === revision.current) setLoading(false); }
  }, []);
  const invalidate = useCallback(() => { revision.current++; }, []);
  useEffect(() => {
    if (authed && owner) void refresh(false);
    else { revision.current++; setSnapshot({ owner, position: null, error: '' }); setLoading(false); }
    return invalidate;
  }, [authed, owner, refresh, invalidate]);
  return { position: authed && snapshot.owner === owner ? snapshot.position : null, error: snapshot.owner === owner ? snapshot.error : '', loading, refresh, owner };
}
