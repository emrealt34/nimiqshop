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
async function fakeHub(page: import('@playwright/test').Page, signer = STAKE_ADDRESS.replace(/ /g, '')) {
  await page.addInitScript((signerAddress: string) => {
    (window as any).__hubSignCalls = [];
    (window as any).HubApi = class {
      static RequestType = { SIGN_MESSAGE: 'sign-message' };
      signMessage(options: Record<string, unknown>) {
        (window as any).__hubSignCalls.push(options);
        return Promise.resolve({ signer: signerAddress, signerPublicKey: 'test-public-key', signature: 'test-signature' });
      }
      on() {}
      checkRedirectResponse() {}
    };
  }, signer);
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
    await expect(card.getByTestId('stake-link-paid-to')).toBeVisible();
    await expect(card.getByTestId('stake-link-count')).toContainText('0 of 5');
    await expect(card.getByTestId('stake-link-confirm')).toBeVisible();
    await expect(card.getByTestId('stake-link-address')).toHaveCount(0);
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
      route.fulfill({ json: { nonce: 'n', message: `nimshop.io link staking wallet to account ${ADDRESS.replace(/ /g, '')}: n`, challenge_token: 'tok' } }));
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
    await card.getByTestId('stake-link-confirm').click();

    await expect.poll(() => posted.length).toBe(1);
    expect(posted[0]).toMatchObject({ challenge_token: 'tok', stake_address: STAKE_ADDRESS.replace(/ /g, ''), signature: 'test-signature' });
    await expect(card.getByTestId('stake-link-wallet')).toHaveCount(2);
    await expect(card.getByTestId('stake-link-count')).toContainText('1 of 5');
  });

  test('a mobile Hub return refreshes the linked wallets', async ({ page }) => {
    let links: string[] = [];
    await page.route('**/api/poolstake/me', route => route.fulfill({ json: poolStakeMe(links) }));
    await page.route('**/api/poolstake/refresh', route => route.fulfill({ json: poolStakeMe(links) }));
    await open(page, path('/cashback'));
    const card = page.getByTestId('linked-stake-wallet');
    await expect(card.getByTestId('stake-link-wallet')).toHaveCount(1);
    links = [STAKE_ADDRESS];
    await page.evaluate(address => window.dispatchEvent(new CustomEvent('nimshop:stake-linked', { detail: { stake_address: address } })), STAKE_ADDRESS);
    await expect(card.getByTestId('stake-link-wallet')).toHaveCount(2);
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
    await expect(card.getByTestId('stake-link-confirm')).toHaveCount(0);
    await expect(card.getByTestId('stake-link-count')).toContainText('5 of 5');
  });
});

/**
 * A link that carries an expected wallet: ?link_stake=NQ…  It never fills an
 * address field or signs on arrival; Hub is invoked only after the buyer taps.
 */
