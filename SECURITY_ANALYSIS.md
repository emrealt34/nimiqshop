# Security Analysis — nim.shop (23 August 2026)

Full-system security review of the **nimiq-shop** stack: Go/fasthttp backend,
BadgerDB storage, Nimiq blockchain payment paths, CryptoRefills supplier
integration, Polygon USDC settlement, admin console, and the new **100%
static client-side frontend**.

The goal of this review, per the project requirement: make it practically
impossible for an attacker to **steal funds, steal goods, forge identity, or
take over the system**.

**2026-08-25 implementation note:** the active purchase path is quote →
CryptoRefills order + one-time wallet → Nimiq Pay NIM→coin payment to the
wallet → CryptoRefills webhook (plus tracker `GET /v5/orders/{id}` polling).
The old USD-balance/deposit/treasury model has been **deleted from the
codebase entirely** (no routes, no records, no treasury watcher remain).
CryptoRefills API calls pass
through one bounded global endpoint-aware queue with per-actor fairness and
fail-fast admission (429 + Retry-After), and every order creation is preceded
by a durable `supplier_request_at` marker (see `CRASH_SAFETY.md`).

---

## 1. Threat model

### 1.1 Assets worth stealing

| # | Asset | Where it lives | Impact if stolen |
|---|-------|----------------|------------------|
| A1 | Operator's CryptoRefills partner account / API credentials | CryptoRefills account + server env | Goods + money |
| A2 | Polygon USDC operator private key | Server env | Settlement wallet drained |
| A3 | Session JWT secret / admin session secret | Server env | Identity forgery |
| A4 | Admin console access | Cookie session + TOTP | Full system control |
| A5 | Users' private keys | **NOT here** — Nimiq Hub/Keyguard only | N/A by design |
| A6 | Customer funds | **NOT here** — no balance, deposit or treasury exists | N/A by design |

Note A5: this system **never sees user private keys**. Login and payments are
signature-based via Nimiq Hub. That removes the single biggest attack class of
custodial services. Note A6: there is nothing custodial to steal — customers
pay the supplier's one-time Lightning invoice directly from their own wallet.

### 1.2 Attacker profiles

1. **Anonymous remote attacker** — probing public endpoints, injecting data.
2. **Malicious customer** — trying to get goods without paying, double-spend,
   or access other customers' orders.
3. **Compromised client machine** — XSS/browser malware against a logged-in user.
4. **Supply-chain attacker** — tampering with frontend dependencies/CDNs.
5. **Insider/ops attacker** — access to server disk, logs, or environment.
6. **Network attacker** — MITM between browser, frontend, backend, oracles.

---

## 2. How money actually moves — and why it cannot be forged

### 2.1 Custodial deposits — removed entirely

Earlier revisions of this document described a "deposit to a treasury
address, match by exact Luna amount, credit an internal ledger" flow. **That
model no longer exists in this codebase**: the treasury watcher, deposit
records, balance ledger and their API routes have been deleted. nim.shop is
strictly non-custodial — the only money movement is the direct payment in
§2.2, from the buyer's own wallet to the supplier's one-time invoice.

### 2.2 Direct NIM → BTC Lightning payment for a product

1. `POST /api/quotes` validates the live product against CryptoRefills and
   creates a supplier order (`POST /v5/orders`, `payment: {type: "via",
   payment_via: "USER_WALLET", coin, network}`). Before the supplier call the
   durable `supplier_request_at` marker is committed, so a crash mid-creation
   is detectable (see `CRASH_SAFETY.md`). The multi-source NIM/USD oracle is
   still calculated, but its NIM number is informational only.
2. The supplier's one-time wallet address (coin amount + network; a BOLT11
   `lnbc...` invoice for Bitcoin Lightning) is returned to the Mini App. Nimiq
   Pay shows the live NIM amount and, after the user's native approval,
   performs the self-custodial NIM→coin conversion and pays the wallet directly.
