package handlers

import (
	"context"
	"time"

	"github.com/valyala/fasthttp"

	"nimiqshop/internal/catalog"
)

// NIMRate serves the ALWAYS-WARM rate snapshot maintained by the 60s
// background refresher (market_rates.go). It NEVER waits for an oracle
// round trip: cold boot → 503 "warming up" while the first fetch runs;
// every other request is instant from memory.
func (h *Handlers) NIMRate(ctx *fasthttp.RequestCtx) {
	h.StartRatesRefresher(context.Background())
	snap := currentRates()
	if !snap.fetched || snap.nimUSD <= 0 {
		// Cold boot (first seconds of the process): kick a refresh and
		// answer immediately — never block the caller on the oracle.
		go h.refreshRatesNow()
		ctx.Response.Header.Set("Retry-After", "2")
		writeError(ctx, fasthttp.StatusServiceUnavailable, "price feed is warming up — retry in a few seconds")
		return
	}
	resp := map[string]interface{}{
		"usd_per_nim": snap.nimUSD,
		"observed_at": snap.nimAt,
		"sources":     snap.nimSources,
		"cached":      true,
		"age_seconds": int(time.Since(snap.updatedAt).Seconds()),
	}
	if snap.btcUSD > 0 {
		resp["usd_per_btc"] = snap.btcUSD
		resp["btc_observed_at"] = snap.btcAt
		resp["btc_sources"] = snap.btcSources
	}
	writeJSON(ctx, fasthttp.StatusOK, resp)
}

func (h *Handlers) FXRates(ctx *fasthttp.RequestCtx) {
	ctx.Response.Header.Set("Cache-Control", "public, max-age=3600")
	writeJSON(ctx, fasthttp.StatusOK, map[string]interface{}{
		"base":         "USD",
		"usd_per_unit": catalog.FXTable(),
	})
}
