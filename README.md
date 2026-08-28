# nim.shop

[![CI](https://github.com/YOUR_USERNAME/nimiq-shop/actions/workflows/ci.yml/badge.svg)](https://github.com/YOUR_USERNAME/nimiq-shop/actions/workflows/ci.yml)
[![Release](https://github.com/YOUR_USERNAME/nimiq-shop/actions/workflows/release.yml/badge.svg)](https://github.com/YOUR_USERNAME/nimiq-shop/actions/workflows/release.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

Accept payments in NIM (Nimiq) and deliver gift cards / mobile top-ups / eSIMs via the [CryptoRefills](https://docs.cryptorefills.com) Business API. Go (fasthttp) backend + **fully static, 100% client-side frontend** (`frontend/`).

> Replace `YOUR_USERNAME` above with your GitHub username/organization once the repository is pushed.

## Open source: automated builds, releases & integrity

This repository is set up so **everything builds automatically on GitHub** — no local toolchain needed to produce a deployable, verifiable release:

- **CI (`.github/workflows/ci.yml`)** — runs on every push and pull request:
  - rebuilds the frontend bundle and **fails if the committed bundle or the
    integrity manifests drifted from source** (reproducible esbuild + SHA-256 manifest);
  - `go vet`, `go build` and `go test` for the backend on **Linux and Windows**;
  - uploads the frontend dist + backend binaries as workflow artifacts.
- **Release (`.github/workflows/release.yml`)** — triggered by pushing a tag, e.g. `git tag v1.0.0 && git push origin v1.0.0`:
  - builds backend binaries for **linux / windows / macos × amd64 / arm64**,
    reproducibly (`-trimpath -buildid=`, CGO disabled — two builds from the same
    tag produce byte-identical binaries);
  - packages the deployable **frontend dist** (`frontend/js/bundle` + `integrity.json`);
  - publishes everything as a **GitHub Release** with a combined **`SHA256SUMS`**
    file covering every artifact.
- **Build integrity (`tools/integrity/`)** — a SHA-256 manifest over every deployed
  file (`frontend/integrity.json`) plus an embedded source manifest for the backend
  binary. `verify.html` lets visitors check a live deployment against the published
  root hash; `tools/integrity/verify.mjs <base-url> [expected-root-hash]` does it in CI.

### Local build (if you want to build yourself)

```bash
# Frontend bundle (esbuild; bundle is committed, this just verifies/recreates it)
cd frontend && npm ci && node build.mjs

# Regenerate integrity manifests (frontend/integrity.json + backend source manifest)
node tools/integrity/generate.mjs all

# Reproducible backend binary (prints the SHA-256 to publish)
./tools/integrity/build-reproducible.sh
```

## New frontend (2026-08 revision)

Instead of the old Astro/React frontend (removed entirely — it was built around a custodial balance/deposit model that nim.shop does not have), there is now a **pure static** app in `frontend/`:

- No framework runtime — plain HTML + CSS + ES modules; a small esbuild step
  (`node build.mjs`) bundles each page into `js/bundle/` (output is committed).
  Can be deployed to any static host (nginx, S3, Netlify, Cloudflare Pages...).
- **Nimiq Hub** wallet login and **Nimiq Pay** (`hub.checkout`) payment fully supported; on mobile if popups are blocked it automatically falls back to redirect flow.
- Full-featured user side: order tracking screen (stage timeline),
  order history (every purchase is a direct Nimiq Pay → Lightning payment),
  delivery codes / PIN / links, live refresh, **support ticket system**
  (open ticket, per-order chat, auto-refresh).
- Fully responsive + safe-area inset supported; design follows Nimiq brand language
  (navy background, Nimiq gold, Nunito). Interface is **English only**.
- Zero CDN dependency: `@nimiq/hub-api`, `@nimiq/identicons`, QR library
  and Nunito font are vendored and SHA-256 pinned; strict CSP on every page.
- Fake API server for preview/development: `node frontend/dev/mock-server.js`.

In this revision two small read-only endpoints were added to the backend (to track direct NIM payments and show delivery codes to the user):
`GET /api/quotes` and `GET /api/quotes/{id}`. Details: `SECURITY_ANALYSIS.md` §8.

**Security:** end-to-end security analysis of the system is in `SECURITY_ANALYSIS.md`
(threat model, why money flows cannot be forged, attack scenario table, residual risks and deployment hardening checklist).

## How it works (the non-custodial model — read this first)

**nim.shop never holds money, and never holds a customer wallet.** There is no
balance, no deposit, no top-up, no internal ledger, and no treasury address.
Those concepts existed in an early draft and have been deleted from the
codebase entirely.

What actually happens when someone buys something:

1. The visitor picks a product (gift card, top-up or eSIM) and clicks buy.
2. The backend asks CryptoRefills (the supplier / merchant of record) to open
   an order. CryptoRefills returns a **one-time payment address** — for the
   Lightning rail this is a BOLT11 `lnbc…` invoice — plus the exact BTC amount.
3. The buyer approves the payment **in their own Nimiq wallet** (Nimiq Pay).
   Nimiq Pay converts NIM → BTC Lightning and pays the supplier's invoice
   **directly**. The NIM/BTC moves from the buyer's wallet to CryptoRefills.
   It never passes through nim.shop, and nim.shop has no key that could touch it.
4. CryptoRefills confirms the payment on-chain and delivers the product
   (code / PIN / top-up / eSIM QR) by email. nim.shop watches the supplier
   webhook + status poll so the buyer's Orders page shows live progress and
   the delivered code.

**What we store about a user:** their Nimiq address (their login identity),
their optional delivery email, and their orders/quotes. **That's it.** No
balance, no stored coins, no custodial wallet. If our server disappeared
tomorrow, no one's money would be affected — because we never had it.

The one thing that can look like a "balance" is the **daily purchase limit**
shown on the Profile page (orders + spend in a rolling 24h window). That is an
anti-fraud limit applied at checkout, **not** money we hold.

## Money flow: direct payment to supplier (CryptoRefills)

Production payment path is:

```
Product selection
    │
    ▼
POST /api/quotes
    │  durable "supplier request started" marker is written (crash-safety)
    │  global CryptoRefills queue → POST /v5/orders
    ▼
CryptoRefills order + one-time wallet address (coin/network/coin_amount,
    30 min payment window)
    │
    ▼
Nimiq Pay: NIM → coin (e.g. BTC Lightning / stablecoin) payment to wallet
    │
    ▼
CryptoRefills: on-chain verification → delivery webhook + status polling
    → nim.shop quote status (delivering → fulfilled)
```

The server has no NIM treasury address, Polygon signer, BTC/Lightning node, customer balance or wallet holding funds. The server only stores the product quote, CryptoRefills order ID and delivery confirmed via webhook/poll. Nimiq Pay converts the user's NIM via its own self-custodial flow into the coin quoted by the supplier and pays directly to the one-time wallet address; money never touches nim.shop.

Security boundary: all API calls to the CryptoRefills account go through a single global, fair queue; there are endpoint rolling-window limits and per-user separate queue/rate budget and idempotency (requests that would wait too long for a budget slot fail-fast with 429 + Retry-After). Fulfillment only progresses via verified webhook + supplier status poll; no calls from browser to supplier.

### Login: Nimiq Hub wallet connection (no email/password)

User account = Nimiq address. No email/password, no `bcrypt`, single identity field `nimiq_address` in `users` table. Flow:

1. `POST /api/auth/challenge` → backend generates random `nonce`, returns it embedded in a short-lived (`ChallengeClaims`, 5 min) JWT. Stateless: we don't store pending login in DB, nonce is signed inside token itself.
2. Frontend opens Nimiq Hub popup via `@nimiq/hub-api` `signMessage()` call, user selects account and signs message containing nonce in Keyguard. Keys never leave browser/backend.
3. `POST /api/auth/hub-login` → `challenge_token` + Hub returned `address`/`public_key`/`signature` is sent. Backend (`internal/nimiq/signature.go`) verifies signature via Ed25519, derives address from public key (`internal/nimiq/address.go`, blake2b + ISO7064 checksum) and checks it matches claimed address, then upserts into `users` table and returns normal session JWT.

If popups are blocked on mobile, `@nimiq/hub-api` automatically falls back to full-page redirect; the redirect result is caught on page load (`frontend/js/hub.js`).

### Avatars: @nimiq/identicons

Nimiq Identicons library `@nimiq/identicons` is used to show consistent visual identity per address (`frontend/js/identicon.js`). Same address always produces same Identicon; Iqons are not used.

### Admin console: separate session + TOTP

`/admin` is completely separate from normal user account: customer JWT (even a valid `Authorization: Bearer …` header) is never accepted on any `/api/admin/*` endpoint.

- First admin is bootstrapped to Badger once only when `ADMIN_USERNAME`, pre-generated **Argon2id PHC** `ADMIN_PASSWORD_HASH` and base32 `ADMIN_TOTP_SECRET` are defined together. Raw admin password is never written to file/DB.
- Login is password + RFC 6238 TOTP. Five failed attempts / 15 min locks username for 30 min; successful login clears lockout record.
- Session cookie is random 32-byte credential; DB stores only `HMAC-SHA-256(ADMIN_SESSION_SECRET, credential)`. Cookie is given as `HttpOnly; Secure; SameSite=Strict; Path=/api/admin`. For local HTTP only `ADMIN_COOKIE_SECURE=false` can be selected for development.
- Login, failed login, lockout, logout and global margin change are appended to immutable admin audit log with IP + user-agent.
- Admin API provides dashboard, user+Identicon details, order/quote/transaction lists, manual-review queue, catalog rules and oracle health. It never shows a balance or deposit view because none exists.

First fill admin fields in `.env.example`. Alternatively on an empty database only, with high-entropy `ADMIN_BOOTSTRAP_TOKEN`, `POST /api/admin/auth/bootstrap` can be called; this endpoint only accepts Argon2id hash and TOTP secret and closes permanently after first successful call.

### Nimiq Pay: NIM to BTC Lightning payment

After user creates quote, server gives one-time wallet address returned by CryptoRefills (if BOLT11, as `lnbc…` invoice in `lightning_invoice` field) to frontend. Mini-app inside Nimiq Pay opens this payment address via standard `lightning:` URI/QR; Nimiq Pay calculates required NIM amount, gets user confirmation and pays NIM directly to CryptoRefills. Mini-app only carries payment address; private key, NIM or coin does not pass through server.

`orders.html` and `order.html` only refresh our local quote status; supplier status is not polled from browser. Live status is updated via backend's `GET /v5/orders/{id}` poll and CryptoRefills webhook.

### Money movement

In this codebase there is no user USD balance/ledger/deposit endpoint. `POST /api/quotes` creates CryptoRefills order, but payment request belongs directly to CryptoRefills's one-time wallet address. Server has no own wallet; operator private key, treasury address, Polygon signer and refund signer have been removed from active flow.

### CryptoRefills integration

CryptoRefills has no FazerCards-style "category → offer" hierarchy; each product is defined by brand/family + country + denomination (e.g. "Airbnb", "US", "100 USD"; denomination is "range" for flexible products). Kinds: `gift_card` | `topup` | `esim` (`backend/internal/cryptorefills/client.go`). Order flow:

1. Live catalog verified with `GET /v5/products/country/{cc}?family_name=...` (do not trust price from client)
2. If verification required, validate phone/field with `POST /v5/orders/validations`
3. Open order with `POST /v5/orders` (`payment: {type: "via", payment_via: "USER_WALLET", coin, network}`); this call goes through global CryptoRefills queue. **Before** the call, durable `supplier_request_at` marker is written to quote (crash-safety: "supplier request started / supplier order id pending" state; details: `CRASH_SAFETY.md`). Order ID + one-time wallet is atomically attached to quote and 30-minute payment window starts (`awaiting_payment`).
4. Nimiq Pay converts NIM to coin and pays one-time wallet. Server has no payment wallet.
5. After on-chain verification CryptoRefills reports status via webhook (`POST /api/webhooks/cryptorefills`) and/or tracker's `GET /v5/orders/{id}` poll; webhook payload is re-verified via global queue and delivery state is written atomically.

CryptoRefills webhook payload is **unsigned** (no HMAC), therefore `internal/handlers/webhook_handlers.go`:

- checks our own added shared-secret query param (`?key=...`) (not a CryptoRefills feature, our defense)
- instead of trusting status in payload, **re-verifies** via `GET /v5/orders/{id}` over global supplier queue
- idempotent: if same order id arrives again (CryptoRefills can retry) and state didn't change, does nothing
- backend won't start without `PUBLIC_WEBHOOK_BASE_URL` and `CRYPTOREFILLS_WEBHOOK_KEY`; no browser-side polling fallback in live fulfillment

## Setup

### Required info

- `CRYPTOREFILLS_PARTNER_ID` — CryptoRefills Business API partner id (goes in `X-Cr-Application` header on every request)
- `CRYPTOREFILLS_WEBHOOK_KEY` — random secret you generate yourself; added as query param to webhook URL and checked on incoming requests
- `PUBLIC_WEBHOOK_BASE_URL` — externally reachable HTTPS address of your backend (to create webhook_url); mandatory because live fulfillment is not done via polling

### One-click on Windows (start.bat / stop.bat)

Double-clicking `start.bat` is enough — assumes Go and Node/npm are installed; creates `.env` files from examples, downloads dependencies, starts backend with `go run ./cmd/server` and frontend with `npm run dev` in two separate `cmd` windows. No setup step for database: BadgerDB runs embedded. To close, `stop.bat`.

Required installations (Docker **not used**, everything runs directly on machine):

- [Go](https://go.dev/dl/) 1.22+
- [Node.js](https://nodejs.org) **22.12+** (LTS)

No database server required — [BadgerDB](https://github.com/dgraph-io/badger) runs embedded inside backend process and keeps data in `BADGER_DIR` folder.

### Local development (manual, all platforms)

```bash
# No database setup — BadgerDB is embedded, creates folder itself on first start.

# Backend
cd backend
cp .env.example .env   # fill .env (JWT_SECRET, CryptoRefills API, webhook secret, ...)
go mod tidy             # needs internet, access to go module proxy required
go run ./cmd/server

# Frontend (in separate terminal)
cd frontend
cp .env.example .env
# Node.js 22.12+ required
npm install
npm run dev
```

- Backend: http://localhost:8080
- Frontend: http://localhost:4321

### Data layer: BadgerDB

Backend uses [BadgerDB](https://github.com/dgraph-io/badger) (embedded, key/value) instead of Postgres. Practical consequences:

- **No migrations.** Badger is schemaless; `internal/db/keys.go` documents all key layout (records + manually maintained indexes). Old `internal/db/migrations/` was removed.
- **Single writer.** Only one process can open data folder at same time; don't try to open same `BADGER_DIR` with another process while backend runs.
- **Money is not `float64`.** USD amounts are stored as fixed-point `int64` micro-dollars (1 USD = 1.000.000) in `internal/money`; same precision as `NUMERIC(18,6)`, no rounding drift. API responses are still 6-decimal strings.
- **Backup = folder copy.** Just copy `BADGER_DIR` while backend is closed.
- **Atomicity.** Quote creation + daily-limit accounting happen in a single Badger transaction; there is no customer balance or ledger by design (non-custodial). Idempotency keys and limit enforcement are tested (`go test ./internal/db/...`).

## Repository layout

```
├── backend/            Go (fasthttp) API — quotes, orders, webhooks, admin
│   └── cmd/server/     server entrypoint (binary name: nimiqshop-server)
├── frontend/           fully static client — HTML + CSS + ES modules (no framework)
│   ├── js/pages/       one entry per page; bundled into js/bundle/ by build.mjs
│   ├── js/bundle/      COMMITTED build output (index.html loads these files)
│   └── integrity.json  SHA-256 manifest of every deployed file
├── tools/integrity/    manifest generator + verifiers + reproducible build script
├── devtools/           Windows one-click start.bat/stop.bat, Cloudflare tunnel helper
└── .github/workflows/  CI + Release (Linux/Windows/macOS builds, SHA256SUMS)
```

## License

[MIT](LICENSE) — free to use, modify and deploy. No warranty.

## Missing / pre-prod TODOs

This is a working skeleton, but check these before going live:

1. **Supplier refund handling** — refunds are recorded on the order record (supplier is merchant of record); nim.shop never moves customer money itself, so there is no internal refund ledger to reconcile.
2. **Rate limiting / abuse protection** — done: in addition to customer API limiter, all CryptoRefills endpoints go through single global, bounded, round-robin queue. Endpoint rolling windows, upstream `X-RateLimit-*`/`Retry-After` observation and per-user second budget apply; requests that would wait too long for budget slot fail-fast 429. For multi-instance production you still need shared Redis/WAF limiter.
3. **eSIM top-up flow** — order flow supports `phone_number` target but frontend doesn't yet offer UI to top-up existing eSIM; only new eSIM purchase exists.
4. **bill_payment product type** — separate catalog/order UI for CryptoRefills's fourth product type (`bill_payment`) not yet added, same quote pattern can be used.
5. **Nimiq RPC trust** — we trust a single public RPC (`rpc.nimiqwatch.com`); as volume grows run your own node or cross-check multiple sources.
6. **`go.sum`** — committed; run `go mod tidy` only after changing dependencies.
