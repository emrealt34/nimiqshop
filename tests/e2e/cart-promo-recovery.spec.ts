import {test,expect,open} from './support/fixtures';
import {path} from './support/data';

const code='ILOVENIM';
const quiz={id:'coupon',title:'Shop quiz',description:'',prize:{title:'Cashback code',value:'',description:'',image_url:''},starts_at:new Date(Date.now()-60000).toISOString(),ends_at:new Date(Date.now()+3600000).toISOString(),phase:'live',question_count:1,participants:1,cashback_reward:{code,cashback_bps:100,max_recipients:0,min_score_percent:0},cashback_unlocked:true,attempt:{answers:[0],score:1,total:1,started_at:new Date().toISOString(),submitted_at:new Date().toISOString()}};

for (const width of [320,1280]) test.describe(`promo cart recovery ${width}px`,()=>{
 test.use({viewport:{width,height:900},allowedErrors:[/Failed to fetch dynamically imported module|net::ERR_FAILED|dynamically imported|Importing a module|Loading chunk/i]});
 test('applied quiz code survives a failed cart module and fresh cart navigation',async({page})=>{
  await page.addInitScript(()=>{
   if(!localStorage.getItem('nimshop_cart'))localStorage.setItem('nimshop_cart',JSON.stringify([{id:'amazon-tr',name:'Amazon TR',type:'giftcard',country:'TR',currency:'TRY',pkg:'TRY250',value:250,denomination:'TRY 250',unitUSD:7.4,qty:1,bgColor:'#1f2348'}]));
  });
  await page.route('**/api/quiz**',r=>r.fulfill({json:new URL(r.request().url()).pathname.endsWith('/quiz')?{items:[quiz]}:quiz}));
  await page.route('**/api/cashback/code?*',r=>r.fulfill({json:{code,cashback_bps:100,cashback_percent:1}}));
  let blocked=false;
  await page.route('**/CartSheet.*.js',r=>{if(!blocked){blocked=true;return r.abort();}return r.continue();});
  await open(page,path('/quiz'));
  await page.getByRole('button',{name:'Use this cashback code',exact:true}).click();
  await expect.poll(()=>page.evaluate(()=>JSON.parse(sessionStorage.getItem('nimshop_cashback_code')||'{}').code)).toBe(code);
  await page.locator('.cart-btn').click();
  await expect(page).toHaveURL(/\/cart\/?$/);
  await expect(page.getByRole('heading',{name:'Your cart',exact:true})).toBeVisible();
  await expect(page.locator('.cart-row')).toHaveCount(1);
  await expect(page.locator('.cart-row')).toContainText('Amazon TR');
  expect(blocked).toBe(true);
  expect(await page.evaluate(()=>JSON.parse(sessionStorage.getItem('nimshop_cashback_code')||'{}').code)).toBe(code);
  // The rest of the shell remains usable after the recovery navigation.
  await page.locator('.topbar a.brand').click();await expect(page).toHaveURL(new RegExp(path('/')+'$'));
  await page.locator('.cart-btn').click();await expect(page.locator('.sheet')).toBeVisible();
 });
});

test.describe('route module recovery',()=>{
 test.use({allowedErrors:[/Failed to fetch dynamically imported module|net::ERR_FAILED|dynamically imported|Importing a module|Loading chunk/i]});
 test('a stale route chunk loads a fresh destination document without a reload loop',async({page})=>{
  let blocked=false;
  await page.route('**/OrdersPage.*.js',r=>{if(!blocked){blocked=true;return r.abort();}return r.continue();});
  await open(page,path('/quiz'));
  await page.locator('nav.mainnav a[href*="orders"]').click();
  await expect(page).toHaveURL(/\/orders\/?$/);
  await expect(page.locator('.orders-page')).toBeVisible();
  expect(blocked).toBe(true);
 });
});
