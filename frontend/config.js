/* nim.shop deployment configuration — 100% static, no build step.
 *
 * This file is the ONLY place where frontend/backend domains are configured.
 * Frontend is fully static (plain HTML/CSS/JS), backend is Go on a separate domain or same-origin.
 *
 * HOW TO CONFIGURE FOR SEPARATE DOMAINS (recommended for production):
 *
 * 1. Frontend domain: https://nim.shop (static host: Cloudflare Pages, Netlify, S3, nginx)
 * 2. Backend domain:  https://api.nim.shop (Go backend)
 *
 * Then set:
 *   API_BASE: 'https://api.nim.shop/api'
 *   FRONTEND_URL: 'https://nim.shop' (for reference, not used in requests)
 *
 * And in backend/.env:
 *   ALLOWED_ORIGINS=https://nim.shop,https://www.nim.shop
 *   PUBLIC_WEBHOOK_BASE_URL=https://api.nim.shop
 *
 * CORS is then automatically handled — backend will allow your frontend origin with credentials.
 *
 * SAME-ORIGIN SETUP (nginx proxy):
 *   API_BASE: '/api' (default)
 *   nginx: / -> static files, /api -> proxy to 127.0.0.1:8080
 *   ALLOWED_ORIGINS can be empty (same-origin needs no CORS)
 *
 * LOCAL DEVELOPMENT:
 *   Option A - live-proxy (same-origin to browser, no CORS needed):
 *     BACKEND=http://127.0.0.1:8080 PORT=4321 node frontend/dev/live-proxy.js
 *     Keep API_BASE: '/api' (proxy forwards /api/* to backend)
 *
 *   Option B - direct separate domains (needs CORS):
 *     API_BASE: 'http://localhost:8080/api'
 *     Backend .env: ALLOWED_ORIGINS=http://localhost:4321,http://localhost:8000
 *     And update CSP meta in HTML files to allow http://localhost:8080 in connect-src
 *
 * IMPORTANT: After changing API_BASE to an absolute URL, you MUST also update CSP in:
 *   - frontend/*.html meta http-equiv="Content-Security-Policy" connect-src
 *   - frontend/_headers file (for Netlify/Cloudflare Pages)
 *   Add your backend domain there, e.g. https://api.nim.shop
 *
 * HUB_URL – Nimiq Hub instance for wallet login and Nimiq Pay
 *   Mainnet: https://hub.nimiq.com
 *   Testnet: https://hub.nimiq-testnet.com
 *
 * NETWORK – 'mainnet' or 'testnet' (informational badge in UI)
 */
window.APP_CONFIG = Object.assign(
  {
    // === EDIT ME FOR YOUR DEPLOYMENT ===
    // For separate domains: 'https://api.yourdomain.com/api'
    // For same-origin nginx: '/api'
    API_BASE: '/api',

    // Frontend URL (for reference/docs only, not used for API calls)
    FRONTEND_URL: 'https://nim.shop',

    // Nimiq Hub
    HUB_URL: 'https://hub.nimiq.com',
    APP_NAME: 'nim.shop',
    NETWORK: 'mainnet', // 'mainnet' | 'testnet'

    // Feature flags
    TEST_MODE: false, // true = show CryptoRefills free TEST products

    // Links
    GITHUB_URL: 'https://github.com/nimiqbase/nimiq-shop',
    RELEASE_REPO: 'nimiqbase/nimiq-shop',
  },
  window.APP_CONFIG || {}
);
