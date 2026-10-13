import { test } from 'node:test';
import assert from 'node:assert/strict';
import { mkdtemp, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { createRequire } from 'node:module';
import { buildSync } from 'esbuild';

test('rating reminder accepts new deliveries, excludes invalid/rated purchases, and merges supplier duplicates', async () => {
 const dir=await mkdtemp(join(tmpdir(),'rating-reminder-'));
 try {
  const file=join(dir,'reminder.cjs');
  buildSync({entryPoints:[new URL('../src/lib/ratingReminder.ts',import.meta.url).pathname],bundle:true,platform:'node',format:'cjs',outfile:file,logLevel:'silent'});
  const {unratedPurchases,ratingReminderKey}=createRequire(import.meta.url)(file);
  const now=Date.parse('2026-10-12T12:00:00Z');
  const old={id:'old',status:'delivered',delivered_at:'2026-10-10T12:00:00Z',product_id:'Amazon',rating:0};
  assert.deepEqual(unratedPurchases([old],[],now),[{kind:'order',id:'old',name:'Amazon'}]);
  for(const changed of [{rating:5},{rating_tx:'a'.repeat(64)},{rated_at:'2026-10-11T12:00:00Z'},{test_mode:true},{status:'processing'},{status:'refunded'},{delivered_at:'2026-10-13T12:00:00Z'},{id:''}]) assert.deepEqual(unratedPurchases([{...old,...changed}],[],now),[]);
  assert.equal(unratedPurchases([{...old,delivered_at:undefined}],[],now).length,1);
  assert.equal(unratedPurchases([{...old,rated_at:'0001-01-01T00:00:00Z'}],[],now).length,1);
  assert.equal(unratedPurchases([{...old,delivered_at:new Date(now).toISOString()}],[],now).length,1);
  const order={...old,supplier_order_id:'same'};const quote={...old,id:'quote',status:'fulfilled',supplier_order_id:'same'};
  assert.deepEqual(unratedPurchases([order],[quote],now),[{kind:'quote',id:'quote',name:'Amazon'}]);
  assert.deepEqual(unratedPurchases([{...order,rating:5}],[quote],now),[]);
  assert.deepEqual(unratedPurchases(null,{items:[quote]},now),[]);
  assert.notEqual(ratingReminderKey('one'),ratingReminderKey('two'));
  assert.notEqual(ratingReminderKey('one',{kind:'quote',id:'a',name:''}),ratingReminderKey('one',{kind:'quote',id:'b',name:''}));
 } finally {await rm(dir,{recursive:true,force:true});}
});
