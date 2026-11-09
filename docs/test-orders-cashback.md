# Test orders and their (simulated) cashback payouts

A **test account** is a Nimiq wallet the operator lists in the admin console. Orders placed by that wallet run the complete normal purchase — same catalog, same validation, same supplier order, a **real payable invoice** — and the only simulated step is the "simulate payment" button on the pay screen. Pressing it walks the order through the real state machine (broadcast → paid → delivering → fulfilled) without any money moving.

Because fulfillment runs for real, everything downstream runs for real too:

- the delivery code and the gift email are produced by the same hooks a paying buyer triggers;
- the **cashback engine books a row** exactly as it does for a real purchase, with `TestMode = true`;
- the **cashback worker pays that row with a simulated `TESTTX-…` hash** — no signing, no RPC, no chain.

That is what makes a test order safe to repeat: a test payout never moves NIM, even when the shop wallet is funded and `CASHBACK_ENABLED=true`.

## Trying it

1. Admin console → **Test accounts** → add your wallet address (only an admin can do this).
2. Confirm the base rate: **Settings → Cashback** (default is 0%; set e.g. `100` bps = 1%).
3. Sign in with the listed wallet, buy any product, choose **cashback to my wallet** as the cashback destination.
4. On the pay screen press **simulate payment**. The order fulfils; the product code/email arrives.
5. Open **Cashback**: the row appears for the buyer, and within one worker tick moves `queued → paid` with a `TESTTX-…` hash. The admin order view badges it `test_mode: true`.

## What never happens for a test order

| Surface | Behaviour |
| --- | --- |
| Public activity feed | hidden unless the operator enables **Test activity** in the admin console |
| Public cashback leaderboard / totals | excluded while test activity is hidden |
| Daily order/spend budget | test quotes do not consume it (`internal/db/limits.go`) |
| Chain | the payout is never signed or broadcast (`TESTTX-` hash, no seed required) |
| Other wallets | `POST /api/quotes/{id}/test-pay` answers 404 for anyone not on the list |

A **real** cashback row behaves the opposite way: it stays `queued` until the worker is enabled *and* the shop wallet holds amount + fee. Nothing about listing a test wallet changes that for anybody else.

## Two different switches

- `TEST_MODE` (env, dev/staging): the supplier itself is simulated — quotes get a fabricated invoice and a `TESTSIM-` order id.
- **Test account list** (admin console, live site): real supplier order, real invoice, real delivery; only payment and the cashback broadcast are simulated.

## Verifying from a checkout

```sh
cd backend
go test ./cmd/server -run 'TestTestWalletCashbackIsPaidThroughTheSimulatedPayout' -v -count=1
```

It lists a wallet, buys through the HTTP API, test-pays it, and asserts:

- the cashback row carries `TestMode` and belongs to the buyer's own wallet;
- a seedless, RPC-less worker pays it with a `TESTTX-` hash, `paid_at` set, and no signed bytes;
- the buyer sees the payout in `/api/cashback/me`, while the public feed and leaderboard stay clean;
- a genuine (unlisted buyer) cashback row created in the same store is **not** paid by that worker.

Test runs have no price oracle, so an HTTP-created quote there books its cashback as `skipped: product NIM price unknown` — the engine's fail-closed guard against inventing an amount. On a live deployment the rate snapshot is warm and the same row is queued and paid in luna.
