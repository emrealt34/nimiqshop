import { test, expect, open } from './support/fixtures';
import { path } from './support/data';

const delivered = { id:'review-quote',product_id:'Amazon',status:'fulfilled',rating:0,created_at:new Date(Date.now()-3*86400000).toISOString(),delivered_at:new Date(Date.now()-2*86400000).toISOString() };
for(const width of [320,390,1280]) {
 test.describe(`returning shopper rating reminder ${width}px`,()=>{
  test.use({viewport:{width,height:844},lang:'tr'});
  test('old unrated purchase offers review, snoozes after later, and never submits a rating itself',async({page,requests})=>{
   let historyReads=0;
   await page.route('**/api/ratings/config',r=>r.fulfill({json:{enabled:true,comments_enabled:true}}));
   await page.route('**/api/orders',r=>{historyReads++;return r.fulfill({json:[]});});
   await page.route('**/api/quotes',r=>{historyReads++;return r.fulfill({json:[delivered]});});
   await open(page,path('/'));
   const sheet=page.locator('.sheet');
   await expect(sheet).toContainText('Henüz değerlendirmediğin alışverişlerin var');
   await expect(sheet).toContainText('Teslim edilmiş 1 alışverişini');
   await expect(sheet.getByRole('link',{name:'Değerlendir · Amazon'})).toHaveAttribute('href',/order\?id=review-quote&type=quote/);
   expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
   expect(requests.filter(r=>/\/rate$|\/rating\/intent$/.test(r))).toEqual([]);
   await sheet.getByRole('button',{name:'Daha sonra',exact:true}).click();
   await expect(sheet).toHaveCount(0);
   expect(await page.evaluate(()=>Number(localStorage.getItem('nimshop.rating-reminder.v2:u1:quote:review-quote')))).toBeGreaterThan(Date.now()+6*86400000);
   const previousReads=historyReads;
   await page.reload();await expect(page.locator('.sheet')).toHaveCount(0);
   await expect(page.locator('.hero')).toBeVisible();
   // Other storefront controls also read history. The reminder must still
   // read fresh purchases on a return visit, even when old ones are snoozed.
   await expect.poll(()=>historyReads).toBeGreaterThanOrEqual(previousReads+2);
  });
  test('review link leads to the existing manual star form',async({page})=>{
   await page.route('**/api/ratings/config',r=>r.fulfill({json:{enabled:true,comments_enabled:true}}));
   await page.route('**/api/orders',r=>r.fulfill({json:[]}));
   await page.route('**/api/quotes',r=>r.fulfill({json:[delivered]}));
   await page.route('**/api/quotes/review-quote',r=>r.fulfill({json:delivered}));
   await open(page,path('/'));
   await page.locator('.sheet').getByRole('link',{name:'Değerlendir · Amazon'}).click();
   await expect(page).toHaveURL(/order\/?\?id=review-quote&type=quote#purchase-rating/);
   await expect(page.locator('.sheet')).toHaveCount(0);
   await expect(page.locator('.rating-flow')).toBeVisible();
   await expect(page.locator('.rating-flow .star-btn')).toHaveCount(5);
   await expect(page.locator('.rating-flow .stars.picker')).toBeInViewport();
  });
  test('fresh delivery prompts on the next shop visit but never on the completed order page',async({page})=>{
   const fresh={...delivered,test_mode:true,delivered_at:new Date().toISOString(),updated_at:new Date().toISOString()};
   await page.route('**/api/ratings/config',r=>r.fulfill({json:{enabled:true,comments_enabled:true}}));
   await page.route('**/api/orders',r=>r.fulfill({json:[]}));
   await page.route('**/api/quotes',r=>r.fulfill({json:[fresh]}));
   await page.route('**/api/quotes/review-quote',r=>r.fulfill({json:fresh}));
   await open(page,path('/order?id=review-quote&type=quote'));
   await expect(page.locator('.rating-flow')).toBeVisible();
   await expect(page.locator('.sheet')).toHaveCount(0);
   await open(page,path('/'));
   await expect(page.locator('.sheet')).toContainText('Henüz değerlendirmediğin alışverişlerin var');
  });
 });
}
test.describe('rating reminder guest privacy',()=>{
 test.use({api:{authed:false}});
 test('guest never requests private purchase histories',async({page,requests})=>{
  await open(page,path('/'));await expect(page.locator('.hero')).toBeVisible();
  expect(requests.filter(r=>/\/api\/(orders|quotes)$/.test(r))).toEqual([]);
  await expect(page.locator('.sheet')).toHaveCount(0);
 });
});
