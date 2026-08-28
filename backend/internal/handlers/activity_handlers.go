package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/valyala/fasthttp"

	"nimiqshop/internal/db"
	"nimiqshop/internal/middleware"
)

/* activity_handlers.go — public, fully-transparent payment feed + star ratings.
 *
 * Trust posture: this surface is intentionally PUBLIC (no auth). It only ever
 * discloses data that is meant to be public — the buyer's address (the wallet
 * that paid is public on-chain anyway), what was bought, how much was paid, and
 * the buyer's star rating. It never exposes redemption codes, balances, order
 * internals, support threads, or any other user's private data.
 */

// queryLimit parses ?limit= from the query string, clamped to [1, 200], default 50.
func queryLimit(ctx *fasthttp.RequestCtx) int {
	n := 50
	if v := ctx.QueryArgs().Peek("limit"); len(v) > 0 {
		if parsed, err := strconv.Atoi(string(v)); err == nil {
			n = parsed
		}
	}
	if n < 1 {
		n = 50
	}
	if n > 200 {
		n = 200
	}
	return n
}

func formatNIM(f float64) string { return strconv.FormatFloat(f, 'f', 5, 64) }

// formatLocal renders the buyer's face value in a sensible locale form.
// CryptoRefills returns denoms like "TRY300" or "100 USD" or simply "25".
// We strip the numeric prefix and let the activity page show e.g. "300 TRY".
func formatLocal(v float64) string {
	if v <= 0 {
		return ""
	}
	// Whole-number values: no decimal. Half-integer values: one decimal.
	if v == float64(int64(v)) {
		return strconv.FormatInt(int64(v), 10)
	}
	return strconv.FormatFloat(v, 'f', 2, 64)
}

// localCurrency extracts a 3-letter currency code from the supplier's
// denomination label ("100 USD" → "USD") or falls back to a sensible default
// for the buyer's country. Country fallback is conservative: TR→TRY, GB→GBP,
// DE→EUR, US→USD, default→USD.
func localCurrency(denom, country string) string {
	d := strings.TrimSpace(denom)
	if d != "" {
		// Look for an ISO-4217 style 3-letter code anywhere in the label.
		upper := strings.ToUpper(d)
		// Try the common "25 TRY", "100 USD", "TRY25", "USD100" patterns.
		for _, code := range []string{
			"USD", "EUR", "GBP", "TRY", "JPY", "CNY", "CAD", "AUD",
			"CHF", "INR", "BRL", "MXN", "ARS", "ZAR", "KRW", "RUB",
			"PLN", "SEK", "NOK", "DKK", "CZK", "HUF", "AED", "SAR",
			"NZD", "SGD", "HKD", "TWD", "THB", "MYR", "IDR", "PHP",
			"EGP", "PKR", "VND", "NGN", "KES", "GHS", "MAD", "TND",
		} {
			if strings.Contains(upper, code) {
				return code
			}
		}
	}
	c := strings.ToUpper(strings.TrimSpace(country))
	switch c {
	case "TR":
		return "TRY"
	case "GB", "UK":
		return "GBP"
	case "DE", "FR", "IT", "ES", "NL", "BE", "AT", "PT", "IE", "FI", "GR":
		return "EUR"
	case "JP":
		return "JPY"
	case "CN":
		return "CNY"
	case "CA":
		return "CAD"
	case "AU", "NZ":
		return "AUD"
	case "CH":
		return "CHF"
	case "IN":
		return "INR"
	case "BR":
		return "BRL"
	case "MX":
		return "MXN"
	}
	return "USD"
}

// ratingSummaryShape renders the global aggregate as a public-facing object.
func ratingSummaryShape(a db.RatingAggregate) map[string]interface{} {
	dist := map[string]int{}
	for i := 1; i <= 5; i++ {
		dist[strconv.Itoa(i)] = a.Dist[i]
	}
	avg := 0.0
	if a.Count > 0 {
		avg = math.Round(a.Average()*100) / 100
	}
	return map[string]interface{}{
		"count":   a.Count,
		"average": avg,
		"sum":     a.Sum,
		"dist":    dist,
	}
}

