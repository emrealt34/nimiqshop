/**
 * Cashback page — the action column is public (v7).
 *
 * The whole "what would I earn / stake it" column used to sit behind a shop
 * session ("Connect your Nimiq wallet to stake") and the calculator's
 * "Stake this amount" hand-off was hidden from signed-out visitors. Neither
 * needed an account: the staking transaction is signed by the buyer's own
 * wallet (the native Nimiq Pay dialog, or the guided wallet hand-off in a
 * plain browser), not by the shop login.
 *
 * These tests pin that down for a visitor with NO session: the programme and
 * calculator render, the hand-off fills the form, the stake form itself is
 * present and pressing its button offers the wallet hand-off instead of a
 * dead end.
 */
import { test, expect, open } from './support/fixtures';
import { path } from './support/data';

test.describe('cashback without an account', () => {
  test.use({ api: { authed: false }, viewport: { width: 1280, height: 900 } });

  test('signed out: the stake form renders in full, not a login wall', async ({ page }) => {
    await open(page, path('/cashback'));

    const form = page.locator('#cb-stake-form');
    await expect(form).toBeVisible();
    // The real controls: preset ladder, amount field and the stake button.
    await expect(form.getByRole('button', { name: '100,000' })).toBeVisible();
    await expect(form.getByRole('button', { name: '10,000,000' })).toBeVisible();
    await expect(form.locator('input[aria-label="Amount in NIM"]')).toHaveValue('10000000');
    await expect(form.getByRole('button', { name: 'Open wallet to stake' })).toBeVisible();
    // …and the page says why that is fine.
    await expect(form).toContainText('No account needed');
    // The old wall is gone for good.
    await expect(form).not.toContainText('Connect your Nimiq wallet to stake');
  });

  test('signed out: the calculator hands its amount to the stake form', async ({ page }) => {
    await open(page, path('/cashback'));

    const handoff = page.getByRole('button', { name: /^Stake [\d,.]+ NIM$/ });
    await expect(handoff, 'calculator "Stake this amount" button is public').toBeVisible();
    await handoff.click();

    // The form picked the calculator's slider value up. The slider starts at
    // the calculator's own floor (100K NIM — STAKE_MIN_CALC in
    // CashbackCalculator.tsx), which is what the button label above shows.
    await expect(page.locator('#cb-stake-form input[aria-label="Amount in NIM"]')).toHaveValue('100000');
  });

  test('signed out: pressing stake opens the wallet hand-off, not a dead end', async ({ page }) => {
    await open(page, path('/cashback'));

    await page.locator('#cb-stake-form').getByRole('button', { name: 'Open wallet to stake' }).click();

    const sheet = page.getByRole('dialog', { name: 'Not inside Nimiq Pay' });
    await expect(sheet).toBeVisible();
    await expect(sheet.getByRole('button', { name: 'Get me there now' })).toBeVisible();
    await expect(sheet.getByRole('link', { name: /wallet\.nimiq\.com/ })).toBeVisible();
  });

  test('signed out: a preset tap then stake is a two-tap path to the wallet', async ({ page }) => {
    await open(page, path('/cashback'));

    const form = page.locator('#cb-stake-form');
    await form.getByRole('button', { name: '500,000' }).click();
    await expect(form.locator('input[aria-label="Amount in NIM"]')).toHaveValue('500000');

    await form.getByRole('button', { name: 'Open wallet to stake' }).click();
    await expect(page.getByRole('dialog', { name: 'Not inside Nimiq Pay' })).toBeVisible();
  });
});
