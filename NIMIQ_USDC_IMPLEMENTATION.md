# Direct NIM → CryptoRefills wallet settlement

The active purchase rail is intentionally non-custodial:

1. `POST /api/quotes` validates the live CryptoRefills product and computes an
   **informational** NIM/USD estimate using the multi-source oracle.
2. The backend persists the quote as `order_creating`, writes the durable
   `supplier_request_at` marker, and creates the supplier order via
   `POST /v5/orders` with `payment: {type: "via", payment_via: "USER_WALLET",
   coin, network}` through the global CryptoRefills queue.
3. The returned one-time wallet address (coin amount + network; a BOLT11
   `lnbc...` invoice for Bitcoin Lightning) is opened in Nimiq Pay. Nimiq Pay
   calculates the current NIM amount, performs the NIM→coin conversion and
   pays the wallet. The user confirms in the Nimiq Pay UI.
4. CryptoRefills receives the coin payment directly, confirms it on-chain and
   sends the final order webhook (the tracker also polls
   `GET /v5/orders/{id}`). The webhook is re-verified through the global
   CryptoRefills queue and the redemption data is stored for the owning user.

The app does **not** have a treasury address, customer balance, NIM wallet,
coin wallet, Polygon signer, Lightning node, or refund signer. It never sees a
private key or takes custody of the payment. The oracle estimate is not used to
settle or authorize payment; the supplier-quoted coin amount on the one-time
wallet is the authoritative amount at payment time.

## Required production configuration

- `CRYPTOREFILLS_PARTNER_ID` (sent as the `X-Cr-Application` header)
- `PUBLIC_WEBHOOK_BASE_URL` (public HTTPS backend URL)
- `CRYPTOREFILLS_WEBHOOK_KEY` (at least 32 random bytes)
- `JWT_SECRET` and the normal admin session secrets

## Important operational boundary

The supplier order is created before the wallet opens so the user receives a
real one-time wallet address. The write-ahead quote state plus idempotency key
prevents a retry from creating a second order. If the process dies between the
marker write and local attachment of the supplier order, the tracker never
auto re-sends (a supplier order may exist; there is no listing endpoint) and
parks the quote in `manual_review` after `WORKER_ORDER_STALE_SECS` measured
from the marker. If the process dies **before** the marker, no supplier request
was ever dispatched and the tracker re-dispatches the creation (bounded to two
attempts, then `manual_review`). Details: `CRASH_SAFETY.md`.
