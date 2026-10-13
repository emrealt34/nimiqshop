import { test, expect, open } from './support/fixtures';
import { path } from './support/data';

for (const location of ['/cashback', '/profile']) {
  test(`cashback states follow the ledger, not stale paid labels or submitted hashes, on ${location}`, async ({ page }) => {
    const hash = 'a'.repeat(64);
    const paidAt = '2026-10-12T12:00:00Z';
    await page.route('**/api/cashback/me**', route => route.fulfill({ json: {
      totals: { paid_nim: 10, paid_count: 1, pending_nim: 40, pending_count: 4, earned_nim: 50, orders: 5 },
      cashbacks: [
        { id: 'queued', product_id: 'Queued order', status: 'queued', status_label: 'paid', amount_nim: 10, tx_hash: hash, paid_at: paidAt },
        { id: 'sending', product_id: 'Sending order', status: 'sending', status_label: 'paid', amount_nim: 10, tx_hash: hash },
        { id: 'broadcast', product_id: 'Broadcast order', status: 'broadcast', status_label: 'paid', amount_nim: 10, tx_hash: hash },
        { id: 'legacy', product_id: 'Legacy order', status: 'paid', status_label: 'paid', amount_nim: 10 },
        { id: 'confirmed', product_id: 'Confirmed order', status: 'paid', status_label: 'pending', amount_nim: 10, tx_hash: hash, paid_at: paidAt },
      ], staker_program_enabled: false,
    } }));
    await open(page, path(location));
    for (const name of ['Queued order', 'Sending order', 'Legacy order']) {
      // Cashback page paginates three rows; the legacy row is on page two.
      if (location === '/cashback' && name === 'Legacy order') break;
      const row = page.locator('.cb-row').filter({ hasText: name });
      await expect(row).toContainText('Pending');
      await expect(row).not.toContainText('Paid');
    }
    await expect(page.locator('.cb-row').filter({ hasText: 'Broadcast order' })).toContainText('Awaiting confirmation');
    if (location === '/cashback') {
      await page.locator('.cb-recent-card .pager-num').filter({ hasText: '2' }).click();
      await expect(page.locator('.cb-row').filter({ hasText: 'Legacy order' })).toContainText('Pending');
    }
    {
      await expect(page.locator('.cb-row').filter({ hasText: 'Confirmed order' })).toContainText('Paid');
    }
  });
}

test('cashback refresh reflects confirmed settlement and survives an API outage without sending a payment', async ({ page }) => {
  await page.setViewportSize({ width: 320, height: 900 });
  let calls = 0;
  let settled = false;
  let unavailable = false;
  await page.route('**/api/cashback/me**', route => {
    calls++;
    if (unavailable) return route.fulfill({ status: 503, json: { error: 'temporary outage' } });
    return route.fulfill({ json: {
      totals: { paid_nim: settled ? 10 : 0, paid_count: settled ? 1 : 0, pending_nim: settled ? 0 : 10, pending_count: settled ? 0 : 1 },
      cashbacks: [{ id: 'one', product_id: 'Refresh order', amount_nim: 10, status: settled ? 'paid' : 'broadcast', tx_hash: 'b'.repeat(64), paid_at: settled ? '2026-10-12T12:00:00Z' : null }],
    } });
  });
  await open(page, path('/cashback'));
  const row = page.locator('.cb-row').filter({ hasText: 'Refresh order' });
  await expect(row).toContainText('Awaiting confirmation');
  const refresh = page.locator('.cb-recent-card + button');
  settled = true;
  await refresh.click();
  await expect(row).toContainText('Paid');
  unavailable = true;
  await refresh.click();
  await expect(page.getByText('Could not refresh cashback payments. Try again.')).toBeVisible();
  await expect(row).toContainText('Paid');
  await expect(refresh).toBeEnabled();
  unavailable = false;
  await refresh.click();
  await expect(page.getByText('Could not refresh cashback payments. Try again.')).toHaveCount(0);
  expect(calls).toBe(4);
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
});
