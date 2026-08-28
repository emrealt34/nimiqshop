# Security review — 23 August 2026

## Scope and method

Reviewed the Astro/React browser client and Go/fasthttp API, wallet-signature login, Badger persistence, Nimiq RPC/price integrations, and the supplier (CryptoRefills) order/webhook paths. I performed dependency scanning with `npm audit --omit=dev`, manual source review of the money/auth/webhook paths, and static searches for dangerous DOM sinks and wildcard CORS. No production credentials or live payment systems were exercised.

## Fixed findings

| Severity | Finding | Fix |
|---|---|---|
| Critical | `JWT_SECRET` silently defaulted to `change-me-in-prod`; a deployment mistake made session/challenge JWTs forgeable. | Removed the fallback. Startup now rejects secrets shorter than 32 bytes and rejects overly long sessions. `.env` is loaded locally without overriding deployment environment variables. |
| High | `Access-Control-Allow-Origin: *` permitted arbitrary origins to call the bearer-token API. | CORS now has an exact `ALLOWED_ORIGINS` allowlist, `Vary: Origin`, minimal methods/headers, and no wildcard. HTTPS is required outside loopback development. |
| High | An order request generated a new server UUID on every retry, so the stored “idempotency key” never prevented duplicate debits/purchases. | `Idempotency-Key` is mandatory for orders, validated, uniquely stored, and returns the existing order on retry/race. The browser generates a UUID per submit. |
| High | The frontend’s Astro 4 dependency tree contained known Astro/Vite/esbuild/sharp advisories. | Updated Astro to 7.2.4 and `@astrojs/react` to 6.0.4; fresh production dependency audit reports **0 vulnerabilities**. Node >= 22.12 is now enforced. |
| Medium | The webhook accepted every request if the key was absent, and used ordinary string comparison. | Missing key is now reject-by-default; enabled webhooks require a 32+ byte secret and use constant-time comparison. The order is still fetched from CryptoRefills before changing local state. |
| Medium | Unbounded request bodies, catalog-query cache growth, very large quantities, and unauthenticated request bursts could exhaust service resources or create unreasonable orders. | Added a 1 MiB configurable body cap (maximum 4 MiB), bounded cache (512 entries), bounded query values, per-process IP token bucket (60/minute, burst 20), and configurable order maximum (default 10; absolute maximum 100). |
| Medium | URLs used by privileged server integrations could be configured as non-TLS/malformed endpoints. | Startup validates Nimiq RPC, price feed, CryptoRefills, and public webhook URLs as absolute HTTPS URLs. |
| Low | JWT parser accepted any HMAC algorithm family rather than explicitly pinning the issuer’s algorithm. | Parsing now explicitly requires HS256. |

## Verification

- `npm ci --ignore-scripts && npm audit --omit=dev`: **0 vulnerabilities**.
- Static review: no `innerHTML`, `dangerouslySetInnerHTML`, `eval`, legacy Iqons package, default JWT secret, or wildcard CORS remains.
- Archive hygiene: `.gitignore` excludes `.env`, Badger data, keys, dependency folders and build artefacts.
- Go unit/build execution could **not** be run in this workspace because the Go toolchain is unavailable (`go: command not found`). Run `gofmt -w .`, `go test ./...`, `go vet ./...`, and `govulncheck ./...` on a Go 1.22+ workstation/CI before deployment.
- The local Node runtime is v20, while the patched Astro version requires Node >=22.12; therefore its build must be executed after upgrading Node. This is an intentional compatibility/security gate, not a successful local build claim.

## Remaining risks and deployment requirements

1. **Bearer token storage:** the SPA uses `localStorage`; any successful same-origin XSS can steal a token. For a high-value production service, place API and frontend on one origin and switch to short-lived HttpOnly/Secure/SameSite cookies plus CSRF protection.
2. **Webhook authentication:** CryptoRefills does not sign webhook bodies. A secret in a query string can be exposed in proxy logs. Keep it redacted in ingress logs, rotate it on exposure, allowlist CryptoRefills source IPs at the edge if available, and retain the current API re-verification step.
3. **Distributed limits:** the rate limiter is process-local. Put a CDN/WAF and shared rate limit in front of multi-instance production deployments. Do not trust `X-Forwarded-For` unless your proxy strips and rewrites it.
4. **Payment reconciliation:** an outage after CryptoRefills accepts an order but before local ID attachment is now covered by the durable `supplier_request_at` marker: the tracker never auto re-sends (duplicate-order guard) and parks the quote in `manual_review` after the stale window, so manual reconciliation is bounded and visible. Alert on `AttachSupplierIDs` errors and `manual_review` quotes before high-value operation.
5. **Upstream trust:** a public Nimiq RPC and one price oracle remain trusted inputs. Use a self-hosted/independently cross-checked node and multiple price sources with sanity bounds for material volume.
6. **Operational hardening:** terminate TLS at a hardened proxy, restrict backend network ingress, encrypt/back up Badger storage, rotate API/JWT/webhook secrets, and run SCA/SAST plus dependency updates in CI.
