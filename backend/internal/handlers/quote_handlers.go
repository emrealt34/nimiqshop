package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/valyala/fasthttp"

	"nimiqshop/internal/catalog"
	"nimiqshop/internal/clientip"
	"nimiqshop/internal/cryptorefills"
	"nimiqshop/internal/db"
	"nimiqshop/internal/middleware"
	"nimiqshop/internal/money"
	"nimiqshop/internal/notification"
	"nimiqshop/internal/phone"
)

type createQuoteRequest struct {
	// ProductID is the supplier family/brand ("Airbnb", "t-mobile").
	ProductID string `json:"product_id"`
	Country   string `json:"country"`
	// Denomination is a fixed label ("100 USD") or "range"; for range
	// products ProductValue carries the chosen face value.
	Denomination string  `json:"denomination,omitempty"`
	ProductValue float64 `json:"product_value,omitempty"`
	Quantity     int     `json:"quantity"`
	Email        string  `json:"email"`
	PhoneNumber  string  `json:"phone_number,omitempty"`
	// Coin/Network are ACCEPTED for backwards compatibility but IGNORED:
	// every order is placed on the BTC Lightning rail. The shop has no
	// coin picker on purpose — Nimiq Pay opens the BOLT11 invoice.
	Coin    string `json:"coin,omitempty"`
	Network string `json:"network,omitempty"`
	// Gift notification: when the buyer wants to send this order as a
	// gift, GiftChannel picks email / sms / both; GiftMessage is the
	// buyer-authored personal text; GiftRecipientPhone is the SMS target
	// when GiftChannel includes sms. Recipient email is taken from the
	// regular Email field (top-ups use it as fallback when sms is
	// chosen without a separate phone).
	GiftChannel        string `json:"gift_channel,omitempty"`
	GiftMessage        string `json:"gift_message,omitempty"`
	GiftRecipientPhone string `json:"gift_recipient_phone,omitempty"`
}

// The one and only payment rail this shop exposes: BTC Lightning. Nimiq
// Pay converts the customer's NIM to BTC Lightning atomically and pays the
// supplier's BOLT11 invoice directly.
const (
	PaymentCoin    = "BTC"
	PaymentNetwork = "Lightning"
)

// CreateQuote is the only purchase path:
//
//  1. local validation (email, country, denomination, quantity)
//  2. supplier DRY-RUN validation (limits, KYC, stock, beneficiary) — no
//     order is created, so it is free and safe
//  3. write-ahead local quote (order_creating) in the same transaction as
//     the daily-limit check
//  4. supplier CreateOrder → one-time wallet address + exact coin amount
//  5. attach (order id + wallet) BEFORE the response
//
// The customer then pays the wallet address with their own wallet (any
// wallet on the selected network). Cryptorefills delivers the product to
// the email (gift cards/eSIMs) or phone (top-ups).
func (h *Handlers) CreateQuote(ctx *fasthttp.RequestCtx) {
	userID := middleware.UserID(ctx)
	var req createQuoteRequest
	if err := readJSON(ctx, &req); err != nil || req.ProductID == "" || req.Country == "" {
		writeError(ctx, fasthttp.StatusBadRequest, "product_id and country are required")
		return
	}
	req.ProductID = strings.TrimSpace(req.ProductID)
	req.Country = strings.ToUpper(strings.TrimSpace(req.Country))
	req.Email = strings.TrimSpace(req.Email)
	req.Denomination = strings.TrimSpace(req.Denomination)
	req.Coin = strings.ToUpper(strings.TrimSpace(req.Coin))
	req.PhoneNumber = strings.TrimSpace(req.PhoneNumber)
	h.createQuoteInner(ctx, userID, req)
}

