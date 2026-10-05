# Deploying the backend on Railway

The production split is:

| Piece | Where | URL |
| --- | --- | --- |
| Frontend (static Astro build) | Cloudflare Pages project `nimshop` | <https://shop.nimiqbase.com> |
| Backend (Go API + BadgerDB) | Railway service `zetas`, project `adventurous-motivation`, environment `production` | <https://zetas-production.up.railway.app> (port **8084**) |
| Public API edge | Cloudflare Tunnel `83001ce9-d661-4eda-bd2f-f2de34ba295b` | <https://shopapi.nimiqbase.com> |
| Staking pool (unchanged) | Orange Pi behind the same tunnel | <https://api.nimiqbase.com> |

The browser only ever talks to `shop.nimiqbase.com`; Pages proxies `/api/*` to
`shopapi.nimiqbase.com`, which the tunnel forwards to Railway. The Railway
domain itself is **not** a public entry point: the backend runs in
`PROXY_HEADER_MODE=forwarded` and answers `403 UNVERIFIED_PROXY` to anything
that did not come through the tunnel.

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
`serviceInstanceDeploy`.

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

`PROXY_HEADER_MODE=forwarded`, not `cloudflare`: the request reaches the
backend as

```text
browser → Cloudflare edge → cloudflared (Orange Pi) → Railway edge → container
```

so the immediate TCP peer is Railway's proxy, not the tunnel, and the
Cloudflare identity headers belong to the *inner* hop. `cloudflare` mode
fails closed here and answers 403 to every request (`UNVERIFIED_PROXY`),
including ones arriving through the tunnel. In `forwarded` mode the
allowlist above covers Railway's private ranges and the request is served.

> Trade-off worth knowing: because cloudflared re-originates the request from
> the Orange Pi, the IP the backend resolves is the tunnel's egress address,
> not the visitor's. Per-IP rate limiting and `/api/geo` therefore see one
> shared address. Fixing that needs the backend to be reached from a proxy
> that can inject a verified real-IP header (the same-origin Node hop the
> launcher documents, or `PROXY_HEADER_MODE=cloudflare` with the connector
> talking to the API directly).

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
