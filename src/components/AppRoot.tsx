/**
 * AppRoot.tsx — single hydrated React root for a page. Wraps the global
 * providers (i18n, toasts/sheets, cart, session bootstrap) around the site shell
 * and the page content so context is shared across the whole island. Each
 * Astro page mounts ONE <AppRoot> via client:load.
 */
import { type ReactNode } from 'react';
import { AppProviders } from './AppProviders';
import { CartProvider } from '../lib/cartStore';
import { SiteShell, type ShellKey } from './shell/SiteShell';
import { Router } from '../lib/router';
import { I18nProvider } from '../i18n';

/** TEST MODE has exactly ONE customer-visible difference: the single
 * "Pay test" button at the payment spot on the real pay screen (checkout +
 * order page). No banner, no parallel screen, no other chrome — owner's
 * contract: "test mode = the original, plus one button that fakes the
 * trigger". Operator-facing test tooling lives in the admin Test Center. */
export function AppRoot({ activeKey, children }: { activeKey: ShellKey; children: ReactNode }) {
  return (
    <I18nProvider>
      <CartProvider>
        <Router initialKey={activeKey} shell={<SiteShell activeKey={activeKey} />} initialPage={children}
          wrap={(content) => <AppProviders>{content}</AppProviders>} />
      </CartProvider>
    </I18nProvider>
  );
}