// createQuoteInner is the shared quote flow (production + test-buy).
func (h *Handlers) createQuoteInner(ctx *fasthttp.RequestCtx, userID string, req createQuoteRequest) {
	if len(req.Country) != 2 {
		writeError(ctx, fasthttp.StatusBadRequest, "country must be a 2-letter code")
		return
	}
	if req.Quantity < 1 || req.Quantity > h.Cfg.MaxOrderQuantity {
		writeError(ctx, fasthttp.StatusBadRequest, "invalid quantity")
		return
	}
	// PAYMENT RAIL: always BTC Lightning. A client-supplied coin/network is
	// ignored — there is no coin selection on the customer side.
	req.Coin = PaymentCoin
	req.Network = PaymentNetwork
	// Presence (best-effort, admin console only).
	info := clientip.Resolve(ctx, h.Cfg.TrustProxy)
	_ = h.Store.TouchUserPresence(userID, info.IP, info.Country)
	if req.Denomination == "" {
		writeError(ctx, fasthttp.StatusBadRequest, "denomination is required (e.g. \"100 USD\" or \"range\")")
		return
	}
	isRange := strings.EqualFold(req.Denomination, "range")
	if isRange {
		if req.ProductValue <= 0 {
			writeError(ctx, fasthttp.StatusBadRequest, "product_value is required for range products")
			return
		}
	}
	// The supplier delivers top-ups to the phone in beneficiary_account and
	// requires strict E.164. Normalize whatever the customer typed (separators,
	// 00 access prefix, national 0-prefix format) BEFORE validation, storage
	// or supplier calls, so every downstream consumer sees the same value.
	if req.PhoneNumber != "" {
		norm, err := phone.Normalize(req.PhoneNumber, req.Country)
		if err != nil {
			writeError(ctx, fasthttp.StatusBadRequest, err.Error())
			return
		}
		req.PhoneNumber = norm
	}
	meta := h.lookupFamilyMeta(ctx, req.ProductID, req.Country)
	// Cryptorefills delivers gift cards / eSIMs to this address — a bad email
	// means a broken delivery, so reject early and strictly. MOBILE TOP-UPS
	// deliver to the PHONE number instead; their email is optional (it is only
	// used as the receipt / gift-note address when the buyer provides one).
	if !isTopUpProduct(meta) {
		if req.Email == "" || !validEmail(req.Email) {
			writeError(ctx, fasthttp.StatusBadRequest, "a valid delivery email is required (the product is delivered to it)")
			return
		}
	}
	if isTopUpProduct(meta) {
		if req.PhoneNumber == "" {
			writeError(ctx, fasthttp.StatusBadRequest, "phone_number is required for mobile top-ups (E.164, e.g. +905551234567)")
			return
		}
		// Normalize already guarantees strict E.164; keep the explicit
		// check so a future refactor cannot silently bypass it.
		if err := phone.Validate(req.PhoneNumber); err != nil {
			writeError(ctx, fasthttp.StatusBadRequest, err.Error())
			return
		}
	}

	// ---- admin catalog rules (purchase-time gate) -----------------------
	rules, err := h.Store.GetCatalogRules()
	if err != nil {
		writeError(ctx, fasthttp.StatusInternalServerError, "could not load catalog policy")
		return
	}
	faceValue, denomLabel, err := resolveFaceValue(req.Denomination, req.ProductValue)
	if err != nil {
		writeError(ctx, fasthttp.StatusBadRequest, err.Error())
		return
	}
	totalFace := faceValue * float64(req.Quantity)
	// The admin price cap is USD; the requested face value is in the
	// product's LOCAL currency ("150.000 IDR"). Convert before gating so a
	// $20 cap means ~$20 in EVERY country's unit — not "local units < 20".
	// Point-based families ("575 Points") have no real currency code —
	// their raw point count must never be compared against the USD cap.
	faceUSD := 0.0
	minFaceUSD := 0.0
	if catalog.CurrencyKnown(meta.Currency) {
		faceUSD = catalog.ToUSD(totalFace, meta.Currency)
		minFaceUSD = catalog.ToUSD(meta.MinFaceUSD, meta.Currency)
	}
	if gerr := catalog.GateQuote(&rules, req.ProductID, meta.Category, meta.Kind, req.Country, meta.AdditionalCats, minFaceUSD, faceUSD); gerr != nil {
		writeError(ctx, fasthttp.StatusForbidden, gerr.Error())
		return
	}

	idempotencyHeader := strings.TrimSpace(string(ctx.Request.Header.Peek("Idempotency-Key")))
	if idempotencyHeader != "" && (len(idempotencyHeader) < 16 || len(idempotencyHeader) > 128) {
		writeError(ctx, fasthttp.StatusBadRequest, "Idempotency-Key must be 16-128 characters")
		return
	}
	idempotencyKey := idempotencyHeader
	if idempotencyKey == "" {
		idempotencyKey = uuid.NewString()
	}
	if existing, err := h.Store.GetQuoteByIdempotencyKey(userID, idempotencyKey); err == nil {
		// Client retry: return the same quote, never a second supplier order.
		writeQuoteCreated(ctx, existing)
		return
	}

	// Double-purchase guard: the same buyer submitting an IDENTICAL cart
	// twice within a minute (double-click, flaky-network retry, a second
	// tab) gets the FIRST quote back instead of a second supplier order.
	// "1 order became 3" was exactly this path: each click carried a fresh
	// idempotency key, so key-based dedup never fired. Matches on every
	// cart dimension and only swallows quotes that are still live
	// (unpaid/unfulfilled) — a deliberate re-purchase after delivery is
	// never blocked. Compares the RESOLVED denomination/face value (what
	// the quote row stores), not the raw request strings.
	if existing, ok := h.recentLiveDuplicateQuote(userID, req, denomLabel, faceValue, time.Now().UTC()); ok {
		log.Printf("quote: duplicate-purchase guard returned existing quote %s for user %s (%s x%s)", existing.ID, userID, req.ProductID, req.Denomination)
		writeQuoteCreated(ctx, existing)
		return
	}

	// faceValue/denomLabel were resolved above for the rules gate.
	// totalFace/faceUSD are in the product's LOCAL currency vs USD. The
	// stored ProductUSD must be the USD-EQUIVALENT (faceUSD): the old code
	// stored the raw local-currency micros, so a "TRY 500" card persisted
	// product_usd=500 micros and the orders page rendered it as "$0.0005"
	// (and the daily USD spend cap barely counted it).
	totalUSD := money.FromFloat(faceUSD)

	// ---- supplier dry run (no order created) -----------------------------
	// Request shape follows the official CryptoRefills docs
	// (https://www.cryptorefills.com/en/api-docs/developers#create-order):
	// flat fields at the delivery level, and — the part that decides WHERE
	// the product is delivered — beneficiary_account:
	//   - mobile topups: the recipient's phone in strict E.164 (normalized above)
	//   - gift cards & eSIMs: the end-user's email address
	// For fixed products: denomination like "25 TRY" or "100 USD", no product_value
	// For range products: denomination "range" + product_value
	beneficiary := req.Email
	if isTopUpProduct(meta) {
		beneficiary = req.PhoneNumber
	}
	delivery := cryptorefills.Delivery{
		BrandName:          req.ProductID,
		CountryCode:        req.Country,
		Denomination:       denomLabel,
		BeneficiaryAccount: beneficiary,
	}
	if isRange {
		v := req.ProductValue
		delivery.ProductValue = &v
		// For range, ensure denomination is exactly "range" per docs
		delivery.Denomination = "range"
	} else {
		// For fixed, ensure product_value is nil — some fixed products like Minecraft "Java & Bedrock Ed" fail if product_value is set
		delivery.ProductValue = nil
	}
	// Email goes to the supplier ONLY when the buyer actually provided one.
	// Top-ups are delivered to the phone number (beneficiary_account), so a
	// top-up order without a buyer email carries no email at all.
	validateReq := &cryptorefills.CreateOrderRequest{
		Deliveries: make([]cryptorefills.Delivery, 0, req.Quantity),
		Payment:    cryptorefills.OrderPayment{Type: "via", PaymentVia: "USER_WALLET", Coin: req.Coin, Network: req.Network},
		Lang:       "en",
	}
	if req.Email != "" {
		validateReq.Email = req.Email
		validateReq.User = &cryptorefills.OrderUser{Email: req.Email}
	}
	for i := 0; i < req.Quantity; i++ {
		validateReq.Deliveries = append(validateReq.Deliveries, delivery)
	}
	validateRes, err := h.CR.ValidateOrder(h.supplierContext(ctx), validateReq)
	if err != nil {
		h.supplierError(ctx, err, "order could not be validated")
		return
	}

	// POINT-BASED PRICE CAP (the honest path): game-currency families
	// ("575 Points", "1000 V-Bucks") carry no real ISO currency code, so
	// their point count can never be compared with the USD cap via the
	// label. The dry-run above just returned the EXACT BTC total for this
	// cart (quantity included) — convert it with the always-warm oracle
	// rate and enforce the admin cap on the price the buyer ACTUALLY pays.
	if !catalog.CurrencyKnown(meta.Currency) && rules.MaxFaceValueUSD > 0 && validateRes != nil {
		if btc, perr := strconv.ParseFloat(strings.TrimSpace(validateRes.CoinAmount), 64); perr == nil && btc > 0 {
			snap := currentRates()
			if snap.btcUSD > 0 {
				if usd := btc * snap.btcUSD; usd > rules.MaxFaceValueUSD {
					writeError(ctx, fasthttp.StatusForbidden, "orders above the current price cap are not accepted")
					return
				}
			} else {
				// Oracle cold: cannot price honestly right now. Proceed — the
				// supplier's own limits still bound the order; the operator
				// sees it in the log.
				log.Printf("quote: point-family %s cap check skipped (btc rate unavailable)", req.ProductID)
			}
		}
	}

	// ---- write-ahead local quote (daily limits in the same txn) ----------
	now := time.Now().UTC()
	q := db.Quote{
		ID: quoteIDNow(), UserID: userID,
		ProductID: req.ProductID, ProductCountry: req.Country,
		Denomination: denomLabel, ProductValue: faceValue, Quantity: req.Quantity,
		IdempotencyKey: idempotencyKey, ProductUSD: totalUSD,
		CustomerEmail: req.Email, PhoneNumber: req.PhoneNumber,
		BeneficiaryAccount: beneficiary,
		Coin:               req.Coin, Network: req.Network,
		// Gift notification metadata — channel/message/phone are all buyer-
		// supplied via the frontend and persisted BEFORE the supplier call
		// so the tracker can fan out the email/SMS when fulfillment lands.
		GiftChannel:        notification.NormalizeChannel(req.GiftChannel),
		GiftMessage:        strings.TrimSpace(req.GiftMessage),
		GiftRecipientPhone: strings.TrimSpace(req.GiftRecipientPhone),
		Status:    "order_creating",
		ExpiresAt: now.Add(30 * time.Minute), CreatedAt: now, UpdatedAt: now,
	}
	if err := h.Store.CreateQuoteWithDailyLimits(q, h.Cfg.DailyOrderLimit, money.FromFloat(h.Cfg.DailySpendLimitUSD), now.Add(-24*time.Hour)); err != nil {
		if errors.Is(err, db.ErrConflict) {
			if existing, lookupErr := h.Store.GetQuoteByIdempotencyKey(userID, idempotencyKey); lookupErr == nil {
				writeQuoteCreated(ctx, existing)
				return
			}
			writeError(ctx, fasthttp.StatusConflict, "quote idempotency key already used")
			return
		}
		if errors.Is(err, db.ErrLimit) {
			// Buyer-facing wording: the common case is "this cart costs more
			// than what's left of today's budget" — say THAT, with the actual
			// limits, instead of a generic "limit reached, wait".
			writeError(ctx, fasthttp.StatusTooManyRequests, fmt.Sprintf(
				"this order exceeds your daily purchase limit (%d orders / $%.0f per 24h) — the price is above what is left today; try a smaller amount or wait for your oldest purchase to drop off",
				h.Cfg.DailyOrderLimit, h.Cfg.DailySpendLimitUSD))
			return
		}
		writeError(ctx, fasthttp.StatusInternalServerError, "could not reserve quote")
		return
	}

	// ---- durable "supplier request started" marker ------------------------
	// Committed BEFORE the supplier call so a crash can never be misread as
	// "the request was never sent" (settlement tracker recovery invariant).
	// Fail closed on a marker write error: the supplier request goes out
	// without the marker only if we deliberately skip it, never by accident.
	if err := h.Store.MarkSupplierRequestStarted(q.ID); err != nil {
		_ = h.Store.MarkQuoteManualReview(q.ID, "could not persist the supplier-request marker before order creation: "+err.Error())
		writeError(ctx, fasthttp.StatusServiceUnavailable, "could not reserve quote")
		return
	}

	// ---- supplier order creation -----------------------------------------
	orderReq := &cryptorefills.CreateOrderRequest{
		Deliveries:  validateReq.Deliveries,
		Payment:     validateReq.Payment,
		User:        validateReq.User,
		Lang:        "en",
		Acquisition: &cryptorefills.Acquisition{UTMSource: "nimshop"},
	}
	order, err := h.CR.CreateOrder(h.supplierContext(ctx), orderReq)
	if err != nil {
		// The intent is durable; a failed creation just flips the quote to
		// failed (no supplier order exists or it will expire unpaid).
		if _, lerr := h.Store.FailOrderAttempt(q.ID); lerr == nil {
			_ = h.Store.MarkSupplierFailure(q.ID, "order creation failed: "+err.Error())
		}
		h.supplierError(ctx, err, "could not create supplier order")
		return
	}
	if order.WalletAddress == "" || order.CoinAmount == "" {
		_ = h.Store.MarkQuoteManualReview(q.ID, "supplier created order without a payable wallet address")
		writeError(ctx, fasthttp.StatusBadGateway, "supplier returned an order without a payment address")
		return
	}
	paymentExpiry := order.UpdatedTime()
	if paymentExpiry.IsZero() {
		paymentExpiry = order.CreatedTime()
	}
	if paymentExpiry.IsZero() {
		paymentExpiry = now
	}
	// The supplier does not return an explicit expiry for Lightning
	// invoices; the documented window is 30 minutes.
	if !paymentExpiry.After(now) {
		paymentExpiry = now
	}
	paymentExpiry = paymentExpiry.Add(30 * time.Minute)
	if err := h.Store.AttachQuotePayment(q.ID, order.ID, order.WalletAddress, order.Coin, order.CoinAmount, order.Network, paymentExpiry); err != nil {
		// The supplier order exists and is durable; the local attach is
		// idempotent-retryable and the tracker will pick it up by supplier
		// order id on the webhook path.
		_ = h.Store.MarkQuoteManualReview(q.ID, "order created but local attach failed: "+err.Error())
		writeError(ctx, fasthttp.StatusServiceUnavailable, "order was created; please retry the quote page")
		return
	}
	q.SupplierOrderID = order.ID
	q.WalletAddress = order.WalletAddress
	q.Coin = order.Coin
	q.CoinAmount = order.CoinAmount
	q.Network = order.Network
	q.PaymentExpiry = paymentExpiry
	q.ExpiresAt = paymentExpiry
	q.Status = "awaiting_payment"
	writeQuoteCreatedWithEstimate(ctx, q, h.nimEstimateForBTC(order.CoinAmount))
}

