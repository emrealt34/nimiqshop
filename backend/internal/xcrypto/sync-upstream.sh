#!/usr/bin/env bash
# Re-vendors (or verifies) the two golang.org/x/crypto packages carried in
# internal/xcrypto. See README.md for why the module is not a dependency.
#
#   ./internal/xcrypto/sync-upstream.sh            # refresh from the pinned version
#   ./internal/xcrypto/sync-upstream.sh v0.58.0    # refresh from another version
#   ./internal/xcrypto/sync-upstream.sh --check    # verify the copies match the
#                                                  # pinned version byte for byte
#   ./internal/xcrypto/sync-upstream.sh --check latest
#                                                  # verify against the newest
#                                                  # upstream release (CI drift job)
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
backend="$(cd "$here/../.." && pwd)"
pinned="$(sed -n 's/^UPSTREAM_VERSION=//p' "$here/UPSTREAM")"

mode=sync
if [[ "${1:-}" == "--check" ]]; then mode=check; shift; fi
version="${1:-$pinned}"

# Resolve and download the module without touching backend/go.mod: use a
# throwaway module so `go mod download` has somewhere to record sums.
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
(
  cd "$tmp"
  go mod init xcrypto-sync >/dev/null 2>&1
  go mod download -json "golang.org/x/crypto@${version}" > mod.json
)
dir="$(sed -n 's/^[[:space:]]*"Dir": "\(.*\)",\{0,1\}$/\1/p' "$tmp/mod.json")"
resolved="$(sed -n 's/^[[:space:]]*"Version": "\(.*\)",\{0,1\}$/\1/p' "$tmp/mod.json")"
if [[ -z "$dir" || -z "$resolved" ]]; then
  echo "could not download golang.org/x/crypto@${version}" >&2; cat "$tmp/mod.json" >&2; exit 2
fi
echo "upstream golang.org/x/crypto ${resolved} at ${dir}"

# The recipe: portable implementations only, no assembly, two mechanical edits.
stage="$tmp/stage"
mkdir -p "$stage/blake2b" "$stage/argon2"
cp "$dir"/blake2b/{blake2b.go,blake2b_generic.go,blake2b_ref.go} "$stage/blake2b/"
cp "$dir"/argon2/{argon2.go,blake2b.go,blamka_generic.go,blamka_ref.go} "$stage/argon2/"
chmod u+w "$stage"/*/*.go
# 1. the *_ref.go build constraint (generic code is the only implementation here)
sed -i '/^\/\/go:build !amd64 || purego || !gc$/,+1d' "$stage"/*/*_ref.go
# 2. argon2 hashes with the sibling blake2b copy
sed -i 's#"golang.org/x/crypto/blake2b"#"nimiqshop/internal/xcrypto/blake2b"#' "$stage"/argon2/*.go
cp "$dir/LICENSE" "$stage/LICENSE"

if [[ "$mode" == check ]]; then
  rc=0
  for f in blake2b/blake2b.go blake2b/blake2b_generic.go blake2b/blake2b_ref.go \
           argon2/argon2.go argon2/blake2b.go argon2/blamka_generic.go argon2/blamka_ref.go LICENSE; do
    if ! diff -u "$here/$f" "$stage/$f"; then rc=1; fi
  done
  if [[ $rc -ne 0 ]]; then
    echo "internal/xcrypto drifts from golang.org/x/crypto ${resolved}: re-run $0 ${resolved} and review the diff" >&2
    exit 1
  fi
  echo "internal/xcrypto matches golang.org/x/crypto ${resolved}"
  exit 0
fi

cp "$stage"/blake2b/*.go "$here/blake2b/"
cp "$stage"/argon2/*.go "$here/argon2/"
cp "$stage/LICENSE" "$here/LICENSE"
printf 'UPSTREAM_MODULE=golang.org/x/crypto\nUPSTREAM_VERSION=%s\n' "$resolved" > "$here/UPSTREAM"
(cd "$backend" && gofmt -l ./internal/xcrypto && go test ./internal/xcrypto/...)
echo "internal/xcrypto refreshed from golang.org/x/crypto ${resolved}"
