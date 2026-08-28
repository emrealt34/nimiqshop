package handlers

import (
	"github.com/valyala/fasthttp"

	"nimiqshop/internal/clientip"
)

// GeoInfo resolves the caller's IP for the frontend without any external call.
//
// Country comes free from Cloudflare's CF-IPCountry header when Cloudflare is
// in front (TRUST_PROXY on). In direct mode (or non-Cloudflare proxy) country
// is empty so the shop falls back to a global catalog. The frontend only ever
// talks to this same-origin endpoint — there is no third-party geo API call.
func (h *Handlers) GeoInfo(ctx *fasthttp.RequestCtx) {
	info := clientip.Resolve(ctx, h.Cfg.TrustProxy)
	writeJSON(ctx, fasthttp.StatusOK, map[string]interface{}{
		"ip":         info.IP,
		"country":    info.Country,
		"cloudflare": info.Cloudflare,
	})
}
