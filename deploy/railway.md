# Deploying the backend on Railway

The production split is:

| Piece | Where | URL |
| --- | --- | --- |
| Frontend (static Astro build) | Cloudflare Pages project `nimshop` | <https://shop.nimiqbase.com> |
| Backend (Go API + BadgerDB) | Railway service `zetas`, project `adventurous-motivation`, environment `production` | <https://zetas-production.up.railway.app> (port **8084**) |
| Public API hostname | Cloudflare (proxied DNS) → Railway | <https://shopapi.nimiqbase.com> |
| Staking pool (unchanged) | Orange Pi | <https://api.nimiqbase.com> |

**Railway hosts ONLY the backend.** The static frontend is Cloudflare Pages and
the database lives on the Railway volume; nothing else runs on Railway. That
split is what the hosting dashboard shows, and it is what the response headers
prove:

```text
$ curl -sI https://shop.nimiqbase.com/api/geo
server: cloudflare
x-railway-edge: ber1            ← the API answers from Railway
cf-ray: a4637aa188ac7561-SEA

$ curl -s https://shopapi.nimiqbase.com/api/geo
{"cloudflare":false,"country":"","ip":"89.222.123.194"}
```

## How a request actually reaches the API (measured, 2026-10-06)

The public hostname `shopapi.nimiqbase.com` is a **proxied Cloudflare DNS
record** pointing straight at the Railway service. There is no separate
`cloudflared` hop on this path: the `x-railway-*` headers are stamped by
Railway's own edge, and `cf-ray` by Cloudflare in front of it.

```text
browser → Cloudflare edge → Railway edge → container
```

Two consequences worth knowing, because both shaped the code:

1. **Cloudflare's `CF-*` headers DO survive the hop — corrected 2026-10-06.**
   An earlier revision of this section said the opposite ("`CF-Connecting-IP` /
   `CF-Ray` / `CF-IPCountry` is not forwarded to the container", on the strength
   of a `/api/geo` answer of `cloudflare:false`). That measurement no longer
   holds and is retracted: with the current Railway deploy a plain GET of the
   live `/api/geo` answers `cloudflare:true`, `country:"US"` and the caller's
   real address, with no hint header sent by the client at all. Railway's edge
   stamps `x-railway-edge` and Cloudflare's `cf-ray` rides along, and the
   container sees the `CF-*` set.

   The consequence for the code is the opposite of what was written here: the
   country CAN come from `CF-IPCountry` on this deployment, and it does. The
   deployment's own carriers are kept as defence in depth for the shapes where a
   hop rebuilds the request and drops that set — `X-Nimshop-Client-Country`
   (what `functions/api/[[path]].js` and `scripts/proxy.mjs` restate from the
   edge) and, for display only, the browser's `X-Nimshop-Country-Hint` read from
   the same-origin `/cdn-cgi/trace`. See `backend/internal/clientip` and
   `src/lib/edgeGeo.ts`. Where both arrive, the console's origin note
   cross-checks them rather than trusting either alone.
