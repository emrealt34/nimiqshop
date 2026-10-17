import { test, expect, open } from './support/fixtures';
import { path } from './support/data';

/**
 * The operator's only upstream recovery path for a purchase stuck in
 * `awaiting_payment`.
 *
 * Every customer-facing endpoint deliberately re-reads local state only, so
 * before this button existed an operator with a paying customer on the phone
 * had nothing to click: the fix was a raw curl against
 * POST /api/admin/orders/{id}/sync. The backend already refused to invent an
 * outcome (cache-bypassing GET + the tracker's own conditional transition),
 * which is why wiring it up is safe — and why the answer it reports back is
 * the supplier's own status, not a guess.
 */
for (const width of [320, 1280]) {
  test.describe(`admin supplier sync ${width}px`, () => {
    test.use({ api: { authed: false }, lang: 'en', viewport: { width, height: 900 } });

    test('operator re-asks the supplier about a stuck purchase and sees its answer', async ({ page }) => {
      const syncs: string[] = [];
      await page.route('**/api/admin/**', route => {
        const url = new URL(route.request().url());
        if (route.request().method() === 'POST' && url.pathname.endsWith('/sync')) {
          syncs.push(url.pathname);
          // What the backend really returns: the re-read quote view.
          return route.fulfill({ json: { id: 'stuck-order', status: 'fulfilled', supplier_status: 'Done' } });
        }
        return route.fulfill({ json: url.pathname.endsWith('/auth/me') ? { admin: { id: 'operator', username: 'operator' } }
          : url.pathname.endsWith('/quotes') ? { quotes: [{ id: 'stuck-order', product_id: 'Amazon', status: 'awaiting_payment', cashback: {} }] } : {} });
      });

      await open(page, path('/admin?section=orders'));
      // Nothing is sent upstream until the operator asks: opening the console
      // must not spend supplier budget on its own.
      expect(syncs).toHaveLength(0);

      await page.getByTestId('supplier-sync').click();
      expect(syncs).toHaveLength(1);
      expect(syncs[0]).toMatch(/\/admin\/orders\/stuck-order\/sync$/);
      await expect(page.getByTestId('supplier-sync-result')).toContainText('Done');
      await expect(page.getByTestId('supplier-sync-result')).toContainText('fulfilled');
      expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
    });

    test('a supplier or policy refusal is shown verbatim and never rendered as a status', async ({ page }) => {
      await page.route('**/api/admin/**', route => {
        const url = new URL(route.request().url());
        if (route.request().method() === 'POST' && url.pathname.endsWith('/sync')) {
          return route.fulfill({ status: 409, json: { error: 'supplier acceptance is unresolved; no supplier id is known, so automatic retry is unsafe' } });
        }
        return route.fulfill({ json: url.pathname.endsWith('/auth/me') ? { admin: { id: 'operator', username: 'operator' } }
          : url.pathname.endsWith('/quotes') ? { quotes: [{ id: 'no-supplier-id', product_id: 'Amazon', status: 'order_creating', cashback: {} }] } : {} });
      });

      await open(page, path('/admin?section=orders'));
      await page.getByTestId('supplier-sync').click();
      await expect(page.getByRole('alert')).toContainText('no supplier id is known');
      await expect(page.getByTestId('supplier-sync-result')).toHaveCount(0);
      // The button stays usable: a refusal is an answer about this quote, not
      // a dead console.
      await expect(page.getByTestId('supplier-sync')).toBeEnabled();
    });
  });
}
