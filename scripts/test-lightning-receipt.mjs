import { test } from 'node:test';
import assert from 'node:assert/strict';
import { paymentHash, lightningReceiptOf, verifyLightningPreimage } from '../src/lib/lightningReceipt.ts';

const hash = '66687aadf862bd776c8fc18b8e9f8e20089714856ee233b3902a591d0d5f2925';
const quote = { coin: 'BTC', network: 'Lightning', lightning_payment_hash: hash, status: 'awaiting_payment' };

test('invoice hash is not evidence of payment; malformed times do not confirm it', () => {
  assert.equal(lightningReceiptOf(quote).confirmed, false);
  assert.equal(lightningReceiptOf({ ...quote, paid_at: 'bad time' }).confirmed, false);
  assert.equal(lightningReceiptOf({ ...quote, payment_observed: true }).confirmed, true);
  assert.equal(lightningReceiptOf({ ...quote, status: 'fulfilled' }).confirmed_at, undefined);
  assert.equal(lightningReceiptOf({ ...quote, paid_at: '2026-10-11T12:00:00Z' }).confirmed_at, '2026-10-11T12:00:00Z');
});

test('public privacy, simulation exclusions, network and hash validation', () => {
  assert.equal(lightningReceiptOf({ ...quote, anonymous: true }, true), null);
  assert.equal(lightningReceiptOf({ ...quote, anonymous: true }, false).payment_hash, hash);
  assert.equal(lightningReceiptOf({ ...quote, test_mode: true }), null);
  assert.equal(lightningReceiptOf({ ...quote, coin: 'USDT', network: 'Polygon' }), null);
  assert.equal(paymentHash(` ${hash.toUpperCase()} `), hash);
  for (const bad of [null, 123, 'a'.repeat(63), 'a'.repeat(65), 'z'.repeat(64), 'lnbc123']) assert.equal(paymentHash(bad), '');
});

test('proof hashes binary preimage, not its ASCII hex representation', async () => {
  assert.equal(await verifyLightningPreimage(hash, '00'.repeat(32)), true);
  assert.equal(await verifyLightningPreimage(hash, '01'.repeat(32)), false);
  assert.equal(await verifyLightningPreimage(hash, 'secret'), false);
  assert.equal(await verifyLightningPreimage('lnbc123', '00'.repeat(32)), false);
});
