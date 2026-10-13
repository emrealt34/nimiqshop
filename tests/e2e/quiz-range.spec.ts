import { test, expect, open } from './support/fixtures';
import { path } from './support/data';

const draft = { title: 'Closest quiz', description: 'Guess the closest value.', prize: { title: 'Manual reward', value: '10 €', description: 'Each winner receives this reward.', image_url: '' }, winner_count: 5, tie_break: 'submitted_at', translations: { fr: { title: 'Quiz boutique', description: 'Découvre la boutique.' } },
 questions: Array.from({length:10},(_,i)=>({ type:'range', prompt:`Range question ${i+1}`,min:0,max:1000,correct_value:500,translations:{fr:{prompt:`Question boutique ${i+1}`}} })) };

for(const width of [320,1280]) {
 test.describe(`quiz JSON and range ${width}px`,()=>{
  test.use({viewport:{width,height:900}});
  test('JSON upload stays editable through save/update and explicit publish',async({page})=>{
   let stored:any=null;let saves=0;let published=0;
   await page.route('**/api/admin/auth/me',route=>route.fulfill({json:{admin:{id:'operator',username:'operator'},expires_at:Math.floor(Date.now()/1000)+3600}}));
   await page.route('**/api/admin/quiz**',route=>{
    if(route.request().method()==='POST'&&route.request().url().endsWith('/publish')){published++;stored.status='published';return route.fulfill({json:stored});}
    if(['POST','PUT'].includes(route.request().method())){saves++;stored={...route.request().postDataJSON(),id:'imported',status:route.request().postDataJSON().status || 'draft'};return route.fulfill({json:stored});}
    return route.fulfill({json:{items:stored?[stored]:[]}});
   });
   await open(page,path('/admin?section=quiz'));
   await page.getByLabel('JSON file').setInputFiles({name:'quiz.json',mimeType:'application/json',buffer:Buffer.from(JSON.stringify(draft))});
   await expect(page.getByLabel('Title',{exact:true})).toHaveValue('Closest quiz');
   await expect(page.locator('.quiz-admin-question')).toHaveCount(10);
   expect(saves).toBe(0);expect(published).toBe(0);
   await page.getByLabel('Title',{exact:true}).fill('Edited imported quiz');
   await page.locator('.quiz-admin-question').first().getByLabel('Correct value',{exact:true}).fill('600');
   await page.getByRole('button',{name:'Save draft',exact:true}).click();
   await expect.poll(()=>stored?.questions[0].correct_value).toBe(600);
   expect(stored.translations.fr.title).toBe('Quiz boutique'); expect(stored.questions[0].translations.fr.prompt).toBe('Question boutique 1'); expect(stored.winner_count).toBe(5);expect(stored.tie_break).toBe('submitted_at');expect(published).toBe(0);
   await page.getByRole('button',{name:'Edit',exact:true}).click();
   await page.getByLabel('Title',{exact:true}).fill('Updated saved quiz');
   await page.getByRole('button',{name:'Save draft',exact:true}).click();
   await expect.poll(()=>stored?.title).toBe('Updated saved quiz');expect(saves).toBe(2);
   await page.getByRole('button',{name:'Publish',exact:true}).click();
   await page.locator('.sheet').getByRole('button',{name:'Confirm',exact:true}).click();
   await expect.poll(()=>published).toBe(1);
   await expect(page.getByRole('button',{name:'Edit',exact:true})).toHaveCount(1);
   await page.getByRole('button',{name:'Edit',exact:true}).click();
   await page.getByLabel('Cashback rate (%)',{exact:true}).fill('4');
   await page.getByLabel('Ends',{exact:true}).fill('2026-11-11T12:00');
   await page.getByRole('button',{name:'Save changes',exact:true}).click();
   await expect.poll(()=>stored?.status).toBe('published');
   expect(stored.cashback_reward.cashback_bps).toBe(400);expect(stored.payout.enabled).toBe(false);
   expect(Date.parse(stored.ends_at)).toBe(Date.parse('2026-11-11T09:00:00Z'));

   expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
  });
  test('numeric guesses resume, invalid guesses cannot submit, results stay on quiz',async({page})=>{
   let attempt:any=null;let phase='live';
   const q=[{id:'range',type:'range',prompt:'Closest number?',options:[],min:0,max:1000}];
   const competition=()=>({id:'range-quiz',title:'Closest quiz',description:'',prize:draft.prize,starts_at:new Date(Date.now()-60000).toISOString(),ends_at:new Date(Date.now()+3600000).toISOString(),phase,question_count:1,participants:3,attempt,has_ranges:true,winner_count:3,winner:phase==='finalized'?'first':'',winners:phase==='finalized'?['first','me','third']:[],is_winner:phase==='finalized',decision_note:'',cancel_reason:'',award_delivered_at:null,leaderboard:attempt?.submitted_at?[{rank:1,participant:'first',score:1,is_me:false,is_winner:phase==='finalized'},{rank:2,participant:'me',score:.999,is_me:true,is_winner:phase==='finalized'}]:[]});
   await page.route('**/api/quiz**',route=>{
    const action=new URL(route.request().url()).pathname.split('/quiz')[1];
    if(!action||action==='/')return route.fulfill({json:{items:[competition()]}});
    if(action.endsWith('/start')){attempt ||= {answers:[-1],score:0,total:1,started_at:new Date().toISOString(),submitted_at:null};return route.fulfill({json:{competition:competition(),attempt,questions:q}});}
    if(action.endsWith('/answers')){attempt.answers=route.request().postDataJSON().answers;return route.fulfill({json:attempt});}
    if(action.endsWith('/submit')){expect(route.request().postDataJSON()).toEqual({answers:[499]});attempt={...attempt,score:.999,submitted_at:new Date().toISOString()};return route.fulfill({json:attempt});}
    return route.fulfill({json:competition()});
   });
   await open(page,path('/quiz'));await page.getByRole('button',{name:'Start quiz',exact:true}).click();
   const number=page.locator('#quiz-range-range');
   await number.fill('500.5');await expect(page.getByRole('button',{name:'Submit answers',exact:true})).toBeDisabled();
   await number.fill('0');await expect.poll(()=>attempt.answers[0]).toBe(0);
   await number.fill('499');await expect.poll(()=>attempt.answers[0]).toBe(499);
   await page.reload();await page.getByRole('button',{name:'Resume attempt',exact:true}).click();await expect(page.locator('#quiz-range-range')).toHaveValue('499');
   await page.getByRole('button',{name:'Submit answers',exact:true}).click();await expect(page.locator('.quiz-result')).toContainText('0.999 / 1');
   phase='finalized';await page.getByRole('button',{name:'Refresh',exact:true}).click();
   await expect(page.locator('.quiz-winner li')).toHaveCount(3);await expect(page.locator('.quiz-leaderboard')).toContainText('#2');
   expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
  });
 });
}