// recentLiveDuplicateQuote finds a quote from the last 60 seconds with an
// IDENTICAL cart (product, country, denomination, value, quantity,
// beneficiary) that is still live — i.e. unpaid or mid-delivery. Returning
// it instead of creating a new supplier order makes accidental double
// purchases (double-click, retry storm, parallel tabs) structurally
// impossible, while an intentional re-purchase — after the first order
// fulfilled, failed or expired, or after the window passed — goes through.
const duplicatePurchaseWindow = 60 * time.Second

func (h *Handlers) recentLiveDuplicateQuote(userID string, req createQuoteRequest, denomLabel string, faceValue float64, now time.Time) (db.Quote, bool) {
	quotes, err := h.Store.ListQuotesForUser(userID, 10)
	if err != nil || len(quotes) == 0 {
		return db.Quote{}, false
	}
	cutoff := now.Add(-duplicatePurchaseWindow)
	for _, q := range quotes {
		if q.CreatedAt.Before(cutoff) {
			break // newest-first: everything older is out of the window
		}
		switch q.Status {
		case cryptorefills.QuoteOrderCreating, cryptorefills.QuoteAwaitingPay,
			cryptorefills.QuotePaidStarted, cryptorefills.QuotePaidReceived,
			cryptorefills.QuoteDelivering:
			// live — a candidate
		default:
			continue // fulfilled/failed/expired/refunded/manual_review: never dedupe
		}
		if q.ProductID == req.ProductID &&
			q.ProductCountry == req.Country &&
			q.Denomination == denomLabel &&
			q.ProductValue == faceValue &&
			q.Quantity == req.Quantity &&
			q.CustomerEmail == req.Email &&
			q.PhoneNumber == req.PhoneNumber {
			return q, true
		}
	}
	return db.Quote{}, false
}

