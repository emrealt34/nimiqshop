import { test, expect, open } from './support/fixtures';
import { path } from './support/data';

test.describe('Hub login hand-off @smoke', () => {
  test.use({ api: { authed: false }, lang: 'tr' });

  test('mobile redirect preserves the challenge and never shows a premature wallet error', async ({ page }) => {
    await page.addInitScript(() => {
      Object.defineProperty(navigator, 'userAgent', { value: 'Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X)' });
      (window as any).HubApi = class {
        static RedirectRequestBehavior = class {};
        signMessage(_request: unknown, behavior: unknown) { (window as any).__hubRedirect = !!behavior; return undefined; }
        on() {}
        checkRedirectResponse() {}
      };
    });
    await page.route('**/api/auth/challenge', (route) => route.fulfill({ json: { challenge_token: 'test-pending-challenge', message: 'nimiqshop.io login: nonce' } }));
    let prematureLogin = 0;
    await page.route('**/api/auth/hub-login', (route) => { prematureLogin++; return route.fulfill({ json: {} }); });
    await open(page, path('/support'));
    await page.locator('.topbar .btn-gold').click();
    const button = page.locator('.sheet').getByRole('button', { name: /Nimiq Hub/ });
    await expect(button).toBeVisible(); await button.click();
    await expect.poll(() => page.evaluate(() => !!(window as any).__hubRedirect)).toBe(true);
    await expect(button).toBeDisabled();
    await expect(page.locator('.login-error')).toHaveCount(0);
    await expect.poll(() => page.evaluate(() => JSON.parse(sessionStorage.getItem('nimshop.pendingLogin') || '{}').challenge_token)).toBe('test-pending-challenge');
    expect(prematureLogin).toBe(0);
  });

  test('a void Hub fallback is a redirect even with a desktop user agent', async ({ page }) => {
    await page.addInitScript(() => {
      (window as any).HubApi = class {
        signMessage() { (window as any).__hubStarted = true; return undefined; }
        on() {}
        checkRedirectResponse() {}
      };
    });
    await page.route('**/api/auth/challenge', (route) => route.fulfill({ json: { challenge_token: 'fallback-challenge', message: 'login nonce' } }));
    await open(page, path('/support'));
    await page.locator('.topbar .btn-gold').click();
    const button = page.locator('.sheet').getByRole('button', { name: /Nimiq Hub/ });
    await button.click();
    await expect.poll(() => page.evaluate(() => !!(window as any).__hubStarted)).toBe(true);
    await expect(button).toBeDisabled();
    await expect(page.locator('.login-error')).toHaveCount(0);
    await expect.poll(() => page.evaluate(() => JSON.parse(sessionStorage.getItem('nimshop.pendingLogin') || '{}').challenge_token)).toBe('fallback-challenge');
  });

  test('a returned Hub signature consumes the pending challenge only after resuming login', async ({ page }) => {
    await page.addInitScript(() => {
      sessionStorage.setItem('nimshop.pendingLogin', JSON.stringify({ challenge_token: 'return-challenge', message: 'login', ts: Date.now() }));
      (window as any).HubApi = class {
        static RequestType = { SIGN_MESSAGE: 'sign-message' };
        callback: ((value: unknown) => void) | null = null;
        on(_type: unknown, callback: (value: unknown) => void) { this.callback = callback; }
        checkRedirectResponse() { this.callback?.({ address: 'NQ07 0000 0000 0000 0000 0000 0000 0000 0000', signerPublicKey: 'test-public-key', signature: 'test-signature' }); }
      };
    });
    const requests: Record<string, unknown>[] = [];
    await page.route('**/api/auth/hub-login', (route) => {
      requests.push(route.request().postDataJSON());
      return route.fulfill({ json: { user: { id: 'hub-return-user', nimiq_address: 'NQ07 0000 0000 0000 0000 0000 0000 0000 0000' }, expires_at: Math.floor(Date.now() / 1000) + 86400 } });
    });
    await open(page, path('/support'));
    await expect.poll(() => requests.length).toBe(1);
    expect(requests[0].challenge_token).toBe('return-challenge');
    await expect.poll(() => page.evaluate(() => sessionStorage.getItem('nimshop.pendingLogin'))).toBeNull();
    await expect.poll(() => page.evaluate(() => JSON.parse(localStorage.getItem('nimshop.sess') || '{}').uid)).toBe('hub-return-user');
    await expect(page.locator('.login-error')).toHaveCount(0);
  });
});
