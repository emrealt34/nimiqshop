# nimiqshop.io

Gift cards, eSIMs and mobile top-ups, paid with **NIM** (Nimiq Pay / BTC
Lightning) or **USDT on Polygon**. A Go API with an embedded BadgerDB store
and a static Astro + React storefront, shipped as one binary, one container
image or a one-line installer.

[![CI](https://github.com/emrealt34/nimiqshop/actions/workflows/ci.yml/badge.svg)](https://github.com/emrealt34/nimiqshop/actions/workflows/ci.yml)
[![Tests](https://github.com/emrealt34/nimiqshop/actions/workflows/tests.yml/badge.svg)](https://github.com/emrealt34/nimiqshop/actions/workflows/tests.yml)
[![Release](https://github.com/emrealt34/nimiqshop/actions/workflows/release.yml/badge.svg)](https://github.com/emrealt34/nimiqshop/actions/workflows/release.yml)
[![Container](https://github.com/emrealt34/nimiqshop/actions/workflows/container.yml/badge.svg)](https://github.com/emrealt34/nimiqshop/actions/workflows/container.yml)
[![Go coverage](https://img.shields.io/endpoint?url=https%3A%2F%2Fraw.githubusercontent.com%2Femrealt34%2Fnimiqshop%2Fbadges%2Fgo-coverage.json)](https://github.com/emrealt34/nimiqshop/actions/workflows/ci.yml)

[![CodeQL](https://github.com/emrealt34/nimiqshop/actions/workflows/codeql.yml/badge.svg)](https://github.com/emrealt34/nimiqshop/actions/workflows/codeql.yml)
[![Security](https://github.com/emrealt34/nimiqshop/actions/workflows/security.yml/badge.svg)](https://github.com/emrealt34/nimiqshop/actions/workflows/security.yml)
[![Semgrep](https://github.com/emrealt34/nimiqshop/actions/workflows/semgrep.yml/badge.svg)](https://github.com/emrealt34/nimiqshop/actions/workflows/semgrep.yml)
[![DevSkim](https://github.com/emrealt34/nimiqshop/actions/workflows/devskim.yml/badge.svg)](https://github.com/emrealt34/nimiqshop/actions/workflows/devskim.yml)
[![OSV-Scanner](https://github.com/emrealt34/nimiqshop/actions/workflows/osv-scanner.yml/badge.svg)](https://github.com/emrealt34/nimiqshop/actions/workflows/osv-scanner.yml)
[![OpenSSF Scorecard](https://api.scorecard.dev/projects/github.com/emrealt34/nimiqshop/badge)](https://scorecard.dev/viewer/?uri=github.com/emrealt34/nimiqshop)
[![Mozilla Observatory](https://img.shields.io/mozilla-observatory/grade/shop.nimiqbase.com?publish&label=observatory)](https://developer.mozilla.org/en-US/observatory/analyze?host=shop.nimiqbase.com)

[![Lint](https://github.com/emrealt34/nimiqshop/actions/workflows/eslint.yml/badge.svg)](https://github.com/emrealt34/nimiqshop/actions/workflows/eslint.yml)
[![Workflow security](https://github.com/emrealt34/nimiqshop/actions/workflows/zizmor.yml/badge.svg)](https://github.com/emrealt34/nimiqshop/actions/workflows/zizmor.yml)
[![Quality](https://github.com/emrealt34/nimiqshop/actions/workflows/quality.yml/badge.svg)](https://github.com/emrealt34/nimiqshop/actions/workflows/quality.yml)
[![Lighthouse](https://github.com/emrealt34/nimiqshop/actions/workflows/performance.yml/badge.svg)](https://github.com/emrealt34/nimiqshop/actions/workflows/performance.yml)
[![Go](https://img.shields.io/github/go-mod/go-version/emrealt34/nimiqshop?filename=backend%2Fgo.mod&logo=go)](backend/go.mod)
[![Node](https://img.shields.io/badge/node-%E2%89%A5%2024-339933?logo=node.js&logoColor=white)](package.json)
[![Latest release](https://img.shields.io/github/v/release/emrealt34/nimiqshop?include_prereleases&sort=semver)](https://github.com/emrealt34/nimiqshop/releases/latest)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

Live shop: <https://shop.nimiqbase.com> · Static preview: <https://emrealt34.github.io/nimiqshop/>

## Contents

- [Install](#install)
- [Run from source](#run-from-source)
- [Configuration](#configuration)
- [Architecture](#architecture)
- [Nimiq Pay balance reference](#nimiq-pay-balance-reference)
- [Testing](#testing)
- [Continuous integration](#continuous-integration)
- [Security](#security)
- [Releases and supply chain](#releases-and-supply-chain)
- [Contributing](#contributing)

## Install

### One-line installer (Linux / Windows)

```bash
curl -fsSL https://github.com/emrealt34/nimiqshop/releases/latest/download/install.sh | bash
nimshop start          # then: nimshop status · nimshop stop · nimshop logs
```

```powershell
irm https://github.com/emrealt34/nimiqshop/releases/latest/download/install.ps1 | iex
nimshop start
```

The installer downloads the release `.zip` for your platform, verifies it
against `SHA256SUMS.txt` and unpacks one process that serves the API and the
frontend together. Edit the install's `backend/.env` for supplier keys.

### Container

```bash
cp backend/.env.example backend/.env    # fill in JWT_SECRET, supplier keys…
docker run -d --name nimshop -p 8084:8084 \
  -v nimshop-data:/data --env-file backend/.env \
  ghcr.io/emrealt34/nimiqshop:latest
```

or `docker compose up -d` with the included [`compose.yaml`](compose.yaml),
which binds the port to the loopback interface only (put a TLS-terminating
reverse proxy in front).
The image is built `FROM scratch` (two static binaries, the frontend bundle
and the CA bundle), runs as uid 65532, exposes `8084`, keeps BadgerDB on the
`/data` volume and ships a `HEALTHCHECK`. Tags: `2.1.0`, `2.1`, `2`, `latest`
for `linux/amd64` and `linux/arm64`.

## Run from source

Needs **Node 24 LTS** and **Go 1.27**.

```bash
git clone https://github.com/emrealt34/nimiqshop.git
cd nimiqshop
npm ci
cp backend/.env.example backend/.env
npm start              # static frontend on :8085, API on :8084 (proxied under /api)
```

| Command | What it does |
| --- | --- |
| `npm start` | Build if needed, run backend + frontend |
| `npm run start:dev` | Astro dev server with hot reload + backend |
| `npm run start:tunnel` | Same, published through a Cloudflare quick tunnel |
| `npm run build` / `build:all` | Frontend → `dist/` / backend + frontend |
| `npm run verify` | Every static gate CI runs (types, i18n, API contract, lint, config) |
| `npm run test:e2e` | Build the test stack and run the full Playwright suite |
| `npm run test:e2e:smoke` | Only `@smoke` tests |
| `go run ./cmd/mockstack` (in `backend/`) | Deterministic CryptoRefills stand-in on `:9020` |

## Configuration

Secrets live only in `backend/.env` (never committed; the template is
[`backend/.env.example`](backend/.env.example)). The server refuses to start
on unsafe values (short `JWT_SECRET`, placeholder secrets, plain-HTTP
origins outside loopback, missing partner id).

| Variable | Purpose |
| --- | --- |
| `SITE_HOST` | Public hostname; drives every user-facing shop name |
| `LISTEN_ADDR` | Bind address (default `:8084`) |
| `STATIC_DIR` | Built frontend folder to serve (empty = API only) |
| `BADGER_DIR` | Embedded database directory (default `./data/badger`) |
| `NIMSHOP_SERVER_METRICS_SCOPE` | Server card metrics: `auto` (source-run default), `instance` (container/process only; Docker default), or `host` (dedicated machine only) |
| `JWT_SECRET` | Shopper session signing key (≥ 32 random bytes) |
| `CRYPTOREFILLS_*` | Supplier partner id, keys, webhook secret |
| `ADMIN_USERNAME`, `ADMIN_PASSWORD_HASH`, `ADMIN_TOTP_SECRET` | Admin console (Argon2id + TOTP SHA-256) |
| `ALLOWED_ORIGINS` / `FRONTEND_URL` | CORS + CSRF allow-list for split deployments |
| `CASHBACK_*`, `TREE_PLANTING_*`, `POOL_*` | Staker cashback, tree-planting donations, validator pool ladder |
| `TEST_MODE` | Sandbox: real catalogue, simulated payments (never on a public host) |

The frontend's API base is the one setting in [`public/config.js`](public/config.js)
(`/api` same-origin by default; `API_URL=https://api.example.com npm run build`
bakes a split deployment into CSP).

## Architecture

```text
browser ──► static Astro/React bundle (dist/)         ──► /api ──► Go server (fasthttp)
                                                                    ├─ handlers/   HTTP API, admin console, webhooks
                                                                    ├─ middleware/ sessions, CSRF, rate tiers, client IP
                                                                    ├─ db/         BadgerDB store (schemaless keyspace)
                                                                    ├─ settlement/ supplier order tracker + notifications
                                                                    ├─ cryptorefills/ supplier client with budget queue
                                                                    ├─ cashback/   NIM payouts, tree donations, stake ladder
                                                                    └─ nimiq/      addresses, signed-message login, RPC
```

- **Backend** (`backend/`): Go 1.27, `fasthttp` + `fasthttp/router`, BadgerDB
  (fsync per commit), Argon2id + BLAKE2b vendored from `x/crypto` v0.57.0
  (`internal/xcrypto`, drift-checked in CI), no CGO.
- **Frontend** (`src/`): Astro 7 + React 19 + TypeScript, six locales with
  build-time parity checks, strict CSP.
- **Login**: Nimiq Hub signed message (ed25519 over the SHA-256 of the
  prefixed challenge); sessions are HttpOnly cookies or bearer tokens.
- **Money**: quotes are the single source of truth; supplier state is
  applied through one state machine (no regressions, idempotent
  webhooks + tracker polls).

## Nimiq Pay balance reference

The Mini App reads the queried address on Nimiq Pay's active network. A zero
wallet-address balance does not include NIM held at a separate HTLC or vesting
contract address, and contract balances are not automatically spendable. See
[`docs/nimiq-pay-get-balance.md`](docs/nimiq-pay-get-balance.md) for SDK
availability, luna units, typed errors and the direct and generic provider calls.

## Testing

| Layer | Where | Runs |
| --- | --- | --- |
| Go unit + API integration | `backend/**/_test.go`, `backend/cmd/server/main_test.go` boots the real router against an in-process supplier double (login, checkout, simulated payment, admin console, CORS) | every push (CI, race detector, `-coverpkg=./...`) |
| Static gates | `npm run verify` — `tsc`, i18n parity, API route contract, ESLint + oxlint, config syntax, workflow policy | every push (CI) |
| Playwright e2e | `tests/e2e` — routing, functional, responsive (desktop/tablet/phone × 6 locales), smoke on Firefox/WebKit/Mobile Safari | nightly (weekdays), `workflow_dispatch`, PRs labelled `e2e` |
| Fuzzing | Go fuzz targets | weekly |
| Lighthouse | `.lighthouserc.json` (accessibility is a hard error) | main + weekly |
| Benchmarks | `go test -bench` + bundle budget | main + weekly |

See [`tests/README.md`](tests/README.md) for the e2e matrix.

## Continuous integration

Every workflow pins actions to full commit SHAs, declares least-privilege
`permissions`, sets `persist-credentials: false`, and is itself audited by
zizmor (pedantic persona) and actionlint on every change.

| Workflow | Purpose |
| --- | --- |
| [CI](.github/workflows/ci.yml) | Frontend gates + build, Go vet/lint/race tests + coverage badge, workflow policy, GitHub Pages deploy of the static preview |
| [Tests](.github/workflows/tests.yml) | Full Playwright matrix (nightly / manual / `e2e` label) |
| [CodeQL](.github/workflows/codeql.yml) · [Semgrep](.github/workflows/semgrep.yml) · [DevSkim](.github/workflows/devskim.yml) · [OSSAR](.github/workflows/ossar.yml) | Static application security testing, all uploading SARIF |
| [Security](.github/workflows/security.yml) | gitleaks, govulncheck, npm audit, Trivy (fs), OpenSSF Scorecard, vendored x/crypto drift check |
| [OSV-Scanner](.github/workflows/osv-scanner.yml) · [Dependency Review](.github/workflows/dependency-review.yml) | Known-vulnerable dependencies on push and in pull requests |
| [Container](.github/workflows/container.yml) | hadolint, image build, `/api/health` + `HEALTHCHECK` smoke test, Trivy image scan |
| [Workflow security](.github/workflows/zizmor.yml) · [Script lint](.github/workflows/script-lint.yml) · [Quality](.github/workflows/quality.yml) | zizmor, actionlint, ShellCheck/PSScriptAnalyzer, codespell, yamllint, markdownlint |
| [ESLint](.github/workflows/eslint.yml) · [SonarCloud](.github/workflows/sonarcloud.yml) | Lint findings as code-scanning alerts; SonarCloud when a token is configured |
| [Performance](.github/workflows/performance.yml) · [Benchmarks](.github/workflows/benchmarks.yml) · [Fuzz](.github/workflows/fuzz.yml) · [Links](.github/workflows/links.yml) | Lighthouse, Go benchmarks + bundle budget, fuzzing, documentation links |
| [Release](.github/workflows/release.yml) · [SBOM](.github/workflows/sbom.yml) | Signed releases and container images; CycloneDX SBOM on tags and monthly |
| [PR triage](.github/workflows/pr-triage.yml) · [Stale](.github/workflows/stale.yml) | Area labels (`e2e` label opts a PR into the Playwright matrix), stale housekeeping |

Dependabot opens grouped weekly PRs for npm, Go modules and Actions with a
7-day release cooldown; security updates bypass the cooldown.

## Security

- Report vulnerabilities privately via
  [GitHub private vulnerability reporting](https://github.com/emrealt34/nimiqshop/security/advisories/new)
  — see [SECURITY.md](SECURITY.md).
- Secret scanning with push protection is on; code scanning combines
  CodeQL, Semgrep, DevSkim, OSSAR, Trivy, gitleaks, govulncheck, OSV and
  Scorecard. The policy is **zero open alerts**: findings are fixed, not
  dismissed, and the two deliberate exceptions (loopback addresses in the
  local CLI / smoke tests) carry inline justifications.
- Hardened defaults: Argon2id admin passwords with TOTP (SHA-256),
  per-resource rate tiers, CSRF double-submit + origin checks, strict CSP,
  HTTPS-only origins outside loopback, `SyncWrites` BadgerDB.

## Releases and supply chain

Pushing a tag `v*` (or running the Release workflow) publishes:

- `nimshop-{linux,windows}-{amd64,arm64}.zip`, `install.sh`, `install.ps1`,
  `SHA256SUMS.txt`;
- signed **SLSA build provenance** for every asset (GitHub Artifact
  Attestations), also attached as `nimshop-<tag>.provenance.sigstore.json`
  for offline verification:

  ```bash
  gh attestation verify nimshop-linux-amd64.zip --repo emrealt34/nimiqshop
  ```

- the multi-arch container image `ghcr.io/emrealt34/nimiqshop:<version>`
  with BuildKit provenance + SBOM attestations and a registry-pushed
  attestation:

  ```bash
  gh attestation verify oci://ghcr.io/emrealt34/nimiqshop:2.1.0 --repo emrealt34/nimiqshop
  ```

A release never uses a dependency or layer cache, and the gate job re-runs
every CI check plus a blocking `govulncheck` first.

## Contributing

- `main` is protected: pull requests need a passing CI, a code-owner review
  and up-to-date branches; force-pushes and deletions are blocked.
- Keep `npm run verify` and the backend suite green locally:

  ```bash
  cd backend && gofmt -l . && go vet ./... && golangci-lint run ./... && go test -race ./...
  ```

- New workflows must pass `npm run check:workflows`, `actionlint` and
  `zizmor --persona=pedantic`.

## License

[MIT](LICENSE).

### Private security reports and whole-backend coverage

Independent OSV, Semgrep, Trivy, DevSkim, OSSAR, Gitleaks, govulncheck and
zizmor checks do not require the GitHub Code Scanning UI. SARIF-producing
scanners retain reports as Actions artifacts. Vulnerability scanner failures
remain blocking; unavailable report-upload APIs are not scanner results.
Private CodeQL and SARIF ingestion require GitHub Code Security entitlement.
Only after enabling that entitlement, set `ENABLE_PRIVATE_CODE_SCANNING=true`
to run CodeQL and upload reports to GitHub's Security tab. OpenSSF Scorecard's
public-repository assessment is not run against this private repository.
SonarCloud separately requires `SONAR_TOKEN`; a missing token means no Sonar
analysis, not a clean scan. Cloudflare remains the production frontend host;
private GitHub Pages requires its own plan support and explicit
`ENABLE_PRIVATE_GITHUB_PAGES=true` opt-in.

The whole-backend target is **100% statement coverage**, not yet achieved.
CI tests every backend package with `-race -coverpkg=./...`. Its existing
54.3% baseline is a regression floor, not the target. The coverage report
merges duplicate instrumentation blocks, lists uncovered statements and
compares exact counts without rounding up or excluding production files.
Run **Go 100% coverage audit** in Actions for the strict target check: it
fails until every instrumented statement is exercised. The README badge
continues to publish the measured result, never a hard-coded 100%.
