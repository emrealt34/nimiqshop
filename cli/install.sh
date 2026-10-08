#!/usr/bin/env bash
# nimiqshop.io installer (Linux) — downloads the latest GitHub Release (backend
# binary + frontend .zip) and wires a one-command CLI. No Go/Node compile
# needed. Windows uses install.ps1 instead; macOS is not supported.
#   curl -fsSL https://github.com/emrealt34/nimiqshop/releases/latest/download/install.sh | bash
#
# Env:
#   NIMSHOP_HOME   install directory (default: $HOME/nimshop)
#   NIMSHOP_PORT   listen port       (default: 8085)
#   NIMSHOP_VERSION  release tag     (default: latest)
set -euo pipefail

REPO="${NIMSHOP_REPO:-emrealt34/nimiqshop}"
HOME_DIR="${NIMSHOP_HOME:-$HOME/nimshop}"
PORT="${NIMSHOP_PORT:-8085}"
TAG="${NIMSHOP_VERSION:-latest}"

say()  { printf '\033[1;32m==>\033[0m %s\n' "$*"; }
die()  { printf '\033[1;31mERROR:\033[0m %s\n' "$*" >&2; exit 1; }

os=$(uname -s | tr '[:upper:]' '[:lower:]')
arch=$(uname -m)
case "$arch" in
  x86_64|amd64) arch=amd64 ;;
  aarch64|arm64) arch=arm64 ;;
  *) die "unsupported architecture: $arch" ;;
esac
case "$os" in
  linux) ;;
  darwin) die "macOS is not supported — this installer is Linux only" ;;
  *) die "unsupported OS: $os (linux only; Windows: install.ps1)" ;;
esac

need() { command -v "$1" >/dev/null 2>&1 || die "need $1 on PATH"; }
need curl
need unzip
need openssl

api="https://api.github.com/repos/${REPO}/releases/${TAG}"
if [[ "$TAG" == latest ]]; then
  api="https://api.github.com/repos/${REPO}/releases/latest"
fi
say "Looking up ${TAG} on ${REPO}…"
json=$(curl -fsSL "$api") || die "could not read GitHub releases — is there a published release?"
tag=$(printf '%s' "$json" | sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -1)
[[ -n "$tag" ]] || die "release JSON had no tag_name"

asset="nimshop-linux-${arch}.zip"
base="https://github.com/${REPO}/releases/download/${tag}"
url="${base}/${asset}"

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
say "Downloading ${asset} (${tag})…"
curl -fL --progress-bar -o "$tmp/$asset" "$url" || die "download failed: $url"

mkdir -p "$HOME_DIR"
say "Extracting into ${HOME_DIR}…"
unzip -q -o "$tmp/$asset" -d "$tmp/x"
# The archive holds one top-level directory (nimshop-linux-<arch>/) — flatten it.
cp -a "$tmp/x/nimshop-linux-${arch}/." "$HOME_DIR/"
rm -rf "$tmp/x"

cd "$HOME_DIR"
[[ -x ./nimshop-server ]] || chmod +x ./nimshop-server
[[ -f ./cli/nimshop ]] && chmod +x ./cli/nimshop

if [[ ! -f backend/.env ]]; then
  say "Writing backend/.env (JWT secret generated; edit CryptoRefills keys)…"
  mkdir -p backend
  # Every release archive ships the example file (see release.yml); a missing
  # one means a damaged download rather than something to paper over.
  [[ -f backend/.env.example ]] || die "backend/.env.example missing from the release archive — re-download"
  cp backend/.env.example backend/.env
  jwt=$(openssl rand -hex 32)
  if grep -q '^JWT_SECRET=' backend/.env; then
    sed -i.bak "s|^JWT_SECRET=.*|JWT_SECRET=${jwt}|" backend/.env
  else
    printf '\nJWT_SECRET=%s\n' "$jwt" >> backend/.env
  fi
  rm -f backend/.env.bak
fi

# Same-origin shop: one process serves API + frontend.
if grep -q '^LISTEN_ADDR=' backend/.env; then
  sed -i.bak "s|^LISTEN_ADDR=.*|LISTEN_ADDR=:${PORT}|" backend/.env && rm -f backend/.env.bak
else
  printf 'LISTEN_ADDR=:%s\n' "$PORT" >> backend/.env
fi
static_abs="${HOME_DIR}/frontend"
if grep -q '^STATIC_DIR=' backend/.env; then
  sed -i.bak "s|^STATIC_DIR=.*|STATIC_DIR=${static_abs}|" backend/.env
else
  printf 'STATIC_DIR=%s\n' "$static_abs" >> backend/.env
fi
rm -f backend/.env.bak

if [[ -f frontend/config.js ]]; then
  sed -i.bak "s|API_BASE: *'[^']*'|API_BASE: '/api'|" frontend/config.js && rm -f frontend/config.js.bak
fi

bin_dir="${HOME}/.local/bin"
mkdir -p "$bin_dir"
cat > "$bin_dir/nimshop" <<EOF
#!/usr/bin/env bash
exec "${HOME_DIR}/cli/nimshop" "\$@"
EOF
chmod +x "$bin_dir/nimshop" "${HOME_DIR}/cli/nimshop"

say "Installed ${tag} → ${HOME_DIR}"
echo
echo "  Start:   nimshop start     (or ${HOME_DIR}/cli/nimshop start)"
echo "  Stop:    nimshop stop"
echo "  Status:  nimshop status"
echo "  Shop:    http://127.0.0.1:${PORT}" # DevSkim: ignore DS162092 the CLI stack listens on loopback by design
echo
echo "  Edit secrets:  ${HOME_DIR}/backend/.env"
echo "  Then:          nimshop start"
echo
if ! printf '%s' "$PATH" | grep -q "$bin_dir"; then
  echo "  Add to PATH:  export PATH=\"${bin_dir}:\$PATH\""
fi
echo
"$HOME_DIR/cli/nimshop" start || true