for (const lang of ['en', 'tr', 'de', 'fr', 'es', 'pt'] as const) {
 test.describe(`quiz authored translations ${lang}`, () => {
  test.use({ lang });
  test('selected site language renders the same quiz and numerical answer bounds', async ({ page }) => {
   const translations = Object.fromEntries(['en','tr','de','fr','es','pt'].map(code => [code, { title: `Shop quiz ${code}`, description: `Shop rules ${code}` }]));
   const question = { id: 'cashback', prompt: 'Default cashback?', options: [], type: 'range', min: 0, max: 200, translations: Object.fromEntries(['en','tr','de','fr','es','pt'].map(code => [code, { prompt: `Cashback base ${code}?` }])) };
   const c = { id:'localized', title:'Default quiz',description:'Default rules',translations,prize:{title:'Default prize',value:'10 €',description:'Default prize rules',image_url:'',translations},starts_at:new Date(Date.now()-60000).toISOString(),ends_at:new Date(Date.now()+3600000).toISOString(),phase:'live',question_count:1,participants:0,attempt:null,has_ranges:true,winner_count:3,winner:'',winners:[],is_winner:false,decision_note:'',cancel_reason:'',award_delivered_at:null,leaderboard:[] };
   await page.route('**/api/quiz**', route => route.fulfill({ json: route.request().url().endsWith('/start') ? {competition:c,attempt:{answers:[-1],score:0,total:1,started_at:new Date().toISOString(),submitted_at:null},questions:[question]} : new URL(route.request().url()).pathname.endsWith('/localized') ? c : {items:[c]} }));
   await open(page, path('/quiz'));
   await expect(page.locator('.quiz-contest')).toContainText(`Shop quiz ${lang}`);
   await expect(page.locator('.quiz-contest')).toContainText(`Shop rules ${lang}`);
   await page.locator('.quiz-contest > button').click();
   await expect(page.locator('.quiz-question legend')).toHaveText(`1. Cashback base ${lang}?`);
   await expect(page.locator('#quiz-range-cashback')).toHaveAttribute('max','200');
   await expect(page.locator('.quiz-question')).not.toContainText('correct_value');
  });
 });
}

