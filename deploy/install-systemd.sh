#!/usr/bin/env bash
# install-systemd.sh — one command to build nim.shop, wire its secrets, and
# install it as two systemd services that run in the background and
# auto-launch on every boot.
#
#   sudo bash deploy/install-systemd.sh [options]
#
# Options:
#   --home DIR         project root           (default: auto-detected)
#   --user NAME        system user to run as  (default: nimshop)
#   --frontend-port N  frontend port          (default: 4321)
#   --api-port N       backend/API port       (default: 8080)
#   --rebuild          force a full rebuild even if outputs look fresh
#   --proxy-secret     ALSO set a shared secret on the local Node→Go proxy hop
#                      (only for servers that NEVER serve the API directly;
#                      with it, browsers cannot call the backend without the
#                      frontend proxy — leave it off if you also use
#                      `npm run start:backend` with a Cloudflare-hosted
#                      frontend)
#   --dry-run          print what would happen, change nothing
#   -h, --help         this help
#
# The script is IDEMPOTENT: safe to re-run any time. Existing secrets in
# backend/.env are never overwritten; units are re-rendered and services
# restarted. See README_SETUP.md for the full guide.

set -euo pipefail

APP_HOME="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SERVICE_USER="nimshop"
FRONTEND_PORT="8085"
API_PORT="8084"
DRY_RUN=0
FORCE_BUILD=0
PROXY_SECRET=0

while [[ $# -gt 0 ]]; do
  case "$1" in
    --home) APP_HOME="$(realpath "$2")"; shift 2 ;;
    --user) SERVICE_USER="$2"; shift 2 ;;
    --frontend-port) FRONTEND_PORT="$2"; shift 2 ;;
    --api-port) API_PORT="$2"; shift 2 ;;
    --rebuild) FORCE_BUILD=1; shift ;;
    --proxy-secret) PROXY_SECRET=1; shift ;;
    --dry-run) DRY_RUN=1; shift ;;
    -h|--help) grep '^#' "$0" | tail -n +2 | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) echo "Unknown option: $1 (see --help)" >&2; exit 1 ;;
  esac
done

say()  { printf '\033[1;32m==>\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33mWARN:\033[0m %s\n' "$*" >&2; }
die()  { printf '\033[1;31mERROR:\033[0m %s\n' "$*" >&2; exit 1; }
run()     { if (( DRY_RUN )); then printf '  [dry-run] %s\n' "$*"; else "$@"; fi; }
run_sh()  { if (( DRY_RUN )); then printf '  [dry-run] %s\n' "$*"; else bash -c "$*"; fi; }

[[ -f "$APP_HOME/package.json" ]] || die "$APP_HOME does not look like the project root (package.json missing). Use --home /path/to/project."
[[ $FRONTEND_PORT =~ ^[0-9]+$ && $API_PORT =~ ^[0-9]+$ && $FRONTEND_PORT != "$API_PORT" ]] || die "--frontend-port and --api-port must be different numbers"
if (( ! DRY_RUN )); then [[ $EUID -eq 0 ]] || die "Run me with sudo (root is needed for systemd)."; fi

# ------------------------------------------------------------------ checks --
command -v node >/dev/null 2>&1 || die "Node.js is not installed. Install Node 22.13+ from https://nodejs.org/ first."
NODE_BIN="$(command -v node)"
NODE_MAJOR="$(node -p 'Number(process.versions.node.split(".")[0])')"
if (( NODE_MAJOR < 22 )); then
  warn "Node $NODE_MAJOR detected — the project asks for Node >= 22.13. It will very likely still build/run, but 22 LTS is recommended."
fi
command -v openssl >/dev/null 2>&1 || die "openssl is required to generate secrets."

say "Project root : $APP_HOME"
say "Service user : $SERVICE_USER   (frontend :$FRONTEND_PORT → API :$API_PORT)"

# ------------------------------------------------------------ backend/.env --
ENV_FILE="$APP_HOME/backend/.env"
ENV_EXAMPLE="$APP_HOME/backend/.env.example"
[[ -f "$ENV_EXAMPLE" ]] || die "backend/.env.example not found — broken checkout?"
if [[ ! -f "$ENV_FILE" ]]; then
  say "Creating backend/.env from the template (your secrets stay in .env)…"
  run cp "$ENV_EXAMPLE" "$ENV_FILE"
