package handlers

import (
	"github.com/valyala/fasthttp"

	"nimiqshop/internal/integrity"
)

// Integrity exposes the backend's provenance at GET /api/integrity (public):
// the running binary's SHA-256 plus the source manifest embedded at build time.
// This is the backend half of the hash-verification story; see
// tools/integrity/README.md.
func (h *Handlers) Integrity(ctx *fasthttp.RequestCtx) {
	writeJSON(ctx, fasthttp.StatusOK, integrity.Report())
}
