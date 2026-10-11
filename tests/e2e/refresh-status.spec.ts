import { test, expect, open } from './support/fixtures';
import { path } from './support/data';
for (const width of [320, 1280]) {
  test.describe(`Refresh status ${width}`, () => {
    test.use({ viewport: { width, height: 844 }, lang: 'tr' });
    for (const stale of [false, true]) {
      test(`served timestamps show ${stale ? 'stale' : 'fresh'} data and partial checks honestly`, async ({ page }) => {
        const checked = new Date(Date.now() - (stale ? 485 : 5) * 60_000).toISOString();
        await page.route('**/data/market/refresh.json', route => route.fulfill({ json: { run_id: '123', catalog: { generated_at: checked, countries: 189, retained_countries: ['KZ'], stock: { checked: 1035, skipped: 7646 } }, market: { generated_at: checked, nim: { usd_per_nim: 0.0003, observed_at: checked }, btc: { usd_per_btc: 80000, observed_at: checked }, fx: { observed_at: '2026-10-11T00:00:00Z', currencies: 166 } } } }));
        await open(page, path('/status'));
        await expect(page.getByRole('heading', { name: 'Güncelleme durumu' })).toBeVisible();
        await expect(page.locator('.refresh-status [role=status]')).toHaveText(stale ? 'Katalog veya kur kontrolü 8 saatten eski.' : 'Katalog veya kur yenilemesi tamamlanmadı. Eksik sorgular ve korunan veriler aşağıda.');
        await expect(page.locator('.refresh-status-list')).toContainText('KZ');
        await expect(page.locator('.refresh-status-list')).toContainText('Bu çalışmada 1.035 kontrol · sırada 7.646');
        await expect(page.getByRole('link', { name: 'GitHub güncelleme çalışmasını gör' })).toHaveAttribute('href', 'https://github.com/emrealt34/nimiqshop/actions/runs/123');
        expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
      });
    }
    test('unavailable report never claims fresh data', async ({ page }) => {
      await page.route('**/data/market/refresh.json', route => route.fulfill({ status: 404, body: '' }));
      await page.route('**/data/catalog/manifest.json', route => route.fulfill({ status: 404, body: '' }));
      await page.route('**/data/market/manifest.json', route => route.fulfill({ status: 404, body: '' }));
      await open(page, path('/status'));
      await expect(page.locator('.refresh-status [role=status]')).toHaveText('Güncelleme durumu yüklenemedi.');
    });
    test('a completed six-hour scan remains fresh within its cadence', async ({ page }) => {
      const checked = new Date(Date.now() - 6.5 * 3600_000).toISOString();
      await page.route('**/data/market/refresh.json', route => route.fulfill({ json: {
        catalog: { generated_at: checked, completed_at: checked, complete: true, retained_countries: [], stock: { mode: 'full', completed: true, checked: 8681, skipped: 0 } },
        market: { generated_at: checked, nim: { observed_at: checked }, btc: { observed_at: checked } },
      } }));
      await open(page, path('/status'));
      await expect(page.locator('.refresh-status [role=status]')).toHaveText('Katalog ve kur kontrolleri güncel.');
      await expect(page.getByText('Stok taraması bitti', { exact: true })).toBeVisible();
    });
    test('complete stock cannot hide a failed rate refresh', async ({ page }) => {
      const checked = new Date(Date.now() - 5 * 60_000).toISOString();
      await page.route('**/data/market/refresh.json', route => route.fulfill({ json: {
        rates_complete: false,
        catalog: { generated_at: checked, complete: true, retained_countries: [], stock: { mode: 'full', completed: true, checked: 8681, skipped: 0 } },
        market: { generated_at: checked, nim: { observed_at: checked }, btc: { observed_at: checked } },
      } }));
      await open(page, path('/status'));
      await expect(page.locator('.refresh-status [role=status]')).toHaveText('Katalog veya kur yenilemesi tamamlanmadı. Eksik sorgular ve korunan veriler aşağıda.');
    });
    test('older snapshots expose their real served dates without inventing a run ID', async ({ page }) => {
      const old = new Date(Date.now() - 9 * 3600_000).toISOString();
      await page.route('**/data/market/refresh.json', route => route.fulfill({ status: 404, body: '' }));
      await page.route('**/data/catalog/manifest.json', route => route.fulfill({ json: { generated_at: old, countries: { TR: {} }, failed: [], verified: {} } }));
      await page.route('**/data/market/manifest.json', route => route.fulfill({ json: { generated_at: old, nim: { observed_at: old }, btc: { observed_at: old } } }));
      await open(page, path('/status'));
      await expect(page.locator('.refresh-status [role=status]')).toHaveText('Katalog veya kur kontrolü 8 saatten eski.');
      await expect(page.getByRole('link', { name: 'GitHub güncelleme çalışmasını gör' })).toHaveAttribute('href', 'https://github.com/emrealt34/nimiqshop/actions/workflows/catalog-refresh.yml');
    });
  });
}