// nimEstimateForBTC converts a BTC amount to an informational NIM estimate
// using both oracles. Any failure returns 0 (field omitted): the exact NIM
// amount is always shown by Nimiq Pay at approval time.
func (h *Handlers) nimEstimateForBTC(btcAmount string) float64 {
	btc, err := strconv.ParseFloat(strings.TrimSpace(btcAmount), 64)
	if err != nil || btc <= 0 {
		return 0
	}
	btcUSD, err1 := h.Oracle.BTCUSD(context.Background())
	nimUSD, err2 := h.Oracle.NIMUSD(context.Background())
	if err1 != nil || err2 != nil || nimUSD.MedianUSD <= 0 {
		return 0
	}
	return btc * btcUSD.MedianUSD / nimUSD.MedianUSD
}

// writeQuoteCreatedWithEstimate adds the informational NIM estimate on top
// of the standard quote response.
func writeQuoteCreatedWithEstimate(ctx *fasthttp.RequestCtx, q db.Quote, nimEstimate float64) {
	if nimEstimate > 0 {
		resp := map[string]interface{}{
			"quote_id":           q.ID,
			"status":             q.Status,
			"payment_method":     "btc_lightning",
			"powered_by":         "cryptorefills",
			"product_id":         q.ProductID,
			"country":            q.ProductCountry,
			"denomination":       q.Denomination,
			"quantity":           q.Quantity,
			"expires_at":         q.ExpiresAt,
			"wallet_address":     q.WalletAddress,
			"coin":               q.Coin,
			"coin_amount":        q.CoinAmount,
			"network":            q.Network,
			"payment_expires_at": q.PaymentExpiry,
			"estimated_nim":      nimEstimate,
		}
		if cryptorefills.IsBOLT11(q.WalletAddress) {
			resp["lightning_invoice"] = q.WalletAddress
			resp["payment_uri"] = cryptorefills.LightningURI(q.WalletAddress)
		}
		writeJSON(ctx, fasthttp.StatusCreated, resp)
		return
	}
	writeQuoteCreated(ctx, q)
}