3. CryptoRefills receives the coin payment directly and confirms it on-chain.
   Its webhook (and the tracker's `GET /v5/orders/{id}` poll) is re-verified
   through the shared queue, then the local quote moves to `fulfilled` and
   stores redemption details.

**Attack analysis**
- *Fake payment*: a browser cannot mark a quote fulfilled; only a verified
  CryptoRefills order webhook / re-fetched order status can do that.
- *Price manipulation*: the oracle estimate cannot change the supplier-quoted
  coin amount. The one-time wallet amount and CryptoRefills' final order
  status are the payment authority.
- *Replay/double payment*: one idempotency key maps to one local quote and one
  attached supplier order. The same wallet/invoice is displayed on retry; the
  UI warns not to pay it twice.
- *Custody*: the server has no NIM/BTC/Lightning/Polygon private key, treasury
  address, or customer balance. It never receives the payment.

### 2.3 Legacy balance/test path

The production router does not expose `POST /api/orders`, wallet balances, or
customer deposits. The only production purchase path is the direct
CryptoRefills quote flow in §2.2. A development-only `POST /api/test/buy`
remains for free CryptoRefills test products; it is idempotent, bounded by the
same supplier queue, and now returns `202` while the CryptoRefills webhook
completes the local test order.

**Attack analysis**
- *Order without funds*: rejected before any supplier call.
- *Tamper with price/quantity*: quantity is clamped (1..MaxOrderQuantity);
  price comes from the supplier, not the client.
- *Double-spend via double click/race*: idempotency key + single transaction.

---

## 3. Authentication & authorization

### 3.1 Wallet login (no passwords exist to steal)

- `POST /api/auth/challenge` → random 16-byte nonce, embedded in a **5-minute
  JWT** (stateless; nothing pending stored server-side).
- User signs the message in Nimiq Hub; keys never leave Keyguard.
- Backend verifies: **Ed25519 signature** over the Nimiq-prefixed message
  (`\x16Nimiq Signed Message:\n<len><msg>` — domain-separated, so a login
  signature can **never be replayed as a transaction**), and derives the
  address from the public key (blake2b + ISO-7064 checksum) to ensure the
  claimed address matches the key that signed.

**Attack analysis**
- *Impersonate another user*: requires their private key (held in Hub/Keyguard).
- *Replay old logins*: nonce is single-use in effect (5-min challenge bound to
  that exact message); signature is message-specific.
- *Signature/transaction crossover*: prevented by the message prefix scheme.
- *Brute-force challenges*: rate-limited (60 req/min/IP, burst 20).

### 3.2 Session tokens

- Session JWTs: HS256 **pinned** (alg-confusion rejected), configurable
  expiry (default 7 days), bearer header only.
- All user-scoped reads enforce ownership at the data layer:
  `GetOrderForUser`, `GetQuoteForUser`, `GetSupportTicketForUser`,
  `GetDeposit(id, userID)` — **no IDOR**: a valid token for user A can never
  read user B's orders, tickets, deposits, or redemption codes (lookups return
  `404`, not `403`, to avoid ID enumeration).
- Frontend: token stored in `localStorage` (see §6.3 for the trade-off), sent
  only to the configured API origin, cleared on logout/expiry.

### 3.3 Admin console — a separate identity plane

- Customer JWTs are **never accepted** on `/api/admin/*` (different
  middleware chain). Even a fully valid customer token gets 401.
- Bootstrap accepts only a deployment token + **Argon2id PHC hash** + TOTP
  secret — a raw admin password never touches disk, and the bootstrap
  endpoint self-destructs after first success.
- Login = username + password (Argon2id) + **RFC 6238 TOTP**. 5 failures →
  30-minute username lockout; all attempts audited (IP + user agent).
- Session = 32 random bytes; DB stores only `HMAC-SHA256(secret, credential)`.
  Cookie: `HttpOnly; Secure; SameSite=Strict; Path=/api/admin` — JavaScript
  cannot read it, CSRF cross-site usage is blocked, and it is never sent to
  non-admin paths.
- Dedicated tighter rate limiter (10/min, burst 5) on admin auth endpoints.
- Immutable audit trail for login, logout, failures, lockouts, margin changes.

---

## 4. Supplier & webhook integrity

### 4.1 One global CryptoRefills queue

