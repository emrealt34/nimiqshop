import { test, expect, open } from './support/fixtures';
import { path } from './support/data';

const baseCompetition = {
  id: 'nimiq-quiz-test', title: 'Nimiq daily competition', description: 'Learn about Nimiq.',
  prize: { title: 'Amazon gift card', value: '5 USD', description: 'Manual delivery by the admin.', image_url: '' },
  starts_at: new Date(Date.now() - 3600000).toISOString(), ends_at: new Date(Date.now() + 3600000).toISOString(),
  phase: 'live', question_count: 2, participants: 0, attempt: null, leaderboard: [],
  winner: '', is_winner: false, decision_note: '', cancel_reason: '', award_delivered_at: null,
};
const questions = [
  { id: 'ticker', prompt: 'Nimiq ticker?', options: ['BTC', 'NIM'] },
  { id: 'wallet', prompt: 'Nimiq wallet?', options: ['Other', 'Nimiq Pay'] },
];

test.describe('quiz login gate @smoke', () => {
  test.use({ api: { authed: false }, lang: 'tr' });
  test('guests see only the login gate and make no quiz API requests', async ({ page, requests }) => {
    await open(page, path('/quiz'));
    await expect(page.getByRole('heading', { name: 'Katılmak için giriş yap' })).toBeVisible();
    await expect(page.getByRole('button', { name: 'Cüzdanla devam et' })).toBeVisible();
    await expect(page.locator('.quiz-contest')).toHaveCount(0);
    expect(requests.filter((request) => request.includes('/api/quiz'))).toEqual([]);
  });
});

test.describe('quiz participant flow @smoke', () => {
  test.use({ lang: 'tr', viewport: { width: 390, height: 844 } });
  test('server-saved progress resumes after reload and submission has no second attempt', async ({ page }) => {
    let attempt: any = null; let submitted = 0; let reservations = 0;
    const competition = () => ({ ...baseCompetition, attempt, participants: attempt ? 1 : 0, leaderboard: attempt?.submitted_at ? [{ participant: 'me', score: 2, is_me: true, is_winner: false }] : [] });
    await page.route('**/api/quiz**', (route) => {
      const action = new URL(route.request().url()).pathname.split('/quiz')[1];
      if (!action || action === '/') return route.fulfill({ json: { items: [competition()] } });
      if (action.endsWith('/start')) {
        if (!attempt) { reservations++; attempt = { answers: [-1, -1], score: 0, total: 2, started_at: new Date().toISOString(), submitted_at: null, prize_email: '' }; }
        return route.fulfill({ json: { competition: competition(), attempt, questions } });
      }
      if (action.endsWith('/answers')) { attempt.answers = route.request().postDataJSON().answers; return route.fulfill({ json: attempt }); }
      if (action.endsWith('/submit')) {
        expect(route.request().postDataJSON()).toEqual({ answers: [1, 1] });
        submitted++; attempt = { ...attempt, score: 2, submitted_at: new Date().toISOString() };
        return route.fulfill({ json: attempt });
      }
      return route.fulfill({ json: competition() });
    });
    await open(page, path('/quiz'));
    await expect(page.locator('.quiz-prize')).toContainText('Amazon gift card');
    await expect(page.locator('.quiz-prize')).toContainText('5 USD');
    await page.getByRole('button', { name: 'Yarışmaya başla' }).click();
    await page.locator('.quiz-question').nth(0).getByRole('radio', { name: 'NIM', exact: true }).check();
    await expect.poll(() => attempt.answers[0]).toBe(1);
    await page.reload();
    await page.getByRole('button', { name: 'Denemeye devam et' }).click();
    await expect(page.locator('.quiz-question').nth(0).getByRole('radio', { name: 'NIM', exact: true })).toBeChecked();
    await page.locator('.quiz-question').nth(1).getByRole('radio', { name: 'Nimiq Pay', exact: true }).check();
    await page.getByRole('button', { name: 'Cevapları gönder', exact: true }).click();
    await expect(page.locator('.quiz-result')).toContainText('Puanın: 2 / 2');
    await expect(page.getByRole('button', { name: 'Yarışmaya başla' })).toHaveCount(0);
    await expect(page.getByRole('button', { name: 'Denemeye devam et' })).toHaveCount(0);
    await expect(page.locator('.quiz-question')).toHaveCount(0);
    expect(reservations).toBe(1); expect(submitted).toBe(1);
  });

  test('a confirmed winner can save a private delivery email, with no automatic payment', async ({ page, requests }) => {
    let email = '';
    const competition = () => ({ ...baseCompetition, phase: 'finalized', winner: 'winner', is_winner: true, attempt: { answers: [1,1], score: 2, total: 2, started_at: new Date().toISOString(), submitted_at: new Date().toISOString(), prize_email: email } });
    await page.route('**/api/quiz**', (route) => {
      if (route.request().method() === 'POST') { email = route.request().postDataJSON().email; return route.fulfill({ json: competition().attempt }); }
      return route.fulfill({ json: new URL(route.request().url()).pathname.endsWith('/quiz') ? { items: [competition()] } : competition() });
    });
    await open(page, path('/quiz'));
    await expect(page.getByRole('heading', { name: 'Kazandın!' })).toBeVisible();
    await page.getByLabel('Ödül teslim e-postası').fill('winner@example.com');
    await page.getByRole('button', { name: 'Teslim e-postasını kaydet' }).click();
    await expect.poll(() => email).toBe('winner@example.com');
    expect(requests.filter((request) => request.startsWith('POST ') && /payment-launch|\/quotes|\/send-order-email/.test(request))).toEqual([]);
  });
});

