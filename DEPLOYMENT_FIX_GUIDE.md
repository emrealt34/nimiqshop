# Deployment Fix Guide — Separate Frontend/Backend Domains + 502 Fix

## Problem you saw

```
Failed to load resource: the server responded with a status of 502 ()  /api/integrity
/api/market/nim-rate 502
/api/catalog/giftcards 502
/api/catalog/topups 502
/api/catalog/esims 502
cr brands(): context deadline exceeded
cr brands(): cryptorefills error (400 GET /v2/brands?country_code=): no detail
```

### Root Causes

1. **Frontend is fully static, backend on separate domain, but `API_BASE` was `/api` (same-origin)**
   - Browser requested `https://frontend-domain/api/*` → static host (Cloudflare Pages/Netlify) has no `/api` → returned 502
   - Fix: set `API_BASE` to absolute backend URL like `https://api.nim.shop/api`

2. **CryptoRefills `?country_code=` empty → 400**
   - `GET /v2/brands?country_code=` with empty value is invalid → CryptoRefills returns 400
   - Fixed in `backend/internal/cryptorefills/client.go`: now omits query param when country empty (global catalog)

3. **CORS not configured for separate domains**
   - Frontend on `https://nim.shop`, backend on `https://api.nim.shop` needs CORS
   - Fixed in `backend/cmd/server/main.go`: proper OPTIONS preflight (204), wildcard subdomain support `https://*.nim.shop`, credentials allowed

## Fixed Solution

### Frontend (static) — 100% static, no build

**File: `frontend/config.js` — EDIT ME**

```js
window.APP_CONFIG = {
  API_BASE: 'https://api.nim.shop/api', // ← ABSOLUTE backend URL for separate domains
  FRONTEND_URL: 'https://nim.shop',
  HUB_URL: 'https://hub.nimiq.com',
  NETWORK: 'mainnet',
  TEST_MODE: false,
};
```

- For **same-origin nginx**: keep `API_BASE: '/api'` and leave backend `ALLOWED_ORIGINS` empty
- For **separate domains**: use absolute URL and set backend `ALLOWED_ORIGINS` to frontend origin(s)

**CSP — already fixed in this repo**

All `frontend/*.html` and `frontend/_headers` now include:

```
connect-src 'self' http://localhost:8080 http://127.0.0.1:8080 https://api.nim.shop https://*.nim.shop https://*.trycloudflare.com https://hub.nimiq.com https://hub.nimiq-testnet.com;
```

If your backend domain is different (e.g. `https://api.example.com`), replace `https://api.nim.shop` with your domain in:
- `frontend/config.js`
- `frontend/*.html` meta CSP
- `frontend/_headers`

### Backend (Go)

**File: `backend/.env`**

```env
LISTEN_ADDR=:8080
BADGER_DIR=./data/badger
JWT_SECRET=your-32-byte-random-secret
ALLOWED_ORIGINS=https://nim.shop,https://www.nim.shop
CRYPTOREFILLS_BASE_URL=https://api.cryptorefills.com
CRYPTOREFILLS_PARTNER_ID=your-partner-id-from-cryptorefills.com/account
CRYPTOREFILLS_WEBHOOK_KEY=your-32-byte-random-secret
PUBLIC_WEBHOOK_BASE_URL=https://api.nim.shop
TRUST_PROXY=true
```

- `ALLOWED_ORIGINS` = frontend origin(s) — backend now checks exact match + `https://*.yourdomain` wildcard
- `PUBLIC_WEBHOOK_BASE_URL` = backend public URL (for CryptoRefills webhook)

**CORS handling — fixed:**

- `GlobalOPTIONS` now returns 204 with CORS headers for preflight
- `applyCORS` checks `Origin` header against allowlist, sets `Access-Control-Allow-Origin`, `Allow-Credentials: true`, `Allow-Methods: GET, POST, PUT, DELETE, OPTIONS`
- Logs blocked origins for debugging

### CryptoRefills API — fixed

- `client.go Brands()` now: if country empty → `GET /v2/brands` (no query), not `?country_code=` → fixes 400 error
- `catalog_handlers.go listBrandsCore()` now has fallback to cached catalog if supplier fails → storefront stays usable
- Ensure `CRYPTOREFILLS_PARTNER_ID` is valid from https://www.cryptorefills.com/account

### Devtools — separate domain dev

- `frontend/dev/live-proxy.js`: serves static + proxies `/api/*` to `BACKEND` env var (same-origin to browser, no CORS needed)
  ```bash
  BACKEND=http://127.0.0.1:8080 PORT=4321 node frontend/dev/live-proxy.js
  ```
- `devtools/start.bat` (Windows): auto-creates `.env`, runs backend + live-proxy + Cloudflare tunnel
- `devtools/tunnel-run.js`: creates `https://*.trycloudflare.com` tunnel for frontend preview

## Verification

1. Set `frontend/config.js` API_BASE to absolute backend URL
2. Set backend `ALLOWED_ORIGINS` to frontend URL
3. Restart backend: `go run ./cmd/server`
4. Open frontend — check console: no 502, no CORS errors
5. Catalog should load: `/api/catalog/giftcards`, `/api/catalog/topups`, `/api/catalog/esims` → 200
6. `/api/integrity` and `/api/market/nim-rate` should be 200 (not 502) because they now go to backend domain

## All Turkish Removed

- `README.md` translated to English
- All Go, JS, TSX, BAT, SH files now 0 Turkish characters (verified via Python regex)