// familyMeta is the cached supplier-side identity of a family, used for
// both the phone-number requirement and the admin catalog-rule gate.
type familyMeta struct {
	Kind           string
	Category       string
	AdditionalCats []string
	MinFaceUSD     float64
	OutOfStock     bool
	// Currency is the family's LOCAL currency code ("IDR", "TRY", "USD"…)
	// parsed from the supplier's range/denomination data. The admin's
	// USD price cap is applied through catalog.ToUSD with this.
	Currency       string
	// DeliveryType is the supplier's per-product delivery channel
	// ("by_email" | "by_phone") — the most direct signal for what
	// beneficiary_account must carry.
	DeliveryType string
}

// isTopUpProduct reports whether the family is a mobile top-up whose
// beneficiary_account must be the recipient's E.164 phone number. Gift
// cards and eSIMs (kind mobile_recharge, category e-sim) are delivered to
// the end-user's EMAIL and must never receive a phone.
func isTopUpProduct(m familyMeta) bool {
	if strings.EqualFold(m.DeliveryType, "by_phone") {
		return true
	}
	return m.Kind == "mobile_recharge" && !strings.EqualFold(m.Category, "e-sim")
}

// lookupFamilyMeta fetches the product family (cached) and returns its
// metadata. It is best-effort: on any lookup failure the caller proceeds
// and the supplier's own validation is the final authority. Successful
// metadata is cached 1h; a MISS/empty result is cached only 30s so a
// transient supplier glitch cannot leave the phone-required check broken
// for 10 minutes (the old code cached the zero meta exactly that long).
func (h *Handlers) lookupFamilyMeta(ctx *fasthttp.RequestCtx, family, country string) familyMeta {
	cacheKey := "family:meta:" + strings.ToLower(family) + ":" + country
	if v, ok := h.cache.get(cacheKey); ok {
		if m, ok := v.(familyMeta); ok {
			return m
		}
	}
	var meta familyMeta
	fams, err := h.CR.ProductsByCountry(h.supplierContext(ctx), country, family, "")
	if err == nil && len(fams) > 0 {
		f := fams[0]
		meta = familyMeta{
			Kind:           f.Kind,
			Category:       f.Category,
			AdditionalCats: f.AdditionalCats,
			OutOfStock:     f.OutOfStock,
		}
		for _, p := range f.Products {
			if meta.Currency == "" {
				if p.Range != nil {
					meta.Currency = p.Range.Currency
				}
				if meta.Currency == "" {
					_, code := catalog.ParseDenominationLabel(p.Denomination)
					meta.Currency = code
				}
			}
			if p.Range != nil {
				if meta.MinFaceUSD == 0 || p.Range.Min < meta.MinFaceUSD {
					meta.MinFaceUSD = p.Range.Min
				}
			}
			if meta.DeliveryType == "" {
				meta.DeliveryType = p.DeliveryType
			}
		}
		h.cache.setTTL(cacheKey, meta, ttlFamilyMeta)
	} else {
		// Miss: short negative TTL only — enough to stop a retry storm,
		// short enough that a glitch self-heals in seconds.
		h.cache.setTTL(cacheKey, meta, ttlFamilyMetaMiss)
	}
	return meta
}

