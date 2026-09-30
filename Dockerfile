# syntax=docker/dockerfile:1.7
#
# nimshop — single image serving the Go API and the built Astro frontend
# from one process (same layout as the release zips: STATIC_DIR points at
# the frontend bundle, BadgerDB lives on a volume).
#
#   docker run -d --name nimshop -p 8084:8084 -v nimshop-data:/data \
#     --env-file backend/.env ghcr.io/emrealt34/nimiqshop:latest
#
# Both build images are pinned by digest (multi-arch index digest, so the
# same line builds linux/amd64 and linux/arm64); bump the tag and the
# digest together. The runtime stage starts from scratch.

# ---------- 1. frontend bundle ----------------------------------------------
# Build stages run on the build host's own platform (no QEMU); the frontend
# bundle is platform-independent and Go cross-compiles via TARGETOS/TARGETARCH.
FROM --platform=$BUILDPLATFORM node:24.21.0-bookworm-slim@sha256:0e0ff40c39bc087845bfb27465a0df4ea419520094bc35842ff83dd8cbe6f9b6 AS frontend
WORKDIR /src
ENV CI=1 \
    ASTRO_TELEMETRY_DISABLED=1
COPY package.json package-lock.json ./
RUN npm ci --ignore-scripts --no-audit --no-fund
COPY . .
# Same-origin API by default (public/config.js: API_BASE '/api').
RUN npm run build

# ---------- 2. backend binary -----------------------------------------------
FROM --platform=$BUILDPLATFORM golang:1.27.1-bookworm@sha256:69a7b9788769bec032d238959b61854e9ae87f57be9029ec04e9885fabf99195 AS backend
WORKDIR /src/backend
ENV CGO_ENABLED=0 \
    GOFLAGS=-mod=readonly
COPY backend/go.mod backend/go.sum ./
RUN go mod download
COPY backend/ ./
ARG TARGETOS
ARG TARGETARCH
# timetzdata embeds the IANA zone database in the binaries: the runtime
# image has no OS packages at all, so there is no /usr/share/zoneinfo.
RUN GOOS="${TARGETOS}" GOARCH="${TARGETARCH}" \
    go build -trimpath -tags timetzdata -ldflags="-s -w" -o /out/nimshop-server ./cmd/server \
    && GOOS="${TARGETOS}" GOARCH="${TARGETARCH}" \
    go build -trimpath -tags timetzdata -ldflags="-s -w" -o /out/healthcheck ./cmd/healthcheck \
    && mkdir -p /out/data /out/tmp

# ---------- 3. runtime ------------------------------------------------------
# Empty base: two static binaries, the frontend bundle and the CA bundle.
# No shell, no package manager, no OS packages to patch; runs as uid 65532
# (the same "nonroot" uid distroless uses).
FROM scratch

LABEL org.opencontainers.image.title="nimshop" \
      org.opencontainers.image.description="Nimiq-powered gift card shop: Go API + static Astro frontend" \
      org.opencontainers.image.source="https://github.com/emrealt34/nimiqshop" \
      org.opencontainers.image.licenses="MIT"

WORKDIR /app
# Outbound TLS (supplier API, price feeds, RPC) needs the CA bundle.
COPY --from=backend /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=backend /out/nimshop-server /app/nimshop-server
COPY --from=backend /out/healthcheck /app/healthcheck
COPY --from=frontend /src/dist /app/frontend
# The data directory must already exist and belong to the runtime user,
# otherwise an anonymous volume is created root-owned and Badger cannot open.
COPY --from=backend --chown=65532:65532 /out/data /data
COPY --from=backend --chown=65532:65532 /out/tmp /tmp

ENV LISTEN_ADDR=:8084 \
    STATIC_DIR=/app/frontend \
    BADGER_DIR=/data/badger

VOLUME ["/data"]
EXPOSE 8084
USER 65532:65532

# Static probe binary (no shell/curl in distroless): GET /api/health.
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
  CMD ["/app/healthcheck"]

ENTRYPOINT ["/app/nimshop-server"]
