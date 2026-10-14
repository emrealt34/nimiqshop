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
        submitted++; attempt = { ...attempt, answers: route.request().postDataJSON().answers, score: 2, submitted_at: new Date().toISOString() };
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
    await expect(page.locator('.quiz-question')).toHaveCount(1);
    await expect(page.locator('.quiz-question legend')).toContainText('2.');
    await page.getByRole('button', { name: 'Önceki soru', exact: true }).click();
    await expect(page.getByRole('radio', { name: 'NIM', exact: true })).toBeChecked();
    await page.getByRole('button', { name: 'Sonraki soru', exact: true }).click();
    await page.locator('.quiz-question').getByRole('radio', { name: 'Nimiq Pay', exact: true }).check();
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
    await page.getByLabel('Shared cashback code').fill('ILOVENIM');
    await page.getByLabel('Code mode',{exact:true}).selectOption('unique');
    await expect(page.getByLabel('Code prefix')).toHaveValue('ILOVENIM');
    await page.getByLabel('Cashback rate (%)', {exact:true}).fill('5');
    await page.getByLabel('Accounts that may unlock the code (0 = unlimited)').fill('0');
    await page.getByRole('button', { name: 'Add question', exact: true }).click();
    await page.getByLabel('Question text').fill('Nimiq ticker?');
    for (const [index, value] of ['BTC', 'NIM', 'ETH', 'USDT'].entries()) await page.getByLabel(`Question 1, option ${index + 1}`, { exact: true }).fill(value);
    await page.getByRole('radio', { name: 'Option 2 is correct' }).check();
    await expect(page.getByRole('button',{name:'Export draft JSON',exact:true})).toBeVisible();
    await page.getByRole('button', { name: 'Save draft', exact: true }).click();
    await expect.poll(() => stored?.questions[0]?.correct_index).toBe(1);
    expect(stored.cashback_reward).toEqual({code:'ILOVENIM',code_mode:'unique',cashback_bps:500,max_recipients:0,min_score_percent:0});
    await expect(page.locator('.quiz-admin-list')).toContainText('Nimiq competition');
    await page.getByRole('button', { name: 'Publish', exact: true }).click();
    await page.locator('.sheet').getByRole('button', { name: 'Confirm', exact: true }).click();
    await expect.poll(() => stored?.status).toBe('published');
    await expect(page.locator('.quiz-admin-list')).toContainText('published');
  });
});

test('completed cashback quiz unlocks a code without a prize pool or ranking',async({page})=>{
 const c={...baseCompetition,cashback_reward:{code:'ILOVENIM',code_mode:'unique',cashback_bps:500,max_recipients:0,min_score_percent:0},cashback_unlocked:true,attempt:{answers:[1,1],score:2,total:2,started_at:new Date().toISOString(),submitted_at:new Date().toISOString()}};
 await page.route('**/api/quiz**',r=>r.fulfill({json:r.request().url().endsWith('/quiz')?{items:[c]}:c}));
 await open(page,path('/quiz'));
 await expect(page.locator('.quiz-cashback-reward')).toContainText('ILOVENIM');
 await expect(page.locator('.quiz-leaderboard')).toHaveCount(0);
 await expect(page.locator('.quiz-earned-code button')).toBeVisible();
});

for(const width of [320,1280])test(`cashback code stays hidden until quiz completion at ${width}px`,async({page})=>{
 await page.setViewportSize({width,height:900});
 const c={...baseCompetition,cashback_reward:{code:'ILOVENIM',code_mode:'shared',cashback_bps:100,max_recipients:0,min_score_percent:0},cashback_unlocked:false};
 await page.route('**/api/quiz**',r=>r.fulfill({json:r.request().url().endsWith('/quiz')?{items:[c]}:c}));
 await open(page,path('/quiz'));await expect(page.locator('.quiz-cashback-reward')).toBeVisible();
 await expect(page.locator('.quiz-contest')).not.toContainText('ILOVENIM');
 await expect(page.locator('.quiz-earned-code')).toHaveCount(0);
 expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
});

