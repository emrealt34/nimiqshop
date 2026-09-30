import { useEffect } from 'react';

/** React hook wrapping setInterval; cleaned up on unmount. */
export function useInterval(fn: () => void, ms: number | null, deps: unknown[] = []) {
  useEffect(() => {
    if (ms === null) return;
    const t = window.setInterval(fn, ms);
    return () => window.clearInterval(t);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [ms, ...deps]);
}
