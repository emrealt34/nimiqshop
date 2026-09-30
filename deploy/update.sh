#!/usr/bin/env bash
# update.sh — rebuild backend + frontend from (changed) source and restart
# the systemd services. Run this after you edit code or pull a new version:
#
#   bash deploy/update.sh            # rebuild whatever is stale, restart
#   bash deploy/update.sh --force    # force a full rebuild
set -euo pipefail

APP_HOME="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$APP_HOME"

[[ -f package.json ]] || { echo "ERROR: $APP_HOME is not the project root" >&2; exit 1; }
command -v node >/dev/null 2>&1 || { echo "ERROR: Node.js not installed" >&2; exit 1; }

if [[ ! -d node_modules ]]; then
  echo "[update] node_modules missing — npm ci…"
  npm ci --no-audit --no-fund
fi

echo "[update] building (skips anything already fresh)…"
node scripts/build-all.mjs "$@"

if systemctl list-unit-files 2>/dev/null | grep -q '^nimshop-frontend'; then
  echo "[update] restarting services…"
  sudo systemctl restart nimshop-backend.service nimshop-frontend.service
else
  echo "[update] systemd services not installed — run: sudo bash deploy/install-systemd.sh"
  exit 0
fi

PORT="$(grep -E '^PORT=' deploy/frontend.env 2>/dev/null | head -n1 | cut -d= -f2- || true)"
PORT="${PORT:-8085}"
sleep 1
# Liveness probe through the frontend port on this machine (reviewed: not
# debug residue).
HEALTH_URL="http://127.0.0.1:$PORT/api/health" # DevSkim: ignore DS162092 local health probe by design
if curl -fsS --max-time 3 "$HEALTH_URL" >/dev/null 2>&1; then
  echo "[update] health OK ($HEALTH_URL)"
else
  echo "[update] WARNING: health check failed — check: journalctl -u nimshop-backend -u nimshop-frontend -n 50" >&2
  exit 1
fi
