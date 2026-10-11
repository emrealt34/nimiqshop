import { test } from 'node:test';
import assert from 'node:assert/strict';
import { mkdtempSync, writeFileSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { createRequire } from 'node:module';
import { buildSync } from 'esbuild';

const dir = mkdtempSync(join(tmpdir(), 'checkout-avatar-'));
const entry = join(dir, 'entry.ts');
const bundle = join(dir, 'bundle.cjs');
writeFileSync(entry, `
globalThis.localStorage = { getItem: () => null, setItem() {}, removeItem() {} };
globalThis.window = { addEventListener() {}, location: { href: '' }, navigator: { language: 'en' } };
globalThis.document = { addEventListener() {}, documentElement: { lang: 'en' }, createElement: () => ({ style: {} }), cookie: '' };
export { buildOrderRequest, setBuyerIdenticon, setGiftIdenticon, setGiftExtras } from ${JSON.stringify(new URL('../src/lib/delivery.ts', import.meta.url).pathname)};
export { prepareBuyerIdenticon } from ${JSON.stringify(new URL('../src/lib/identicon.ts', import.meta.url).pathname)};
`);
buildSync({ entryPoints: [entry], bundle: true, platform: 'node', format: 'cjs', outfile: bundle,
  define: { 'import.meta.env.BASE_URL': '"/"', 'import.meta.env': '{}' }, loader: { '.css': 'empty' }, logLevel: 'silent' });
const client = createRequire(import.meta.url)(bundle);
process.on('exit', () => rmSync(dir, { recursive: true, force: true }));

test('checkout binds avatars to the current wallet and suppresses stale or anonymous identity', () => {
  const item = { id: 'card', delivery_type: 'by_email', country: 'TR', denomination: '25 USD', qty: 1 };
  client.setBuyerIdenticon('old-buyer');
  client.setGiftIdenticon('old-gift');
  client.setGiftExtras('email', 'enjoy');
  const request = (opts) => client.buildOrderRequest(item, { email: 'buyer@example.com', ...opts });
  assert.equal(request({ identicon: 'current-wallet' }).gifter_identicon, 'current-wallet');
  assert.equal(request({ identicon: '' }).gifter_identicon, undefined, 'failed preparation cannot reuse another wallet');
  assert.equal(request({ identicon: 'current-wallet', anonymous: true, gift: true }).gifter_identicon, undefined);
  assert.equal(request({ identicon: 'current-wallet' }).gift_channel, undefined, 'ordinary purchase still carries its avatar');
});

test('a stalled avatar decode times out instead of holding payment indefinitely', async () => {
  const saved = globalThis.Image;
  globalThis.Image = class { set src(_value) { /* simulate a decode that never completes */ } };
  try {
    assert.equal(await client.prepareBuyerIdenticon('NQ07 0000 0000 0000 0000 0000 0000 0000 0000', 20), '');
    assert.equal(await client.prepareBuyerIdenticon(null), '');
  } finally { globalThis.Image = saved; }
});
