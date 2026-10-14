import {test,expect,open} from './support/fixtures';
import {path} from './support/data';
test('test menu switches public test visibility with one click',async({page})=>{
 let enabled=false;
 await page.route('**/api/admin/auth/me',r=>r.fulfill({json:{admin:{id:'operator',username:'operator'},expires_at:Math.floor(Date.now()/1000)+3600}}));
 await page.route('**/api/admin/test-activity',r=>{if(r.request().method()==='POST')enabled=r.request().postDataJSON().enabled;return r.fulfill({json:{enabled}})});
 await page.setViewportSize({width:320,height:900});
 await open(page,path('/admin?section=test'));
 const toggle=page.getByRole('switch');await expect(toggle).toHaveAttribute('aria-checked','false');
 await toggle.click();await expect(toggle).toHaveAttribute('aria-checked','true');expect(enabled).toBe(true);
 await toggle.click();await expect(toggle).toHaveAttribute('aria-checked','false');expect(enabled).toBe(false);
});
test('explicitly displayed test payments keep a visible simulation label',async({page})=>{
 await page.route('**/api/activity**',r=>r.fulfill({json:{items:[{id:'shown-simulation',title:'Amazon',country:'US',status:'fulfilled',nim:5000,usd:'5',time:new Date().toISOString(),test_mode:true,test_activity_visible:true}],summary:{count:0,average:0,dist:[0,0,0,0,0]}}}));
 await open(page,path('/activity'));
 await expect(page.locator('.feed-item[data-order-id="shown-simulation"]')).toContainText('TEST · simulated');
});
