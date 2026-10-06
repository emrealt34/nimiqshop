package handlers

import (
	"github.com/valyala/fasthttp"

	"nimiqshop/internal/clientip"
)

// GeoInfo resolves the caller's IP for the frontend without any external call.
//
// Country comes free from Cloudflare's CF-IPCountry header when the hop carries
// it (TRUST_PROXY on, with the edge trace vouching for it) — or from the edge's
// own restatement of it on X-Nimshop-Client-Country. In direct mode, and behind
// a proxy that strips the edge's attribution altogether (which is what this
// deployment's path does — CF-* never arrive), country is empty so the shop
// falls back to a global catalog.
//
// country_hint is reported separately and is NOT part of that decision: it is
// the country the visitor's browser read from the same-origin edge trace and
// forwarded (see src/lib/edgeGeo.ts). /api/geo keeps the two apart on purpose —
// the hint is display metadata for the operator console, and nothing that
// depends on the visitor's address may read it.
func (h *Handlers) GeoInfo(ctx *fasthttp.RequestCtx) {
	info := clientip.Resolve(ctx, h.Cfg.TrustProxy, h.Cfg.ClientIPPolicy())
	writeJSON(ctx, fasthttp.StatusOK, map[string]interface{}{
		"ip":           info.IP,
		"country":      info.Country,
		"cloudflare":   info.Cloudflare,
		"country_hint": clientip.CountryHint(ctx),
	})
}
