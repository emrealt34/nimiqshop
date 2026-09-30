package handlers

import (
	"bytes"
	"encoding/base64"
	"image/png"

	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/valyala/fasthttp"

	"nimiqshop/internal/catalog"
	"nimiqshop/internal/clientip"
	"nimiqshop/internal/cryptorefills"
	"nimiqshop/internal/db"
	"nimiqshop/internal/i18n"
	"nimiqshop/internal/middleware"
	"nimiqshop/internal/money"
	"nimiqshop/internal/phone"
)

type createQuoteRequest struct {
	// ProductID is the supplier family/brand ("Airbnb", "t-mobile").
	ProductID string `json:"product_id"`
	Country   string `json:"country"`
	// Brand is an optional hint for the originating supplier brand when one family fans out
	// to multiple brands (e.g. Turk Telecom Data / Credits / Bundle). When set, checkout prefers
	// this brand for the supplier delivery instead of inferring from denomination alone.
	Brand   string `json:"brand,omitempty"`
	BrandID string `json:"brand_id,omitempty"`
	// Denomination is a fixed label ("100 USD") or "range"; for range
	// products ProductValue carries the chosen face value.
	Denomination string  `json:"denomination,omitempty"`
	ProductValue float64 `json:"product_value,omitempty"`
	Quantity     int     `json:"quantity"`
	Email        string  `json:"email"`
	PhoneNumber  string  `json:"phone_number,omitempty"`
	// Coin/Network are ACCEPTED for backwards compatibility.
	// When payment_method = "usdt_polygon" the coin is overridden to USDT on
	// Polygon (direct wallet pay, reduced cashback). When empty/"nimiq_pay"
	// we fall back to BTC Lightning through Nimiq Pay.
	Coin    string `json:"coin,omitempty"`
	Network string `json:"network,omitempty"`
	// PaymentMethod: "nimiq_pay" (default, BTC Lightning → Nimiq Pay) or
	// "usdt_polygon" (direct USDT on Polygon at StableMult × NIM cashback rate).
	PaymentMethod string `json:"payment_method,omitempty"`
	// CashbackDestination: buyer's choice "cashback" (default) or "trees".
	// Locked at quote time; only applies to nimiq_pay orders.
	CashbackDestination string `json:"cashback_destination,omitempty"`
	// Anonymous keeps this purchase in the public activity feed while hiding
	// the buyer wallet identity and payment transaction details. The buyer
	// still sees the complete quote in their own Orders.
	Anonymous bool `json:"anonymous,omitempty"`
	// Gift notification: GiftChannel says this order IS a gift (the only
	// accepted value is "email"), GiftMessage is the buyer-authored personal
	// text and the note is sent to the order's Email. There is no SMS note
	// and no separate recipient field: a text cannot carry the buyer's
	// message and the shop does not send SMS at all — the note has exactly
	// one carrier, the Mailtrap email.
	GiftChannel string `json:"gift_channel,omitempty"`
	GiftMessage string `json:"gift_message,omitempty"`
	// GifterIdenticon is the buyer's identicon as a PNG data URI, rasterized
	// by the checkout from the same @nimiq/identicons face the site shows.
	// Only a NAMED email gift keeps it; an anonymous quote drops it here.
	GifterIdenticon string `json:"gifter_identicon,omitempty"`
	CashbackCode    string `json:"cashback_code,omitempty"`
}

// Default payment rail: BTC Lightning through Nimiq Pay.
//
// The stablecoin rail is USDT on Polygon — the shop has never run anything
// else in this build; there is no legacy USDC id to accept.
const (
	PaymentCoinNIM       = "BTC"
	PaymentNetworkNIM    = "Lightning"
	PaymentCoinUSDT      = "USDT"
	PaymentNetworkStable = "Polygon (Matic)"

	PaymentMethodNIM  = "nimiq_pay"
	PaymentMethodUSDT = "usdt_polygon"
)

// IsStablecoinMethod reports whether a payment-method string is the
// stablecoin-on-Polygon rail (usdt_polygon).
func IsStablecoinMethod(m string) bool {
	return m == PaymentMethodUSDT
}