func payloadField(payload json.RawMessage, field string) string {
	if len(payload) == 0 {
		return ""
	}
	var p map[string]interface{}
	if err := json.Unmarshal(payload, &p); err != nil {
		return ""
	}
	if s, ok := p[field].(string); ok {
		return s
	}
	return ""
}

// ListActivity returns the newest public payments (delivered purchases and
// fulfilled direct-NIM payments), merged newest-first, plus the global rating
// summary. Wallet deposits are intentionally absent: nim.shop is non-custodial
// — there is no balance and no top-up, so the only payments that exist are
// purchases. Each entry carries its live status so the feed shows the stage.
func (h *Handlers) ListActivity(ctx *fasthttp.RequestCtx) {
	limit := queryLimit(ctx)

	orders, _ := h.Store.ListFeedOrders(limit)
	quotes, _ := h.Store.ListFeedQuotes(limit)

	// Resolve every buyer address in a single read transaction.
	idSet := map[string]struct{}{}
	for _, o := range orders {
		idSet[o.UserID] = struct{}{}
	}
	for _, q := range quotes {
		idSet[q.UserID] = struct{}{}
	}
	ids := make([]string, 0, len(idSet))
	for id := range idSet {
		ids = append(ids, id)
	}
	addrs, _ := h.Store.UserAddresses(ids)

	type timed struct {
		t time.Time
		e map[string]interface{}
	}
	var all []timed

	for _, o := range orders {
		title := payloadField(o.Payload, "product_name")
		if title == "" {
			title = "Purchase"
		}
		e := map[string]interface{}{
			"type":     "purchase",
			"id":       o.ID,
			"address":  addrs[o.UserID],
			"time":     o.UpdatedAt,
			"status":   o.Status,
			"kind":     o.Kind,
			"title":    title,
			"country":  payloadField(o.Payload, "country"),
			"quantity": o.Quantity,
			"usd":      o.PriceUSD.String(),
		}
		if o.NimUsdRate > 0 {
			// NIM equivalent at the rate snapshotted at purchase time.
			e["nim"] = formatNIM((float64(o.PriceUSD) / 1_000_000) / o.NimUsdRate)
		}
		r := o.Rating
		e["rating"] = &r
		if o.RatedAt != nil {
			e["rated_at"] = *o.RatedAt
		}
		all = append(all, timed{o.UpdatedAt, e})
	}

	for _, q := range quotes {
		e := map[string]interface{}{
			"type":   "cryptorefills_purchase",
			"id":     q.ID,
			"time":   q.UpdatedAt,
			"status": q.Status,
			"title":  q.ProductID + " " + q.Denomination,
			"usd":    q.ProductUSD.String(),
			// The buyer's LOCAL face value (TRY 300, USD 100 etc.) — what they
			// actually saw on the product page in their own currency. The activity
			// feed renders this prominently so a viewer in Istanbul sees TRY
			// amounts, not USD. We still keep `usd` for the global average and
			// for any viewer who has not picked a country.
			"country":        q.ProductCountry,
			"local_amount":   formatLocal(q.ProductValue),
			"local_currency": localCurrency(q.Denomination, q.ProductCountry),
		}
		if q.CoinAmount != "" && q.Coin != "" {
			e["paid"] = q.CoinAmount + " " + q.Coin
			if q.Network != "" {
				e["network"] = q.Network
			}
		}
		r := q.Rating
		e["rating"] = &r
		if q.RatedAt != nil {
			e["rated_at"] = *q.RatedAt
		}
		all = append(all, timed{q.UpdatedAt, e})
	}

	sort.SliceStable(all, func(i, j int) bool { return all[i].t.After(all[j].t) })
	if len(all) > limit {
		all = all[:limit]
	}

	out := make([]map[string]interface{}, 0, len(all))
	for _, t := range all {
		out = append(out, t.e)
	}

	agg, _ := h.Store.GetRatingAggregate()
	summary := ratingSummaryShape(agg)

	// Public average delivery time across delivered purchases (seconds).
	var durSum int64
	var durN int
	for _, o := range orders {
		if o.Status == "delivered" || o.Status == "complete" || o.Status == "fulfilled" {
			durN++
			durSum += int64(o.UpdatedAt.Sub(o.CreatedAt).Seconds())
		}
	}
	if durN > 0 {
		summary["avg_delivery_seconds"] = durSum / int64(durN)
		summary["delivered_count"] = durN
	}
	if h.Presence != nil {
		summary["active_users"] = h.Presence.ActiveCount()
	}

	writeJSON(ctx, fasthttp.StatusOK, map[string]interface{}{
		"items":   out,
		"summary": summary,
	})
}

