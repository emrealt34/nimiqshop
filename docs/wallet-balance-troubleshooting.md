# Wallet balance & stake troubleshooting

Two reports shaped this page (both from the owner, fixed 2026-10-25):

1. **"Hiç stake etmemiş bir adresle girince 'Stake miktarı okunamadı' çıkıyor"**
   — `/api/wallet/stake` answered **502** for an address that simply has no
   staking position (example: `NQ14 6MRC 30QR YDCB U9N8 CVGA 1TH7 K5D1 X2H6`).
   It happened inside Nimiq Pay too.
2. **"Balance debug satırı kullanıcıya açık görünüyor."**

## 1. "Never staked" is an answer, not a failure

`getStakerByAddress` reports "this address has no staking position" as a
JSON-RPC **error**, and the providers do not put the sentence in the same
field. Live from `rpc.nimiqwatch.com` (2026-10-25):

```json
{"jsonrpc":"2.0","id":1,
 "error":{"code":-32603,
          "message":"Internal error",
          "data":"No staker with address: NQ14 6MRC 30QR YDCB U9N8 CVGA 1TH7 K5D1 X2H6"}}
```

The verdict is in **`data`**; `message` is a generic `"Internal error"`. Older
NimiqWatch wording carried it in `message` (`"no staker with this address in
this pool"`). A check that reads only `message` therefore saw a chain outage,
and:

* `GET /api/wallet/stake?address=…` → **502 `staking lookup unavailable`**
  → the strip printed **"Stake miktarı okunamadı"** for a zero stake;
* `GET /api/wallet/balance` → `stake_available: false` → same line;
* `internal/chainstake` (chain-verified staking fallback) → treated the same
  answer as an outage instead of a definitive "not delegated".

One rule now covers all three, in `internal/nimiq` (`client.go`):

```go
errors.Is(err, nimiq.ErrNoStaker)   // typed verdict
nimiq.IsNoStaker(err)               // reads `message` AND `data`
```

`GetStaker` normalizes both provider shapes (plus a bare `null` result) into
`ErrNoStaker`; a real outage (HTTP 5xx, timeout, "chain unavailable", rate
limit) stays an ordinary error and is **never cached as "not staked"**.

Verification:

```bash
cd backend
go test ./internal/nimiq ./internal/chainstake ./cmd/server   # offline
NIMIQ_LIVE=1 go test ./internal/nimiq -run LiveNoStaker -v    # real provider, owner's address
```

The regression test `TestPublicWalletStakeForANeverStakedAddressIsZeroNot502`
uses the **live** response shape above and fails with
`502 {"error":"staking lookup unavailable"}` on the old code.

## 2. "Balance debug" is opt-in

`WalletBalance` used to print its whole reading — `inPay=… signedIn=…`, the
wallet address, every host account, luna figures, raw error strings — in a
`<details>` block for **every** buyer inside Nimiq Pay.

Diagnostics are still needed when a figure is reported wrong, so they are
gated, not deleted (`src/lib/debugFlags.ts`):

| Switch | Scope |
| --- | --- |
| `?nimshopDebug=1` (or `=true`) | one visit — works on a phone inside Nimiq Pay, where there is no console |
| `localStorage['nimshop.debug'] = '1'` | sticky, per browser |
| nothing | **no diagnostics on screen** (the default) |

Support flow: send the link with the parameter, ask for a screenshot, done.

The stored value must be exactly `1`; a storage error (private mode) keeps
diagnostics **off** but the URL parameter still works.