// IsStablecoinCoin reports whether a coin is the stablecoin rail's coin (USDT).
func IsStablecoinCoin(coin string) bool {
	return strings.EqualFold(coin, PaymentCoinUSDT)
}

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
	req.Brand = strings.TrimSpace(req.Brand)
	req.BrandID = strings.TrimSpace(req.BrandID)
	req.Coin = strings.ToUpper(strings.TrimSpace(req.Coin))
	req.PhoneNumber = strings.TrimSpace(req.PhoneNumber)
	req.CashbackCode = strings.TrimSpace(req.CashbackCode)
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
	unlock := lockCreateQuote(userID)
	defer unlock()
	// PAYMENT RAIL: default BTC Lightning (Nimiq Pay). If client selected the
	// stablecoin rail (usdt_polygon), switch to USDT on Polygon (Matic) —
	// always available, no env switch. Stablecoin orders get reduced cashback
	// (enforced in enqueueCashbackOnFulfill). An unknown method id is a hard
	// 400, never a silent rail switch.
	method := strings.ToLower(strings.TrimSpace(req.PaymentMethod))
	if method == "" {
		method = PaymentMethodNIM
	}
	if method != PaymentMethodNIM && method != PaymentMethodUSDT {
		writeError(ctx, fasthttp.StatusBadRequest, "payment_method must be nimiq_pay or usdt_polygon")
		return
	}
	if IsStablecoinMethod(method) {
		req.Coin = PaymentCoinUSDT
		req.Network = PaymentNetworkStable
		req.PaymentMethod = PaymentMethodUSDT
	} else {
		req.Coin = PaymentCoinNIM
		req.Network = PaymentNetworkNIM
		req.PaymentMethod = PaymentMethodNIM
	}
	// Cashback destination is locked per quote. "trees" is valid on both
	// Nimiq Pay and USDT orders; anything else defaults to the buyer wallet.
	dest := strings.ToLower(strings.TrimSpace(req.CashbackDestination))
	if dest != db.TreeDestTrees {
		dest = db.TreeDestCashback
	}
	req.CashbackDestination = dest
	// Normalize and bind the caller's intent BEFORE any fallible supplier or
	req.ProductID = strings.TrimSpace(req.ProductID)
	req.Country = strings.ToUpper(strings.TrimSpace(req.Country))
	req.Email = strings.TrimSpace(req.Email)
	req.Denomination = strings.TrimSpace(req.Denomination)
	req.CashbackCode = strings.ToUpper(strings.TrimSpace(req.CashbackCode))
	// Gift note: canonicalized BEFORE the fingerprints are taken, so one
	// idempotency key can never produce two different notes. Whether the note
	// can actually be delivered is checked once the product kind is known
	// (see requireGiftContacts below).
	gift, giftErr := normalizeGiftNoteShape(req.GiftChannel, req.GiftMessage)
	if giftErr != nil {
		writeError(ctx, fasthttp.StatusBadRequest, giftErr.Error())
		return
	}
	req.GiftChannel, req.GiftMessage = gift.Channel, gift.Message
	req.GifterIdenticon = normalizeGifterIdenticon(req.GifterIdenticon, req.Anonymous, gift.Channel)
	if req.PhoneNumber != "" {
		normalized, err := phone.Normalize(req.PhoneNumber, req.Country)
		if err != nil {
			writeError(ctx, 400, err.Error())
			return
		}
		req.PhoneNumber = normalized
	}
	normalizeQuoteSelection(&req.Denomination, &req.ProductValue)
	idempotencyKey, ok := checkoutKey(ctx)
	if !ok {
		return
	}
	requestFP, purchaseFP := singleFingerprints(req)
	if h.existingCheckout(ctx, userID, idempotencyKey, requestFP, purchaseFP) {
		return
	}
	cashbackView, err := h.resolveQuoteCashback(req.CashbackCode, userID)
	if err != nil {
		writeError(ctx, fasthttp.StatusBadRequest, err.Error())
		return
	}
	req.CashbackCode = cashbackView.Code
	// Presence (best-effort, admin console only).
	info := clientip.Resolve(ctx, h.Cfg.TrustProxy, h.Cfg.ClientIPPolicy())
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
	if !meta.Known {
		if meta.Unavailable {
			writeError(ctx, 409, "this product is not available; no order was created")
			return
		}
		writeError(ctx, 503, "product metadata cannot be verified; no order was created")
		return
	}
	// Cryptorefills delivers gift cards / eSIMs to this address — a bad email
	// means a broken delivery, so reject early and strictly. MOBILE TOP-UPS
	// deliver to the PHONE number instead; their email is optional (it is only
	// used as the receipt / gift-note address when the buyer provides one).
	if meta.deliveryChannelFor(req.ProductID, req.Denomination) == "by_account" {
		writeError(ctx, 409, "this product is delivered to a utility account number, which the shop does not support; no order was created")
		return
	}
	topUpSel := isTopUpSelection(meta, req.ProductID, req.Denomination)
	if !topUpSel {
		if req.Email == "" || !validEmail(req.Email) {
			writeError(ctx, fasthttp.StatusBadRequest, "a valid delivery email is required (the product is delivered to it)")
			return
		}
	}
	if topUpSel {
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

	// The note has exactly one requirement, and it is checked where the
	// product kind is known: an address to put it in.
	gift, giftErr = requireGiftContacts(gift, req.Email)
	if giftErr != nil {
		writeError(ctx, fasthttp.StatusBadRequest, giftErr.Error())
		return
	}

	// ---- admin catalog rules (purchase-time gate) -----------------------
	rules, err := h.Store.GetCatalogRules()
	if err != nil {
		writeError(ctx, fasthttp.StatusInternalServerError, "could not load catalog policy")
		return
	}
	faceValue, denomLabel, faceCurrency, err := meta.resolveSelection(req.Denomination, req.ProductValue)
	if err != nil {
		writeError(ctx, fasthttp.StatusBadRequest, err.Error())
		return
	}
	meta.Currency = faceCurrency
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

	// ---- supplier dry run (no order created) -----------------------------
	// Request shape follows the official CryptoRefills docs
	// (https://www.cryptorefills.com/en/api-docs/developers#create-order):
	// flat fields at the delivery level, and — the part that decides WHERE
	// the product is delivered — beneficiary_account:
	//   - mobile topups: the recipient's phone in strict E.164 (normalized above)
	//   - gift cards & eSIMs: the end-user's EMAIL and must never receive a phone.
	// For fixed products: denomination like "25 TRY" or "100 USD", no product_value
	// For range products: denomination "range" + product_value
	beneficiary := req.Email
	if topUpSel {
		beneficiary = req.PhoneNumber
	}
	// Pick the correct brand for this SKU when one family fans out to multiple brands
	brandForDelivery := meta.BrandName
	if req.Brand != "" {
		brandForDelivery = req.Brand
	} else if meta.BrandByDenom != nil {
		if b, ok := meta.BrandByDenom[denomLabel]; ok && b != "" {
			brandForDelivery = b
		}
	}
	delivery := cryptorefills.Delivery{
		BrandName:          brandForDelivery,
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

	// Monetary limits use structured prices / the live supplier total only.
	liveUSD, priceErr := quotedUSD(validateRes.CoinAmount, req.PaymentMethod, currentRates().btcUSD, faceUSD, faceUSD > 0)
	if priceErr != nil {
		writeError(ctx, 503, priceErr.Error())
		return
	}
	if rules.MaxFaceValueUSD > 0 && liveUSD > rules.MaxFaceValueUSD {
		writeError(ctx, fasthttp.StatusForbidden, "orders above the current price cap are not accepted")
		return
	}
	totalUSD := money.FromFloat(liveUSD)
	validatePromoOrderCap(&cashbackView, liveUSD)

	// Preserve the buyer's selected language for async emails/retry. Must be
	// resolved BEFORE we build the supplier request so the supplier also sees
	// the buyer's locale.
	quoteLang := i18n.ParseLangCtx(ctx)
	i18n.SetCookieCtx(ctx, quoteLang) // refresh cookie so it survives the full year

	orderReq := &cryptorefills.CreateOrderRequest{Deliveries: validateReq.Deliveries, Payment: validateReq.Payment, User: validateReq.User, Lang: quoteLang, Acquisition: &cryptorefills.Acquisition{UTMSource: "nimshop"}}
	supplierRequest, err := cryptorefills.MarshalCreateRequest(orderReq)
	if err != nil {
		writeError(ctx, 400, "invalid supplier request")
		return
	}
	// ---- write-ahead local quote (daily limits in the same txn) ----------
	now := time.Now().UTC()

	q := db.Quote{
		ID: quoteIDNow(), UserID: userID, Lang: quoteLang,
		ProductID: req.ProductID, ProductCountry: req.Country,
		Denomination: denomLabel, ProductValue: faceValue, ProductCurrency: faceCurrency, Quantity: req.Quantity,
		// Single-item carts get the same delivery manifest the batch path
		// builds, so "how does this arrive?" is answered by ONE code path
		// everywhere instead of per-screen guesswork.
		Lines:          []db.QuoteLine{singleQuoteLine(req, meta, denomLabel, beneficiary)},
		IdempotencyKey: idempotencyKey, ProductUSD: totalUSD,
		RequestFingerprint: requestFP, PurchaseFingerprint: purchaseFP,
		SupplierRequest: supplierRequest, ValidatedCoinAmount: validateRes.CoinAmount,
		EndUserIP: info.IP, EndUserAgent: string(ctx.Request.Header.UserAgent()),
		CustomerEmail: req.Email, PhoneNumber: req.PhoneNumber,
		BeneficiaryAccount: beneficiary,
		Coin:               req.Coin, Network: req.Network,
		PaymentMethod:       req.PaymentMethod,
		CashbackDestination: req.CashbackDestination,
		Anonymous:           req.Anonymous,
		// TEST MODE: the quote rides the REAL pipeline, but its payment is
		// simulated end to end (no supplier order, TESTSIM- attach, the
		// customer pays through the simulated pay button).
		TestMode: h.Cfg.TestMode,
		// Gift notification metadata — already canonicalized and checked
		// against this order's contacts above, and persisted BEFORE the
		// supplier call so the tracker can send the email when fulfillment
		// lands. An empty channel means "not a gift": the message is
		// deliberately gone with it.
		GiftChannel:            gift.Channel,
		GiftMessage:            gift.Message,
		GifterIdenticonDataURI: req.GifterIdenticon,
		CashbackCode:           cashbackView.Code,
		CashbackCodeBps:        cashbackView.Bps,
		CashbackCodeMaxUSD:     cashbackView.PromoCapUSD,
		Status:                 "order_creating",
		// Provisional deadline while the supplier order is still being
		// created; the final ExpiresAt is set from PaymentExpiryFor after
		// the supplier returns the invoice (documented window − 5m buffer).
		ExpiresAt: now.Add(cryptorefills.PaymentWindow - cryptorefills.PaymentSafetyBuffer), CreatedAt: now, UpdatedAt: now,
	}
	if snap := currentRates(); snap.nimUSD > 0 {
		q.NimUsdRate = snap.nimUSD
		if q.ProductUSD > 0 {
			q.EstimatedNIM = float64(q.ProductUSD) / 1_000_000 / snap.nimUSD
		}
	}
	if err := h.Store.CreateQuoteWithPurchaseLimits(q, h.Cfg.DailyOrderLimit, money.FromFloat(h.Cfg.DailySpendLimitUSD), money.FromFloat(h.Cfg.MonthlySpendLimitUSD), now,
		h.quoteGateOptions(&q, &cashbackView)); err != nil {
		if h.quoteGateError(ctx, err) {
			return
		}
		if h.quoteReservationConflict(ctx, err, userID, idempotencyKey, requestFP) {
			return
		}
		if errors.Is(err, db.ErrMonthlyLimit) {
			h.writeMonthlyLimitError(ctx, userID, q.ProductUSD, "price")
			return
		}
		if errors.Is(err, db.ErrLimit) {
			// Buyer-facing wording: the common case is "this order costs more
			// than what's left of today's budget" — say THAT, with the buyer's
			// actual numbers, instead of a generic "limit reached, wait". The
			// response also carries DAILY_LIMIT_EXCEEDED so the storefront
			// shows the daily-limit screen rather than a rate-limit retry.
			h.writeDailyLimitError(ctx, userID, q.ProductUSD, "price")
			return
		}
		writeError(ctx, fasthttp.StatusInternalServerError, "could not reserve quote")
		return
	}

	// TEST MODE: everything above ran for real (validation, pricing, rules,
	// the write-ahead quote) — from here NO real supplier order is created.
	// Attach a simulated invoice instead; the customer "pays" it with the
	// simulated pay button (POST /api/quotes/{id}/test-pay).
	if h.Cfg.TestMode {
		h.finishSimulatedCreation(ctx, q, faceUSD, func(latest db.Quote) {
			h.writeQuoteCreatedExtra(ctx, latest, map[string]any{"simulated_payment": true})
		})
		return
	}

	if !h.claimSupplierDispatch(ctx, q) {
		return
	}
	order, err := h.CR.CreateOrder(h.supplierContext(ctx), orderReq)
	if err != nil {
		h.supplierCreateFailed(ctx, q, err)
		return
	}
	h.finishSupplierCreation(ctx, q, order, nil)
}

// nimEstimateForBTC converts a BTC amount to an informational NIM estimate
// using both oracles. Any failure returns 0 (field omitted): the exact NIM
// amount is always shown by Nimiq Pay at approval time.
func (h *Handlers) nimEstimateForBTC(btcAmount string) float64 {
	btc, err := strconv.ParseFloat(strings.TrimSpace(btcAmount), 64)
	snap := currentRates()
	if err != nil || btc <= 0 || snap.nimUSD <= 0 || snap.btcUSD <= 0 || time.Since(snap.updatedAt) > 5*time.Minute {
		return 0
	}
	// Informational only, never an extra network call in a payment handler.
	return btc * snap.btcUSD / snap.nimUSD
}

// familyMeta is the cached supplier-side identity of a family, used for
// both the phone-number requirement and the admin catalog-rule gate.
type familyMeta struct {
	Products       []cryptorefills.Product
	Unavailable    bool
	Known          bool
	BrandName      string
	Kind           string
	Category       string
	AdditionalCats []string
	MinFaceUSD     float64
	OutOfStock     bool
	// Currency is the family's LOCAL currency code ("IDR", "TRY", "USD"…)
	// read from the supplier's structured range/amount data. The admin's
	// USD price cap is applied through catalog.ToUSD with this.
	Currency string
	// DeliveryType is the supplier's per-product delivery channel
	// ("by_email" | "by_phone") — the most direct signal for what
	// beneficiary_account must carry.
	DeliveryType string
	// BrandByDenom maps a fixed denomination label to the originating supplier brand.
	// Needed when one family name fans out to multiple brands (e.g. Turk Telecom Data / Credits / Bundle
	// share family "Turk Telecom" but each denomination belongs to a distinct brand). The map lets the
	// checkout send the correct brand for the selected SKU instead of always using the first family's brand.
	BrandByDenom     map[string]string
	BrandByProductID map[string]string
}

// isTopUpProduct reports whether the family is a mobile top-up whose
// beneficiary_account must be the recipient's E.164 phone number. Gift
// cards and eSIMs (kind mobile_recharge, category e-sim) are delivered to
// the end-user's EMAIL and must never receive a phone.
func isTopUpProduct(m familyMeta) bool {
	return m.deliveryChannel() == "by_phone"
}

// deliveryChannel is the family-level supplier channel ("by_phone" |
// "by_email" | "by_account"). Category is ONLY the fallback for a family
// that reports no delivery_type at all. Mixed families keep "" and must be
// resolved per SKU with deliveryChannelFor.
func (m familyMeta) deliveryChannel() string {
	if dt := strings.ToLower(strings.TrimSpace(m.DeliveryType)); dt != "" {
		return dt
	}
	if m.Kind == "mobile_recharge" && !strings.EqualFold(m.Category, "e-sim") {
		return "by_phone"
	}
	return "by_email"
}

// deliveryChannelFor resolves ONE selected SKU: product_id → denomination
// label → range product → family default. ROOT FIX for "asked for an email
// on a phone product" (33 live families mix both channels under one name).
func (m familyMeta) deliveryChannelFor(productID, denomination string) string {
	pid := strings.TrimSpace(productID)
	den := strings.TrimSpace(denomination)
	for _, p := range m.Products {
		if pid != "" && p.ProductID == pid && p.DeliveryType != "" {
			return strings.ToLower(p.DeliveryType)
		}
	}
	if den != "" && !strings.EqualFold(den, "range") {
		for _, p := range m.Products {
			if (p.Denomination == den || p.LocalizedDenomination == den) && p.DeliveryType != "" {
				return strings.ToLower(p.DeliveryType)
			}
		}
	}
	if strings.EqualFold(den, "range") {
		for _, p := range m.Products {
			if p.Range != nil && p.DeliveryType != "" {
				return strings.ToLower(p.DeliveryType)
			}
		}
	}
	return m.deliveryChannel()
}

// isTopUpSelection is isTopUpProduct for one concrete SKU.
func isTopUpSelection(m familyMeta, productID, denomination string) bool {
	return m.deliveryChannelFor(productID, denomination) == "by_phone"
}

// ---- gift note ------------------------------------------------------------

// giftMessageMax caps the personal text a buyer may attach to a gift note.
// It is the limit of the email that carries it — the whole message is stored
// and the whole message is sent.
const giftMessageMax = 2000

// giftNote is the validated, storable form of one gift notification.
type giftNote struct {
	// Channel is "" when this is not a gift at all, and "email" when it is —
	// the only channel the shop has ever kept. There is no SMS note.
	Channel string
	// Message is the buyer's personal text. Never a code.
	Message string
}

// needsEmail is the whole channel rule for a new note: it goes to an inbox.
// The checkout (src/lib/giftNote.ts) and this file answer with the same rule,
// which is why the UI has no channel picker — a second option would be an
// option the API refuses.
func (g giftNote) needsEmail() bool { return g.Channel == "email" }

// normalizeGiftNoteShape binds the caller's gift fields to one canonical form.
// Called before the request/purchase fingerprints are taken, so a retry with
// the same key can never store a different note. Clearing the message along
// with an empty channel is the other half of the point: an orphan "gift
// message" with no channel used to sit on a self-purchase and could still be
// rendered as a gift on the order.
func normalizeGiftNoteShape(channel, message string) (giftNote, error) {
	var n giftNote
	n.Message = strings.TrimSpace(message)
	// Empty channel → not a gift. Anything NON-empty that is not exactly
	// "email" is refused rather than dropped: a buyer who asked for a channel
	// we do not send must hear that, not watch their note quietly vanish.
	if strings.TrimSpace(channel) == "" {
		return giftNote{}, nil
	}
	n.Channel = strings.ToLower(strings.TrimSpace(channel))
	if n.Channel != "email" {
		// A refusal, not a downgrade: silently dropping the channel would
		// leave a buyer who asked for a text wondering why only an email
		// went out.
		return giftNote{}, fmt.Errorf(`gift notes are sent by email only — set gift_channel to "email" (the shop does not send SMS)`)
	}
	if utf8.RuneCountInString(n.Message) > giftMessageMax {
		return n, fmt.Errorf("the gift message is too long — %d characters is the limit", giftMessageMax)
	}
	return n, nil
}

// normalizeGifterIdenticon keeps (or drops) the buyer's identicon for the
// gift email. The rules:
//   - an anonymous gift NEVER carries it — the avatar is as identifying as
//     the name, and the anonymous seal at Build() is the second wall anyway;
//   - only a channel that actually sends a note ("email") needs one;
//   - it must be a decodable PNG data URI of a sane size — the email pipeline
//     re-draws it as bgcolor cells (Gmail blocks data: images), so anything
//     exotic is dropped to the note's placeholder avatar rather than refused:
//     a gift must not fail because an avatar did not rasterize.
func normalizeGifterIdenticon(uri string, anonymous bool, channel string) string {
	if uri == "" || anonymous || channel != "email" {
		return ""
	}
	const prefix = "data:image/png;base64,"
	if !strings.HasPrefix(uri, prefix) {
		return ""
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(strings.TrimPrefix(uri, prefix)))
	if err != nil {
		return ""
	}
	if len(raw) > 48<<10 {
		return "" // a 160px identicon PNG is ~14-18KB; anything bigger is not one
	}
	cfg, err := png.DecodeConfig(bytes.NewReader(raw))
	if err != nil || cfg.Width < 16 || cfg.Height < 16 || cfg.Width > 320 || cfg.Height > 320 {
		return ""
	}
	return prefix + base64.StdEncoding.EncodeToString(raw)
}

// requireGiftContacts refuses a note that could not be delivered. A note is
// emailed, so the one thing it needs is a working address, and it needs it on
// EVERY product kind: on an emailed order the delivery address is already that
// address, but on a top-up cart the buyer has to give one — the credit lands on
// a number, and a number cannot hold a gift message. That is why the checkout
// asks a top-up gift buyer for an email again (see src/lib/giftNote.ts).
func requireGiftContacts(n giftNote, email string) (giftNote, error) {
	if n.Channel == "" {
		return giftNote{}, nil
	}
	if n.needsEmail() && !validEmail(strings.TrimSpace(email)) {
		return n, errors.New("a gift note needs a valid recipient email — the note is only ever sent by email")
	}
	return n, nil
}

// singleQuoteLine builds the one-line delivery manifest for a single-item
// quote. It mirrors exactly what createQuoteBatchInner records per line, so
// single and batch orders describe their delivery identically.
func singleQuoteLine(req createQuoteRequest, meta familyMeta, denomLabel, beneficiary string) db.QuoteLine {
	kind, channel := "gift_card", "email"
	switch {
	case isTopUpSelection(meta, req.ProductID, req.Denomination):
		kind, channel = "topup", "phone"
	case strings.EqualFold(meta.Category, "e-sim") || strings.EqualFold(meta.Kind, "esim"):
		kind = "esim"
	}
	faceLabel := denomLabel
	if strings.EqualFold(req.Denomination, "range") && req.ProductValue > 0 {
		faceLabel = strconv.FormatFloat(req.ProductValue, 'g', -1, 64)
	}
	faceValue, _, faceCurrency, _ := meta.resolveSelection(req.Denomination, req.ProductValue)
	return db.QuoteLine{
		ProductValue: faceValue, ProductCurrency: faceCurrency,
		ProductID: req.ProductID, Country: req.Country, Denomination: denomLabel,
		Quantity: req.Quantity, Kind: kind,
		DeliveryChannel: channel, DeliveryTarget: beneficiary,
		FaceLabel: faceLabel,
	}
}

// familyMetaCacheKey shapes the family-metadata cache key in ONE place.
// The language is part of the key because the metadata is read straight out
// of the supplier's language-dependent family payload — without it a Turkish
// buyer's entry would answer a German one.
func familyMetaCacheKey(family, country, lang string) string {
	return "family:meta:" + strings.ToLower(family) + ":" + country + ":" + lang
}

// lookupFamilyMeta fetches the product family (cached) and returns its
// metadata. It is best-effort: on any lookup failure the caller proceeds
// and the supplier's own validation is the final authority. Successful
// metadata is cached 1h; a MISS/empty result is cached only 30s so a
// transient supplier glitch cannot leave the phone-required check broken
// for 10 minutes (the old code cached the zero meta exactly that long).
func (h *Handlers) lookupFamilyMeta(ctx *fasthttp.RequestCtx, family, country string) familyMeta {
	lang := supplierLang(ctx)
	cacheKey := familyMetaCacheKey(family, country, lang)
	if v, ok := h.cache.get(cacheKey); ok {
		if m, ok := v.(familyMeta); ok {
			return m
		}
	}
	var meta familyMeta
	fams, err := h.CR.ProductsByCountry(h.supplierContext(ctx), country, family, "", lang)
	if err == nil && len(fams) > 0 {
		// CryptoRefills may return multiple entries for the same family name (e.g. Turk Telecom
		// Data / Credits / Bundle each with family="Turk Telecom" but distinct brands/categories).
		// Merge all products so validation and SKU lookup see the full 20-item catalogue, not just the first 4.
		var allProducts []cryptorefills.Product
		brandByDenom := make(map[string]string)
		brandByPID := make(map[string]string)
		for _, f := range fams {
			for _, p := range f.Products {
				allProducts = append(allProducts, p)
				if p.Denomination != "" {
					if _, exists := brandByDenom[p.Denomination]; !exists && f.Brand != "" {
						brandByDenom[p.Denomination] = f.Brand
					}
				}
				if p.ProductID != "" && f.Brand != "" {
					if _, exists := brandByPID[p.ProductID]; !exists {
						brandByPID[p.ProductID] = f.Brand
					}
				}
			}
		}
		f0 := fams[0]
		// Known if ANY variant is known, OutOfStock only if ALL variants are out of stock
		known := false
		allOut := true
		for _, f := range fams {
			if !f.OutOfStock && (f.Family != "" || f.Brand != "" || f.BrandID != "") {
				known = true
			}
			if !f.OutOfStock {
				allOut = false
			}
		}
		meta = familyMeta{
			Products:         allProducts,
			Known:            known,
			BrandName:        f0.Brand,
			Kind:             f0.Kind,
			Category:         f0.Category,
			AdditionalCats:   f0.AdditionalCats,
			OutOfStock:       allOut,
			BrandByDenom:     brandByDenom,
			BrandByProductID: brandByPID,
		}
		if meta.BrandName == "" {
			meta.BrandName = family
		}
		// Currency / min / delivery detection across merged products
		for _, p := range allProducts {
			if meta.Currency == "" {
				if p.Range != nil {
					meta.Currency = p.Range.Currency
				}
				if meta.Currency == "" {
					_, code := catalog.ProductMoney(p)
					meta.Currency = code
				}
			}
			if p.Range != nil {
				if meta.MinFaceUSD == 0 || p.Range.Min < meta.MinFaceUSD {
					meta.MinFaceUSD = p.Range.Min
				}
			}
		}
		// Family-level channel only when every product agrees; a mixed family
		// keeps "" so isTopUpSelection() resolves per SKU.
		{
			seen := map[string]bool{}
			for _, p := range allProducts {
				if dt := strings.ToLower(strings.TrimSpace(p.DeliveryType)); dt != "" {
					seen[dt] = true
				}
			}
			if len(seen) == 1 {
				for dt := range seen {
					meta.DeliveryType = dt
				}
			} else {
				meta.DeliveryType = ""
			}
		}
		h.cache.setTTL(cacheKey, meta, ttlFamilyMeta)
	} else {
		meta.Unavailable = err == nil && len(fams) == 0
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

// resolveFaceValue validates selection shape, NOT a label's numeric content.
// Fixed SKU pricing is supplied separately by the catalog / live invoice.
func resolveFaceValue(denomination string, value float64) (float64, string, error) {
	s := strings.TrimSpace(denomination)
	if strings.EqualFold(s, "range") {
		if value <= 0 || value >= maxFaceValue || math.IsNaN(value) || math.IsInf(value, 0) {
			return 0, "", errors.New("product_value must be positive for range products")
		}
		return value, "range", nil
	}
	if s == "" {
		return 0, "", errors.New("denomination is required")
	}
	return 0, s, nil
}

// Exact opaque label matching selects a SKU; no part of its name is parsed.
// The caller's fixed product_value is never an authoritative price.
func (m familyMeta) resolveSelection(denomination string, value float64) (float64, string, string, error) {
	face, label, err := resolveFaceValue(denomination, value)
	if err != nil {
		return 0, "", "", err
	}
	for _, p := range m.Products {
		if label == "range" && p.Range != nil {
			return face, label, p.Range.Currency, nil
		}
		if label != "range" && p.Denomination == label {
			amount, currency := catalog.ProductMoney(p)
			return amount, label, currency, nil
		}
	}
	// Supplier dry-run remains the authority on selection/stock. Never invent a
	// price or a country currency when fixed-price metadata is missing.
	if label == "range" {
		return face, label, m.Currency, nil
	}
	return 0, label, "", nil
}

func quoteIDNow() string {
	return uuid.NewString()
}

// ListUserQuotes returns the authenticated user's quotes newest-first.
func (h *Handlers) ListUserQuotes(ctx *fasthttp.RequestCtx) {
	userID := middleware.UserID(ctx)
	quotes, err := h.Store.ListQuotesForUser(userID, 100)
	if err != nil {
		writeError(ctx, fasthttp.StatusInternalServerError, "could not load quotes")
		return
	}
	now := time.Now().UTC()
	for i := range quotes {
		quotes[i] = quotes[i].PublicQuote(now)
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
	out := map[string]interface{}{
		"test_mode":   h.Cfg.TestMode,
		"quote":       q.PublicQuote(time.Now().UTC()),
		"can_pay":     q.CanPay(time.Now().UTC()),
		"fulfillment": fulfillment,
		"refund":      quoteRefundView(q),
	}
	if cb, err := h.Store.GetCashbackByQuote(q.ID); err == nil {
		out["cashback"] = map[string]interface{}{
			"status":          cb.Status,
			"amount_nim":      float64(cb.AmountLuna) / 100000.0,
			"amount_luna":     cb.AmountLuna,
			"bps":             cb.Bps,
			"memo":            cb.Memo,
			"tx_hash":         cb.TxHash,
			"skip_reason":     cb.SkipReason,
			"paid_at":         cb.PaidAt,
			"cashback_source": cb.CashbackSource,
			"cashback_code":   cb.CashbackCode,
		}
	} else {
		h.attachQuoteCashbackEstimate(out, q, q.EstimatedNIM)
	}
	writeJSON(ctx, fasthttp.StatusOK, out)
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
			out["supplier_refund"] = q.Refund
		}
		return out
	case "failed":
		if q.RefundReason != "" {
			out := map[string]interface{}{
				"status": "failed",
				"detail": "The supplier could not complete this payment. A refund has not been confirmed. If you sent funds, contact Cryptorefills or open a support ticket before another payment.",
			}
			return out
		}
	}
	return nil
}