// RatingSummary returns just the global star-rating aggregate (public).
func (h *Handlers) RatingSummary(ctx *fasthttp.RequestCtx) {
	agg, _ := h.Store.GetRatingAggregate()
	summary := ratingSummaryShape(agg)
	if h.Presence != nil {
		summary["active_users"] = h.Presence.ActiveCount()
	}
	writeJSON(ctx, fasthttp.StatusOK, summary)
}

// PresenceHeartbeat is a public endpoint that registers a visitor so the live
// "X users shopping now" counter stays current. The visitor supplies a stable
// pseudonymous id (its wallet address when signed in).
func (h *Handlers) PresenceHeartbeat(ctx *fasthttp.RequestCtx) {
	if h.Presence != nil {
		var req struct {
			ID string `json:"id"`
		}
		_ = readJSON(ctx, &req)
		h.Presence.Ping(req.ID)
	}
	writeJSON(ctx, fasthttp.StatusOK, map[string]interface{}{"ok": true})
}

type rateOrderRequest struct {
	Rating int `json:"rating"`
}

// RateOrder lets the authenticated owner of a DELIVERED order record a 1-5 star
// rating. It is idempotent and returns the updated global summary so the UI can
// refresh the aggregate in one round trip.
func (h *Handlers) RateOrder(ctx *fasthttp.RequestCtx) {
	orderID, _ := ctx.UserValue("id").(string)
	userID := middleware.UserID(ctx)

	var req rateOrderRequest
	if err := readJSON(ctx, &req); err != nil || req.Rating < 1 || req.Rating > 5 {
		writeError(ctx, fasthttp.StatusBadRequest, "rating must be an integer from 1 to 5")
		return
	}

	o, agg, err := h.Store.SetOrderRating(orderID, userID, req.Rating)
	if errors.Is(err, db.ErrNotFound) {
		writeError(ctx, fasthttp.StatusNotFound, "order not found")
		return
	}
	if errors.Is(err, db.ErrConflict) {
		writeError(ctx, fasthttp.StatusConflict, "this order cannot be rated yet (delivery must complete)")
		return
	}
	if err != nil {
		writeError(ctx, fasthttp.StatusInternalServerError, "could not save rating")
		return
	}

	writeJSON(ctx, fasthttp.StatusOK, map[string]interface{}{
		"order_id": o.ID,
		"rating":   o.Rating,
		"rated_at": o.RatedAt,
		"summary":  ratingSummaryShape(agg),
	})
}

type rateQuoteRequest struct {
	Rating int `json:"rating"`
}