test.describe('Link opened from a URL @smoke', () => {
  test.use({ lang: 'en' });

  test('a signed-in buyer sees the expected wallet, URL is cleaned, and nothing is signed until tap', async ({ page }) => {
    await fakeHub(page);
    const challenges: string[] = [];
    await page.route('**/api/poolstake/me', route => route.fulfill({ json: poolStakeMe([]) }));
    await page.route('**/api/poolstake/refresh', route => route.fulfill({ json: poolStakeMe([]) }));
    await page.route('**/api/poolstake/stake-address/challenge', route => {
      challenges.push(String(route.request().postData()));
      return route.fulfill({ json: { nonce: 'n', message: `nimshop.io link staking wallet to account ${ADDRESS.replace(/ /g, '')}: n`, challenge_token: 'tok' } });
    });
    await page.route('**/api/poolstake/stake-address', route => route.fulfill({ json: stakeLinkResult([STAKE_ADDRESS]) }));

    await open(page, path(`/cashback?link_stake=${STAKE_ADDRESS.replace(/ /g, '')}`));

    const card = page.getByTestId('linked-stake-wallet');
    await expect(card.getByTestId('stake-link-intent')).toBeVisible();
    await expect(card.getByTestId('stake-link-address')).toHaveCount(0);
    await expect(card.getByTestId('stake-link-confirm')).toBeVisible();
    // Opening the link is not consent: no challenge, no Hub signature yet.
    await page.waitForTimeout(300);
    expect(challenges).toHaveLength(0);
    // The address bar no longer carries the wallet, so a refresh cannot reopen it.
    expect(page.url()).not.toContain('link_stake');

    await card.getByTestId('stake-link-confirm').click();
    await expect.poll(() => challenges.length).toBe(1);
    await expect.poll(() => page.evaluate(() => (window as any).__hubSignCalls.length)).toBe(1);
    expect(await page.evaluate(() => (window as any).__hubSignCalls[0].signer)).toBe(STAKE_ADDRESS.replace(/ /g, ''));
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

test.describe('Cross-device pairing link (?stake_link=<token>) @smoke', () => {
  const VALID_INVITE = 'eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJ1aWQiOiJ1MSIsImFjY291bnQiOiJOUTA3MDAwMDAwMDAwMDAwMDAwMDAwMDAwMDAwMDAwMDAwMDAifQ.abcdefghijklmnopqrstuvwxyz0123456789_-ABCDE';

  test('signed-in buyer creates a pairing link and sees the QR + copy panel', async ({ page }) => {
    await page.route('**/api/poolstake/me', route => route.fulfill({ json: poolStakeMe([]) }));
    await page.route('**/api/poolstake/stake-address/invite', route => {
      if (route.request().method() === 'POST') {
        return route.fulfill({
          json: {
            invite_token: VALID_INVITE,
            account_address: ADDRESS.replace(/ /g, ''),
            expires_at: Math.floor(Date.now() / 1000) + 900,
            ttl_seconds: 900,
            linked_count: 0,
            linked_wallet_limit: 5,
          },
        });
      }
      return route.continue();
    });
    await open(page, path('/cashback'));

    const card = page.getByTestId('linked-stake-wallet');
    await card.getByTestId('stake-link-create-invite').click();
    await expect(card.getByTestId('stake-link-invite-box')).toBeVisible();
    await expect(card.getByTestId('stake-link-invite-copy')).toBeVisible();
  });

  test.describe('receiving device (signed out)', () => {
    test.use({ lang: 'en', api: { authed: false, mode: 'ok', orders: 0, preference: 'cashback' } });

    test('signed-out browser opens ?stake_link=, sees Hub + Nimiq Pay options, signs and links directly', async ({ page }) => {
      await fakeHub(page);
      const claims: Record<string, unknown>[] = [];
      await page.route('**/api/poolstake/stake-address/invite**', route => {
        const url = route.request().url();
        if (url.includes('/challenge')) {
          return route.fulfill({
            json: {
              nonce: 'inv-n',
              message: `nimshop.io link staking wallet to account ${ADDRESS.replace(/ /g, '')}: inv-n`,
              challenge_token: 'inv-chal-tok',
              account_address: ADDRESS.replace(/ /g, ''),
            },
          });
        }
        if (url.includes('/claim')) {
          claims.push(route.request().postDataJSON());
          return route.fulfill({
            json: {
              linked: true,
              via_invite: true,
              account_address: ADDRESS.replace(/ /g, ''),
              stake_address: STAKE_ADDRESS.replace(/ /g, ''),
              stake_nim: 500,
              stake_source: 'chain',
              linked_count: 1,
              linked_wallet_limit: 5,
            },
          });
        }
        return route.fulfill({
          json: {
            valid: true,
            account_address: ADDRESS.replace(/ /g, ''),
            expires_at: Math.floor(Date.now() / 1000) + 900,
            at_limit: false,
            linked_count: 0,
            linked_wallet_limit: 5,
          },
        });
      });

      await open(page, path(`/cashback?stake_link=${VALID_INVITE}`));
      const claimCard = page.getByTestId('stake-invite-claim-card');
      await expect(claimCard).toBeVisible();
      expect(page.url()).not.toContain('stake_link');
      await expect(claimCard.getByTestId('stake-invite-claim-hub')).toBeVisible();
      await expect(claimCard.getByTestId('stake-invite-open-pay')).toBeVisible();

      await claimCard.getByTestId('stake-invite-claim-hub').click();
      await expect.poll(() => claims.length).toBe(1);
      expect(claims[0]).toMatchObject({
        invite_token: VALID_INVITE,
        challenge_token: 'inv-chal-tok',
        stake_address: STAKE_ADDRESS.replace(/ /g, ''),
      });
      await expect(claimCard.getByTestId('stake-invite-claimed')).toBeVisible();
    });
  });
});
