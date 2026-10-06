package handlers

// CreateQuoteBatch — the WHOLE CART in ONE supplier order and ONE Lightning
// invoice. CryptoRefills v5 takes a deliveries ARRAY per order, so N
// different cart items never need N separate payments: one POST /v5/orders
// with one Delivery per item returns ONE wallet address whose invoice
// already embeds the total price. The buyer scans/pays once; the supplier
// fulfills every delivery on that single order.
//
// Why a separate endpoint instead of extending POST /api/quotes: the single
// route's semantics (one product per quote) are baked into dedupe, admin
// tooling and the settlement tracker; a batch quote IS still one local quote
// row (and therefore one tracker job, one webhook, one fulfillment payload
// with every delivery's code inside), just with a combined display label.
//
// Failure semantics: the supplier validates the batch as a WHOLE — if one
// item is invalid the entire order is refused with the problem list, and
// nothing is charged. The frontend then either retries, drops the offending
// item, or falls back to the per-item checkout.

import (
	"errors"
	"fmt"
	"strings"
	"time"

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

const (
	batchMaxItems      = 10 // distinct line items in one checkout (sane upper bound)
	batchMaxUnitsTotal = 40 // total delivered units in one checkout
)

type batchQuoteItem struct {
	ProductID    string  `json:"product_id"`
	Country      string  `json:"country"`
	Brand        string  `json:"brand,omitempty"`
	BrandID      string  `json:"brand_id,omitempty"`
	Denomination string  `json:"denomination,omitempty"`
	ProductValue float64 `json:"product_value,omitempty"`
	Quantity     int     `json:"quantity,omitempty"`
	PhoneNumber  string  `json:"phone_number,omitempty"`
	// Gift note as the checkout collected it: "email" (or "" for no gift) plus
	// the buyer's text. The note is chosen ONCE per cart (one toggle, one
	// recipient) and the frontend stamps it on every row, so these are read off
	// the first row that carries a channel and cleared from the items — see
	// batchQuoteRequest. Without these fields the decoder simply dropped them
	// and a multi-item cart's gift note never existed. The note has one
	// carrier, the email: there is no phone recipient field any more.
	GiftChannel string `json:"gift_channel,omitempty"`
	GiftMessage string `json:"gift_message,omitempty"`
	// GifterIdenticon rides with the note (see createQuoteRequest); hoisted
	// off the first row that carries it, like the message.
	GifterIdenticon string `json:"gifter_identicon,omitempty"`
}

type batchQuoteRequest struct {
	// AckActiveCheckout: "continue anyway" on the unresolved-checkout hold.
	AckActiveCheckout bool `json:"ack_active_checkout,omitempty"`

	Items []batchQuoteItem `json:"items"`
	Email string           `json:"email,omitempty"`
	// The cart's gift note, hoisted out of the item rows before validation so
	// a batch — which is ONE local quote — notifies once, not once per line.
	GiftChannel         string `json:"gift_channel,omitempty"`
	GiftMessage         string `json:"gift_message,omitempty"`
	GifterIdenticon     string `json:"gifter_identicon,omitempty"`
	CashbackCode        string `json:"cashback_code,omitempty"`
	PaymentMethod       string `json:"payment_method,omitempty"`
	CashbackDestination string `json:"cashback_destination,omitempty"`
	// Anonymous keeps this checkout in the public activity feed while hiding
	// the buyer wallet identity and payment transaction details. The buyer
	// still sees the complete batch in their own Orders.
	Anonymous bool `json:"anonymous,omitempty"`
	// set by the handler before supplier calls
	Coin    string `json:"-"`
	Network string `json:"-"`
}

// CreateQuoteBatch handles POST /api/quotes/batch.
func (h *Handlers) CreateQuoteBatch(ctx *fasthttp.RequestCtx) {
	userID := middleware.UserID(ctx)
	var req batchQuoteRequest
	if err := readJSON(ctx, &req); err != nil {
		writeError(ctx, fasthttp.StatusBadRequest, "invalid request body")
		return
	}
	if len(req.Items) == 0 {
		writeError(ctx, fasthttp.StatusBadRequest, "items is required")
		return
	}
	if len(req.Items) > batchMaxItems {
		writeError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("a batch checkout is limited to %d distinct items", batchMaxItems))
		return
	}
	req.Email = strings.TrimSpace(req.Email)
	req.CashbackCode = strings.TrimSpace(req.CashbackCode)
	req.PaymentMethod = strings.ToLower(strings.TrimSpace(req.PaymentMethod))
	// Default payment rail: Nimiq Pay (BTC Lightning). USDT on Polygon
	// (Matic) is always available as the second rail — no env switch. An
	// unknown method id is a hard 400, never a silent rail switch.
	if req.PaymentMethod == "" {
		req.PaymentMethod = PaymentMethodNIM
	}
	if req.PaymentMethod != PaymentMethodNIM && req.PaymentMethod != PaymentMethodUSDT {
		writeError(ctx, fasthttp.StatusBadRequest, "payment_method must be nimiq_pay or usdt_polygon")
		return
	}
	if IsStablecoinMethod(req.PaymentMethod) {
		req.Coin = PaymentCoinUSDT
		req.Network = PaymentNetworkStable
	} else {
		req.PaymentMethod = PaymentMethodNIM
		req.Coin = PaymentCoinNIM
		req.Network = PaymentNetworkNIM
	}
	dest := strings.ToLower(strings.TrimSpace(req.CashbackDestination))
	if dest != db.CashbackDestBurn {
		dest = db.CashbackDestWallet
	}
	// Stablecoin orders earn reduced cashback but still support burn.
	// Codes/staker multipliers still apply; the 50% multiplier is applied at
	// enqueue time (see IsStablecoinMethod), so nothing changes here.
	req.CashbackDestination = dest
	totalUnits := 0
	for i := range req.Items {
		it := &req.Items[i]
		it.ProductID = strings.TrimSpace(it.ProductID)
		it.Country = strings.ToUpper(strings.TrimSpace(it.Country))
		it.Brand = strings.TrimSpace(it.Brand)
		it.BrandID = strings.TrimSpace(it.BrandID)
		it.Denomination = strings.TrimSpace(it.Denomination)
		it.PhoneNumber = strings.TrimSpace(it.PhoneNumber)
		if it.PhoneNumber != "" {
			normalized, err := phone.Normalize(it.PhoneNumber, it.Country)
			if err != nil {
				writeError(ctx, 400, err.Error())
				return
			}
			it.PhoneNumber = normalized
		}
		if it.ProductID == "" || len(it.Country) != 2 {
			writeError(ctx, fasthttp.StatusBadRequest, "every item needs product_id and a 2-letter country")
			return
		}
		if it.Quantity < 1 {
			it.Quantity = 1
		}
		if it.Quantity > h.Cfg.MaxOrderQuantity {
			writeError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("%s: quantity above the per-item limit (%d)", it.ProductID, h.Cfg.MaxOrderQuantity))
			return
		}
		normalizeQuoteSelection(&it.Denomination, &it.ProductValue)
		// ---- gift note: read it once for the whole cart -------------------
		// Any row may carry it (the checkout stamps them all with the same
		// data); the first non-empty one wins, is canonicalized, and is then
		// cleared from the item — otherwise two identical lines with different
		// gift text would refuse to merge, and the request fingerprint would
		// depend on how many rows repeated the same note.
		if it.GiftChannel != "" || it.GiftMessage != "" || it.GifterIdenticon != "" {
			note, nerr := normalizeGiftNoteShape(it.GiftChannel, it.GiftMessage)
			if nerr != nil {
				writeError(ctx, fasthttp.StatusBadRequest, nerr.Error())
				return
			}
			if req.GiftChannel == "" && note.Channel != "" {
				req.GiftChannel, req.GiftMessage = note.Channel, note.Message
			}
			if req.GifterIdenticon == "" && it.GifterIdenticon != "" {
				req.GifterIdenticon = it.GifterIdenticon
			}
			it.GiftChannel, it.GiftMessage, it.GifterIdenticon = "", "", ""
		}
		totalUnits += it.Quantity
	}
	// The envelope may also carry the note directly (an API caller posting one
	// order for a whole cart). Shape it exactly like the rows were shaped, so
	// both entry points store byte-identical values and the fingerprint sees
	// that value.
	if req.GiftChannel != "" || req.GiftMessage != "" {
		note, nerr := normalizeGiftNoteShape(req.GiftChannel, req.GiftMessage)
		if nerr != nil {
			writeError(ctx, fasthttp.StatusBadRequest, nerr.Error())
			return
		}
		req.GiftChannel, req.GiftMessage = note.Channel, note.Message
	}
	req.GifterIdenticon = normalizeGifterIdenticon(req.GifterIdenticon, req.Anonymous, req.GiftChannel)
	// Identical lines collapse into one delivery row with a summed quantity:
	// the total is untouched, but one product can never be priced twice in
	// contradictory ways inside a single supplier order.
	req.Items = mergeBatchItems(req.Items)
	for i := range req.Items {
		if req.Items[i].Quantity > h.Cfg.MaxOrderQuantity {
			writeError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("%s: quantity above the per-item limit (%d) after merging duplicate cart lines", req.Items[i].ProductID, h.Cfg.MaxOrderQuantity))
			return
		}
	}
	if totalUnits > batchMaxUnitsTotal {
		writeError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("a batch checkout is limited to %d units in total", batchMaxUnitsTotal))
		return
	}
	if len(req.Items) == 1 {
		// Single-item cart through the batch route: run the exact single
		// flow so every guard (duplicate, gift, point-cap) stays identical.
		it := req.Items[0]
		h.createQuoteInner(ctx, userID, createQuoteRequest{
			ProductID: it.ProductID, Country: it.Country, Denomination: it.Denomination,
			ProductValue: it.ProductValue, Quantity: it.Quantity,
			Email: req.Email, PhoneNumber: it.PhoneNumber, CashbackCode: req.CashbackCode,
			PaymentMethod: req.PaymentMethod, CashbackDestination: req.CashbackDestination,
			// A cart whose lines merged into one is still a gift: hand the
			// hoisted note over, or the buyer's message dies here.
			GiftChannel: req.GiftChannel, GiftMessage: req.GiftMessage, GifterIdenticon: req.GifterIdenticon,
		})
		return
	}
	h.createQuoteBatchInner(ctx, userID, req)
}

