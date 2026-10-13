import fs from 'node:fs';
import path from 'node:path';
import { chromium, expect } from '@playwright/test';

const cfg = JSON.parse(fs.readFileSync(process.argv[2], 'utf8'));
const browser = await chromium.launch({ executablePath: cfg.executable, headless: true, args: ['--no-sandbox'] });
let page;
try {
  const context = await browser.newContext({ viewport: { width: 1280, height: 900 } });
  context.setDefaultTimeout(30000);
  await context.addCookies(cfg.cookies);
  await context.addInitScript(({ address, uid }) => {
    localStorage.setItem('nimshop.sess', JSON.stringify({ uid, address, name: 'Isolated staker' }));
    localStorage.setItem('nimshop.addr', address);
    localStorage.setItem('nimshop.lang', 'en');
    localStorage.setItem('nimshop_country', 'US');
  }, cfg);
  page = await context.newPage();
  const errors = [];
  page.on('pageerror', (error) => errors.push(error.message));
  if (cfg.checkout) {
    await context.addInitScript((quantity) => {
      localStorage.setItem('nimshop_cart', JSON.stringify([{
        id: 'test-steam', name: 'Steam', type: 'giftcard', country: 'US', currency: 'USD',
        pkg: 'test-steam-50', value: 50, denomination: '50 USD', unitUSD: 50,
        qty: quantity, brand: 'test-steam', category: 'giftcard', delivery_type: 'by_email',
      }]));
    }, cfg.quantity);
    await page.setViewportSize(cfg.quantity === 2 ? { width: 390, height: 844 } : { width: 1280, height: 900 });
    await page.goto(`${cfg.base}/cart/`, { waitUntil: 'networkidle' });
    await page.locator('.btn-gold.btn-block.btn-lg').click();
    await page.getByPlaceholder('you@gmail.com').first().fill('buyer@example.test');
    await page.locator('#cdp-my-email-again').fill('buyer@example.test');
    const created = page.waitForResponse((response) => /\/api\/quotes(?:\/batch)?$/.test(new URL(response.url()).pathname) && response.request().method() === 'POST', { timeout: 90000 });
    await page.getByRole('button', { name: /Continue to payment/i }).click();
    const response = await created;
    const quote = await response.json();
    expect([200, 201], JSON.stringify(quote)).toContain(response.status());
    const id = quote.id || quote.quote?.id || quote.quote_id;
    expect(id).toBeTruthy();
    await page.goto(`${cfg.base}/order/?type=quote&id=${encodeURIComponent(id)}`, { waitUntil: 'networkidle' });
    await page.screenshot({ path: path.join(cfg.output, `${cfg.label}-order.png`), fullPage: true });
    fs.writeFileSync(cfg.result, id, { mode: 0o600 });
    expect(errors).toEqual([]);
  } else {
  for (const [device, width, height] of [['desktop', 1280, 900], ['phone', 390, 844]]) {
    await page.setViewportSize({ width, height });
    await page.goto(`${cfg.base}/cashback/`, { waitUntil: 'networkidle' });
    const rate = cfg.state.cashback_percent;
    await expect(page.locator('.cb-rate-value')).toContainText(`${rate}%`, { timeout: 30000 });
    await expect(page.locator('.cb-row[data-payment-status="pending"]')).toHaveCount(cfg.pending, { timeout: 30000 });
    await expect(page.locator('.cb-row[data-payment-status="paid"]')).toHaveCount(cfg.paid, { timeout: 30000 });
    const overflow = await page.evaluate(() => document.documentElement.scrollWidth > innerWidth + 1);
    expect(overflow, `${cfg.label}: ${device} overflow`).toBe(false);
    await page.screenshot({ path: path.join(cfg.output, `${cfg.label}-${device}.png`), fullPage: true });
    console.log(`ACTUAL SHOP UI: ${cfg.label} ${device} rate=${rate}% pending=${cfg.pending} paid=${cfg.paid}`);
  }
  expect(errors).toEqual([]);
  }
} catch (error) {
  if (page) {
    await page.screenshot({ path: path.join(cfg.output, 'failure.png'), fullPage: true });
    console.log('BROWSER FAILURE:', page.url(), (await page.locator('body').innerText()).slice(-4000));
  }
  throw error;
} finally {
  await browser.close();
}
