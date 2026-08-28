# nim.shop — Crash and payment safety

**Version:** 2026-08-25 · **Active rail:** NIM → BTC Lightning → CryptoRefills (merchant of record)

## Active flow

```text
[1] Product detail + informational NIM/USD estimate
[2] POST /api/quotes
    ├─ local quote persisted as `order_creating` (WAL intent)
    ├─ durable `supplier_request_at` marker committed (see below)
    ├─ global CryptoRefills queue → POST /v5/orders
    └─ supplier order id + BOLT11 invoice (30-min payment window) are
       attached atomically → quote becomes `awaiting_payment`
[3] Nimiq Pay opens lightning:lnbc… (the supplier's BOLT11 invoice)
    └─ user approves the NIM → BTC Lightning atomic swap in the wallet
[4] CryptoRefills receives BTC Lightning directly and confirms on-chain
[5] CryptoRefills webhook (POST /api/webhooks/cryptorefills) and/or the
    tracker's GET /v5/orders/{id} poll
    ├─ re-verification through the global queue
    └─ local quote becomes `delivering` → `fulfilled` and stores redemption data
```

The server has no NIM treasury address, NIM/BTC/Lightning/Polygon wallet,
private key, customer balance, or refund signer. The NIM/USD oracle is only an
estimate for the UI; the BOLT11 amount returned by CryptoRefills is
authoritative when the user approves payment.

## Order-creation crash recovery (the durable marker)

The dangerous window is the gap between "the quote row exists locally" and "the
supplier order exists". An in-memory queue can never prove what happened after
a process death, so the tracker relies on one durable fact instead:

`Quote.SupplierRequestAt` (`supplier_request_at`) is committed **immediately
before** the `POST /v5/orders` call begins (fail-closed: if the marker write
fails, the quote goes to `manual_review` and no request is dispatched).

- **marker == zero** ⇒ no supplier request was ever dispatched (the request
  would have lived only in the in-process queue, which dies with the process).
  The tracker **re-dispatches** the creation, bounded by `OrderAttempts`
  (2 total; beyond that → `manual_review`), after a short grace period.
- **marker set** ⇒ a supplier order **may** exist (the request left, or left
  and its response was lost). The supplier has no "list my orders" endpoint,
  so the tracker never auto re-sends (duplicate-order guard); after
  `WORKER_ORDER_STALE_SECS` (25–3600, default 300) **measured from the
  marker**, the quote is parked in `manual_review` with the marker time in the
  log for operator investigation.

The stale clock runs from the marker, not from the quote write, and the
validation range guarantees it always exceeds the 20s supplier call timeout —
a healthy in-flight creation can never be flagged as crashed.

## Crash behavior

| Crash point | Durable state | Recovery | Risk |
|---|---|---|---|
| Before the marker write | `order_creating` quote, marker zero, idempotency key | Tracker re-dispatches `POST /v5/orders` (bounded to 2 attempts, then `manual_review`) | No supplier request was ever dispatched; no duplicate order possible |
| After the marker write, before the supplier order is attached (process death mid-call or lost response) | `order_creating`, marker set, no `supplier_order_id` | After `WORKER_ORDER_STALE_SECS` from the marker → `manual_review` | A supplier order may exist; never auto re-sent, so at worst one order needs manual reconciliation |
| After the order/wallet is returned to the browser | `awaiting_payment` + wallet + `payment_expiry` (30 min) | User can reopen the same wallet from the quote/order page; tracker polls supplier status; unpaid expiry → `expired` | One idempotency key maps to one local quote and one wallet |
| During supplier payment/delivery | Quote in `payment_started` / `payment_received` / `delivering` | CryptoRefills retries its webhook; the handler re-verifies through the global queue; the tracker's `GET /v5/orders/{id}` poll is the safety net | No local wallet or double-spend path exists |
| During webhook handling | Supplier retries on non-2xx; local transitions are conditional | Duplicate webhook after `fulfilled` is a no-op | Redemption codes remain owner-only |
| Quote expires unpaid | `expired` status | Local sweeper releases the daily-limit slot; the unpaid supplier order is not reused | No NIM/coin is held by nim.shop |

## Global CryptoRefills queue

Every CryptoRefills client method uses one shared fair dispatcher: catalog,
order creation, phone validation, fulfillment polling and webhook verification
all share the same partner-account budget. The queue applies:

- the documented endpoint rolling windows;
- upstream `X-RateLimit-Limit`, `X-RateLimit-Remaining`, `X-RateLimit-Reset`,
  `Retry-After` and quota headers;
- a bounded total queue (default 2000) and bounded per-actor queue (default 100);
- round-robin fairness plus a per-actor rolling request budget (default 30/min,
  burst 6);
- **fail-fast admission**: a request whose next budget slot is more than
  `MaxAdmissionWait` (default 5s) away is rejected immediately with 429 +
  `Retry-After: 15` instead of parking in the queue until its caller context
  times out;
- atomic per-user daily quote limits and `Idempotency-Key` protection.

The queue is process-global to the single CryptoRefills client used by this
server. If the deployment has multiple pods, put the same account-level
limiter behind Redis/WAF or run one supplier worker; separate in-process
queues cannot share state across processes.

## Refund boundary

Because the payment is direct to the supplier (CryptoRefills, merchant of
record) through Nimiq Pay, nim.shop does not sign or broadcast a customer
refund. A supplier payment/order failure remains visible as `failed` or
`manual_review` and is handled by the supplier's own refund policy/support
flow. This is intentional: adding a refund wallet would violate the
no-custody requirement.