Every CryptoRefills client method enters the same in-process scheduler before
any HTTP request is sent. It uses a fair round-robin queue keyed by
authenticated user (or peer IP for anonymous traffic), a bounded global queue
(default 2000) with a bounded per-actor queue (default 100), and a second
per-actor rolling budget (default 30/min, burst 6). The endpoint rolling
windows are exact: `GET /v2/brands` 60/min, `GET /v2/homepage` 30/min,
`GET /v3/payment_vias` 30/min, `GET /v5/products/country/{cc}` 60/min,
`GET /v4/products/price` 120/min, `GET /v5/orders/{id}` 120/min,
`POST /v5/orders/validations` 60/10min, `POST /v5/orders` 30/10min; unknown
routes fall back to a conservative 10/min bucket. Admission is fail-fast: a
request whose next budget slot is more than ~5s away is rejected with
429 + `Retry-After` instead of parking in the queue. Upstream `X-RateLimit-*`
and `Retry-After` headers can throttle the same local bucket. Multiple pods
still need a shared Redis/WAF limiter because an in-process queue cannot
coordinate separate processes.

### 4.2 Webhook integrity

- CryptoRefills partner credentials (`X-Cr-Application` / `X-Cr-Version`)
  **never reach the browser** — the catalog endpoints are server-side cached
  proxies.
- Webhooks: CryptoRefills doesn't sign payloads, so the handler
  1. requires a ≥32-byte shared key, compared in **constant time**, and
     rejects by default when unconfigured;
  2. **never trusts the POSTed body** — re-fetches the order
     (`GET /v5/orders/{id}`) with its own credentials before changing local
     state;
  3. is idempotent (safe under CryptoRefills retries).
- Order fulfillment (codes/PINs/links) flows supplier → server → owning user
  only; the frontend renders it as **text**, never as HTML.

---

## 5. Frontend security (the new static app)

### 5.1 Zero supply-chain surface at runtime

- **No CDNs, no remote scripts, no build step.** Every dependency is vendored
  and hashed:

| File | Origin | SHA-256 |
|------|--------|---------|
| `vendor/HubApi.umd.js` | @nimiq/hub-api 1.14.0 (standalone UMD) | `49ba84bf45069b7dcbf12abedee8122370f236243dd24b2cd208c52dd2d3432b` |
| `vendor/identicons.module.js` | @nimiq/identicons 1.6.2 | `041a811844d065efdbb9b411cd2cc81fa69e22377e081141f6d5a5b851c7d8e6` |
| `vendor/identicons.min.svg` | @nimiq/identicons 1.6.2 asset | `b6cf341a74e7a6e09a2eef3b7a71dd4ed15c9a38b99f1c568dfb07d186d7140c` |
| `vendor/qrcode.js` | qrcode-generator 1.4.4 (MIT) | `18ae399f81182bc9de916e9c77b195df20cc58d6f2d55a62b085a299f1bf1780` |
| `fonts/nunito-var.woff2` | Google Fonts (SIL OFL) | (see `fonts/OFL.txt`) |

  A CDN compromise or dependency confusion cannot touch a deployed copy.
  Re-verify with `sha256sum vendor/*` after any update.

### 5.2 XSS defenses

- **No `innerHTML` with data.** All DOM is built via a single `el()` helper
  that uses `createElement`/`textContent`/attribute setters. Product names,
  support messages, order payloads, redemption codes, error strings — all
  rendered as text.
- The only sanctioned HTML-injection points are hard-coded constants (icon
  SVG paths, the brand mark) — audited, no data interpolation.
- Identicons render via `toDataUrl()` into `<img src>`; the fallback painter
  strips markup characters and URL-encodes the SVG.
- External links use `rel="noopener noreferrer"`.
- Strict **Content-Security-Policy** (meta + `_headers`): `script-src 'self'`
  (no inline scripts, no eval paths), `object-src 'none'`, `base-uri 'self'`,
  `form-action 'self'`, `frame-ancestors 'none'`, `upgrade-insecure-requests`,
  with `frame-src` locked to official Nimiq Hub origins only. Even a
  hypothetical injection cannot load remote code.

### 5.3 Clickjacking / framing

- `frame-ancestors 'none'` + `X-Frame-Options: DENY` — the shop cannot be
  embedded in an attacker's iframe to overlay fake UI.
- Hub itself decides how it renders; we only talk to the pinned Hub origins.