for (const width of [320, 1280]) {
 test.describe(`single question progression ${width}px`, () => {
  test.use({viewport:{width,height:900},allowedErrors:[/500/]});
  test('one question at a time, failed saves retry, numeric bounds, resume, back and final submission', async ({page}) => {
   let attempt:any=null; let rejectNextSave=true; let submissions=0;
   const questions=[{id:'first',type:'choice',prompt:'Cashback base?',options:['100 NIM','105 NIM']},{id:'calc',type:'range',prompt:'Calculate cashback',options:[],min:0,max:100},{id:'last',type:'choice',prompt:'Contact support?',options:['Nimiq','CryptoRefills']}];
   const c=()=>({id:'steps',title:'Shop knowledge',description:'Short description',prize:{title:'NIM prize pool',value:'10 000 NIM',description:'Total pool shared by the top 3.',image_url:''},starts_at:new Date(Date.now()-60000).toISOString(),ends_at:new Date(Date.now()+3600000).toISOString(),phase:'live',question_count:3,participants:1,attempt,has_ranges:true,winner_count:5,tie_break:'submitted_at',winner:'',winners:[],is_winner:false,decision_note:'',cancel_reason:'',award_delivered_at:null,leaderboard:attempt?.submitted_at?[{rank:1,participant:'me',score:3,is_me:true,is_winner:false,submitted_at:attempt.submitted_at}]:[]});
   await page.route('**/api/quiz**',route=>{
    const action=new URL(route.request().url()).pathname.split('/quiz')[1];
    if(!action||action==='/')return route.fulfill({json:{items:[c()]}});
    if(action.endsWith('/start')){attempt ||= {answers:[-1,-1,-1],score:0,total:3,started_at:new Date().toISOString(),submitted_at:null};return route.fulfill({json:{competition:c(),attempt,questions}});}
    if(action.endsWith('/answers')){if(rejectNextSave){rejectNextSave=false;return route.fulfill({status:500,json:{error:'Temporary save failure'}});}attempt.answers=route.request().postDataJSON().answers;return route.fulfill({json:attempt});}
    if(action.endsWith('/submit')){submissions++;expect(route.request().postDataJSON().answers).toEqual([0,20,1]);attempt={...attempt,score:3,submitted_at:new Date().toISOString()};return route.fulfill({json:attempt});}
    return route.fulfill({json:c()});
   });
   await open(page,path('/quiz'));await page.getByRole('button',{name:'Start quiz',exact:true}).click();
   await expect(page.locator('.quiz-question')).toHaveCount(1);
   await expect(page.locator('#quiz-step-title')).toHaveText('Question 1 / 3');
   await expect(page.getByRole('button',{name:'Submit answers',exact:true})).toHaveCount(0);
   await expect(page.getByRole('button',{name:'Next question',exact:true})).toBeDisabled();
   await page.getByRole('radio',{name:'100 NIM',exact:true}).check();
   await expect(page.getByRole('button',{name:'Retry saving',exact:true})).toBeVisible();
   await expect(page.getByRole('button',{name:'Next question',exact:true})).toBeDisabled();
   await page.getByRole('button',{name:'Retry saving',exact:true}).click();
   await expect(page.getByRole('button',{name:'Next question',exact:true})).toBeEnabled();
   await page.getByRole('button',{name:'Next question',exact:true}).click();
   await expect(page.locator('#quiz-step-title')).toHaveText('Question 2 / 3');
   await page.locator('#quiz-range-calc').fill('20.5');
   await expect(page.getByRole('button',{name:'Next question',exact:true})).toBeDisabled();
   await page.locator('#quiz-range-calc').fill('20');await expect.poll(()=>attempt.answers[1]).toBe(20);
   await expect(page.getByRole('button',{name:'Next question',exact:true})).toBeEnabled();
   await page.locator('#quiz-range-calc').press('Enter');await expect(page.locator('#quiz-step-title')).toHaveText('Question 3 / 3');expect(submissions).toBe(0);
   await page.reload();await page.getByRole('button',{name:'Resume attempt',exact:true}).click();
   await expect(page.locator('#quiz-step-title')).toHaveText('Question 3 / 3');
   await page.getByRole('button',{name:'Previous question',exact:true}).click();await expect(page.locator('#quiz-range-calc')).toHaveValue('20');
   await page.getByRole('button',{name:'Previous question',exact:true}).click();await expect(page.getByRole('radio',{name:'100 NIM',exact:true})).toBeChecked();
   await page.getByRole('button',{name:'Next question',exact:true}).click();await page.getByRole('button',{name:'Next question',exact:true}).click();
   await page.getByRole('radio',{name:'CryptoRefills',exact:true}).check();await expect(page.getByRole('button',{name:'Submit answers',exact:true})).toBeEnabled();
   await page.getByRole('button',{name:'Submit answers',exact:true}).click();await expect(page.locator('.quiz-result')).toContainText('3 / 3');await expect(page.locator('.quiz-leaderboard time')).toHaveAttribute('datetime',attempt.submitted_at);expect(submissions).toBe(1);
   expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
  });
 });
}

