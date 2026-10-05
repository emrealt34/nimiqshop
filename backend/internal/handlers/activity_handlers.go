package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/valyala/fasthttp"

	"nimiqshop/internal/cashback"
	"nimiqshop/internal/clientip"
	"nimiqshop/internal/db"
	"nimiqshop/internal/middleware"
)

/* activity_handlers.go — public, fully-transparent payment feed + star ratings.
 *
 * Trust posture: this surface is intentionally PUBLIC (no auth). It discloses
 * what was bought, how much was paid, and the buyer's star rating. Named rows
 * may show the paying wallet address; anonymous rows stay in the feed but omit
 * the buyer identity and payment transaction details. It never exposes
 * redemption codes, balances, order internals, support threads, or any other
 * user's private data.
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

var (
	markdownLinkOnlyRE = regexp.MustCompile(`^\*{0,2}\[([^\]]+)\]\([^)]+\)\*{0,2}$`)
	markdownLinkRE     = regexp.MustCompile(`\[[^\]]+\]\([^)]+\)`)
	trailingValueRE    = regexp.MustCompile(`\s*\([^()]*\)\s*$`)
	domainSuffixRE     = regexp.MustCompile(`(?i)(\.com|\.co|\.org|\.net)(\.[a-z]{2})?$`)
	trailingMoneyRE    = regexp.MustCompile(`(?i)\s+([0-9]+([.,][0-9]+)?\s*(USD|EUR|GBP|TRY|JPY|CNY|CAD|AUD|CHF|INR|BRL|MXN|PLN|SEK|NOK|DKK|AED|SAR)|(USD|EUR|GBP|TRY|JPY|CNY|CAD|AUD|CHF|INR|BRL|MXN|PLN|SEK|NOK|DKK|AED|SAR)\s*[0-9]+([.,][0-9]+)?)\s*$`)
)

// cleanPublicProductLabel keeps public titles human-readable without exposing
// supplier Markdown, denominations, quantities, or country-domain decoration.
func cleanPublicProductLabel(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	if match := markdownLinkOnlyRE.FindStringSubmatch(s); len(match) > 1 {
		s = match[1]
	} else {
		s = markdownLinkRE.ReplaceAllString(s, "")
	}
	s = strings.ReplaceAll(s, "**", "")
	s = trailingValueRE.ReplaceAllString(s, "")
	s = trailingMoneyRE.ReplaceAllString(s, "")
	s = strings.Join(strings.Fields(s), " ")
	s = domainSuffixRE.ReplaceAllString(s, "")
	return strings.TrimSpace(s)
}

func cleanPublicTitleParts(raw string) []string {
	parts := make([]string, 0, 3)
	for _, part := range strings.Split(raw, "+") {
		if title := cleanPublicProductLabel(part); title != "" {
			parts = append(parts, title)
		}
	}
	return parts
}

// quoteBatchTitles exposes only the public product family names needed by the
// activity thumbnail stack. Delivery targets and the rest of QuoteLine stay
// private; only ProductID/Country are safe for this public summary.
func quoteBatchTitles(q db.Quote) []string {
	fromLines := make([]string, 0, len(q.Lines))
	for _, line := range q.Lines {
		fromLines = append(fromLines, cleanPublicTitleParts(line.ProductID)...)
	}
	if len(fromLines) > 1 {
		if len(fromLines) > 3 {
			return fromLines[:3]
		}
		return fromLines
	}

	// Older persisted batch quotes may not have Lines yet. Their ProductID is
	// still stored as a joined family list, sometimes with repeated malformed
	// denomination fragments. Keep the first occurrence of each clean brand.
	if q.IsBatch || q.BatchItems > 1 {
		out := make([]string, 0, 3)
		seen := map[string]bool{}
		for _, title := range cleanPublicTitleParts(q.ProductID) {
			key := strings.ToLower(title)
			if !seen[key] {
				seen[key] = true
				out = append(out, title)
			}
		}
		if len(out) > 3 {
			return out[:3]
		}
		return out
	}
	return fromLines
}

// ListActivity returns the newest public payments (delivered purchases and
// fulfilled direct-NIM payments), merged newest-first, plus the global rating
// summary. Wallet deposits are intentionally absent: shop.nimiqbase.com is non-custodial
// — there is no balance and no top-up, so the only payments that exist are
// purchases. Each entry carries its live status so the feed shows the stage.
func (h *Handlers) ListActivity(ctx *fasthttp.RequestCtx) {
	limit := queryLimit(ctx)

	orders, _ := h.Store.ListFeedOrders(limit)
	quotes, _ := h.Store.ListFeedQuotes(limit)

	// Resolve named buyer addresses in a single read transaction. Anonymous
	// purchases still appear in the feed, but their wallet identity is never
	// looked up or emitted.
	idSet := map[string]struct{}{}
	for _, o := range orders {
		if !o.Anonymous {
			idSet[o.UserID] = struct{}{}
		}
	}
	for _, q := range quotes {
		if !q.Anonymous {
			idSet[q.UserID] = struct{}{}
		}
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
		title := cleanPublicProductLabel(payloadField(o.Payload, "product_name"))
		if title == "" {
			title = cleanPublicProductLabel(o.ProductID)
		}
		if title == "" {
			title = "Purchase"
		}
		e := map[string]interface{}{
			"type":      "purchase",
			"id":        o.ID,
			"anonymous": o.Anonymous,
			"time":      o.UpdatedAt,
			"status":    o.Status,
			"kind":      o.Kind,
			"title":     title,
			"country":   payloadField(o.Payload, "country"),
			"quantity":  o.Quantity,
			"usd":       o.PriceUSD.String(),
		}
		if !o.Anonymous {
			e["address"] = addrs[o.UserID]
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
		batchTitles := quoteBatchTitles(q)
		isBatch := q.IsBatch || q.BatchItems > 1 || len(batchTitles) > 1
		quoteCountry := q.ProductCountry
		if quoteCountry == "" && len(q.Lines) > 0 {
			quoteCountry = q.Lines[0].Country
		}
		title := cleanPublicProductLabel(q.ProductID)
		if title == "" {
			title = "Purchase"
		}
		if isBatch && len(batchTitles) > 0 {
			title = strings.Join(batchTitles, " + ")
		}
		e := map[string]interface{}{
			"type":      "cryptorefills_purchase",
			"id":        q.ID,
			"anonymous": q.Anonymous,
			"time":      q.UpdatedAt,
			"status":    q.Status,
			"title":     title,
			"usd":       q.ProductUSD.String(),
			// The buyer's LOCAL face value (TRY 300, USD 100 etc.) — what they
			// actually saw on the product page in their own currency. The activity
			// feed renders this prominently so a viewer in Istanbul sees TRY
			// amounts, not USD. We still keep `usd` for the global average and
			// for any viewer who has not picked a country.
			"country":        quoteCountry,
			"local_amount":   formatLocal(q.ProductValue),
			"local_currency": localCurrency(q.Denomination, quoteCountry),
		}
		if !q.Anonymous {
			e["address"] = addrs[q.UserID]
		}
		if isBatch {
			count := q.BatchItems
			if count < 1 {
				count = len(q.Lines)
			}
			if count < 1 {
				count = len(batchTitles)
			}
			e["is_batch"] = true
			e["batch_items"] = count
			e["batch_summary_items"] = batchTitles
			e["batch_summary"] = strings.Join(batchTitles, ", ")
		}
		// Product price/amount stays public, but the exact payment-rail
		// transaction details stay private for anonymous purchases.
		if !q.Anonymous && q.CoinAmount != "" && q.Coin != "" {
			e["paid"] = q.CoinAmount + " " + q.Coin
			if q.Network != "" {
				e["network"] = q.Network
			}
			// Owner (2026-10-05): the purchase's Lightning payment hash rides
			// on the public feed row — the tx of what you bought, in the open.
			if q.LightningPaymentHash != "" {
				e["tx"] = q.LightningPaymentHash
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

	// Public average delivery time across delivered purchases (seconds),
	// measured the way users experience it: from the moment the money was
	// observed (paid_at) until the purchase completed — NOT from order
	// creation. Quotes carry the payment stamp; legacy quotes without one
	// fall back to the payment handoff time, and quotes with neither are
	// skipped rather than guessed. Anonymous purchases contribute here too:
	// only wallet identity and payment transaction details are private,
	// not the purchase itself.
	var durSum int64
	var durN int
	for _, q := range quotes {
		if q.Status == "delivered" || q.Status == "complete" || q.Status == "fulfilled" {
			var start *time.Time
			if q.PaidAt != nil && !q.PaidAt.IsZero() {
				start = q.PaidAt
			} else if !q.PaymentHandoffAt.IsZero() {
				start = &q.PaymentHandoffAt
			}
			if start == nil || !q.UpdatedAt.After(*start) {
				continue
			}
			durN++
			durSum += int64(q.UpdatedAt.Sub(*start).Seconds())
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

// presenceIDMax bounds the client-supplied presence identifier. A wallet
// address is 44 characters; 64 leaves room for any future format while making
// the per-entry memory cost of a heartbeat attack trivial to reason about.
const presenceIDMax = 64

// PresenceHeartbeat is a public endpoint that registers a visitor so the live
// "X users shopping now" counter stays current. The visitor supplies a stable
// pseudonymous id (its wallet address when signed in).
//
// HARDENED. The counter is public social proof and the endpoint is
// unauthenticated, which makes it two things at once:
//
//  1. A forgery target. Anyone could POST a million distinct ids and make the
//     storefront claim a million shoppers are browsing. The id is now
//     validated and, for an ANONYMOUS ping, replaced by the resolved client
//     IP: one visitor from one address is one presence entry no matter how
//     many ids it invents. A signed-in ping keeps its wallet id (that is the
//     honest pseudonym) but is still length/charset checked.
//  2. A memory target. Bounded ids plus IP anchoring means the map can hold at
//     most one entry per live client IP plus one per signed-in wallet, and the
//     tracker's own shard caps bound the rest.
//
// Normal users are unaffected: the browser already sends its wallet address
// when signed in, and an anonymous ping never needed a client-chosen identity
// for the counter to be meaningful.
func (h *Handlers) PresenceHeartbeat(ctx *fasthttp.RequestCtx) {
	if h.Presence != nil {
		var req struct {
			ID string `json:"id"`
		}
		_ = readJSON(ctx, &req)
		id := sanitizePresenceID(req.ID)
		if userID := middleware.UserID(ctx); userID != "" {
			// Authenticated: the account id is the identity, and it is not
			// client-chosen at all.
			id = "u:" + userID
		} else if id == "" {
			// Anonymous and no usable id: anchor on the verified client IP so
			// the visitor still counts exactly once.
			if ip := clientip.Resolve(ctx, h.Cfg.TrustProxy, h.Cfg.ClientIPPolicy()).IP; ip != "" {
				id = "ip:" + ip
			}
		}
		h.Presence.Ping(id)
	}
	ctx.Response.Header.Set("Cache-Control", "no-store")
	writeJSON(ctx, fasthttp.StatusOK, map[string]interface{}{"ok": true})
}

// sanitizePresenceID accepts only a boring, bounded identifier. Everything
// else becomes "" so the caller falls back to the IP anchor rather than
// storing attacker-chosen bytes.
func sanitizePresenceID(raw string) string {
	id := strings.TrimSpace(raw)
	if id == "" || len(id) > presenceIDMax {
		return ""
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' ||
			c == '-' || c == '_' || c == '.' || c == ':' {
			continue
		}
		return ""
	}
	return id
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

// GetAccountLimits returns the signed-in user's daily order/spend usage and
// current-month purchase spend against the env-configured ceilings, plus
// reset times. Cashback caps are independent. Authed.
func (h *Handlers) GetAccountLimits(ctx *fasthttp.RequestCtx) {
	userID := middleware.UserID(ctx)
	now := time.Now().UTC()
	usage, err := h.Store.GetUserDailyBudget(userID, now.Add(-24*time.Hour))
	if err != nil {
		writeError(ctx, fasthttp.StatusServiceUnavailable, "Could not load purchase limits. Please try again.")
		return
	}
	monthly, err := h.Store.GetUserMonthlyUsage(userID, now)
	if err != nil {
		writeError(ctx, fasthttp.StatusServiceUnavailable, "Could not load purchase limits. Please try again.")
		return
	}
	monthlyMax := h.Cfg.MonthlySpendLimitUSD
	monthlyUsed := monthly.SpendUSD.Float()
	monthlyRemaining := monthlyMax - monthlyUsed
	if monthlyRemaining < 0 {
		monthlyRemaining = 0
	}
	nextMonth := time.Date(now.Year(), now.Month()+1, 1, 0, 0, 0, 0, time.UTC)
	// The rolling window resets when the OLDEST purchase in it turns 24h
	// old. With nothing purchased, report a full 24h window instead so the
	// countdown always reads sensibly.
	resetsAt := now.Add(24 * time.Hour)
	if !usage.OldestAt.IsZero() {
		resetsAt = usage.OldestAt.Add(24 * time.Hour)
	}
	out := map[string]interface{}{
		"used_orders":           usage.OrderCount,
		"max_orders":            h.Cfg.DailyOrderLimit,
		"used_usd":              usage.SpendUSD.String(),
		"max_usd":               h.Cfg.DailySpendLimitUSD,
		"used_monthly_usd":      monthly.SpendUSD.String(),
		"max_monthly_usd":       monthlyMax,
		"remaining_monthly_usd": monthlyRemaining,
		"month_resets_at":       nextMonth,
		"window_seconds":        86400,
		"resets_at":             resetsAt,
		"server_now":            now,
	}
	if h.Oracle != nil {
		if q, err := h.Oracle.NIMUSD(context.Background()); err == nil && q.MedianUSD > 0 {
			used := float64(usage.SpendUSD) / 1000000 / q.MedianUSD
			max := h.Cfg.DailySpendLimitUSD / q.MedianUSD
			out["used_nim"] = formatNIM(used)
			out["max_nim"] = formatNIM(max)
		}
	}
	paidLuna, pendingLuna, paidCount, pendingCount, _ := h.Store.UserCashbackTotals(userID)
	// v2: the buyer's base (pool staker base when staked, any amount;
	// otherwise the operator's universal base) + the wallet's ledger boost.
	// buyerBaseBps runs the same pool lookup the fulfillment path uses, so
	// the profile rate is the rate the next delivery pays.
	bps, _ := h.buyerBaseBps(ctx, userID)
	if h.Cfg.PoolAPIURL != "" && h.Pool != nil {
		bps += h.ledgerBoostBpsBestEffort(userID)
	}
	out["cashback"] = map[string]interface{}{
		"paid_nim":         cashback.NIMFromLuna(paidLuna),
		"pending_nim":      cashback.NIMFromLuna(pendingLuna),
		"paid_luna":        paidLuna,
		"pending_luna":     pendingLuna,
		"paid_count":       paidCount,
		"pending_count":    pendingCount,
		"cashback_bps":     bps,
		"cashback_percent": float64(bps) / 100.0,
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
		stages, current := buildOrderStages(o, h.Cfg.SiteName())
		title := cleanPublicProductLabel(payloadField(o.Payload, "product_name"))
		if title == "" {
			title = cleanPublicProductLabel(o.ProductID)
		}
		if title == "" {
			title = "Purchase"
		}
		resp := map[string]interface{}{
			"id":            o.ID,
			"type":          "purchase",
			"anonymous":     o.Anonymous,
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
		publicTitles := quoteBatchTitles(q)
		publicTitle := cleanPublicProductLabel(q.ProductID)
		if len(publicTitles) > 1 {
			publicTitle = strings.Join(publicTitles, " + ")
		}
		if publicTitle == "" {
			publicTitle = "Purchase"
		}
		// Everything is public here EXCEPT the delivery codes (owner-only):
		// the live status timeline plus the supplier order id (an opaque
		// reference — not a payment hash, since stablecoin payers are
		// private by design on the supplier side).
		payment := []map[string]interface{}{}
		// Supplier order IDs are not public tracking identifiers. They can
		// expose redemption data via a partner API and are owner/admin-only.

		resp := map[string]interface{}{
			"id":           q.ID,
			"type":         "cryptorefills_purchase",
			"anonymous":    q.Anonymous,
			"status":       q.Status,
			"nim":          q.EstimatedNIM,
			"title":        publicTitle,
			"usd":          q.ProductUSD.String(),
			"created_at":   q.CreatedAt,
			"updated_at":   q.UpdatedAt,
			"stages":       quoteStages(q),
			"transactions": payment,
			// Delivery CHANNEL (not the address/number) so the public timeline
			// can say "credit went to the number" instead of promising a code
			// for a top-up. The target itself stays owner-only.
			"delivery_channel": publicDeliveryChannel(q),
			"payment_method":   q.PaymentMethod,
			"coin":             q.Coin,
			"lines":            publicLines(q),
		}
			// Owner (2026-10-05): the purchase tx IS public summary content — the
	// old "no payment-network details here" policy is superseded by him.
	if !q.Anonymous && q.LightningPaymentHash != "" {
		resp["tx"] = q.LightningPaymentHash
	}
writeJSON(ctx, fasthttp.StatusOK, resp)
		return
	}

	writeError(ctx, fasthttp.StatusNotFound, "order not found")
}

// publicDeliveryChannel reduces the per-line manifest to "email" | "phone" |
// "both" for the public tracking view. No addresses or numbers are exposed.
func publicDeliveryChannel(q db.Quote) string {
	email, phone := false, false
	for _, ln := range q.Lines {
		switch ln.DeliveryChannel {
		case "phone":
			phone = true
		case "email":
			email = true
		}
	}
	if len(q.Lines) == 0 {
		if q.PhoneNumber != "" {
			phone = true
		} else if q.CustomerEmail != "" {
			email = true
		}
	}
	switch {
	case email && phone:
		return "both"
	case phone:
		return "phone"
	case email:
		return "email"
	}
	return ""
}

// publicLines exposes only what each line delivers and how — never to whom.
func publicLines(q db.Quote) []map[string]interface{} {
	out := make([]map[string]interface{}, 0, len(q.Lines))
	for _, ln := range q.Lines {
		out = append(out, map[string]interface{}{
			"product_id":       ln.ProductID,
			"face_label":       ln.FaceLabel,
			"quantity":         ln.Quantity,
			"kind":             ln.Kind,
			"delivery_channel": ln.DeliveryChannel,
		})
	}
	return out
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
	s2 := mk("payment_settled", "Nimiq Pay payment", "Waiting for payment confirmation.", "pending")
	s3 := mk("supplier_processing", "Supplier delivery", "Cryptorefills is preparing the delivery.", "pending")
	s4 := mk("delivery_complete", "Delivery", "The code / redemption details are delivered to the buyer.", "pending")
	switch st {
	case "order_creating":
		s2["status"] = "in_progress"
	case "awaiting_payment":
		s2["status"] = "in_progress"
		s2["timestamp"] = updated
	case "payment_started":
		s2["status"] = "in_progress"
		s2["description"] = "Payment started — waiting for confirmation."
		s2["timestamp"] = updated
	case "payment_received", "delivering":
		s2["status"] = "completed"
		s2["timestamp"] = updated
		s3["status"] = "in_progress"
		s3["timestamp"] = updated
	case "fulfilled":
		s2["status"] = "completed"
		s2["timestamp"] = updated
		s3["status"] = "completed"
		s3["timestamp"] = updated
		s4["status"] = "completed"
		s4["timestamp"] = updated
	case "expired":
		s2["status"] = "failed"
		s2["description"] = "Payment window elapsed."
		s2["timestamp"] = updated
		s3["status"] = "failed"
		s4["status"] = "failed"
	case "failed", "refunded", "manual_review":
		s2["status"] = "failed"
		s2["timestamp"] = updated
		s3["status"] = "failed"
		s3["timestamp"] = updated
		s4["status"] = "failed"
	}
	return []map[string]interface{}{s1, s2, s3, s4}
}
