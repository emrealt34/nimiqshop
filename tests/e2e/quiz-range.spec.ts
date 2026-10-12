import { test, expect, open } from './support/fixtures';
import { path } from './support/data';

const draft = { title: 'Closest quiz', description: 'Guess the closest value.', prize: { title: 'Manual reward', value: '10 €', description: 'Each winner receives this reward.', image_url: '' }, winner_count: 3, translations: { fr: { title: 'Quiz boutique', description: 'Découvre la boutique.' } },
 questions: Array.from({length:10},(_,i)=>({ type:'range', prompt:`Range question ${i+1}`,min:0,max:1000,correct_value:500,translations:{fr:{prompt:`Question boutique ${i+1}`}} })) };

for(const width of [320,1280]) {
 test.describe(`quiz JSON and range ${width}px`,()=>{
  test.use({viewport:{width,height:900}});
  test('JSON upload stays editable through save/update and explicit publish',async({page})=>{
   let stored:any=null;let saves=0;let published=0;
   await page.route('**/api/admin/auth/me',route=>route.fulfill({json:{admin:{id:'operator',username:'operator'},expires_at:Math.floor(Date.now()/1000)+3600}}));
   await page.route('**/api/admin/quiz**',route=>{
    if(route.request().method()==='POST'&&route.request().url().endsWith('/publish')){published++;stored.status='published';return route.fulfill({json:stored});}
    if(['POST','PUT'].includes(route.request().method())){saves++;stored={...route.request().postDataJSON(),id:'imported',status:'draft'};return route.fulfill({json:stored});}
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
   expect(stored.translations.fr.title).toBe('Quiz boutique'); expect(stored.questions[0].translations.fr.prompt).toBe('Question boutique 1'); expect(stored.winner_count).toBe(3);expect(published).toBe(0);
   await page.getByRole('button',{name:'Edit',exact:true}).click();
   await page.getByLabel('Title',{exact:true}).fill('Updated saved quiz');
   await page.getByRole('button',{name:'Save draft',exact:true}).click();
   await expect.poll(()=>stored?.title).toBe('Updated saved quiz');expect(saves).toBe(2);
   await page.getByRole('button',{name:'Publish',exact:true}).click();
   await page.locator('.sheet').getByRole('button',{name:'Confirm',exact:true}).click();
   await expect.poll(()=>published).toBe(1);
   await expect(page.getByRole('button',{name:'Edit',exact:true})).toHaveCount(0);
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
