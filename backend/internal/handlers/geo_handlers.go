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
// country_hint and ip_hint are reported separately and are NOT part of that
// decision: they are what the edge stated about this request (the visitor's own
// address on X-Nimshop-Client-IP, the country on the hop's restatement or, as a
// last resort, the value the browser read from the same-origin edge trace — see
// src/lib/edgeGeo.ts). /api/geo keeps them apart on purpose: they are display
// metadata for the operator console, and nothing that depends on the visitor's
// address — rate limits, the supplier payload, audit lines — may read them.
func (h *Handlers) GeoInfo(ctx *fasthttp.RequestCtx) {
	info := clientip.Resolve(ctx, h.Cfg.TrustProxy, h.Cfg.ClientIPPolicy())
	writeJSON(ctx, fasthttp.StatusOK, map[string]interface{}{
		"ip":           info.IP,
		"country":      info.Country,
		"cloudflare":   info.Cloudflare,
		"country_hint": clientip.CountryHint(ctx),
		"ip_hint":      clientip.ClientIPHint(ctx),
	})
}