// maxFaceValue is the sanity ceiling for a parsed face value. The old
// ceiling (10,000) silently dropped ENTIRE COUNTRIES whose currencies carry
// large numbers — "150.000 IDR", "1.000.000 VND", "50.000 IQD" — making
// checkout fail with "denomination is required" in Indonesia, Vietnam,
// Iraq, Lebanon, Colombia, Turkey's high denominations, etc. 100 million
// still rejects absurd garbage (1e308 bodies) while accepting every real
// fiat denomination on earth.
const maxFaceValue = 100_000_000

// resolveFaceValue normalizes the denomination to a face value in the
// currency's major units plus the supplier label ("100 USD").
//
// Country-proof parsing (this is the fix for "some countries can't buy"):
//   - "100 USD", "TRY300", "$25", "₩5,000", "120 TL"  prefix/suffix/symbol
//   - "150.000 IDR", "1.000.000 VND"                  dot-grouped thousands
//   - "1,000,000 COP"                                 comma-grouped
//   - "1 000 AED"                                     space-grouped
//   - "25,50 EUR", "10.5 USD"                         decimal comma/dot
//   - "٥٠٠ SAR"                                       unicode digits
//   - "Java & Bedrock Ed" (no number at all)          LABEL-ONLY: value 0,
//     the supplier prices fixed products by the exact label, so the
//     purchase proceeds instead of failing with "denomination is
//     required". A zero face value just doesn't count toward the daily
//     USD spend cap — the per-ORDER daily limit still applies.
func resolveFaceValue(denomination string, value float64) (faceValue float64, label string, err error) {
	if strings.EqualFold(denomination, "range") {
		if value <= 0 {
			return 0, "", errors.New("product_value must be positive for range products")
		}
		return value, "range", nil
	}
	s := strings.TrimSpace(denomination)
	if s == "" {
		// If denomination empty but value provided (e.g. Minecraft "Java & Bedrock Ed" with value 26.95), use value
		if value > 0 && value < maxFaceValue {
			return value, "fixed", nil
		}
		return 0, "", errors.New(`denomination is required (e.g. "100 USD" or "range")`)
	}
	if f := parseMoneyAmount(s); f > 0 && f < maxFaceValue {
		return f, s, nil
	}
	// No (sane) number in the label: keep the label verbatim, value 0.
	return 0, s, nil
}