for (const width of [320,1280]) test(`quiz stays visible during slow detail loading and confirmed completion at ${width}px`, async ({page}) => {
 await page.setViewportSize({width,height:900});
 let completed=false;let detailReads=0;
 const attempt={answers:[1,1],score:2,total:2,started_at:new Date().toISOString(),submitted_at:null as string|null};
 const c=()=>({...baseCompetition,cashback_reward:{code:completed?'ILOVENIM':'',cashback_bps:100,max_recipients:0,min_score_percent:0},cashback_unlocked:completed,attempt:completed?{...attempt,submitted_at:new Date().toISOString()}:null});
 await page.route('**/api/quiz**',async route=>{
  const action=new URL(route.request().url()).pathname;
  if(action.endsWith('/quiz'))return route.fulfill({json:{items:[c()]}});
  if(action.endsWith('/start'))return route.fulfill({json:{competition:c(),attempt,questions}});
  detailReads++;
  if(detailReads===1)await new Promise(resolve=>setTimeout(resolve,9000));
  return route.fulfill({json:c()});
 });
 await open(page,path('/quiz'));
 // The list already includes everything needed for the card. A slow detail
 // request must not replace the loaded page with an empty content area.
 await expect(page.locator('.quiz-contest')).toContainText(baseCompetition.title,{timeout:2000});
 await page.getByRole('button',{name:'Start quiz',exact:true}).click();
 await expect(page.locator('.quiz-question')).toBeVisible();
 completed=true;
 // Completion may be confirmed by refresh after a lost submit response or
 // another tab. Keep its result visible even while questions remain in state.
 await page.evaluate(()=>window.dispatchEvent(new Event('focus')));
 await expect(page.locator('.quiz-result')).toContainText('2 / 2',{timeout:12000});
 await expect(page.locator('.quiz-earned-code')).toContainText('ILOVENIM');
 await expect(page.locator('.quiz-question')).toHaveCount(0);
});

for (const width of [320,1280]) test(`failed autosave never blocks quiz navigation, local resume or final submission at ${width}px`, async ({page}) => {
 await page.setViewportSize({width,height:900});
 let attempt:any={answers:[-1,-1],score:0,total:2,started_at:new Date().toISOString(),submitted_at:null};let submits=0;let saves=0;
 const c=()=>({...baseCompetition,attempt,cashback_reward:{code:attempt.submitted_at?'ILOVENIM':'',cashback_bps:100,max_recipients:0,min_score_percent:0},cashback_unlocked:!!attempt.submitted_at});
 await page.route('**/api/quiz**',async route=>{
  const action=new URL(route.request().url()).pathname;
  if(action.endsWith('/quiz'))return route.fulfill({json:{items:[c()]}});
  if(action.endsWith('/start'))return route.fulfill({json:{competition:c(),attempt,questions}});
  if(action.endsWith('/answers')){
   saves++;
   if(saves===1)return route.fulfill({status:429,json:{error:'rate limit exceeded'}});
   await new Promise(resolve=>setTimeout(resolve,9000));
   return route.fulfill({status:409,json:{error:'quiz attempt already submitted'}}).catch(()=>{});
  }
  if(action.endsWith('/submit')){submits++;expect(route.request().postDataJSON().answers).toEqual([1,1]);attempt={...attempt,answers:[1,1],score:2,submitted_at:new Date().toISOString()};return route.fulfill({json:attempt});}
  return route.fulfill({json:c()});
 });
 await open(page,path('/quiz'));await page.getByRole('button',{name:'Resume attempt',exact:true}).click();
 await page.getByRole('radio',{name:'NIM',exact:true}).check();
 await expect(page.getByRole('button',{name:'Retry saving',exact:true})).toBeVisible();
 await expect(page.getByRole('button',{name:'Next question',exact:true})).toBeEnabled();
 await page.getByRole('button',{name:'Next question',exact:true}).click();
 await expect(page.locator('#quiz-step-title')).toHaveText('Question 2 / 2');
 await page.reload();await page.getByRole('button',{name:'Resume attempt',exact:true}).click();
 await expect(page.locator('#quiz-step-title')).toHaveText('Question 2 / 2');
 await page.getByRole('button',{name:'Previous question',exact:true}).click();
 await expect(page.getByRole('radio',{name:'NIM',exact:true})).toBeChecked();
 await page.getByRole('button',{name:'Next question',exact:true}).click();
 await page.getByRole('radio',{name:'Nimiq Pay',exact:true}).check();
 await expect.poll(()=>saves).toBe(2);
 await page.getByRole('button',{name:'Submit answers',exact:true}).click();
 await expect(page.locator('.quiz-result')).toContainText('2 / 2');
 await expect(page.locator('.quiz-earned-code')).toContainText('ILOVENIM');expect(submits).toBe(1);
 expect(await page.evaluate(()=>Object.keys(localStorage).filter(k=>k.startsWith('nimshop.quiz.draft:')).length)).toBe(0);
});

