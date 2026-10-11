import { test } from 'node:test';
import assert from 'node:assert/strict';
import { refreshDue } from './catalog-refresh-due.mjs';

const now = Date.parse('2026-10-11T18:00:00Z');
const run = { id: 10, event: 'schedule', head_branch: 'main', status: 'completed', conclusion: 'success', created_at: '2026-10-11T17:30:00Z' };
const refresh = { name: 'Refresh and persist catalog snapshots', status: 'completed', conclusion: 'success' };
async function check({ candidate = run, jobs = [refresh], artifact = true, expired = false, event = 'schedule', source, runId = 20 } = {}) {
  const get = async path => path.includes('/jobs?') ? { jobs } : path.includes('/artifacts?')
    ? { artifacts: artifact ? [{ name: 'catalog-snapshots', expired }] : [] } : { workflow_runs: [candidate] };
  return (await refreshDue({ event, source, runId, now, get })).due;
}

test('recent successful published data skips duplicate supplier calls', async () => assert.equal(await check(), false));
test('manual refresh always runs without a freshness API request', async () => {
  assert.equal((await refreshDue({ event: 'workflow_dispatch', get: () => { throw Error('must not be called'); } })).due, true);
});
test('Railway dispatches use the guard even though their GitHub event is workflow_dispatch', async () => {
  assert.equal(await check({ event: 'workflow_dispatch', source: 'railway-watchdog' }), false);
});
test('guard-only runs, missing artifacts and failed deployments cannot suppress refresh', async () => {
  for (const params of [{ jobs: [] }, { jobs: [{ ...refresh, conclusion: 'failure' }] }, { artifact: false }, { expired: true },
    { candidate: { ...run, conclusion: 'failure' } }, { candidate: { ...run, status: 'in_progress' } }, { runId: 10 }]) {
    assert.equal(await check(params), true);
  }
});
test('stale snapshots, other branches, invalid and future timestamps refresh', async () => {
  for (const candidate of [{ ...run, created_at: '2026-10-11T17:05:00Z' }, { ...run, head_branch: 'other' },
    { ...run, created_at: 'bad' }, { ...run, created_at: '2026-10-12T00:00:00Z' }]) assert.equal(await check({ candidate }), true);
});
