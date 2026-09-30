/* Deployment configuration — 100% static, no build step.
 *
 * This file is the ONE place where the frontend's API URL is configured.
 * Frontend is fully static; the backend is Go, same-origin or separate domain.
 *
 * ── OPTION 1 (default): same-origin '/api' ─────────────────────────────────
 *   API_BASE: '/api'
 *   Works everywhere the backend (or a proxy to it) answers on the SAME
 *   origin as the pages: the local launcher (npm start / static-server),
 *   nginx with `location /api { proxy_pass http://127.0.0.1:8084; }`,
 *   Cloudflare tunnel to the static server, etc. No CORS, nothing to change.
 *
 * ── OPTION 2: separate domains (frontend on Cloudflare Pages etc.) ─────────
 *   API_BASE: 'https://api.yourdomain.com/api'
 *   Then, so CORS is allowed by the backend, ONE line in backend/.env:
 *     FRONTEND_URL=https://your-frontend.pages.dev
 *   (that single line feeds the whole CORS allowlist).
 *   You MUST also add the API origin to CSP connect-src — the easy way:
 *     API_URL=https://api.yourdomain.com npm run build
 *   which bakes the origin into CSP + a config seed automatically. Or build
 *   once and edit only this file + CSP meta by hand (see below).
 *
 * IMPORTANT for Option 2 by hand: after changing API_BASE to an absolute URL,
 * also allow that origin in connect-src (HTML meta + public/_headers).
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
    // Separate domains (this project's production shape):
    //   'https://shopapi.nimiqbase.com/api'  ← frontend on shop.nimiqbase.com,
    //                                           API on shopapi.nimiqbase.com (CORS via FRONTEND_URL in backend/.env)
    // Same-origin (local launcher / nginx): '/api'
    // NOTE: `npm run build` AUTO-bakes this from backend/.env PUBLIC_API_URL —
    // edit that one line and rebuild instead of touching this file.
    API_BASE: 'https://shopapi.nimiqbase.com/api',

    // Public shop hostname. ONE value for titles, footer, Hub appName,
    // and copy. Backend SITE_HOST is the live source via GET /api/site.
    SITE_HOST: 'shop.nimiqbase.com',

    // Frontend URL (for reference/docs only, not used for API calls)
    FRONTEND_URL: 'https://shop.nimiqbase.com',

    // Nimiq Hub
    HUB_URL: 'https://hub.nimiq.com',
    APP_NAME: 'shop.nimiqbase.com',
    NETWORK: 'mainnet', // 'mainnet' | 'testnet'

    // Feature flags
    // TEST MODE sandbox — ON by default, matches backend .env.example
    // TEST_MODE=true. Normal users, normal flow, real everything — only the
    // payments are simulated (checkout and the order page both show the same
    // "Pay now — simulated" payment screen, plus a gold site banner). Go live
    // by setting BOTH to false: TEST_MODE: false here and TEST_MODE=false in
    // the backend .env.
    TEST_MODE: true,

    // Links
    GITHUB_URL: 'https://github.com/emrealt34/nimiqshop',
    RELEASE_REPO: 'emrealt34/nimiqshop',
  },
  window.APP_CONFIG || {}
);