test('an invalid submission acknowledgement retains the local draft and permits retry',async({page})=>{
 let attempt:any={answers:[-1,-1],score:0,total:2,started_at:new Date().toISOString(),submitted_at:null};let submits=0;
 const c=()=>({...baseCompetition,attempt});
 await page.route('**/api/quiz**',route=>{
  const action=new URL(route.request().url()).pathname;
  if(action.endsWith('/quiz'))return route.fulfill({json:{items:[c()]}});
  if(action.endsWith('/start'))return route.fulfill({json:{competition:c(),attempt,questions}});
  if(action.endsWith('/answers'))return route.fulfill({json:attempt});
  if(action.endsWith('/submit')){submits++;if(submits===1)return route.fulfill({json:{}});attempt={...attempt,answers:[1,1],score:2,submitted_at:new Date().toISOString()};return route.fulfill({json:attempt});}
  return route.fulfill({json:c()});
 });
 await open(page,path('/quiz'));await page.getByRole('button',{name:'Resume attempt',exact:true}).click();
 await page.getByRole('radio',{name:'NIM',exact:true}).check();await page.getByRole('button',{name:'Next question',exact:true}).click();
 await page.getByRole('radio',{name:'Nimiq Pay',exact:true}).check();await page.getByRole('button',{name:'Submit answers',exact:true}).click();
 await expect(page.getByRole('alert')).toBeVisible();await expect(page.locator('.quiz-result')).toHaveCount(0);
 expect(await page.evaluate(()=>Object.keys(localStorage).filter(k=>k.startsWith('nimshop.quiz.draft:')).length)).toBe(1);
 await page.getByRole('button',{name:'Submit answers',exact:true}).click();await expect(page.locator('.quiz-result')).toContainText('2 / 2');
 expect(submits).toBe(2);
});

for (const width of [320,1280]) test(`completed quiz reviews correct, incorrect and numerical answers at ${width}px`,async({page})=>{
 await page.setViewportSize({width,height:900});
 const c={...baseCompetition,attempt:{answers:[1,0,19],score:1.99,total:3,started_at:new Date().toISOString(),submitted_at:new Date().toISOString()},question_count:3,cashback_reward:{code:'ILOVENIM',cashback_bps:100,max_recipients:0,min_score_percent:0},cashback_unlocked:true,review:[
  {question:questions[0],answer:1,correct_answer:1,correct:true},
  {question:questions[1],answer:0,correct_answer:1,correct:false},
  {question:{id:'calc',type:'range',prompt:'2% of 1000 NIM?',options:[],min:0,max:100},answer:19,correct_answer:20,correct:false}
 ]};
 await page.route('**/api/quiz**',r=>r.fulfill({json:new URL(r.request().url()).pathname.endsWith('/quiz')?{items:[c]}:c}));
 await open(page,path('/quiz'));
 const review=page.locator('.quiz-review');await expect(review).toBeVisible();
 await expect(review.locator('li')).toHaveCount(3);
 await expect(review.locator('li').nth(0)).toContainText('Correct');
 await expect(review.locator('li').nth(1)).toContainText('Incorrect');
 await expect(review.locator('li').nth(1).locator('dd')).toHaveText(['Other','Nimiq Pay']);
 await expect(review.locator('li').nth(2).locator('dd')).toHaveText(['19','20']);
 await expect(page.locator('.quiz-question')).toHaveCount(0);
 expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
});