### 5.4 Hub communication

- `@nimiq/hub-api` communicates with `hub.nimiq.com` (or testnet) via
  iframe/postMessage; the library validates the Hub origin. We pin the origin
  in config and CSP `frame-src`.
- Redirect-return flows (mobile) resume through `sessionStorage` payloads that
  are only ever **our own** challenge/payment metadata, time-bounded (10 min),
  and the resumed actions still go through full server-side verification.

### 5.5 Client-side input handling

- Quantity clamped in UI (1–10) and re-validated server-side.
- `Idempotency-Key` = `crypto.randomUUID()` per submit (CSPRNG).
- URL params (`?id=`, `?ticket=`) are used only through
  `encodeURIComponent()` in API paths; nothing is spliced into HTML.
- Amounts displayed exactly as returned by the server (strings/ints), never
  recomputed from floats for payment decisions.
- Supplier-provided redemption links pass through `safeHref()` — only
  `http(s):` URLs may become clickable; anything else (e.g. `javascript:`)
  is rendered as copyable plain text.

---

## 6. Known trade-offs & residual risks (honest list)

| # | Item | Risk | Mitigation / recommendation |
|---|------|------|-----------------------------|
| R1 | **Bearer JWT in localStorage** | A successful same-origin XSS could exfiltrate the session token | CSP+no-innerHTML make XSS very unlikely; server-side ownership checks bound the blast radius. Highest-security option: serve API same-origin and switch to short-lived HttpOnly/SameSite cookies + CSRF token. |
| R2 | **Webhook key in query string** | Could appear in proxy/access logs | Redact in ingress logs, rotate on suspicion, allowlist CryptoRefills IPs at the edge if offered; payload is never trusted anyway. |
| R3 | **Process-local rate limiter** | Bypassed by distributed bots or if behind a proxy that rewrites IPs | Deploy a WAF/CDN with shared rate limits; do not trust `X-Forwarded-For` unless your proxy rewrites it. |
| R4 | **Public Nimiq RPC + public price feeds** | Availability/manipulation of upstream data | Median of ≥4 sources with spread cap fails closed; for scale, run your own node and add sources. |
| R5 | **Badger files on disk** | Disk theft exposes ledger + hashed admin credential | Encrypt the volume, restrict FS permissions, back up encrypted. (Admin secrets are hash-only; users' real funds are keys we don't hold.) |
| R6 | **Shared treasury address** | All deposits funnel through one key | Hardware-secured treasury key, withdrawals multisig; per-user HD addresses are the scaling path. |
| R7 | **Static host without header support** | Missing HSTS etc. | Meta-CSP still enforces the policy; prefer hosts that allow custom headers (a `_headers` file is provided). |
| R8 | **No refund initiation from UI** | UX, not security | Admin-initiated refunds keep money movement under audit. |

Nothing in R1–R8 allows **remote theft of user funds or goods** when the
deployment checklist below is followed; they are hardening items.

---

## 7. Attacker walkthrough — what happens when they try

| Attack | Result |
|--------|--------|
| Send fake `tx_hash` / call `/quotes/{id}/payment` without paying | State moves to "submitted" only; nothing settles until real coins confirm on-chain. |
| Pay 0.00001 NIM less/more than the quote | No match — exact Luna amounts; payment never settles; quote expires. |
| Replay someone else's login signature | Fails — signature is bound to a fresh server nonce with 5-min expiry and domain-separated from transactions. |
| Register with someone else's address | Impossible — address must be derived from the signing public key. |
| Enumerate/read other users' orders, tickets, codes | 404 — all reads are ownership-scoped at the data layer. |
| Buy with client-side price tampering | Ignored — price always re-fetched from CryptoRefills at order time. |
| Double-click / script-spam order endpoint | Idempotency key returns the same order; single-transaction debit. |
| Buy with zero balance | 402 before any supplier call; no invoice is created. |
| Poison the price oracle (one feed) | Median + min-sources + max-spread fails closed; no quote issued. |
| Forge webhook "delivered" | Key check + full re-fetch from CryptoRefills with own credentials. |
| Steal the CryptoRefills partner secret from the frontend | Not there — never shipped to the browser. |
| Brute-force admin login | Argon2id + TOTP + 10/min limiter + 5-strikes lockout + audit. |
| Use customer token on admin API | Different identity plane — always 401. |
| XSS the frontend to steal something | No innerHTML sinks; strict CSP blocks remote/inline scripts; secrets of value (keys) aren't in the browser at all. |
| Clickjack into approving a payment | frame-ancestors 'none'; payments require explicit Hub/Keyguard confirmation anyway. |
| Compromise a CDN | Nothing loads from CDNs. |