// parseMoneyAmount extracts the monetary amount from a free-form
// denomination label, handling every real-world currency formatting habit:
// thousands grouping with spaces ("1 000"), dots ("1.000.000") or commas
// ("1,000,000"), decimal commas ("25,50"), mixed ("1.234,56" / "1,234.56"),
// currency prefixes/suffixes/symbols ("TRY300" / "100 USD" / "₩5,000") and
// Unicode digit blocks (Arabic-Indic, Extended Arabic-Indic, Devanagari).
// It is the Go twin of the frontend's parseMoneyAmount in util.js — keep
// the two in lockstep.
// parseMoneyAmount delegates to the catalog package's shared parser so
// every layer (catalog filters, quote gate, admin cap) parses denomination
// labels IDENTICALLY. (The old local duplicate drifted from the frontend.)
func parseMoneyAmount(s string) float64 {
	v, _ := catalog.ParseDenominationLabel(s)
	return v
}

func parseSupplierTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05", time.RFC3339Nano} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}

func quoteIDNow() string {
	return uuid.NewString()
}

// writeQuoteCreated is the quote response. On the Lightning rail the
// wallet address IS the BOLT11 invoice: the response exposes it as
// lightning_invoice plus a ready-to-open lightning: URI for Nimiq Pay and
// an informational NIM estimate (exact NIM is computed by Nimiq Pay).
func writeQuoteCreated(ctx *fasthttp.RequestCtx, q db.Quote) {
	status := fasthttp.StatusAccepted
	if q.WalletAddress != "" {
		status = fasthttp.StatusCreated
	}
	resp := map[string]interface{}{
		"quote_id":       q.ID,
		"status":         q.Status,
		"payment_method": "btc_lightning",
		"powered_by":     "cryptorefills",
		"product_id":     q.ProductID,
		"country":        q.ProductCountry,
		"denomination":   q.Denomination,
		"quantity":       q.Quantity,
		"expires_at":     q.ExpiresAt,
	}
	if q.WalletAddress != "" {
		resp["wallet_address"] = q.WalletAddress
		resp["coin"] = q.Coin
		resp["coin_amount"] = q.CoinAmount
		resp["network"] = q.Network
		resp["payment_expires_at"] = q.PaymentExpiry
		if cryptorefills.IsBOLT11(q.WalletAddress) {
			resp["lightning_invoice"] = q.WalletAddress
			resp["payment_uri"] = cryptorefills.LightningURI(q.WalletAddress)
		}
	}
	writeJSON(ctx, status, resp)
}

