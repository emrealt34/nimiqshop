import { test } from 'node:test';
import assert from 'node:assert/strict';
import { checkCatalog } from './catalog-watchdog.mjs';
const now = Date.parse('2026-10-11T20:00:00Z');
const recent = { id: 1, head_branch: 'main', event: 'workflow_dispatch', status: 'completed', conclusion: 'success', created_at: '2026-10-11T19:30:00Z' };
async function check(runs, { jobs = [{ name: 'Refresh and persist catalog snapshots', conclusion: 'success' }], artifacts = [{ name: 'catalog-snapshots', expired: false }] } = {}) {
  let dispatched = 0;
  const result = await checkCatalog({ now, get: async p => p.includes('/jobs?') ? { jobs } : p.includes('/artifacts?') ? { artifacts } : { workflow_runs: runs }, dispatch: async () => { dispatched++; } });
  assert.equal(dispatched, result.action === 'dispatch' ? 1 : 0);
  return result;
}
test('six-hour watchdog skips real recent published snapshots and running refreshes', async () => {
  assert.equal((await check([recent])).reason, 'published_snapshot_recent');
  assert.equal((await check([{ ...recent, status: 'in_progress', conclusion: null }])).reason, 'refresh_running');
  assert.equal((await check([{ ...recent, status: 'in_progress', conclusion: null, created_at: '2026-10-11T18:00:00Z' }])).reason, 'refresh_running', 'a two-hour full scan is still active');
});
test('stale, failed, guard-only or missing snapshots dispatch exactly one main refresh', async () => {
  for (const [runs, options] of [[[]], [[{ ...recent, created_at: '2026-10-11T14:00:00Z' }]], [[{ ...recent, conclusion: 'failure' }]], [[recent], { jobs: [] }], [[recent], { artifacts: [] }], [[recent], { artifacts: [{ name: 'catalog-snapshots', expired: true }] }], [[{ ...recent, head_branch: 'other' }]]]) {
    assert.equal((await check(runs, options)).action, 'dispatch');
  }
});
test('lookup and dispatch failures are visible failures, never false successes', async () => {
  await assert.rejects(checkCatalog({ get: async () => { throw Error('HTTP 503'); }, dispatch: () => assert.fail('must not dispatch') }), /503/);
  await assert.rejects(checkCatalog({ get: async () => ({ workflow_runs: [] }), dispatch: async () => { throw Error('HTTP 403'); } }), /403/);
});
