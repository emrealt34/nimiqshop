/**
 * Linked staking wallets, end to end, against the real pages.
 *
 * The customer story this pins: a buyer stakes from a regular wallet, shops
 * through Nimiq Pay, and must be able to see WHICH wallets earn his cashback and
 * WHICH wallet is paid — and link more of his staking wallets, or remove one,
 * without the shop ever moving his payout.
 *
 * The API is a stateful mock (the real backend is covered by the Go tests); the
 * Hub signature is faked the same way hub-login.spec.ts fakes it.
 */
import { test, expect, open, ADDRESS, STAKE_ADDRESS, OTHER_ADDRESS, poolStakeMe, stakeLinkResult } from './support/fixtures';
import { path } from './support/data';

const THIRD_ADDRESS = 'NQ55 0000 1111 2222 3333 4444 5555 6666 7777';

/** Fake Nimiq Hub: signMessage resolves with a signature immediately. */
async function fakeHub(page: import('@playwright/test').Page) {
  await page.addInitScript(() => {
    (window as any).HubApi = class {
      static RequestType = { SIGN_MESSAGE: 'sign-message' };
      signMessage() { return Promise.resolve({ signerPublicKey: 'test-public-key', signature: 'test-signature' }); }
      on() {}
      checkRedirectResponse() {}
    };
  });
}

test.describe('Linked staking wallets @smoke', () => {
  test.use({ lang: 'en' });

  test('the card names every earning wallet, the summed stake, and the wallet that is paid', async ({ page }) => {
    await page.route('**/api/poolstake/me', route => route.fulfill({ json: poolStakeMe([STAKE_ADDRESS, OTHER_ADDRESS]) }));
    await open(page, path('/cashback'));

    const card = page.getByTestId('linked-stake-wallet');
    await expect(card).toBeVisible();
    await expect(card.getByTestId('stake-link-wallet')).toHaveCount(3);
    await expect(card.getByTestId('stake-link-earning')).toContainText('3 wallets');
    // 100 + 150 NIM, plus the sign-in wallet with none.
    await expect(card.getByTestId('stake-link-total')).toContainText('250');
    await expect(card.getByTestId('stake-link-paid-to')).toBeVisible();
    await expect(card.getByTestId('stake-link-paid-to')).toContainText('NQ07');
    // The rule that stops "link more wallets" from being read as "more rate".
    await expect(card).toContainText('counted once');
  });

  test('a buyer with no link sees the sign-in wallet as the only earner and can open the link form', async ({ page }) => {
    await page.route('**/api/poolstake/me', route => route.fulfill({ json: poolStakeMe([]) }));
    await open(page, path('/cashback'));

    const card = page.getByTestId('linked-stake-wallet');
    await expect(card.getByTestId('stake-link-earning')).toContainText(ADDRESS.split(' ')[0]);
    await expect(card.getByTestId('stake-link-paid-to')).toHaveCount(0);
    await expect(card.getByTestId('stake-link-count')).toContainText('0 of 5');
    await card.getByTestId('stake-link-open').click();
    await expect(card.getByTestId('stake-link-address')).toBeVisible();
  });

  test('linking a second wallet signs the challenge, posts it, and the card lists both', async ({ page }) => {
    await fakeHub(page);
    let links: string[] = [];
    const posted: Record<string, unknown>[] = [];
    await page.route('**/api/poolstake/me', route => route.fulfill({ json: poolStakeMe(links) }));
    // The page re-reads through POST /poolstake/refresh after a change; it must
    // see the same state the link just wrote, not the fixture's default.
    await page.route('**/api/poolstake/refresh', route => route.fulfill({ json: poolStakeMe(links) }));
    await page.route('**/api/poolstake/stake-address/challenge', route =>
      route.fulfill({ json: { nonce: 'n', message: `nimshop.io link stake wallet ${STAKE_ADDRESS} to account ${ADDRESS}: n`, challenge_token: 'tok' } }));
    await page.route('**/api/poolstake/stake-address', async route => {
      if (route.request().method() === 'POST') {
        posted.push(route.request().postDataJSON());
        links = [STAKE_ADDRESS];
        return route.fulfill({ json: stakeLinkResult(links) });
      }
      return route.fulfill({ json: stakeLinkResult(links) });
    });
    await open(page, path('/cashback'));

    const card = page.getByTestId('linked-stake-wallet');
    await card.getByTestId('stake-link-open').click();
    await card.getByTestId('stake-link-address').fill(STAKE_ADDRESS);
    await card.getByTestId('stake-link-confirm').click();

    await expect.poll(() => posted.length).toBe(1);
    expect(posted[0]).toMatchObject({ challenge_token: 'tok', stake_address: STAKE_ADDRESS, signature: 'test-signature' });
    await expect(card.getByTestId('stake-link-wallet')).toHaveCount(2);
    await expect(card.getByTestId('stake-link-count')).toContainText('1 of 5');
  });

  test('removing one linked wallet sends its address and leaves the others earning', async ({ page }) => {
    let links = [STAKE_ADDRESS, OTHER_ADDRESS];
    const deleted: string[] = [];
    await page.route('**/api/poolstake/me', route => route.fulfill({ json: poolStakeMe(links) }));
    await page.route('**/api/poolstake/refresh', route => route.fulfill({ json: poolStakeMe(links) }));
    await page.route('**/api/poolstake/stake-address**', route => {
      if (route.request().method() === 'DELETE') {
        const address = new URL(route.request().url()).searchParams.get('address') || '';
        deleted.push(address);
        links = links.filter(a => a.replace(/ /g, '') !== address.replace(/ /g, ''));
      }
      return route.fulfill({ json: stakeLinkResult(links) });
    });
    await open(page, path('/cashback'));

    const card = page.getByTestId('linked-stake-wallet');
    await expect(card.getByTestId('stake-link-wallet')).toHaveCount(3);
    await card.getByTestId('stake-link-unlink').first().click();

    await expect.poll(() => deleted.length).toBe(1);
    expect(deleted[0].replace(/ /g, '')).toBe(STAKE_ADDRESS.replace(/ /g, ''));
    await expect(card.getByTestId('stake-link-wallet')).toHaveCount(2);
    await expect(card.getByTestId('stake-link-total')).toContainText('150');
  });

  test('the sixth wallet is refused up front and the card says why', async ({ page }) => {
    const full = [STAKE_ADDRESS, OTHER_ADDRESS, THIRD_ADDRESS, 'NQ66 0000 1111 2222 3333 4444 5555 6666 8888', 'NQ77 0000 1111 2222 3333 4444 5555 6666 9999'];
    await page.route('**/api/poolstake/me', route => route.fulfill({ json: poolStakeMe(full) }));
    await open(page, path('/cashback'));

    const card = page.getByTestId('linked-stake-wallet');
    await expect(card.getByTestId('stake-link-limit')).toBeVisible();
    await expect(card.getByTestId('stake-link-open')).toHaveCount(0);
    await expect(card.getByTestId('stake-link-count')).toContainText('5 of 5');
  });
});

