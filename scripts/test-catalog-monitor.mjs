import { test } from 'node:test';
import assert from 'node:assert/strict';
import { monitorCatalog } from './catalog-monitor.mjs';
const now=Date.parse('2026-10-12T09:47:00Z');
const report={complete:true,rates_complete:true,run_id:'real',catalog:{generated_at:'2026-10-12T06:17:00Z',complete:true,stock:{mode:'full',completed:true,unresolved:0},retained_countries:[]},market:{generated_at:'2026-10-12T07:17:00Z',nim:{observed_at:'2026-10-12T07:17:00Z'},btc:{observed_at:'2026-10-12T07:17:00Z'}}};
test('read-only monitor checks served full data without any dispatch or second request',async()=>{
 const r=await monitorCatalog({now,readReport:async()=>report,readRuns:async()=>{throw Error('unnecessary request')}});
 assert.equal(r.state,'published');
});
test('partial, stale, future and failed served reports cannot become healthy',async()=>{
 for(const value of [{...report,complete:false},{...report,catalog:{...report.catalog,retained_countries:['FR']}},{...report,market:{...report.market,generated_at:'2026-10-10T06:00:00Z'}},{...report,market:{...report.market,generated_at:'2026-10-13T06:00:00Z'}}]){
 const r=await monitorCatalog({now,readReport:async()=>value,readRuns:async()=>({workflow_runs:[]})});assert.notEqual(r.state,'published');
 }
 const active={id:1,name:'Catalog and rates · every 6 hours',event:'schedule',head_branch:'main',status:'in_progress',created_at:'2026-10-12T06:17:00Z'};
 assert.equal((await monitorCatalog({now,readReport:async()=>({...report,complete:false}),readRuns:async()=>({workflow_runs:[active]})})).state,'refresh_in_progress');
 assert.equal((await monitorCatalog({now,readReport:async()=>{throw Error('read failed')},readRuns:async()=>({workflow_runs:[]})})).state,'unavailable');
});