fi

env_get() { grep -E "^$2=" "$1" 2>/dev/null | head -n1 | cut -d= -f2- || true; }
env_set() { # env_set <file> <KEY> <value>  (replaces or appends, idempotent)
  awk -v k="$2" -v v="$3" '
    $0 ~ ("^" k "=") { print k "=" v; found=1; next }
    { print }
    END { if (!found) print k "=" v }
  ' "$1" > "$1.tmp" && mv "$1.tmp" "$1"
}
env_set_checked() { # env_set that never mutates anything in --dry-run
  if (( DRY_RUN )); then printf '  [dry-run] set %s=%s…\n' "$2" "${3:0:14}"; else env_set "$@"; fi
}
rand_secret() { openssl rand -base64 48 | tr -d '\n=+/' | cut -c1-56; }
rand_password() { openssl rand -base64 15 | tr -d '\n'; }

say "Configuring backend/.env for a same-origin deployment (static server proxies /api)…"

# JWT secret: generate if missing, placeholder, or too short.
jwt="$(env_get "$ENV_FILE" JWT_SECRET)"
if [[ -z "$jwt" || "$jwt" == REPLACE_WITH_* || "$jwt" == CHANGE_THIS_* || ${#jwt} -lt 32 ]]; then
  env_set_checked "$ENV_FILE" JWT_SECRET "$(rand_secret)"
  say "  JWT_SECRET generated."
else
  say "  JWT_SECRET already set — keeping it."
fi

# Shared proxy secret between the Node frontend server and the Go backend.
# DEFAULT: EMPTY. An empty secret keeps BOTH modes working: full stack (the
# local proxy still forwards verified visitor IPs from loopback) AND
# backend-only (`npm run start:backend` with the frontend on Cloudflare —
# browsers hit the Go API directly, which a set secret would reject with
# 403). --proxy-secret opts into the hardened hop for full-stack-only boxes.
secret="$(env_get "$ENV_FILE" FORWARDED_HEADER_SECRET)"
if (( PROXY_SECRET )); then
  if [[ -z "$secret" ]]; then
    secret="$(rand_secret)"
    env_set_checked "$ENV_FILE" FORWARDED_HEADER_SECRET "$secret"
    say "  FORWARDED_HEADER_SECRET generated (--proxy-secret: shared with the frontend proxy)."
  else
    say "  FORWARDED_HEADER_SECRET already set — keeping it."
  fi
else
  if [[ -n "$secret" ]]; then
    warn "FORWARDED_HEADER_SECRET is set in backend/.env — direct browser access to the API (Cloudflare-hosted frontend) will get 403."
    warn "Run 'npm run start:backend' with it cleared, or re-run this installer without --proxy-secret after removing the line, if you need direct access."
  else
    say "  FORWARDED_HEADER_SECRET left EMPTY (both full-stack and backend-only/Cloudflare modes work)."
  fi
  secret=""
fi

# Correct proxy mode for "browser → Node static server → Go" (the template's
# PROXY_HEADER_MODE=cloudflare is ONLY for direct cloudflared→Go tunnels and
# makes every local request fail with 403 UNVERIFIED_PROXY).
env_set_checked "$ENV_FILE" PROXY_HEADER_MODE "forwarded"
env_set_checked "$ENV_FILE" TRUST_PROXY "true"
env_set_checked "$ENV_FILE" LISTEN_ADDR ":$API_PORT"
say "  PROXY_HEADER_MODE=forwarded, TRUST_PROXY=true, LISTEN_ADDR=:$API_PORT"

# Admin password: prompt (if interactive) or generate; never keep placeholders.
admin_user="$(env_get "$ENV_FILE" ADMIN_USERNAME)"
admin_pw="$(env_get "$ENV_FILE" ADMIN_PASSWORD)"
GENERATED_ADMIN=0
if [[ -z "$admin_pw" || "$admin_pw" == CHANGE_THIS_* || "$admin_pw" == REPLACE_WITH_* ]]; then
  if [[ -t 0 && -t 1 && $DRY_RUN -eq 0 ]]; then
    read -r -p "Admin panel password for user '${admin_user:-admin}' (Enter = generate): " -s admin_pw; echo
  fi
  if [[ -z "${admin_pw:-}" ]]; then admin_pw="$(rand_password)"; GENERATED_ADMIN=1; fi
  env_set_checked "$ENV_FILE" ADMIN_PASSWORD "$admin_pw"
  say "  ADMIN_PASSWORD set."
else
  say "  ADMIN_PASSWORD already set — keeping it."
fi

# -------------------------------------------------------- deploy/frontend.env --
FRONTEND_ENV="$APP_HOME/deploy/frontend.env"
# The Node frontend reaches the Go API over loopback only; the API port is
# never a public interface (reviewed: not debug residue).
BACKEND_URL="http://127.0.0.1:$API_PORT" # DevSkim: ignore DS162092 loopback-only internal hop by design
# Liveness probe through the frontend port on this machine.
HEALTH_URL="http://127.0.0.1:$FRONTEND_PORT/api/health" # DevSkim: ignore DS162092 local health probe by design
if (( DRY_RUN )); then
  printf '  [dry-run] write %s:\n' "$FRONTEND_ENV"
  printf '    PORT=%s\n    BACKEND=%s\n' "$FRONTEND_PORT" "$BACKEND_URL"
  [[ -n "$secret" ]] && printf '    FORWARDED_HEADER_SECRET=<generated>\n'
  printf '    # API_URL=https://api.example.com   ← uncomment to point the frontend at a remote backend\n'
  printf '    NIMSHOP_NO_URL_OVERRIDE=1\n'
else
  {
    echo "# Generated by deploy/install-systemd.sh — environment for nimshop-frontend.service"
    echo "# If FORWARDED_HEADER_SECRET is present it must match the same key in backend/.env."
    echo "PORT=$FRONTEND_PORT"
    echo "BACKEND=$BACKEND_URL"
    [[ -n "$secret" ]] && echo "FORWARDED_HEADER_SECRET=$secret"
    echo "# ONE LINE to point this frontend at a remote backend (no rebuild needed):"
    echo "# API_URL=https://api.example.com"
    echo "# Pin /config.js to this origin; ignore leftover launcher/tunnel records."
    echo "NIMSHOP_NO_URL_OVERRIDE=1"
  } > "$FRONTEND_ENV"
  say "Wrote $FRONTEND_ENV (ports, optional proxy secret, optional API_URL knob)."
fi

# ------------------------------------------------------------------- build --
need_build=0
[[ -x "$APP_HOME/backend/bin/nimshop-server" ]] || need_build=1
[[ -f "$APP_HOME/dist/index.html" ]] || need_build=1
(( FORCE_BUILD )) && need_build=1
if (( need_build )); then
  say "Building (backend → backend/bin/nimshop-server, frontend → dist/)…"
  command -v npm >/dev/null 2>&1 || die "npm not found — install Node.js first."
  if [[ ! -d "$APP_HOME/node_modules" ]]; then
    run_sh "cd '$APP_HOME' && npm ci --no-audit --no-fund"
  fi
  if [[ ! -x "$APP_HOME/backend/bin/nimshop-server" ]] && ! command -v go >/dev/null 2>&1; then
    die "Backend binary missing and Go is not installed. Install Go 1.26+ (https://go.dev/dl/) or drop a prebuilt binary at backend/bin/nimshop-server."
  fi
  build_flags=""
  (( FORCE_BUILD )) && build_flags="--force"
  run_sh "cd '$APP_HOME' && node scripts/build-all.mjs $build_flags"
else
  say "Builds look fresh — skipping (use --rebuild to force)."
fi

# ---------------------------------------------------------- user + ownership --
SERVICE_GROUP="$(id -gn "$SERVICE_USER" 2>/dev/null || echo "$SERVICE_USER")"
if ! id -u "$SERVICE_USER" >/dev/null 2>&1; then
  say "Creating system user '$SERVICE_USER'…"
  run useradd --system --user-group --home-dir "$APP_HOME" --shell /usr/sbin/nologin "$SERVICE_USER"
  SERVICE_GROUP="$(id -gn "$SERVICE_USER" 2>/dev/null || echo "$SERVICE_USER")"
fi
run mkdir -p "$APP_HOME/backend/data"
say "Granting '$SERVICE_USER' ownership of the app directory (it must write backend/data and read .env)…"
run chown -R "$SERVICE_USER:$SERVICE_GROUP" "$APP_HOME"
run chmod 600 "$ENV_FILE"
run chmod 600 "$FRONTEND_ENV"

# ------------------------------------------------------------- systemd units --
render_unit() { # <template> <destination>
  sed -e "s|__APP_HOME__|$APP_HOME|g" \
      -e "s|__SERVICE_USER__|$SERVICE_USER|g" \
      -e "s|__SERVICE_GROUP__|$SERVICE_GROUP|g" \
      -e "s|__NODE_BIN__|$NODE_BIN|g" \
      -e "s|__FRONTEND_PORT__|$FRONTEND_PORT|g" \
      -e "s|__API_PORT__|$API_PORT|g" \
      "$1" > "$2"
}

say "Installing systemd units…"
if (( DRY_RUN )); then
  render_unit "$APP_HOME/deploy/nimshop-backend.service"  /tmp/nimshop-backend.service
  render_unit "$APP_HOME/deploy/nimshop-frontend.service" /tmp/nimshop-frontend.service
  echo "  [dry-run] units rendered to /tmp/nimshop-backend.service and /tmp/nimshop-frontend.service"
else
  render_unit "$APP_HOME/deploy/nimshop-backend.service"  /etc/systemd/system/nimshop-backend.service
  render_unit "$APP_HOME/deploy/nimshop-frontend.service" /etc/systemd/system/nimshop-frontend.service
fi

# ------------------------------------------------------ enable + start (boot) --
if (( ! DRY_RUN )); then
  run systemctl daemon-reload
  say "Enabling auto-launch on boot and (re)starting services…"
  run systemctl enable nimshop-backend.service nimshop-frontend.service
  run systemctl restart nimshop-backend.service nimshop-frontend.service
else
  printf '  [dry-run] systemctl daemon-reload\n'
  printf '  [dry-run] systemctl enable --now nimshop-backend nimshop-frontend\n'
fi

# -------------------------------------------------------------- health check --
if (( ! DRY_RUN )) && command -v curl >/dev/null 2>&1; then
  say "Waiting for $HEALTH_URL …"
  ok=""
  for _ in $(seq 1 30); do
    if curl -fsS --max-time 2 "$HEALTH_URL" >/dev/null 2>&1; then ok=1; break; fi
    sleep 1
  done
  if [[ -n "$ok" ]]; then
    say "Health check passed ✔"
  else
    warn "Health check did not pass within 30s. Inspect logs with:"
    warn "  journalctl -u nimshop-backend -u nimshop-frontend -n 50 --no-pager"
  fi
fi

# ------------------------------------------------------------------ summary --
IP="$(hostname -I 2>/dev/null | awk '{print $1}')"
[[ -n "$IP" ]] || IP="<server-ip>"
cat <<SUMMARY

──────────────────────────────────────────────────────────────────────
 nim.shop is installed and running in the background

   Site         : $IP:$FRONTEND_PORT   (plain HTTP on the LAN — terminate TLS
                  in front of it before exposing the shop publicly)
   Admin panel  : $IP:$FRONTEND_PORT/admin
   Admin user   : ${admin_user:-admin}
   API (health) : $HEALTH_URL
   Data         : $APP_HOME/backend/data  (BadgerDB — back this up!)
   Config       : $APP_HOME/backend/.env + deploy/frontend.env

SUMMARY
if (( GENERATED_ADMIN )) && (( ! DRY_RUN )); then
  echo "   Admin password: $admin_pw   ← generated now, save it (also in backend/.env)"
  echo
fi
cat <<SUMMARY2
 Manage the services:
   systemctl status nimshop-frontend nimshop-backend   # state
   journalctl -u nimshop-backend -f                    # live backend logs
   sudo systemctl restart nimshop-frontend nimshop-backend
   sudo systemctl stop nimshop-frontend nimshop-backend
   sudo systemctl disable nimshop-frontend nimshop-backend   # stop auto-launch on boot

 Auto-launch on boot is ENABLED (systemctl enable). Reboot to verify,
 or check with: systemctl is-enabled nimshop-backend
──────────────────────────────────────────────────────────────────────
SUMMARY2
