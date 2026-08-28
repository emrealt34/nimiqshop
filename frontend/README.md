# nim.shop — static frontend

100% client-side, 100% static. **No build step, no server rendering, no framework runtime** — plain HTML + CSS + ES modules. Every page is a static file you can host anywhere (nginx, S3/CloudFront, Netlify, Cloudflare Pages, GitHub Pages, any CDN).

- Fully responsive, mobile-first, works from 320px to 4K
- Safe-area insets for notched devices (`viewport-fit=cover` + `env(...)` everywhere)
- Nimiq design language: deep navy, Nimiq gold, Nunito, pill buttons
- English only (no other languages present)
- **Nimiq Hub** wallet login + **Nimiq Pay** checkout fully supported (popup on desktop, automatic redirect fallback on mobile)
- Zero CDN dependencies — `@nimiq/hub-api`, `@nimiq/identicons`, QR library and the Nunito font are vendored and SHA-256 pinned (see root `SECURITY_ANALYSIS.md` §5.1). Strict CSP on every page.

## Pages

| File | Purpose |
|------|---------|
| `index.html` | Storefront: catalog (gift cards / top-ups / eSIMs), search, purchase sheet — pay with **NIM via Nimiq Pay** (quote flow) |
| `wallet.html` | Safe redirect/info page explaining the non-custodial direct-payment flow; no balance or deposits |
| `orders.html` | Direct CryptoRefills-Lightning purchase history, filters, live local-status refresh |
| `order.html?id=…` | Live stage tracking, delivery codes/PIN/links, inline support thread (`?type=quote&id=…` for direct Lightning payments) |
| `support.html` | Ticket list, new ticket (bound to a real order/quote), chat-style conversation with auto-refresh |

## Configure — Separate Domains vs Same-Origin

Frontend is **fully static** and backend is **separate**. They communicate via `config.js` — this is the only place where domains are set.

### Option A: Same-Origin (nginx) — simplest, no CORS

- Frontend + Backend on same domain: `https://nim.shop`
- nginx: `/` → static frontend, `/api` → proxy to `127.0.0.1:8080`
- `config.js`: `API_BASE: '/api'` (default)
- Backend `.env`: `ALLOWED_ORIGINS` can be empty
- CSP: `connect-src 'self'` is enough

```js
// frontend/config.js
window.APP_CONFIG = {
  API_BASE: '/api',
  FRONTEND_URL: 'https://nim.shop',
  HUB_URL: 'https://hub.nimiq.com',
  NETWORK: 'mainnet',
};
```

### Option B: Separate Domains (Cloudflare Pages + API) — production

- Frontend: `https://nim.shop` (static: Cloudflare Pages, Netlify, S3)
- Backend: `https://api.nim.shop` (Go backend)

Then:

```js
// frontend/config.js — MUST be absolute URL
window.APP_CONFIG = {
  API_BASE: 'https://api.nim.shop/api',
  FRONTEND_URL: 'https://nim.shop',
  HUB_URL: 'https://hub.nimiq.com',
  NETWORK: 'mainnet',
};
```

```env
# backend/.env
ALLOWED_ORIGINS=https://nim.shop,https://www.nim.shop
PUBLIC_WEBHOOK_BASE_URL=https://api.nim.shop
```

And update CSP in `frontend/*.html` and `frontend/_headers` to allow your backend domain:

```
connect-src 'self' https://api.nim.shop https://*.nim.shop https://*.trycloudflare.com https://hub.nimiq.com https://hub.nimiq-testnet.com;
```

This is already done in this repo — `https://api.nim.shop` and `https://*.nim.shop` and `https://*.trycloudflare.com` are included in CSP.

**Why you saw 502 errors:**
- Frontend tried to fetch `/api/integrity`, `/api/market/nim-rate`, `/api/catalog/giftcards` from **frontend domain** (same-origin `/api`) but backend is on separate domain → frontend host returned 502.
- Fix: set `API_BASE` to absolute backend URL `https://api.nim.shop/api` and set `ALLOWED_ORIGINS` to frontend origin. Then browser will request correctly to backend domain with CORS.

Backend now handles OPTIONS preflight properly (204 + CORS headers) and supports wildcard subdomains like `https://*.nim.shop`.

### Local Development

```bash
# Option 1: live-proxy (same-origin to browser, no CORS needed) — recommended
cd frontend
BACKEND=http://127.0.0.1:8080 PORT=4321 node dev/live-proxy.js
# Keep config.js API_BASE='/api' — proxy forwards /api/* to backend

# Option 2: direct separate domains (needs CORS)
# frontend/config.js: API_BASE='http://localhost:8080/api'
# backend/.env: ALLOWED_ORIGINS=http://localhost:4321,http://localhost:8000
# And add http://localhost:8080 to CSP connect-src in HTML

# UI-only preview with mock API (NEVER deploy):
node dev/mock-server.js  # http://localhost:8080
```

The mock server simulates whole backend (catalog, login, orders and quote settlement stages, plus support bot reply) so every screen can be clicked through. Production fulfillment is driven by CryptoRefills webhook, not supplier-status polling.

## Deploy

