// FX refresher — keeps internal/catalog's USD rate table honest without
// anyone editing Go code.
//
// Design mirrors the NIM/BTC rate refresher in market_rates.go:
//   - the last good snapshot is loaded from the store at boot, so a restart
//     never falls back to the embedded baseline while the network is cold;
//   - the first fetch runs at boot (in the background), then every
//     FX_REFRESH_INTERVAL (default 6h — fiat moves slowly, and the feed
//     itself refreshes daily);
//   - a failed fetch KEEPS the previous snapshot (stale beats wrong), so a
//     flaky feed can never blank the table;
//   - the embedded table stays as the last-resort fallback.
//
// What this rate is used for: the admin's USD price cap and the pre-quote
// display estimate. Real orders are priced from the supplier's validated
// amount and the live oracle BTC rate, so an FX outage can never change what
// a buyer is charged.
package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/valyala/fasthttp"

	"nimiqshop/internal/catalog"
)

const fxSnapshotMetaKey = "fx_snapshot"

// fxPayloadJSON is the persisted snapshot shape (store meta).
type fxPayloadJSON struct {
	Rates  map[string]float64 `json:"rates"`
	Source string             `json:"source"`
	At     time.Time          `json:"at"`
}

// erAPIRates is the shape of open.er-api.com/v6/latest/USD (and the several
// free mirrors that speak the same dialect):
//
//	{ "result": "success", "base_code": "USD", "rates": { "TRY": 49.13, ... } }
type erAPIRates struct {
	Result   string             `json:"result"`
	BaseCode string             `json:"base_code"`
	Rates    map[string]float64 `json:"rates"`
}

var (
	fxOnce        sync.Once
	fxHTTPClient  = &http.Client{Timeout: 20 * time.Second}
	fxRefreshLock sync.Mutex // single-flight: a slow feed cannot stack fetches
)

