package handlers

import (
	"github.com/valyala/fasthttp"

	"nimiqshop/internal/clientip"
)

// GeoInfo resolves the caller's IP for the frontend without any external call.
//
// Country comes free from Cloudflare's CF-IPCountry header when Cloudflare is
// in front (TRUST_PROXY on) — in BOTH proxy modes: the edge sets it (and the
// edge trace that vouches for it), and the deployment's own hop deletes any
// client-supplied copy before forwarding, so a visitor cannot pick a country.
// In direct mode (or behind a generic proxy that carries no edge trace) country
// is empty so the shop falls back to a global catalog. The frontend only ever
// talks to this same-origin endpoint — there is no third-party geo API call.
func (h *Handlers) GeoInfo(ctx *fasthttp.RequestCtx) {
	info := clientip.Resolve(ctx, h.Cfg.TrustProxy, h.Cfg.ClientIPPolicy())
	writeJSON(ctx, fasthttp.StatusOK, map[string]interface{}{
		"ip":         info.IP,
		"country":    info.Country,
		"cloudflare": info.Cloudflare,
	})
}