/**
 * A link that carries the wallet: ?link_stake=NQ…  It must open the form with
 * that wallet filled in, and sign NOTHING until the buyer taps Link.
 */
test.describe('Link opened from a URL @smoke', () => {
  test.use({ lang: 'en' });

  test('a signed-in buyer lands on the form pre-filled, the URL is cleaned, and nothing is signed until Link', async ({ page }) => {
    await fakeHub(page);
    const challenges: string[] = [];
    await page.route('**/api/poolstake/me', route => route.fulfill({ json: poolStakeMe([]) }));
    await page.route('**/api/poolstake/refresh', route => route.fulfill({ json: poolStakeMe([]) }));
    await page.route('**/api/poolstake/stake-address/challenge', route => {
      challenges.push(String(route.request().postData()));
      return route.fulfill({ json: { nonce: 'n', message: `nimshop.io link stake wallet ${STAKE_ADDRESS.replace(/ /g, '')} to account ${ADDRESS.replace(/ /g, '')}: n`, challenge_token: 'tok' } });
    });
    await page.route('**/api/poolstake/stake-address', route => route.fulfill({ json: stakeLinkResult([STAKE_ADDRESS]) }));

    await open(page, path(`/cashback?link_stake=${STAKE_ADDRESS.replace(/ /g, '')}`));

    const card = page.getByTestId('linked-stake-wallet');
    await expect(card.getByTestId('stake-link-intent')).toBeVisible();
    await expect(card.getByTestId('stake-link-address')).toHaveValue(STAKE_ADDRESS);
    // Opening the link is not consent: no challenge, no Hub signature yet.
    await page.waitForTimeout(300);
    expect(challenges).toHaveLength(0);
    // The address bar no longer carries the wallet, so a refresh cannot reopen it.
    expect(page.url()).not.toContain('link_stake');

    await card.getByTestId('stake-link-confirm').click();
    await expect.poll(() => challenges.length).toBe(1);
  });

  test('a malformed link_stake value is ignored: no form, no notice', async ({ page }) => {
    await page.route('**/api/poolstake/me', route => route.fulfill({ json: poolStakeMe([]) }));
    await open(page, path('/cashback?link_stake=not-a-wallet'));
    const card = page.getByTestId('linked-stake-wallet');
    await expect(card).toBeVisible();
    await expect(card.getByTestId('stake-link-intent')).toHaveCount(0);
    await expect(card.getByTestId('stake-link-address')).toHaveCount(0);
  });
});

test.describe('Link opened from a URL, signed out @smoke', () => {
  test.use({ lang: 'en', api: { authed: false, mode: 'ok', orders: 0, preference: 'cashback' } });

  test('a signed-out buyer is told to sign in first, and the intent survives the page', async ({ page }) => {
    await open(page, path(`/cashback?link_stake=${STAKE_ADDRESS.replace(/ /g, '')}`));
    await expect(page.getByTestId('stake-link-signin')).toBeVisible();
    await expect(page.getByTestId('stake-link-signin')).toContainText('NQ73');
    expect(page.url()).not.toContain('link_stake');
    // Reloading without the parameter keeps the intent (sessionStorage).
    await page.reload({ waitUntil: 'load' });
    await expect(page.getByTestId('stake-link-signin')).toBeVisible();
  });
});