// FXRefreshInterval returns the configured refresh cadence (default 6h).
func FXRefreshInterval() time.Duration {
	raw := strings.TrimSpace(fxEnv("FX_REFRESH_INTERVAL"))
	if raw == "" {
		return 6 * time.Hour
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d < time.Minute {
		log.Printf("fx: FX_REFRESH_INTERVAL=%q is not a usable duration; using 6h", raw)
		return 6 * time.Hour
	}
	return d
}

// FXRefresherDisabled reports whether the operator turned the refresher off
// (FX_REFRESH_DISABLED=true). With it off the embedded baseline is served,
// exactly like the pre-refresher code.
func FXRefresherDisabled() bool {
	switch strings.ToLower(strings.TrimSpace(fxEnv("FX_REFRESH_DISABLED"))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

func fxEnv(key string) string { return strings.TrimSpace(os.Getenv(key)) }

// ParseERRates extracts the rate map from an ER-API style body and converts
// it to this package's convention: USD per ONE unit of the currency.
//
// The feed quotes the opposite direction — "1 USD = 49.13 TRY" — so every
// rate is inverted here, ONCE, at the boundary. Getting this backwards is a
// silent 2400x error on TRY (it happened while writing this file and the
// local smoke test caught it: /api/market/fx served 49.13 for TRY).
//
// Exported for tests; a body that is not a successful USD-based payload with
// positive, finite rates is an error.
func ParseERRates(body []byte) (map[string]float64, error) {
	var payload erAPIRates
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("fx feed: %w", err)
	}
	// Some mirrors omit result/base_code; when present they must agree.
	if payload.Result != "" && !strings.EqualFold(payload.Result, "success") {
		return nil, fmt.Errorf("fx feed: result=%q", payload.Result)
	}
	if payload.BaseCode != "" && !strings.EqualFold(payload.BaseCode, "USD") {
		return nil, fmt.Errorf("fx feed: base_code=%q (need USD)", payload.BaseCode)
	}
	if len(payload.Rates) == 0 {
		return nil, fmt.Errorf("fx feed: no rates in payload")
	}
	// units-per-USD -> USD-per-unit. A non-positive or non-finite quote
	// cannot be inverted; drop it rather than emit an Inf/NaN into the table.
	out := make(map[string]float64, len(payload.Rates))
	for code, perUSD := range payload.Rates {
		if perUSD <= 0 || math.IsNaN(perUSD) || math.IsInf(perUSD, 0) {
			continue
		}
		out[strings.ToUpper(strings.TrimSpace(code))] = 1 / perUSD
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("fx feed: every rate was non-positive")
	}
	return out, nil
}

// StartFXRefresher launches the background FX refresher (idempotent). Called
// from main at boot.
func (h *Handlers) StartFXRefresher(stop context.Context) {
	if FXRefresherDisabled() {
		log.Printf("fx: refresher disabled (FX_REFRESH_DISABLED); serving the embedded baseline table")
		return
	}
	fxOnce.Do(func() {
		go func() {
			// Warm restart: serve the last persisted snapshot immediately.
			if h.Store != nil {
				if raw, err := h.Store.LoadMeta(fxSnapshotMetaKey); err == nil && len(raw) > 0 {
					var p fxPayloadJSON
					if err := json.Unmarshal(raw, &p); err == nil && len(p.Rates) > 0 {
						if n := catalog.SetLiveFX(p.Rates, p.Source, p.At); n > 0 {
							log.Printf("fx: restored %d live rates from disk (observed %s)", n, p.At.Format(time.RFC3339))
						}
					}
				}
			}
			h.refreshFXNow()
			interval := FXRefreshInterval()
			log.Printf("fx: refresher armed (every %s, source %s)", interval, fxRatesURL())
			ticker := time.NewTicker(interval)
			defer ticker.Stop()
			for {
				select {
				case <-stop.Done():
					return
				case <-ticker.C:
					h.refreshFXNow()
				}
			}
		}()
	})
}

// fxRatesURL resolves the feed URL (FX_RATES_URL overrides the default).
func fxRatesURL() string {
	if u := fxEnv("FX_RATES_URL"); u != "" {
		return u
	}
	return "https://open.er-api.com/v6/latest/USD"
}

// FXRefreshNow forces an immediate refresh and reports how many currencies
// were accepted (0 = refused/failed, previous snapshot kept). Used by the
// boot path, the ticker, and the admin endpoint.
func (h *Handlers) FXRefreshNow() (int, time.Time, error) {
	fxRefreshLock.Lock()
	defer fxRefreshLock.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fxRatesURL(), nil)
	if err != nil {
		return 0, time.Time{}, err
	}
	req.Header.Set("User-Agent", "nimshop-fx/1.0 (+https://shop.nimiqbase.com)")
	resp, err := fxHTTPClient.Do(req)
	if err != nil {
		return 0, time.Time{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, time.Time{}, fmt.Errorf("fx feed: HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return 0, time.Time{}, err
	}
	rates, err := ParseERRates(body)
	if err != nil {
		return 0, time.Time{}, err
	}
	at := time.Now().UTC()
	// The feed's own observation time is preferable to ours when present.
	if t, ok := erAPIObservedAt(body); ok {
		at = t
	}
	n := catalog.SetLiveFX(rates, "open.er-api.com", at)
	if n == 0 {
		return 0, time.Time{}, fmt.Errorf("fx feed: payload rejected by sanity checks (%d currencies)", len(rates))
	}
	if h.Store != nil {
		if b, err := json.Marshal(fxPayloadJSON{Rates: rates, Source: "open.er-api.com", At: at}); err == nil {
			_ = h.Store.SaveMeta(fxSnapshotMetaKey, b, 30*24*time.Hour)
		}
	}
	return n, at, nil
}

// refreshFXNow is the logging wrapper used by the boot path and the ticker.
func (h *Handlers) refreshFXNow() {
	n, at, err := h.FXRefreshNow()
	if err != nil {
		liveAt, src, count := catalog.LiveFXStatus()
		if liveAt.IsZero() {
			log.Printf("fx: refresh failed (%v); serving the embedded baseline table", err)
		} else {
			log.Printf("fx: refresh failed (%v); keeping %d rates from %s (%s)", err, count, src, liveAt.Format(time.RFC3339))
		}
		return
	}
	log.Printf("fx: refreshed %d live rates (observed %s)", n, at.Format(time.RFC3339))
}

// erAPIObservedAt pulls the feed's own timestamp when it provides one; the
// ER-API family reports it as a unix seconds value.
func erAPIObservedAt(body []byte) (time.Time, bool) {
	var probe struct {
		TimeLastUpdateUnix *int64 `json:"time_last_update_unix"`
	}
	if err := json.Unmarshal(body, &probe); err != nil || probe.TimeLastUpdateUnix == nil {
		return time.Time{}, false
	}
	u := *probe.TimeLastUpdateUnix
	if u <= 0 {
		return time.Time{}, false
	}
	t := time.Unix(u, 0).UTC()
	// Reject obviously future stamps (clock skew / broken feed).
	if t.After(time.Now().UTC().Add(24 * time.Hour)) {
		return time.Time{}, false
	}
	return t, true
}

// AdminRefreshFX forces an immediate FX refresh from the admin console and
// reports what happened — the operator's "did the rates update?" button.
// GET /api/market/fx already exposes the same state publicly; this only adds
// the on-demand trigger plus the raw feed error for debugging.
func (h *Handlers) AdminRefreshFX(ctx *fasthttp.RequestCtx) {
	n, at, err := h.FXRefreshNow()
	resp := map[string]any{"ok": err == nil}
	if err != nil {
		resp["error"] = err.Error()
		liveAt, source, count := catalog.LiveFXStatus()
		resp["live_source"] = source
		resp["live_currencies"] = count
		if !liveAt.IsZero() {
			resp["live_observed_at"] = liveAt.UTC().Format(time.RFC3339)
			resp["live_age_seconds"] = int(time.Since(liveAt).Seconds())
		}
		writeJSON(ctx, fasthttp.StatusBadGateway, resp)
		return
	}
	resp["currencies"] = n
	resp["observed_at"] = at.UTC().Format(time.RFC3339)
	resp["source"] = fxRatesURL()
	if identity := adminIdentity(ctx); identity.User.ID != "" {
		h.audit(identity.User.ID, "admin.settings.fx_refreshed", ctx,
			fmt.Sprintf("FX refreshed from %s: %d currencies", fxRatesURL(), n))
	}
	writeJSON(ctx, fasthttp.StatusOK, resp)
}