for (const width of [320,1280]) {
 test.describe(`automatic quiz payment ${width}px`,()=>{
  test.use({viewport:{width,height:900}});
  test('winner sees memo and payment explorer without an email claim',async({page})=>{
   const hash='a'.repeat(64);
   const competition={id:'automatic',title:'Automatic NIM quiz',description:'',prize:{title:'NIM pool',value:'10 000 NIM'},starts_at:'2026-10-12T15:25:00+03:00',ends_at:'2026-11-11T12:00:00+03:00',phase:'finalized',question_count:10,participants:5,attempt:{answers:[],score:10,total:10,started_at:'2026-10-12T12:30:00Z',submitted_at:'2026-10-12T12:35:00Z'},winner:'player',winners:['player'],is_winner:true,tie_break:'submitted_at',winner_count:5,payout:{enabled:true,source:'cashback_wallet',pool_nim:10000,per_winner_nim:2000},payouts:[{participant:'player',status:'paid',amount_nim:2000,memo:'nimshop.io #1 2000 NIM 11111111-2222-3333-4444-555555555555',tx_hash:hash,paid_at:'2026-11-11T09:02:00Z'}],leaderboard:[{rank:1,participant:'player',score:10,is_me:true,is_winner:true}]};
   await page.route('**/api/quiz**',route=>route.fulfill({json:new URL(route.request().url()).pathname.endsWith('/quiz')?{items:[competition]}:competition}));
   await open(page,path('/quiz'));
   await expect(page.getByRole('heading',{name:'Automatic NIM quiz'})).toBeVisible();
   await expect(page.locator('.quiz-payout-list')).toContainText('2000 NIM');
   await expect(page.locator('.quiz-payout-list')).toContainText('Memo: nimshop.io #1');
   await expect(page.locator('.quiz-payout-list a')).toHaveAttribute('href',new RegExp(hash));
   await expect(page.locator('input[type="email"]')).toHaveCount(0);
   expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
  });
 });
}
