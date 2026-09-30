#!/usr/bin/env bash
# devtools/stop.sh — best-effort stop of everything devtools started.
set -euo pipefail
cd "$(dirname "$0")/.."
pkill -f nimshop-server 2>/dev/null || true
pkill -f 'astro.js dev' 2>/dev/null || true
pkill -f scripts/static-server.mjs 2>/dev/null || true
pkill -f cloudflared 2>/dev/null || true
# Free the default ports if anything is still holding them.
for port in 8085 8084; do
  pid="$(lsof -ti tcp:$port 2>/dev/null || true)"
  if [ -n "$pid" ]; then kill -9 $pid 2>/dev/null || true; fi
done
# A killed launcher never runs its cleanup, and a leftover public-url.json holds
# a tunnel URL that is already dead — the next start must not wire the shop to it.
rm -f devtools/.runtime/public-url.json devtools/.runtime/config.js
echo "Stopped."
