import {test,expect,open} from './support/fixtures';
import {path} from './support/data';
test('supplier processing shows elapsed time and updates it',async({page})=>{
 const q={id:'duration-test',product_id:'Amazon',status:'processing',created_at:new Date(Date.now()-120000).toISOString(),paid_at:new Date(Date.now()-65000).toISOString()};
 await page.route('**/api/quotes/duration-test',r=>r.fulfill({json:q}));
 await open(page,path('/order?id=duration-test&type=quote'));
 const timer=page.locator('.timeline [role="timer"]');await expect(timer).toBeVisible();
 const before=await timer.innerText();await expect.poll(()=>timer.innerText()).not.toBe(before);
});
test('completed supplier duration uses payment and delivery timestamps',async({page})=>{
 const paid=Date.now()-120000;
 const q={id:'duration-done',product_id:'Amazon',status:'fulfilled',rating:5,created_at:new Date(paid-60000).toISOString(),paid_at:new Date(paid).toISOString(),delivered_at:new Date(paid+65000).toISOString()};
 await page.route('**/api/quotes/duration-done',r=>r.fulfill({json:q}));
 await open(page,path('/order?id=duration-done&type=quote'));
 await expect(page.locator('.timeline')).toContainText('1 min 5s');
 await expect(page.locator('.timeline [role="timer"]')).toHaveCount(0);
});