func (h *Handlers) createQuoteBatchInner(ctx *fasthttp.RequestCtx, userID string, req batchQuoteRequest) {
	unlock := lockCreateQuote(userID)
	defer unlock()
	req.CashbackCode = strings.ToUpper(strings.TrimSpace(req.CashbackCode))
	idempotencyKey, ok := checkoutKey(ctx)
	if !ok {
		return
	}
	requestFP, purchaseFP := batchFingerprints(req)
	if h.existingCheckout(ctx, userID, idempotencyKey, requestFP, purchaseFP, req.AckActiveCheckout) {
		return
	}

	cashbackView, err := h.resolveQuoteCashback(req.CashbackCode, userID)
	if err != nil {
		writeError(ctx, fasthttp.StatusBadRequest, err.Error())
		return
	}
	req.CashbackCode = cashbackView.Code

	rules, err := h.Store.GetCatalogRules()
	if err != nil {
		writeError(ctx, fasthttp.StatusInternalServerError, "could not load catalog policy")
		return
	}

	// ---- per-item local validation + delivery building --------------------
	deliveries := make([]cryptorefills.Delivery, 0, batchMaxUnitsTotal)
	// Per-line deliveries, kept so a failed batch can be diagnosed line by
	// line ("which item broke the cart?") without rebuilding anything.
	perItemDeliveries := make([][]cryptorefills.Delivery, len(req.Items))
	totalUSD := 0.0
	totalUnits := 0
	brands := make([]string, 0, len(req.Items))
	labels := make([]string, 0, len(req.Items))
	// Per-line delivery manifest: the buyer-visible truth about HOW each line
	// arrives. Built here (where topUp/beneficiary are already resolved) so no
	// surface has to guess from a joined label.
	lines := make([]db.QuoteLine, 0, len(req.Items))
	// "You get" for a batch: the face value SUMMED PER CURRENCY. The old
	// frontend parsed the joined denomination label and showed only the
	// FIRST item's value as if it were the whole cart — a 3-item cart
	// displayed "50 USD" next to an invoice for all three. The server owns
	// this number now; it is display-only and never used for pricing.
	faceByCurrency := make(map[string]float64, 4)
	// Only a COMPLETE total may be displayed: a partial sum next to an
	// invoice covering more items is a wrong number on the pay sheet.
	faceTotalsComplete := true
	allPricesKnown := true
	seenAnyCard := false // does ANY item deliver to an email?
	// allFaceKnown: every item has an ISO currency, so totalFaceUSD is a
	// trustworthy expectation to check the supplier invoice against.
	for i, it := range req.Items {
		meta := h.lookupFamilyMeta(ctx, it.ProductID, it.Country)
		if !meta.Known {
			if meta.Unavailable {
				blocked := []blockedItem{{Index: i, ProductID: it.ProductID, Reason: "this product is not available right now"}}
				writeJSON(ctx, 409, map[string]any{"error": "some items cannot be ordered", "detail": blockedItemsMessage(blocked), "blocked_items": blocked,
					"supplier_error": true, "code": "SUPPLIER_PROBLEMS", "problems": itemProblems(err), "removable": true, "remaining": len(req.Items) - 1})
				return
			}
			writeError(ctx, 503, "product metadata cannot be verified; no order was created")
			return
		}
		// lookupFamilyMeta is best-effort: on a supplier hiccup it returns a
		// ZERO meta, and a zero meta reads as "not a top-up" — which used to
		// send a mobile recharge to the buyer's EMAIL as its beneficiary.
		// The supplier then refused the whole cart with an opaque
		// INVALID_BENEFICIARY_ACCOUNT. An explicit phone number on the line
		// is unambiguous intent, so trust it when the metadata is missing.
		if meta.deliveryChannelFor(it.ProductID, it.Denomination) == "by_account" {
			blocked := []blockedItem{{Index: i, ProductID: it.ProductID, Reason: "delivered to a utility account number — not supported"}}
			writeJSON(ctx, 409, map[string]any{"error": "some items cannot be ordered", "detail": blockedItemsMessage(blocked), "blocked_items": blocked, "removable": true, "remaining": len(req.Items) - 1})
			return
		}
		topUp := isTopUpSelection(meta, it.ProductID, it.Denomination)
		if !topUp && it.PhoneNumber != "" && meta.DeliveryType == "" && meta.Kind == "" {
			topUp = true
		}
		if !topUp {
			if req.Email == "" || !validEmail(req.Email) {
				writeError(ctx, fasthttp.StatusBadRequest, "a valid delivery email is required (the products are delivered to it)")
				return
			}
			seenAnyCard = true
		}
		beneficiary := req.Email
		if topUp {
			if it.PhoneNumber == "" {
				writeError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("%s: phone_number is required for mobile top-ups (E.164)", it.ProductID))
				return
			}
			norm, nerr := phone.Normalize(it.PhoneNumber, it.Country)
			if nerr != nil {
				writeError(ctx, fasthttp.StatusBadRequest, nerr.Error())
				return
			}
			it.PhoneNumber = norm
			beneficiary = norm
		}
		faceValue, denomLabel, faceCurrency, ferr := meta.resolveSelection(it.Denomination, it.ProductValue)
		if ferr != nil {
			writeError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("%s: %s", it.ProductID, ferr.Error()))
			return
		}
		meta.Currency = faceCurrency
		totalFace := faceValue * float64(it.Quantity)
		faceUSD := 0.0
		minFaceUSD := 0.0
		if catalog.CurrencyKnown(meta.Currency) && faceValue > 0 {
			faceUSD = catalog.ToUSD(totalFace, meta.Currency)
			minFaceUSD = catalog.ToUSD(meta.MinFaceUSD, meta.Currency)
		}
		if gerr := catalog.GateQuote(&rules, it.ProductID, meta.Category, meta.Kind, it.Country, meta.AdditionalCats, minFaceUSD, faceUSD); gerr != nil {
			writeError(ctx, fasthttp.StatusForbidden, gerr.Error())
			return
		}
		// Currency for the display total: the family meta first, else the
		// denomination label's own code ("50 USD" -> USD). Only a currency
		// we actually know is shown — a bare number with no currency would
		// be worse than showing nothing.
		if totalFace > 0 {
			ccy := strings.ToUpper(strings.TrimSpace(meta.Currency))
			if ccy != "" {
				faceByCurrency[ccy] += totalFace
			} else {
				// An item whose currency we cannot name would silently drop
				// out of the displayed total, showing the buyer LESS than
				// the invoice covers. Suppress the whole display instead.
				faceTotalsComplete = false
			}
		} else if faceValue <= 0 {
			// Point-based / unpriced line: it is part of the invoice but has
			// no face value to add, so any total we render would understate
			// the cart.
			faceTotalsComplete = false
		}
		if faceUSD <= 0 {
			allPricesKnown = false
		}
		totalUSD += faceUSD
		totalUnits += it.Quantity

		brandForDelivery := meta.BrandName
		if it.Brand != "" {
			brandForDelivery = it.Brand
		} else if meta.BrandByDenom != nil {
			if b, ok := meta.BrandByDenom[denomLabel]; ok && b != "" {
				brandForDelivery = b
			}
		}
		delivery := cryptorefills.Delivery{
			BrandName:          brandForDelivery,
			CountryCode:        it.Country,
			Denomination:       denomLabel,
			BeneficiaryAccount: beneficiary,
		}
		if strings.EqualFold(it.Denomination, "range") {
			v := it.ProductValue
			delivery.ProductValue = &v
			delivery.Denomination = "range"
		} else {
			delivery.ProductValue = nil
		}
		// Quantity is already validated against MaxOrderQuantity (≤100), but
		// clamp again so the allocation size never depends on raw user input.
		qty := it.Quantity
		if qty < 1 {
			qty = 1
		}
		if qty > h.Cfg.MaxOrderQuantity {
			qty = h.Cfg.MaxOrderQuantity
		}
		if qty > 100 {
			qty = 100
		}
		// No capacity hint: qty is ≤100 so growth is negligible, and it keeps
		// the allocation size independent of request input.
		var lineDeliveries []cryptorefills.Delivery
		for k := 0; k < qty; k++ {
			deliveries = append(deliveries, delivery)
			lineDeliveries = append(lineDeliveries, delivery)
		}
		perItemDeliveries[i] = lineDeliveries
		_ = i
		unitLabel := denomLabel
		if strings.EqualFold(it.Denomination, "range") {
			unitLabel = fmt.Sprintf("%g", it.ProductValue)
		}
		if it.Quantity > 1 {
			brands = append(brands, fmt.Sprintf("%s ×%d", it.ProductID, it.Quantity))
			labels = append(labels, fmt.Sprintf("%s (%s ×%d)", it.ProductID, unitLabel, it.Quantity))
		} else {
			brands = append(brands, it.ProductID)
			labels = append(labels, fmt.Sprintf("%s (%s)", it.ProductID, unitLabel))
		}
		lineKind := "gift_card"
		lineChannel := "email"
		switch {
		case topUp:
			lineKind, lineChannel = "topup", "phone"
		case strings.EqualFold(meta.Category, "e-sim") || strings.EqualFold(meta.Kind, "esim"):
			lineKind = "esim"
		}
		lines = append(lines, db.QuoteLine{
			ProductValue: faceValue, ProductCurrency: faceCurrency,
			ProductID: it.ProductID, Country: it.Country, Denomination: denomLabel,
			Quantity: it.Quantity, Kind: lineKind,
			DeliveryChannel: lineChannel, DeliveryTarget: beneficiary,
			FaceLabel: unitLabel,
		})
	}

	// A note needs one thing: an address to be emailed to. Checked BEFORE the
	// supplier dry-run, so an undeliverable note never costs the buyer an
	// invoice they cannot use (and a cart of only top-ups now has to give that
	// address, because nothing in it is emailed by itself).
	gift, giftErr := requireGiftContacts(
		giftNote{Channel: req.GiftChannel, Message: req.GiftMessage},
		req.Email,
	)
	if giftErr != nil {
		writeError(ctx, fasthttp.StatusBadRequest, giftErr.Error())
		return
	}

	// ---- supplier DRY-RUN on the WHOLE batch (free, no order created) -----
	validateReq := &cryptorefills.CreateOrderRequest{
		Deliveries: deliveries,
		Payment:    cryptorefills.OrderPayment{Type: "via", PaymentVia: "USER_WALLET", Coin: req.Coin, Network: req.Network},
		Lang:       "en",
	}
	if req.Email != "" {
		validateReq.Email = req.Email
		validateReq.User = &cryptorefills.OrderUser{Email: req.Email}
	}
	validateRes, err := h.CR.ValidateOrder(h.supplierContext(ctx), validateReq)
	if err != nil {
		// WHICH ITEM BROKE THE CART? The supplier refuses the batch as a
		// whole and never names the offending delivery, so re-validate each
		// line on its own (free dry-runs, in parallel) and hand the frontend
		// an actionable list: "Remove <product> and continue".
		if supplierBlamesItem(err) {
			if blocked := diagnoseBatch(h.supplierContext(ctx), h.CR, req.Items, perItemDeliveries, req.Email, validateReq.Payment); len(blocked) > 0 && len(blocked) < len(req.Items) {
				writeJSON(ctx, fasthttp.StatusConflict, map[string]interface{}{
					"error":         "some items cannot be ordered",
					"detail":        blockedItemsMessage(blocked),
					"blocked_items": blocked,
					"removable":     true,
					"remaining":     len(req.Items) - len(blocked),
				})
				return
			}
		}
		h.supplierError(ctx, err, "the cart could not be validated as one order")
		return
	}
	liveUSD, priceErr := quotedUSD(validateRes.CoinAmount, req.PaymentMethod, currentRates().btcUSD, totalUSD, allPricesKnown)
	if priceErr != nil {
		writeError(ctx, 503, priceErr.Error())
		return
	}
	if rules.MaxFaceValueUSD > 0 && liveUSD > rules.MaxFaceValueUSD {
		writeError(ctx, fasthttp.StatusForbidden, "orders above the current price cap are not accepted")
		return
	}
	totalUSD = liveUSD
	validatePromoOrderCap(&cashbackView, liveUSD)

	// Preserve the buyer's selected language for async emails/retry. Resolved
	// before the supplier request so Cryptorefills also sees the buyer locale.
	quoteLang := i18n.ParseLangCtx(ctx)
	i18n.SetCookieCtx(ctx, quoteLang)

	orderReq := &cryptorefills.CreateOrderRequest{Deliveries: validateReq.Deliveries, Payment: validateReq.Payment, User: validateReq.User, Lang: quoteLang, Acquisition: &cryptorefills.Acquisition{UTMSource: "nimshop"}}
	supplierRequest, err := cryptorefills.MarshalCreateRequest(orderReq)
	if err != nil {
		writeError(ctx, 400, "invalid supplier request")
		return
	}
	info := clientip.Resolve(ctx, h.Cfg.TrustProxy, h.Cfg.ClientIPPolicy())
	// The cart path never noted the buyer's origin (only the single-product
	// one did), so a cart-only customer was invisible in the operator's
	// People panel. Same resolved info, no second header parse.
	h.noteUserPresenceFrom(userID, info)
	// ---- write-ahead local quote (ONE row for the whole cart) -------------
	now := time.Now().UTC()
	if !faceTotalsComplete {
		faceByCurrency = nil
	}
	q := db.Quote{
		IsBatch: true, BatchItems: len(req.Items), FaceValueTotals: batchFaceTotals(faceByCurrency),
		Lines: lines,
		ID:    quoteIDNow(), UserID: userID, Lang: quoteLang,
		// ProductID carries the joined brand list — it is exactly what the
		// orders page renders as the row title. Country stays empty: a batch
		// may mix countries, and a wrong single flag would render a wrong
		// country badge.
		ProductID: strings.Join(brands, " + "), ProductCountry: "",
		Denomination: strings.Join(labels, " + "), ProductValue: 0,
		Quantity:       totalUnits,
		IdempotencyKey: idempotencyKey, ProductUSD: money.FromFloat(totalUSD),
		RequestFingerprint: requestFP, PurchaseFingerprint: purchaseFP,
		SupplierRequest: supplierRequest, ValidatedCoinAmount: validateRes.CoinAmount,
		EndUserIP: info.IP, EndUserAgent: string(ctx.Request.Header.UserAgent()),
		CustomerEmail: req.Email, PhoneNumber: "", BeneficiaryAccount: batchBeneficiary(req.Email, seenAnyCard),
		// The cart's ONE gift note — hoisted from the rows and checked against
		// this cart's contacts above. Without this the batch quote row carried
		// no gift at all and the tracker had nothing to send.
		GiftChannel: gift.Channel, GiftMessage: gift.Message, GifterIdenticonDataURI: req.GifterIdenticon,
		Coin: req.Coin, Network: req.Network,
		PaymentMethod:       req.PaymentMethod,
		CashbackDestination: req.CashbackDestination,
		Anonymous:           req.Anonymous,
		// TEST MODE: real validation and pricing, simulated payment (see
		// createQuoteInner's branch — the batch shares the semantics).
		TestMode:           h.Cfg.TestMode,
		CashbackCode:       cashbackView.Code,
		CashbackCodeBps:    cashbackView.Bps,
		CashbackCodeMaxUSD: cashbackView.PromoCapUSD,
		Status:             "order_creating",
		ExpiresAt:          now.Add(cryptorefills.PaymentWindow - cryptorefills.PaymentSafetyBuffer), CreatedAt: now, UpdatedAt: now,
	}
	// Both rails need a locked NIM/USD rate for their NIM cashback basis.
	// The final supplier invoice replaces this provisional estimate at attach.
	if snap := currentRates(); snap.nimUSD > 0 {
		q.NimUsdRate = snap.nimUSD
		if q.ProductUSD > 0 {
			q.EstimatedNIM = float64(q.ProductUSD) / 1_000_000 / snap.nimUSD
		}
	}
	if err := h.Store.CreateQuoteWithPurchaseLimits(q, h.Cfg.DailyOrderLimit, money.FromFloat(h.Cfg.DailySpendLimitUSD), money.FromFloat(h.Cfg.MonthlySpendLimitUSD), now,
		h.quoteGateOptions(&q, &cashbackView, req.AckActiveCheckout)); err != nil {
		if h.quoteGateError(ctx, err) {
			return
		}
		if h.quoteReservationConflict(ctx, err, userID, idempotencyKey, requestFP) {
			return
		}
		if errors.Is(err, db.ErrMonthlyLimit) {
			h.writeMonthlyLimitError(ctx, userID, q.ProductUSD, "cart")
			return
		}
		if errors.Is(err, db.ErrLimit) {
			// Same refusal as the single-item path, with the batch numbers:
			// code DAILY_LIMIT_EXCEEDED plus used/left/resets so the checkout
			// stops instead of re-attempting every item one by one.
			h.writeDailyLimitError(ctx, userID, q.ProductUSD, "cart")
			return
		}
		writeError(ctx, fasthttp.StatusInternalServerError, "could not reserve quote")
		return
	}

	// TEST MODE: no real supplier order — simulated attach, same batch
	// payload back to the customer (see createQuoteInner's branch).
	if h.Cfg.TestMode {
		if !faceTotalsComplete {
			faceByCurrency = nil
		}
		h.finishSimulatedCreation(ctx, q, totalUSD, func(latest db.Quote) {
			// Same payload writeBatchQuoteCreated produces, plus the
			// simulated-payment marker for the pay sheet.
			h.writeQuoteCreatedExtra(ctx, latest, map[string]any{
				"is_batch": true, "batch_items": len(req.Items),
				"face_value_totals": batchFaceTotals(faceByCurrency),
				"simulated_payment": true,
			})
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
	if !faceTotalsComplete {
		faceByCurrency = nil
	}
	h.finishSupplierCreation(ctx, q, order, map[string]any{"is_batch": true, "batch_items": len(req.Items), "face_value_totals": batchFaceTotals(faceByCurrency)})
}

// batchBeneficiary — the crash-recovery beneficiary for a batch quote: the
// email when any card/eSIM is inside, else empty (pure top-up batches carry
// the phone per delivery; the legacy fallback rule never invents one).
func batchBeneficiary(email string, anyCard bool) string {
	if anyCard {
		return email
	}
	return ""
}
