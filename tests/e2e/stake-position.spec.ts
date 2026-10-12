import { test, expect, open, ADDRESS } from './support/fixtures';
import { path } from './support/data';

const pool = 'NQ49N8MBXYCRXBUP404CKXKKL49MA7BTF082';
const other = 'NQ12CPDG4KKU70UBNC7U0Q82T8U7US17PYHU';
const position = (delegation = '', active = 0, inactive = 0, retired = 0) => ({
  address: ADDRESS, exists: !!delegation, delegation, active_nim: active,
  inactive_nim: inactive, retired_nim: retired, inactive_from: null,
  checked_at: new Date().toISOString(), from_cache: false,
});
async function wire(page: import('@playwright/test').Page, read: () => any, registry = true) {
  await page.route('**/api/poolstake/position**', route => {
    const value = read();
    return value === null ? route.fulfill({ status: 502, json: { error: 'staking lookup unavailable' } }) : route.fulfill({ json: value });
  });
  await page.route('**/api/poolstake/validator**', route => route.fulfill({ json: { found: registry, name: 'Independent Validator', logo: '' } }));
  await page.route('**/api/cashback/rate', route => route.fulfill({ json: { staker_program_enabled: true, cashback_bps: 0, staker_base_bps: 100, pool_validator_address: pool, stake_cashback: { max_boost_percent: 10, min_stake_nim: 100 } } }));
}
async function wallet(page: import('@playwright/test').Page, reject = false) {
  await page.addInitScript(({ reject }) => {
    const calls: unknown[] = [];
    (window as any).__stakeCalls = calls;
    const send = async (args: any) => {
      calls.push(args);
      if (reject) throw Object.assign(new Error('Payment cancelled'), { name: 'PermissionDeniedError' });
      return 'a'.repeat(64);
    };
    (window as any).nimiq = { connected: true, sendNewStakerTransaction: send, sendStakeTransaction: send, sendUpdateStakerTransaction: send, sendSetActiveStakeTransaction: send, sendRetireStakeTransaction: send, sendRemoveStakeTransaction: send };
  }, { reject });
}
for (const width of [320, 768, 1280]) {
  test.describe(`staking position ${width}px`, () => {
    test.use({ viewport: { width, height: 900 } });
    for (const scenario of ['none', 'here', 'elsewhere', 'inactive', 'retired', 'unavailable', 'unlisted'] as const) {
      test(`${scenario} shows correct amounts and actions`, async ({ page }) => {
        const value = scenario === 'none' ? position() : scenario === 'here' ? position(pool, 200) : scenario === 'inactive' ? position(pool, 0, 30) : scenario === 'retired' ? position(other, 0, 0, 40) : scenario === 'unavailable' ? null : position(other, 200, 30, 40);
        await wire(page, () => value, scenario !== 'unlisted');
        await open(page, path('/cashback'));
        const card = page.locator('.cb-position');
        await expect(card).toBeVisible();
        await expect(card).toHaveAttribute('aria-busy', 'false');
        if (value?.exists) {
          await expect(card.locator('dd').nth(0)).toContainText(`${value.active_nim} NIM`);
          await expect(card.locator('dd').nth(1)).toContainText(`${value.inactive_nim} NIM`);
          await expect(card.locator('dd').nth(2)).toContainText(`${value.retired_nim} NIM`);
          await expect(card.locator('code')).toContainText(value.delegation.slice(0, 4));
        }
        if (scenario === 'elsewhere' || scenario === 'unlisted') {
          await expect(card.getByRole('button', { name: /Move.*Nimiq Base Staking/ })).toBeEnabled();
          await expect(page.locator('#cb-stake-form input')).toHaveCount(0);
          await card.getByRole('button', { name: /Move.*Nimiq Base Staking/ }).click();
          await expect(page.getByRole('dialog', { name: 'Not inside Nimiq Pay' })).toBeVisible();
        } else if (scenario === 'unavailable' || scenario === 'retired') {
          await expect(page.locator('#cb-stake-form input')).toHaveCount(0);
          if (scenario === 'retired') await expect(card.getByRole('button', { name: 'Manage in wallet' })).toBeEnabled();
        } else await expect(page.locator('#cb-stake-form input').first()).toBeVisible();
        expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1)).toBe(true);
        await page.screenshot({ path: test.info().outputPath('staking-card.png'), fullPage: true });
      });
    }
  });
}
test('moving another delegation uses the wallet update method, preserves inactive stake and blocks duplicate sends', async ({ page }) => {
  await wallet(page);
  let current = position(other, 200, 30);
  await wire(page, () => current);
  await open(page, path('/cashback'));
  const move = page.locator('.cb-position').getByRole('button', { name: /Move.*Nimiq Base Staking/ });
  await move.click();
  await expect.poll(() => page.evaluate(() => (window as any).__stakeCalls.length)).toBe(1);
  expect(await page.evaluate(() => (window as any).__stakeCalls[0])).toEqual({ newDelegation: pool });
  await expect(move).toBeDisabled();
  await expect(page.locator('#cb-stake-form')).toContainText('Transaction confirmation is pending');
  await page.reload();
  await expect(page.locator('.cb-position').getByRole('button', { name: /Move.*Nimiq Base Staking/ })).toBeDisabled();
  current = position(pool, 200, 30);
  await page.locator('#cb-stake-form .alert.info').getByRole('button', { name: 'Refresh' }).click();
  await expect(page.locator('#cb-stake-form .alert.info')).toHaveCount(0);
  await expect(page.locator('#cb-stake-form').getByRole('button', { name: 'Add stake', exact: true })).toBeEnabled();
});
test('state changing before approval blocks a second staker transaction', async ({ page }) => {
  await wallet(page);
  let current = position();
  await wire(page, () => current);
  await open(page, path('/cashback'));
  current = position(other, 200);
  await page.locator('#cb-stake-form .cb-primary').click();
  await expect(page.locator('#cb-stake-form')).toContainText('Your staking position changed');
  expect(await page.evaluate(() => (window as any).__stakeCalls.length)).toBe(0);
});
test('declined stake remains retryable without creating a pending transaction', async ({ page }) => {
  await wallet(page, true);
  await wire(page, () => position());
  await open(page, path('/cashback'));
  await page.locator('#cb-stake-form .cb-primary').click();
  await expect(page.locator('#cb-stake-form .alert.error')).toBeVisible();
  await expect(page.locator('#cb-stake-form .alert.info')).toHaveCount(0);
  await expect(page.locator('#cb-stake-form .cb-primary')).toBeEnabled();
});