---

## 8. Findings fixed during this review

| Severity | Finding | Fix |
|----------|---------|-----|
| High | Direct-NIM purchases (quotes) had **no user-facing read API**: buyers could not track settlement or retrieve delivered codes after paying — both a product break and a support/social-engineering risk. | Added ownership-scoped `GET /api/quotes` and `GET /api/quotes/{id}`; settlement worker now stores supplier redemption data on the quote (`CompleteQuoteWithFulfillment`, guarded transition from `polygon_confirmed` only); `UpdatedAt` tracked on quotes. Verified with `go build`, `go vet`, `go test ./...` (all pass). |
| Medium | Old frontend pinned `@nimiq/hub-api@1.6.5`, a version that does not exist on the registry — builds would break or be forced to an unintended substitute (supply-chain risk). | New frontend vendors the real, current `@nimiq/hub-api@1.14.0` standalone build with recorded SHA-256, and its API usage was written against that version's actual types. |
| Medium | Legacy Astro frontend carried a Node/SSR runtime dependency chain for what is a purely static site (larger patch surface). | New frontend is plain static HTML/CSS/JS — zero dependencies to patch, zero server rendering. The legacy code (built around a custodial balance/deposit model) has been deleted from the repository. |
| Low | Order stage texts were server-localized (Turkish) while the product requires an English-only UI. | Frontend renders its own English copy keyed by stage id; UI language is deterministic regardless of backend strings. |

All fixes from the previous `SECURITY_REVIEW.md` (no JWT default, strict CORS,
mandatory idempotency keys, webhook key enforcement, body/cache/query limits,
HS256 pinning, dependency audit) were re-verified in code and remain in place.

---

## 9. Deployment hardening checklist

1. **TLS everywhere** (HSTS in `_headers`), HTTP → HTTPS redirect at the edge.
2. Serve the static frontend and `/api` **from one origin** (e.g. nginx:
   `/` → static, `/api` → proxy `127.0.0.1:8080`); then CSP `connect-src 'self'`
   is exact and CORS is irrelevant. If origins differ, set
   `ALLOWED_ORIGINS` to the frontend origin **exactly** and update CSP.
3. Generate fresh high-entropy secrets: `JWT_SECRET` (≥32 bytes),
   `ADMIN_SESSION_SECRET`, `ADMIN_BOOTSTRAP_TOKEN` or env admin seed
   (Argon2id PHC + TOTP), `CRYPTOREFILLS_WEBHOOK_KEY` (≥32 bytes).
4. Backend listens on loopback behind the proxy; block direct public ingress
   to `:8080`.
5. Encrypt the Badger data volume; restrict permissions; encrypted backups.
6. Treasury key in hardware security / multisig; never on the app server if
   avoidable.
7. WAF/CDN rate limiting in front of the backend for production scale.
8. Redact query strings in ingress logs (webhook key); alert on admin
   lockouts and `manual_review` quotes.
9. Monitor: oracle health endpoint, settlement failures, refund paths,
   `AttachSupplierIDs` errors and `manual_review` quotes created by the
   order-creation stale marker (reconciliation before high volume).
10. Pin and re-hash vendor files on every update (`sha256sum vendor/*`).

---

## 10. Verification performed

- `go build ./...`, `go vet ./...`, `go test ./...` — **all pass** (Go 1.23).
- Frontend: every module passes `node --check`; static server smoke-tested
  (all pages/assets 200, path traversal blocked); full mock-API flow
  exercised end-to-end (login → balance → order → quote → ticket).
- Code-level re-verification of every claim in §2–§4 against the current
  source (auth, middleware, handlers, settlement, db).
- No production credentials, live keys, or real payments were used.
