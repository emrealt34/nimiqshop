# internal/xcrypto

Verbatim copies of two packages from `golang.org/x/crypto` **v0.57.0**
(commit `3f62bf119e84c6e35e8518a2958089ade622d1a3`), BSD-3-Clause — see
[LICENSE](LICENSE):

| package | upstream files | what it is used for |
|---|---|---|
| `blake2b` | `blake2b.go`, `blake2b_generic.go`, `blake2b_ref.go` | Nimiq address derivation and transaction hashing (`internal/nimiq`) |
| `argon2` | `argon2.go`, `blake2b.go`, `blamka_generic.go`, `blamka_ref.go` | Argon2id admin password hashes (`internal/auth`) |

Only the portable Go implementations are carried (no amd64 assembly), and the
only edits are:

1. the `//go:build !amd64 || purego || !gc` constraint on `*_ref.go` is removed
   (the generic code is the only implementation here), and
2. `argon2` imports `nimiqshop/internal/xcrypto/blake2b` instead of
   `golang.org/x/crypto/blake2b`.

## Why not depend on golang.org/x/crypto?

The module carries the permanent advisory
[GO-2026-5932](https://osv.dev/GO-2026-5932) ("`golang.org/x/crypto/openpgp` is
unmaintained"). It has no fixed version — it is attached to every release of
the module — so every dependency scanner that works at module granularity
(OSV-Scanner, Trivy, Scorecard) flags any project that lists the module, even
though this shop never imported `openpgp`. Carrying the two small, stable
primitives we actually use removes the module (and `x/net`, `x/term`, `x/text`
it pulls in) from the build graph instead of suppressing the finding.

Both packages are frozen upstream (BLAKE2b is RFC 7693, Argon2 is RFC 9106) and
`xcrypto_test.go` pins them to the RFC test vectors, so a bad copy fails
`go test`.

Because the copies are byte-identical to upstream (after those two edits),
`golangci-lint` skips the `unused`/`U1000`/`whitespace` checks for this
directory only — the generic-only build leaves upstream's asm-selection
variables (`useAVX2`, `useSSE4`, …) and two helpers unreferenced.

## Updating / drift check

`sync-upstream.sh` is the single source of truth for the recipe; `UPSTREAM`
records the pinned version.

```sh
cd backend
./internal/xcrypto/sync-upstream.sh --check          # copies == pinned upstream?
./internal/xcrypto/sync-upstream.sh --check latest   # copies == newest upstream?
./internal/xcrypto/sync-upstream.sh v0.58.0          # re-vendor from a new version
go test ./internal/xcrypto/...
```

The `Security` workflow runs `--check latest` on every push and on its daily
schedule (job `xcrypto-drift`), so a new upstream release that touches either
package turns the job red and the diff is printed in the log — the copies can
never silently fall behind.
