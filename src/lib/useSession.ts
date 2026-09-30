/**
 * useSession.ts — hydration-safe auth state for React pages.
 *
 * WHY THIS EXISTS: every Astro page is pre-rendered on the server (SSG), where
 * `localStorage` does not exist. Any component that called `isAuthed()` during
 * render therefore shipped the SIGNED-OUT branch ("Connect wallet") inside the
 * static HTML, and React later threw it away during hydration — a visible
 * "Connect wallet" flash on every navigation, plus a hydration error that made
 * React re-render the whole island.
 *
 * `useSession()` returns `null` until the component has mounted (i.e. during
 * SSR and the very first client paint) and the real boolean afterwards. Callers
 * MUST treat `null` as "not decided yet" and render a neutral skeleton — never
 * the signed-out card.
 *
 * It also subscribes to `nimshop:session`, so a login/logout updates every page
 * immediately (previously a page stayed on the "Connect wallet" card after a
 * successful login until a full reload).
 */
import { useEffect, useState } from 'react';
import { isAuthed, subscribeSession } from './session';

export function useSession(): boolean | null {
  const [authed, setAuthed] = useState<boolean | null>(null);
  useEffect(() => {
    setAuthed(isAuthed());
    return subscribeSession((d) => setAuthed(!!d.authed));
  }, []);
  return authed;
}