// RateQuote lets the authenticated owner of a FULFILLED direct-NIM purchase
// record a 1-5 star rating. Mirrors RateOrder; returns the updated summary.
func (h *Handlers) RateQuote(ctx *fasthttp.RequestCtx) {
	quoteID, _ := ctx.UserValue("id").(string)
	userID := middleware.UserID(ctx)

	var req rateQuoteRequest
	if err := readJSON(ctx, &req); err != nil || req.Rating < 1 || req.Rating > 5 {
		writeError(ctx, fasthttp.StatusBadRequest, "rating must be an integer from 1 to 5")
		return
	}

	q, agg, err := h.Store.SetQuoteRating(quoteID, userID, req.Rating)
	if errors.Is(err, db.ErrNotFound) {
		writeError(ctx, fasthttp.StatusNotFound, "order not found")
		return
	}
	if errors.Is(err, db.ErrConflict) {
		writeError(ctx, fasthttp.StatusConflict, "this order cannot be rated yet (delivery must complete)")
		return
	}
	if err != nil {
		writeError(ctx, fasthttp.StatusInternalServerError, "could not save rating")
		return
	}

	writeJSON(ctx, fasthttp.StatusOK, map[string]interface{}{
		"order_id": q.ID,
		"rating":   q.Rating,
		"rated_at": q.RatedAt,
		"summary":  ratingSummaryShape(agg),
	})
}

// GetAccountLimits returns the signed-in user's daily order/spend usage and the
// rolling-window reset time, so the profile page can show "X/3 orders, $Y/$50,
// resets in 2h". Authed.
func (h *Handlers) GetAccountLimits(ctx *fasthttp.RequestCtx) {
	userID := middleware.UserID(ctx)
	usage, _ := h.Store.GetUserDailyUsage(userID, 24*time.Hour)
	// The rolling window resets when the OLDEST purchase in it turns 24h
	// old. With nothing purchased, the reset was computed as time.Now(),
	// which the profile rendered as "Resets in now" — report a full 24h
	// window instead so the countdown always reads sensibly.
	resetsAt := time.Now().Add(24 * time.Hour)
	if !usage.OldestAt.IsZero() {
		resetsAt = usage.OldestAt.Add(24 * time.Hour)
	}
	out := map[string]interface{}{
		"used_orders":    usage.OrderCount,
		"max_orders":     h.Cfg.DailyOrderLimit,
		"used_usd":       usage.SpendUSD.String(),
		"max_usd":        h.Cfg.DailySpendLimitUSD,
		"window_seconds": 86400,
		"resets_at":      resetsAt,
		"server_now":     time.Now().UTC(),
	}
	if q, err := h.Oracle.NIMUSD(context.Background()); err == nil && q.MedianUSD > 0 {
		used := float64(usage.SpendUSD) / 1000000 / q.MedianUSD
		max := h.Cfg.DailySpendLimitUSD / q.MedianUSD
		out["used_nim"] = formatNIM(used)
		out["max_nim"] = formatNIM(max)
	}
	writeJSON(ctx, fasthttp.StatusOK, out)
}

/* ---------------- Public live tracking ---------------- */