### nginx (recommended: one origin)

```nginx
server {
    listen 443 ssl http2;
    server_name nim.shop;

    root /var/www/nimshop/frontend;  # this folder
    index index.html;

    add_header X-Frame-Options DENY always;
    add_header X-Content-Type-Options nosniff always;
    add_header Referrer-Policy strict-origin-when-cross-origin always;
    add_header Strict-Transport-Security "max-age=63072000; includeSubDomains; preload" always;
    add_header Permissions-Policy "camera=(), microphone=(), geolocation=(), payment=(), usb=()" always;

    location /api/ {
        proxy_pass http://127.0.0.1:8080;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
    }

    location / { try_files $uri $uri.html $uri/ =404; }
}
```

### Separate Domains — Cloudflare Pages + Backend

1. Deploy `frontend/` to Cloudflare Pages (build: none, output: `frontend/`)
2. Deploy Go backend to `api.nim.shop` (e.g. VPS, Fly.io, Railway)
3. Edit `frontend/config.js`: `API_BASE='https://api.nim.shop/api'`
4. Edit `backend/.env`: `ALLOWED_ORIGINS=https://nim.shop` and `PUBLIC_WEBHOOK_BASE_URL=https://api.nim.shop`
5. Ensure `_headers` and HTML CSP include `https://api.nim.shop` in `connect-src` (already done)

CORS is now prevented by proper env config — no wrong requests to wrong URL.

### Netlify / Cloudflare Pages (same-origin or separate)
Drop the folder in; `_headers` applies hardened headers automatically. Build command: none. Publish directory: this folder.

### S3 + CloudFront
Upload all files, set default root object to `index.html`, and add same response headers via CloudFront function/response-headers policy.

## Backend requirements

The frontend uses only existing backend endpoints, plus the user-scoped quote endpoints added in this revision (needed so direct-NIM purchases can be tracked and their codes delivered):

- `GET /api/quotes` — list my CryptoRefills Lightning quotes
- `GET /api/quotes/{id}` — quote detail + fulfillment

See root `SECURITY_ANALYSIS.md` §8 for details.

## Fixing CryptoRefills 400 / timeout errors

If you saw:

```
cr brands(): context deadline exceeded
supplier error on GET /api/catalog/giftcards: context deadline exceeded
cr brands(): cryptorefills error (400 GET /v2/brands?country_code=): no detail
```

Fixes applied in this revision:

1. **Empty country_code 400 error** — `client.go` Brands() now omits `?country_code=` when country is empty (global catalog). Previously it sent `?country_code=` which CryptoRefills returns 400 for.
2. **Timeout (context deadline exceeded)** — usually means:
   - `CRYPTOREFILLS_PARTNER_ID` missing/invalid
   - Network blocked to `api.cryptorefills.com`
   - Or queue overloaded. Increase `CRYPTOREFILLS_QUEUE_MAX` or check partner ID.
   - Backend now has fallback to cached catalog if supplier fails, so storefront stays usable.

Ensure backend `.env` has valid `CRYPTOREFILLS_PARTNER_ID` from https://www.cryptorefills.com/account

## File map

```
index.html orders.html order.html wallet.html support.html
config.js                 ← deployment config (edit me) — SET API_BASE HERE FOR SEPARATE DOMAINS
_headers                  ← hardened headers for Netlify/CF Pages — UPDATE connect-src FOR BACKEND DOMAIN
css/app.css               ← design system (tokens, components, responsive, safe-area)
css/fonts.css             ← vendored Nunito variable font
fonts/nunito-var.woff2    ← SIL OFL license included
vendor/                   ← HubApi 1.14.0, identicons 1.6.2, qrcode-generator 1.4.4
js/util.js                ← DOM builder (XSS-safe), formatters, icon() helper
js/icons.js               ← Lucide icons (lucide-static 1.34.0, ISC) — all UI icons
js/api.js                 ← typed API client — uses CFG.API_BASE (supports absolute URLs)
js/session.js             ← token/address state + expiry handling
js/hub.js                 ← Nimiq Hub login + Nimiq Pay + redirect recovery
js/identicon.js           ← identicon rendering (+ safe fallback)
js/ui.js                  ← toasts, sheets, badges, timelines, QR, copy, countdowns
js/shell.js               ← header, nav, account menu, tab bar, login sheet
js/pages/*.js             ← page controllers
dev/live-proxy.js         ← DEV: serves static + proxies /api/* to BACKEND env var (same-origin to browser, no CORS)
dev/mock-server.js        ← DEV-ONLY mock backend for previews
```

## Design & responsiveness notes

- Layout is fluid with `clamp()` typography and CSS grid `auto-fill` — no layout breakpoint ever hides functionality, only reflows it.
- Mobile: bottom tab bar; purchase/login flows open as bottom sheets; desktop: top nav + centered dialogs. Both honor `safe-area-inset-*`.
- Touch targets ≥ 44px, visible focus rings, `prefers-reduced-motion` respected, semantic roles/labels on interactive components.
- Dark-only theme (`color-scheme: dark`) matching Nimiq brand navy `#042133` and gold `#E2A62B`.
