/**
 * Identicon.tsx — an <img> showing an address identicon. Resolves the URL
 * lazily via the vendored @nimiq/identicons module; shows an empty slot until
 * the real one arrives (the empty slot beats a wrong face — original behavior).
 */
import { useEffect, useState } from 'react';
import { resolveIdenticonUrl, identiconPlaceholder, canonicalIdenticonInput } from '../../lib/identicon';

export function Identicon({ address, className = 'identicon', size }: { address?: string | null; className?: string; size?: number }) {
  // Start with a synchronous, deterministic face (never an empty slot), then
  // swap in the real @nimiq/identicons face as soon as it resolves.
  const key = canonicalIdenticonInput(address);
  const [src, setSrc] = useState<string>(() => identiconPlaceholder(address));

  useEffect(() => {
    setSrc(identiconPlaceholder(address));
    let alive = true;
    resolveIdenticonUrl(address).then((u) => {
      if (alive) setSrc(u);
    });
    return () => {
      alive = false;
    };
  }, [key, address]);

  return (
    <img
      className={className}
      src={src}
      alt=""
      aria-hidden="true"
      width={size}
      height={size}
      style={size ? { width: size, height: size } : undefined}
    />
  );
}