2. **The API sees the visitor's address, not the tunnel's.** `/api/geo` reports
   the caller's own address (measured 2026-10-06: the request's real egress IP,
   not a Railway or Cloudflare one), which is also what the operator console's
   People panel shows. If a future change puts an origin-pinned tunnel
   (`cloudflared` on the Orange Pi) back in front, the peeled chain ends at the
   tunnel's egress instead — one address for every visitor — and that is the
   case `X-Nimshop-Client-IP` (set by the Pages function / Node proxy from the
   edge's own attribution) exists to cover.

The Railway domain itself is **not** a public entry point: the backend runs in
`PROXY_HEADER_MODE=forwarded` and answers `403 UNVERIFIED_PROXY` to anything
that did not come through the proxy hop.

## 1. What the Railway service is configured with

Build/deploy settings (service → Settings):

| Setting | Value | Why not the default |
| --- | --- | --- |
| Builder | `RAILPACK` | The repo `Dockerfile` ends with `VOLUME ["/data"]`, which Railway rejects: `dockerfile invalid: docker VOLUME at Line 71 is not supported, use Railway Volumes`. Railpack ignores the Dockerfile entirely. |
| Root Directory | `backend` | Only the Go module is built; the frontend ships from Pages. |
| Build Command | `go build -tags timetzdata -ldflags="-s -w" -o out ./cmd/server` | Railpack otherwise builds the alphabetically first command, `./cmd/cashback-test`. |
| Start Command | `./out` | Matches the build output above. |
| Region | `europe-west4` (Rotterdam) | Closest to the tunnel origin. |
| Volume | `zetas-volume` → `/data` | BadgerDB lives in `/data/badger`; without it every redeploy would start from an empty database. |
| Watch Paths | `backend/**` | A docs or frontend push must not redeploy the API. |
| Domain target port | **8084** | The server listens on `:8084`; a domain pointed at another port answers 502. |

Deploys are triggered by pushes to `main` (GitHub trigger
`dd1591d7-513f-483c-9887-fdb8f09b69db`) or manually with
`serviceInstanceDeploy`. Watch Paths is `backend/**`, so a docs- or
frontend-only push must not be expected to roll the API.

When checking whether a backend change is live, do not guess from timing: hit an
endpoint that names what you changed. (`/api/geo` gained `country_hint` and
`ip_hint` fields, which is how the edge-attribution work was confirmed on the
running service.)

## 2. Environment variables that differ from `backend/.env.example`

```text
SITE_HOST                 shop.nimiqbase.com
PUBLIC_API_URL            https://shopapi.nimiqbase.com
FRONTEND_URL              https://shop.nimiqbase.com
LISTEN_ADDR               :8084
BADGER_DIR                /data/badger
STATIC_DIR                (unset — Pages serves the frontend)
CRYPTOREFILLS_PARTNER_ID  YQyw0dJ0FM

TRUST_PROXY               true
PROXY_HEADER_MODE         forwarded
TRUSTED_PROXY_CIDRS       100.64.0.0/10,10.0.0.0/8,172.16.0.0/12,192.168.0.0/16,169.254.0.0/16,fc00::/7,fe80::/10,127.0.0.1/32,::1/128

SESSION_COOKIE_SAME_SITE  none     # shop.* → shopapi.* is cross-site
SESSION_COOKIE_SECURE     true
ADMIN_COOKIE_SECURE       true
```

`PROXY_HEADER_MODE=forwarded`, not `cloudflare`: the immediate TCP peer is
Railway's edge, which is not Cloudflare, so the trust decision has to be made
against the forwarded chain rather than against `CF-Connecting-IP` alone. (The
`CF-*` set does arrive — see the correction above — but the peer cannot be
trusted as Cloudflare from inside the container, and `cloudflare` mode fails
closed on that and answers 403 to every request.) `cloudflare` mode fails closed here and answers 403 to
every request (`UNVERIFIED_PROXY`). In `forwarded` mode the allowlist above
covers Railway's private ranges and the request is served.

Because Railway's edge passes the normalized `X-Forwarded-For` through, the
resolved address IS the visitor's (confirmed live: `/api/geo` returned the
owner's own address, and the operator console shows it per person). The
`X-Nimshop-Client-IP` carrier keeps that true even on a deployment whose inner
hop re-originates the request (an origin tunnel), where peeling the chain would
otherwise yield the tunnel's egress for every visitor.

### Cashback payout wallet (`CASHBACK_WALLET_SEED`)

- **Where it lives:** ONLY as a Railway Variable on the `zetas` service.
  Never in this repo, never in GitHub Secrets, never in CI, never in chat or
  notes. GitHub Secrets would expose the payout key to every workflow run
  while production does not read it from there at all.
- **Accepted formats:** a 24-word BIP39 recovery phrase as ONE line
  (lowercase, single spaces, no numbering) or a 32-byte hex seed (64 chars,
  optional `0x`). The server derives BIP39 → SLIP-10 ed25519 →
  `m/44'/242'/<account>'` itself (`CASHBACK_WALLET_ACCOUNT`, default 0).
- **Self-check:** at startup the server logs the derived address
  ("verify this address matches your wallet"). Compare it with your wallet
  before funding anything; the admin status also reports `wallet_configured`.
- **Rotation rule:** a phrase that was ever pasted into a chat, mail or
  document is compromised. Generate a fresh wallet offline, move the funds,
  replace the Variable, restart. The old phrase goes to the trash.

## 3. Staking-pool integration (shop ↔ pool)

Both features are server-to-server only; the pool sends no CORS headers.

```bash
POOL_API_URL              https://api.nimiqbase.com
POOL_VALIDATOR_ADDRESS    NQ49 N8MB XYCR XBUP 404C KXKK L49M A7BT F082
POOL_FEED_API_KEY         <openssl rand -hex 32 — must equal GPOOL_FEED_API_KEY on the pool>
```

Verified against the live pool:

```console
$ curl -s https://api.nimiqbase.com/api/cashback/terms
{"staker_base_bps":0,"staker_base_percent":0,"min_stake_luna":1,"rule":"any_positive_stake","pool_fee_percentage":0}

$ curl -s -o /dev/null -w '%{http_code}\n' \
    https://api.nimiqbase.com/api/stakers/NQ49N8MBXYCRXBUP404CKXKKL49MA7BTF082
404            # not a delegator — exactly what internal/poolstake expects

$ curl -s -o /dev/null -w '%{http_code}\n' \
    "https://api.nimiqbase.com/api/cashback/profit?address=…"   # no X-Feed-Key
403
```

The shop applies the pool's `staker_base_bps` **verbatim**: a pool answering
`0` means stakers earn the base rate, not the 0.5% the shop defaults to when
the pool is unreachable. Raise it on the pool side (`GPOOL_CASHBACK_BASE_BPS`)
if stakers are meant to get a boost.

## 4. Operations

```bash
# Are we alive?
curl -s https://shopapi.nimiqbase.com/api/health | python3 -m json.tool

# Build logs of the newest deployment
railway logs --build -p <project-id> -e production -s zetas

# Manual deploy
railway up --service zetas            # or serviceInstanceDeploy over the API
```

Railway's free plan forces `sleepApplication=true`, so an idle service sleeps
and the first request after that waits for the container to boot (10–30 s).
Set it to `false` on a paid plan for an always-warm shop.