// ListUserQuotes returns the authenticated user's quotes newest-first.
func (h *Handlers) ListUserQuotes(ctx *fasthttp.RequestCtx) {
	userID := middleware.UserID(ctx)
	quotes, err := h.Store.ListQuotesForUser(userID, 100)
	if err != nil {
		writeError(ctx, fasthttp.StatusInternalServerError, "could not load quotes")
		return
	}
	writeJSON(ctx, fasthttp.StatusOK, quotes)
}

// GetUserQuote returns one quote with its fulfillment/redemption details.
func (h *Handlers) GetUserQuote(ctx *fasthttp.RequestCtx) {
	id := ctx.UserValue("id").(string)
	userID := middleware.UserID(ctx)
	q, err := h.Store.GetQuoteForUser(id, userID)
	if err != nil {
		writeError(ctx, fasthttp.StatusNotFound, "quote not found")
		return
	}
	var fulfillment json.RawMessage
	if len(q.Fulfillment) > 0 && string(q.Fulfillment) != "null" {
		fulfillment = q.Fulfillment
	}
	writeJSON(ctx, fasthttp.StatusOK, map[string]interface{}{
		"quote":       q,
		"fulfillment": fulfillment,
		"refund":      quoteRefundView(q),
	})
}

// quoteRefundView is the customer-facing refund block for a quote.
// Cryptorefills is the merchant of record: refunds are executed by the
// supplier, and this view only reports what the supplier told us.
func quoteRefundView(q db.Quote) map[string]interface{} {
	switch q.Status {
	case "refunded":
		out := map[string]interface{}{
			"status": "refunded",
			"detail": "Cryptorefills refunded this order to the customer's payment method.",
		}
		if len(q.Refund) > 0 && string(q.Refund) != "null" {
			out["supplier_refund"] = json.RawMessage(q.Refund)
		}
		return out
	case "failed":
		if q.RefundReason != "" {
			out := map[string]interface{}{
				"status": "failed",
				"detail": "The supplier could not complete this payment. If you sent money, Cryptorefills will process the refund — contact them or open a support ticket.",
			}
			return out
		}
	}
	return nil
}
