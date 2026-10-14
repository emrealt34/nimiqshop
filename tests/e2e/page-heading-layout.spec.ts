import {test,expect,open} from './support/fixtures';
import {path} from './support/data';

for(const width of [320,1280])for(const lang of ['tr','de'] as const)test.describe(`page headings ${width}px ${lang}`,()=>{
 test.use({viewport:{width,height:900},lang});
 test('secondary page titles are centered and controls fit the viewport',async({page},testInfo)=>{
  await page.route('**/api/catalog/products/**',r=>r.fulfill({json:[{family:'p1',kind:'giftcard',country_code:'TR',products:[{product_id:'heading-sku',denomination:'10 USD',coin:'BTC',coin_amount:'0.0001',delivery_type:'by_email',face_value:{currency_code:'USD',amount:{type:'fixed',price:'10'}}}]}]}));
  await page.route('**/api/catalog/unavailable**',r=>r.fulfill({json:{country:'TR',families:[]}}));
  await page.route('**/api/catalog/price**',r=>r.fulfill({json:{coin:'BTC',coin_amount:'0.0001',price_expires_at:new Date(Date.now()+60000).toISOString()}}));
  for(const route of ['/activity','/orders','/cashback','/profile','/support','/quiz','/cart','/track?order=o1','/product?id=p1&country=TR','/order?id=o1','/status','/admin']){
   await open(page,path(route));
   const heading=page.locator('#page-content h1, .orders-page .section-head h2, .order-summary-heading h2').first();
   await expect(heading,route).toBeVisible();
   expect(await heading.evaluate(el=>getComputedStyle(el).textAlign),route).toBe('center');
   expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),route).toBe(true);
   if(route==='/cashback'){
    const refresh=page.locator('.cb-recent-card .cb-ledger-refresh');await expect(refresh).toBeVisible();
    expect(await refresh.evaluate(el=>!!el.closest('.cb-payouts-head'))).toBe(true);
    if(lang==='tr'){
     await page.screenshot({path:testInfo.outputPath('cashback-heading.png')});
     await page.locator('.cb-recent-card').screenshot({path:testInfo.outputPath('cashback-refresh-card.png')});
    }
   }
  }
 });
});
