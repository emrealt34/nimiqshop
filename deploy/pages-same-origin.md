# Same-origin API on Cloudflare Pages

## Architecture

Astro still builds static HTML. `public/_routes.json` limits Pages Functions
execution to `/api/*`; `functions/api/[[path]].js` forwards those requests to
the fixed `https://shopapi.nimiqbase.com` upstream. The browser uses `/api`.
The existing local Node proxy and nginx setup remain unchanged.

Use Wrangler / the GitHub Actions workflow to deploy from the repository root;
Functions live outside `dist`, and uploading `dist` alone via dashboard drag
and drop does NOT deploy this proxy. API requests consume Pages Functions
(Workers) quota. Do not set a fail-open policy that serves assets for API errors.

## GitHub deployment

1. Rotate any token previously shared in chat. Store the replacement with
   Account / Cloudflare Pages / Edit permission in repository Actions secret
   `CLOUDFLARE_API_TOKEN`. Never commit credentials.
2. Set Actions secret `CLOUDFLARE_ACCOUNT_ID` to your account ID.
3. Set Actions variable `CLOUDFLARE_PAGES_PROJECT` to `nimshop`.
4. Merge the patch into `main`. The workflow tests the proxy, builds static
   pages, verifies artifacts and deploys both `dist` and `functions`.
5. Remove cross-origin `PUBLIC_API_URL` / `API_URL` overrides from other Pages
   build configurations, or set them to `/api`. The Actions build already
   explicitly sets `/api`. Avoid concurrent native Git builds and Actions
   publishing different settings to the same production branch.

## API prerequisites / 502

The public upstream returned HTTP 502 during investigation. This patch cannot
repair its server or tunnel. The proxy preserves failures as non-cacheable JSON
with an error status; it NEVER turns failures into fake successful catalogs,
payments or sessions. Inspect origin service, port, tunnel and logs separately.

- Keep `FRONTEND_URL=https://shop.nimiqbase.com` in the backend; preserve Go's
  Origin/CSRF checks. Do not replace the allowlist with `*`.
- Prefer an empty `SESSION_COOKIE_DOMAIN` (host-only). Proxy responses strip
  explicit Domain attributes while preserving Secure, HttpOnly, SameSite,
  expiry and multiple Set-Cookie headers. Existing API-host-only sessions may
  require one new login on the shop origin.
- Cloudflare visitor attribution is a security boundary. Do not disable the
  backend's trusted-hop validation to make a proxy work. Verify `/api/geo`,
  presence, rate limiting and actual client-IP handling in the deployed same-zone
  path. A cross-zone Worker hop may not preserve the original visitor address.
- Do not cache `/api/*` in Cloudflare cache rules. The Function emits no-store.
- No automatic retries for POST/payment requests.

## Checks

Requires Node 24+:

```sh
npm ci
npm run test:pages-proxy
npm run build
npm run check
node scripts/check-pages-build.mjs
npm run check:workflows
npx wrangler pages functions build functions
```

After deployment, check `/api/auth/session`, `/api/catalog/brands?kind=giftcard&country=TR`,
`/api/market/fx`, and `/api/geo`, plus login/logout and presence in the browser.
Test catalog types giftcard/mobile_recharge/esim, activity, cashback and trees.
Requests must target `shop.nimiqbase.com/api/...`, not `shopapi...`.
Do not test live checkout by making a real payment without explicit approval.

## Other console messages

- The cited hashed client asset returned 200 at investigation time. Build checks
  verify emitted HTML asset references exist; this is not evidence that every
  historic edge-cache 404 is repaired. Deploy HTML/assets together; investigate
  stale browser/service-worker/edge entries if old hashes recur.
- The redundant HubApi preload is removed; the deferred script is retained.
- `ERR_BLOCKED_BY_CLIENT` for Cloudflare Web Analytics is a browser/extension
  block, not CORS. Disable automatic Web Analytics injection in the Cloudflare
  dashboard if analytics is unwanted. Do not proxy it around ad blockers.

## Rollback

Rollback the whole Pages deployment (assets + Function together) and revert the
Git commit. Reverting only the Function leaves `/api` without its upstream.