test.describe('operator quiz authoring @smoke', () => {
  test.use({ api: { authed: false } });
  test('operator authors questions and a displayed prize, saves a draft and publishes through confirmation', async ({ page }) => {
    let stored: any = null;
    await page.route('**/api/admin/auth/me', (route) => route.fulfill({ json: { admin: { id: 'operator', username: 'operator' }, expires_at: Math.floor(Date.now()/1000)+3600 } }));
    await page.route('**/api/admin/quiz**', (route) => {
      if (route.request().method() === 'POST' && new URL(route.request().url()).pathname.endsWith('/publish')) { stored.status = 'published'; return route.fulfill({ json: stored }); }
      if (route.request().method() === 'POST') { stored = { ...route.request().postDataJSON(), id: 'admin-quiz-test', status: 'draft', participants: 0 }; return route.fulfill({ json: stored }); }
      return route.fulfill({ json: { items: stored ? [stored] : [] } });
    });
    await open(page, path('/admin?section=quiz'));
    await page.getByRole('button', { name: 'New competition', exact: true }).click();
    await page.getByLabel('Title', { exact: true }).fill('Nimiq competition');
    await page.getByLabel('Prize name (e.g. Amazon gift card)').fill('Amazon gift card');
    await page.getByLabel('Value (e.g. 5 USD)').fill('5 USD');
    await page.getByRole('button', { name: 'Add question', exact: true }).click();
    await page.getByLabel('Question text').fill('Nimiq ticker?');
    for (const [index, value] of ['BTC', 'NIM', 'ETH', 'USDT'].entries()) await page.getByLabel(`Question 1, option ${index + 1}`, { exact: true }).fill(value);
    await page.getByRole('radio', { name: 'Option 2 is correct' }).check();
    await page.getByRole('button', { name: 'Save draft', exact: true }).click();
    await expect.poll(() => stored?.questions[0]?.correct_index).toBe(1);
    await expect(page.locator('.quiz-admin-list')).toContainText('Nimiq competition');
    await page.getByRole('button', { name: 'Publish', exact: true }).click();
    await page.locator('.sheet').getByRole('button', { name: 'Confirm', exact: true }).click();
    await expect.poll(() => stored?.status).toBe('published');
    await expect(page.locator('.quiz-admin-list')).toContainText('published');
  });
});