// TrackStatus is a PUBLIC (no-auth) live view of any order's stage. Anyone with
// an order id can see WHERE a purchase is in its lifecycle — that transparency
// is the anti-fraud proof. What they can NEVER see here is the delivery itself
// (redemption codes / PINs / claim links): those stay owner-only, served by the
// authenticated GetOrder / GetUserQuote endpoints. So the public can verify the
// shop is fulfilling orders in real time without being able to steal goods.
func (h *Handlers) TrackStatus(ctx *fasthttp.RequestCtx) {
	id, _ := ctx.UserValue("id").(string)
	if id == "" {
		writeError(ctx, fasthttp.StatusBadRequest, "order id is required")
		return
	}

	// Try a purchase order first, then a direct quote.
	if o, err := h.Store.GetOrder(id); err == nil {
		stages, current := buildOrderStages(o)
		title := payloadField(o.Payload, "product_name")
		if title == "" {
			title = "Purchase"
		}
		resp := map[string]interface{}{
			"id":            o.ID,
			"type":          "purchase",
			"status":        o.Status,
			"kind":          o.Kind,
			"title":         title,
			"country":       payloadField(o.Payload, "country"),
			"quantity":      o.Quantity,
			"usd":           o.PriceUSD.String(),
			"created_at":    o.CreatedAt,
			"updated_at":    o.UpdatedAt,
			"stages":        stages,
			"current_stage": current,
		}
		if o.NimUsdRate > 0 {
			resp["nim"] = formatNIM((float64(o.PriceUSD) / 1_000_000) / o.NimUsdRate)
		}
		writeJSON(ctx, fasthttp.StatusOK, resp)
		return
	}

	if q, err := h.Store.GetQuote(id); err == nil {
		// Everything is public here EXCEPT the delivery codes (owner-only):
		// the live status timeline plus the supplier order id (an opaque
		// reference — not a payment hash, since stablecoin payers are
		// private by design on the supplier side).
		payment := []map[string]interface{}{}
		if q.SupplierOrderID != "" {
			payment = append(payment, map[string]interface{}{"label": "Supplier order", "network": "Cryptorefills", "hash": q.SupplierOrderID})
		}
		if q.CoinAmount != "" && q.Coin != "" {
			payment = append(payment, map[string]interface{}{"label": "Payment", "network": q.Network, "hash": q.CoinAmount + " " + q.Coin})
		}
		resp := map[string]interface{}{
			"id":           q.ID,
			"type":         "cryptorefills_purchase",
			"status":       q.Status,
			"title":        q.ProductID + " " + q.Denomination,
			"usd":          q.ProductUSD.String(),
			"created_at":   q.CreatedAt,
			"updated_at":   q.UpdatedAt,
			"stages":       quoteStages(q),
			"transactions": payment,
		}
		writeJSON(ctx, fasthttp.StatusOK, resp)
		return
	}

	writeError(ctx, fasthttp.StatusNotFound, "order not found")
}

// quoteStages renders the public lifecycle of a Cryptorefills purchase from
// its status, mirroring the order timeline so /track looks consistent.
func quoteStages(q db.Quote) []map[string]interface{} {
	st := q.Status
	created := q.CreatedAt.Format(time.RFC3339)
	updated := q.UpdatedAt.Format(time.RFC3339)
	mk := func(id, title, desc, status string) map[string]interface{} {
		return map[string]interface{}{"id": id, "title": title, "description": desc, "status": status}
	}
	s1 := mk("order_placed", "Order placed", "The purchase was created.", "completed")
	s1["timestamp"] = created
	s2 := mk("crypto_payment", "Stablecoin payment", "Waiting for the wallet payment to be confirmed on-chain.", "pending")
	s3 := mk("supplier_delivery", "Supplier delivery", "Cryptorefills is preparing the delivery.", "pending")
	s4 := mk("delivery", "Delivery", "The code / redemption details are delivered to the buyer.", "pending")
	switch {
	case st == "order_creating":
		s2["status"] = "in_progress"
	case st == "awaiting_payment":
		s2["status"] = "in_progress"
		s2["timestamp"] = updated
	case st == "payment_started":
		s2["status"] = "in_progress"
		s2["description"] = "Payment broadcast — waiting for on-chain confirmation."
		s2["timestamp"] = updated
	case st == "payment_received" || st == "delivering":
		s2["status"] = "completed"
		s2["timestamp"] = updated
		s3["status"] = "in_progress"
		s3["timestamp"] = updated
	case st == "fulfilled":
		s2["status"] = "completed"
		s2["timestamp"] = updated
		s3["status"] = "completed"
		s3["timestamp"] = updated
		s4["status"] = "completed"
		s4["timestamp"] = updated
	case st == "expired":
		s2["status"] = "failed"
		s2["description"] = "Payment window elapsed."
		s2["timestamp"] = updated
		s3["status"] = "failed"
		s4["status"] = "failed"
	case st == "failed" || st == "refunded" || st == "manual_review":
		s2["status"] = "failed"
		s2["timestamp"] = updated
		s3["status"] = "failed"
		s3["timestamp"] = updated
		s4["status"] = "failed"
	}
	return []map[string]interface{}{s1, s2, s3, s4}
}
