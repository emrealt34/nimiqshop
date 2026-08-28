#!/usr/bin/env bash
#
# build-reproducible.sh — build the nim.shop backend so its binary is
# BYTE-FOR-BYTE reproducible from the open-source source tree.
#
# Why this matters: reproducibility is what lets anyone prove that the binary
# running on the server was built from the published source. Two independent
# builds from the same source + same flags MUST produce identical bytes, hence
# an identical SHA-256. That hash is the value published out-of-band (release /
# signed git tag); the running server reports its own binary hash at
# /api/integrity, and the two are compared.
#
# Steps:
#   1. regenerate the source manifest (embedded into the binary, deterministic)
#   2. build with -trimpath (no absolute paths) and a blank build id
#   3. print the binary's SHA-256 — PUBLISH this
#
# Usage:
#   ./tools/integrity/build-reproducible.sh
#
# Prereq: Go 1.22+ and Node on PATH.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
BIN="$ROOT/bin/nimiqshop"

cd "$ROOT/backend"

echo "→ regenerating embedded source manifest"
node "$ROOT/tools/integrity/generate.mjs" backend >/dev/null

echo "→ building reproducibly"
CGO_ENABLED=0 go build \
  -trimpath \
  -buildvcs=false \
  -ldflags="-buildid=" \
  -o "$BIN" \
  ./cmd/server

echo "→ binary SHA-256 (PUBLISH this):"
if command -v sha256sum >/dev/null 2>&1; then
  sha256sum "$BIN"
else
  shasum -a 256 "$BIN"
fi
