# Security Policy

This shop is MIT licensed. Please report vulnerabilities **privately** — never
in a public issue, discussion, or pull request.

## Supported versions

Only the latest release on the `main` branch receives security fixes.
See <https://github.com/emrealt34/nimiqshop/releases>.

## How to report

1. Preferred: open a private GitHub security advisory at
   <https://github.com/emrealt34/nimiqshop/security/advisories/new>.
2. Alternatively, email the maintainer at the address listed on
   <https://github.com/emrealt34>.

Please include a description, steps to reproduce, affected version/commit and,
if possible, a proof of concept. Do not file public issues for secrets, payment
bugs, wallet/seed handling, or auth bypasses.

## Disclosure timeline

- **Acknowledgement:** within 3 business days.
- **Triage & severity assessment:** within 7 days of acknowledgement.
- **Fix or mitigation:** targeted within 30 days for high/critical issues and
  90 days for lower severities.
- **Public disclosure:** coordinated with the reporter after a fix is released;
  credit is given unless you prefer to stay anonymous.

## What we run automatically

- CodeQL on Go and JavaScript/TypeScript — results on the repository's
  security page (<https://github.com/emrealt34/nimiqshop/security>; the
  code-scanning list itself is visible to maintainers)
- Gitleaks secret scan (full history)
- `govulncheck` for Go modules (blocking)
- `npm audit` for frontend dependencies (blocking at high/critical)
- Trivy filesystem scan (blocking for fixable HIGH/CRITICAL; SARIF to the
  Security tab)
- OSV-Scanner, DevSkim and OSSAR static analysers (SARIF to the Security tab)
- Dependency Review on every pull request (fails on high/critical)
- Drift check of the two vendored `golang.org/x/crypto` packages
  (`backend/internal/xcrypto`) against the newest upstream release
- OpenSSF Scorecard (<https://scorecard.dev/viewer/?uri=github.com/emrealt34/nimiqshop>)
- golangci-lint (errcheck, staticcheck, unused, …) and oxlint/ESLint as CI gates
- ShellCheck for every shell script and the PowerShell parser for `cli/*.ps1`
- A repository workflow policy check (`npm run check:workflows`): least-privilege
  `permissions`, a `timeout-minutes` on every job, SHA-pinned actions with version
  comments, `persist-credentials: false` on every checkout, concurrency groups on
  push + pull-request workflows, and no `pull_request_target`
- Dependabot weekly updates (npm, Go, Actions), grouped per ecosystem
- Go native fuzz tests (`go test -fuzz`) for input parsers, weekly
- A CycloneDX SBOM per release tag
- Release assets ship with `SHA256SUMS.txt` and signed build provenance
  (`gh attestation verify <asset> --repo emrealt34/nimiqshop`); a release is only
  published after the full CI gate plus a blocking `govulncheck`, and the
  published archives are then re-downloaded, checksum-verified and booted

Not a gate, but reported: Lighthouse budgets (`.lighthouserc.json`), weekly
benchmarks and the `dist/` size report, markdown link check, and codespell.

Scanner findings are fixed in code rather than dismissed as false positives.

## Admin two-factor codes

Admin TOTP uses HMAC-SHA-256 (RFC 6238, 6 digits, 30 s). Releases before this
change used SHA-1; the secret is unchanged but authenticator entries must be
re-added with algorithm SHA-256 (import the `otpauth://` URI returned by the
bootstrap endpoint).

## Secrets

Never commit `.env`, wallet seeds, JWT secrets, or CryptoRefills keys.
`backend/.env.example` is the only env template in git.
