/**
 * BuildGuard — the page notices when a NEWER deploy has shipped and offers a
 * one-tap refresh.
 *
 * WHY: every fix here lands as new content-hashed chunks, and the HTML is
 * served `max-age=0, must-revalidate`, so a plain reload always picks the new
 * build up. The failure mode is the client that never reloads: a tab left
 * open for days, or a mobile WebView (Nimiq Pay) that keeps running the
 * bundle it loaded at launch. The owner then keeps seeing the OLD site while
 * the live site is already fixed. This island compares the build id baked
 * into its own HTML (`<meta name="build-id">`, set at build time from
 * CF_PAGES_COMMIT_SHA) against the build id of a no-store fetch of `/`; when
 * they differ, a small pill invites the one tap that fixes everything.
 *
 * It never reloads on its own — a surprise reload mid-checkout could drop a
 * payment flow — and it stays completely silent when offline, when the fetch
 * is blocked, or when the ids match.
 */
import { useEffect, useState } from 'react';
import { useT } from '../../i18n';

function currentBuild(): string {
  return document.querySelector('meta[name="build-id"]')?.getAttribute('content') || '';
}

async function latestBuild(): Promise<string> {
  const res = await fetch('/', { cache: 'no-store', headers: { accept: 'text/html' } });
  if (!res.ok) return '';
  const html = await res.text();
  const m = html.match(/name="build-id" content="([^"]+)"/);
  return m ? m[1] : '';
}

export function BuildGuard() {
  const t = useT();
  const [stale, setStale] = useState(false);

  useEffect(() => {
    let stopped = false;
    let lastCheck = 0;
    const check = async () => {
      const now = Date.now();
      if (now - lastCheck < 5 * 60_000) return; // at most one probe per 5 min
      lastCheck = now;
      try {
        const mine = currentBuild();
        const live = await latestBuild();
        if (!stopped && mine && live && mine !== live) setStale(true);
      } catch {
        /* offline / blocked: stay quiet */
      }
    };
    const onVisible = () => {
      if (document.visibilityState === 'visible') void check();
    };
    document.addEventListener('visibilitychange', onVisible);
    return () => {
      stopped = true;
      document.removeEventListener('visibilitychange', onVisible);
    };
  }, []);

  if (!stale) return null;
  return (
    <button type="button" className="build-guard" onClick={() => window.location.reload()}>
      {t('app.updateReady')}
    </button>
  );
}

export default BuildGuard;
