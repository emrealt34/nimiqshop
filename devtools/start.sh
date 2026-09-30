#!/usr/bin/env bash
# devtools/start.sh — builds everything (Go backend + Astro frontend) then
# launches the stack with a Cloudflare quick tunnel. This is the one-click
# "devtools does it all" entrypoint for Linux/macOS.
set -euo pipefail
cd "$(dirname "$0")/.."
MODE="${1:-tunnel}"
node devtools/devtools.mjs "$MODE"