test('wallet change clears the previous position before a delayed response completes', async ({ page }) => {
  let current = position(other, 200);
  await wire(page, () => current);
  await open(page, path('/cashback'));
  await expect(page.locator('.cb-position dd').first()).toContainText('200 NIM');
  const next = 'NQ73SE1XYRRFQ8NCDQCPHLJMNR858P7V2HPD';
  let release!: () => void;
  const gate = new Promise<void>(resolve => { release = resolve; });
  await page.route('**/api/poolstake/position', async route => { await gate; await route.fulfill({ json: { ...position(pool, 500), address: next } }); });
  try {
    await page.evaluate(next => {
      localStorage.setItem('nimshop.sess', JSON.stringify({ uid: 'u2', address: next, expiresAt: Math.floor(Date.now()/1000) + 86400 }));
      localStorage.setItem('nimshop.addr', next);
      window.dispatchEvent(new CustomEvent('nimshop:session', { detail: { authed: true, address: next } }));
    }, next);
    await expect(page.locator('.cb-position')).not.toContainText('200 NIM');
    release();
    await expect(page.locator('.cb-position dd').first()).toContainText('500 NIM');
  } finally { release(); }
});

test('an uncertain wallet submission stays blocked after reload', async ({ page }) => {
  await wallet(page);
  await page.addInitScript(() => {
    (window as any).nimiq.sendNewStakerTransaction = async () => { throw Object.assign(new Error('Submission outcome unknown'), { type: 'TRANSACTION_OUTCOME_UNKNOWN' }); };
  });
  await wire(page, () => position());
  await open(page, path('/cashback'));
  await page.locator('#cb-stake-form .cb-primary').click();
  await expect(page.locator('#cb-stake-form .cb-primary')).toBeDisabled();
  await page.reload();
  await expect(page.locator('#cb-stake-form .alert.info')).toContainText('Transaction confirmation is pending');
  await expect(page.locator('#cb-stake-form .cb-primary')).toBeDisabled();
});
