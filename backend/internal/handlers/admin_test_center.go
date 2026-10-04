package handlers

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/valyala/fasthttp"

	"nimiqshop/internal/catalog"
	"nimiqshop/internal/cryptorefills"
	"nimiqshop/internal/db"
	"nimiqshop/internal/i18n"
	"nimiqshop/internal/loopback"
	"nimiqshop/internal/money"
	"nimiqshop/internal/phone"
	"nimiqshop/internal/settlement"
	"nimiqshop/internal/testutil"
)

/* admin_test_center.go — the operator's end-to-end sandbox.
 *
 * WHAT IT IS: buy a REAL product from the REAL catalog through the SAME
 * checkout pipeline (validation, pricing, catalog rules, write-ahead quote,
 * payment attach) — but against a SIMULATED supplier, then drive the payment
 * through the SAME state machine the real tracker/webhook path uses
 * (ApplySupplierOrder → publishToFeed → settlement.NotifyFulfilled), so the
 * gift email, the activity feed and the admin order views are byte-for-byte
 * what a real buyer's order produces.
 *
 * WHAT IT NEVER DOES: touch the real Cryptorefills API, move real money,
 * or leave the safety rails (see Quote.TestMode): no real supplier poll, no
 * active-checkout hold, no daily budget consumption. Everything else RUNS:
 * the cashback row is enqueued by the real rate engine and paid by the real
 * worker with a SIMULATED TESTTX- hash (no signing, no RPC), tree
 * contributions are recorded, the wallet memo goes through the same policy
 * pipeline simulated. The only real side effects are the gift email through
 * Mailtrap and local records — that is the point of the test.
 *
 * RAILS: every payment flow the shop exposes — Nimiq Pay (BTC Lightning
 * invoice) and USDT (Polygon) — gets the same simulated auto-pay; only the
 * fake wallet's shape differs (checksum-valid lnbc… vs 0x…).
 *
 * Guard: admin session required, and only TestMode quotes (supplier ids
 * TESTSIM-*) can ever be driven by test-pay.
 */

// testCenterUserID is the synthetic buyer every test purchase is booked on:
// one stable identity so the operator can find all test orders in People,
// without touching any real customer's stats.
const testCenterUserID = "test-center"

// testCenterSampleWallet is the gifter wallet shown on test gift emails —
// the same sample the standalone test-email endpoint uses.
const testCenterSampleWallet = "NQ08 D44A 44B9 0F77 2E22 8C13 C345 6FBD XKH9"

type adminTestPurchaseRequest struct {
	ProductID           string  `json:"product_id"`
	Country             string  `json:"country"`
	Denomination        string  `json:"denomination,omitempty"`
	ProductValue        float64 `json:"product_value,omitempty"`
	Quantity            int     `json:"quantity,omitempty"`
	Email               string  `json:"email"`
	PhoneNumber         string  `json:"phone_number,omitempty"`
	GiftMessage         string  `json:"gift_message,omitempty"`
	Anonymous           bool    `json:"anonymous,omitempty"`
	GifterIdenticon     string  `json:"gifter_identicon,omitempty"`
	PaymentMethod       string  `json:"payment_method,omitempty"` // nimiq_pay | usdt_polygon
	CashbackDestination string  `json:"cashback_destination,omitempty"`
	CashbackCode        string  `json:"cashback_code,omitempty"`
}

// AdminTestPurchase buys a real catalog product on the simulated supplier.
//
//	POST /api/admin/test-purchase
//
// Response: the same payment payload a real quote returns, plus
// "simulated": true.
func (h *Handlers) AdminTestPurchase(ctx *fasthttp.RequestCtx) {
	identity := adminIdentity(ctx)
	var req adminTestPurchaseRequest
	if err := readJSON(ctx, &req); err != nil || req.ProductID == "" || req.Country == "" {
		writeError(ctx, fasthttp.StatusBadRequest, "product_id and country are required")
		return
	}
	req.ProductID = strings.TrimSpace(req.ProductID)
	req.Country = strings.ToUpper(strings.TrimSpace(req.Country))
	req.Email = strings.TrimSpace(req.Email)
	req.Denomination = strings.TrimSpace(req.Denomination)
	req.PhoneNumber = strings.TrimSpace(req.PhoneNumber)
	req.CashbackCode = strings.ToUpper(strings.TrimSpace(req.CashbackCode))
	if len(req.Country) != 2 {
		writeError(ctx, fasthttp.StatusBadRequest, "country must be a 2-letter code")
		return
	}
	if req.Quantity < 1 {
		req.Quantity = 1
	}
	if req.Quantity > h.Cfg.MaxOrderQuantity {
		writeError(ctx, fasthttp.StatusBadRequest, "invalid quantity")
		return
	}
	// Same rail rules as the real checkout: default Nimiq Pay (BTC
	// Lightning), or USDT on Polygon. Unknown ids are a hard 400.
	method := strings.ToLower(strings.TrimSpace(req.PaymentMethod))
	if method == "" {
		method = PaymentMethodNIM
	}
	if method != PaymentMethodNIM && method != PaymentMethodUSDT {
		writeError(ctx, fasthttp.StatusBadRequest, "payment_method must be nimiq_pay or usdt_polygon")
		return
	}
	coin, network := PaymentCoinNIM, PaymentNetworkNIM
	if method == PaymentMethodUSDT {
		coin, network = PaymentCoinUSDT, PaymentNetworkStable
	}
	dest := strings.ToLower(strings.TrimSpace(req.CashbackDestination))
	if dest != db.CashbackDestBurn {
		dest = db.CashbackDestWallet
	}

	// Gift note: same canonicalization as the checkout (channel is email —
	// the note's one and only carrier), same anonymous/avatar rules.
	gift, giftErr := normalizeGiftNoteShape(giftChannelFor(req.GiftMessage), req.GiftMessage)
	if giftErr != nil {
		writeError(ctx, fasthttp.StatusBadRequest, giftErr.Error())
		return
	}
	req.GifterIdenticon = normalizeGifterIdenticon(req.GifterIdenticon, req.Anonymous, gift.Channel)

	if req.PhoneNumber != "" {
		norm, err := phone.Normalize(req.PhoneNumber, req.Country)
		if err != nil {
			writeError(ctx, fasthttp.StatusBadRequest, err.Error())
			return
		}
		req.PhoneNumber = norm
	}
	normalizeQuoteSelection(&req.Denomination, &req.ProductValue)
	if req.Denomination == "" {
		writeError(ctx, fasthttp.StatusBadRequest, "denomination is required (e.g. \"100 USD\" or \"range\")")
		return
	}

	// Real catalog: same metadata source, same availability and rules gates.
	meta := h.lookupFamilyMeta(ctx, req.ProductID, req.Country)
	if !meta.Known {
		if meta.Unavailable {
			writeError(ctx, 409, "this product is not available; no order was created")
			return
		}
		writeError(ctx, 503, "product metadata cannot be verified; no order was created")
		return
	}
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
		if err := phone.Validate(req.PhoneNumber); err != nil {
			writeError(ctx, fasthttp.StatusBadRequest, err.Error())
			return
		}
	}
	gift, giftErr = requireGiftContacts(gift, req.Email)
	if giftErr != nil {
		writeError(ctx, fasthttp.StatusBadRequest, giftErr.Error())
		return
	}

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

	// One synthetic buyer for every test purchase (stable, findable, real
	// records — support flows work on it like on any user).
	if _, err := h.Store.GetOrCreateUserByID(testCenterUserID, testCenterSampleWallet); err != nil {
		writeError(ctx, fasthttp.StatusInternalServerError, "could not ready the test buyer")
		return
	}
	cashbackView, err := h.resolveQuoteCashback(req.CashbackCode, testCenterUserID)
	if err != nil {
		writeError(ctx, fasthttp.StatusBadRequest, err.Error())
		return
	}
	req.CashbackCode = cashbackView.Code

	beneficiary := req.Email
	if isTopUpProduct(meta) {
		beneficiary = req.PhoneNumber
	}
	now := time.Now().UTC()
	idem := "test-center-" + uuid.NewString()
	inner := createQuoteRequest{
		ProductID: req.ProductID, Country: req.Country,
		Denomination: req.Denomination, ProductValue: req.ProductValue,
		Quantity: req.Quantity, Email: req.Email, PhoneNumber: req.PhoneNumber,
		Coin: coin, PaymentMethod: method,
	}
	delivery := cryptorefills.Delivery{
		BrandName: meta.BrandName, CountryCode: req.Country,
		Denomination: denomLabel, BeneficiaryAccount: beneficiary,
	}
	// A range product is selected by the amount the buyer typed: the supplier
	// needs product_value to price THAT amount. Without it the delivery only
	// says "range", which the supplier answers with OUT_OF_STOCK — the sandbox
	// could not test a single range product. The customer paths already set
	// this (quote_handlers.go, quote_batch.go); this one now matches them
	// verbatim, including the forced "range" label the docs require.
	if strings.EqualFold(denomLabel, "range") {
		v := req.ProductValue
		delivery.ProductValue = &v
		delivery.Denomination = "range"
	}
	supplierReq := &cryptorefills.CreateOrderRequest{
		Deliveries: []cryptorefills.Delivery{delivery},
		Payment:    cryptorefills.OrderPayment{Type: "via", PaymentVia: "USER_WALLET", Coin: coin, Network: network},
		Lang:       "en", // admin UI is English in supplier metadata; the email respects quote.Lang below.
	}
	if req.Email != "" {
		supplierReq.Email = req.Email
		supplierReq.User = &cryptorefills.OrderUser{Email: req.Email}
	}
	// The supplier sets the price, so the sandbox must ask it — exactly like a
	// real checkout does. Before this call the panel could only offer a
	// face-value guess (face currency -> USD via the FX table -> BTC), which
	// runs a few percent under what the supplier actually charges, because the
	// supplier's price carries its own FX and margin. Validation is read-only:
	// no order is created, no money moves, and the simulated supplier still
	// fulfils this quote below.
	validateRes, err := h.CR.ValidateOrder(h.supplierContext(ctx), supplierReq)
	if err != nil {
		h.supplierError(ctx, err, "order could not be validated")
		return
	}
	liveUSD, priceErr := quotedUSD(validateRes.CoinAmount, method, currentRates().btcUSD, faceUSD, faceUSD > 0)
	if priceErr != nil {
		writeError(ctx, fasthttp.StatusServiceUnavailable, priceErr.Error())
		return
	}
	if rules.MaxFaceValueUSD > 0 && liveUSD > rules.MaxFaceValueUSD {
		writeError(ctx, fasthttp.StatusForbidden, "orders above the current price cap are not accepted")
		return
	}
	supplierRequest, _ := cryptorefills.MarshalCreateRequest(supplierReq)
	testLang := i18n.ParseLangCtx(ctx)
	i18n.SetCookieCtx(ctx, testLang)
	q := db.Quote{
		ID: quoteIDNow(), UserID: testCenterUserID, Lang: testLang,
		ProductID: req.ProductID, ProductCountry: req.Country,
		Denomination: denomLabel, ProductValue: faceValue, ProductCurrency: faceCurrency, Quantity: req.Quantity,
		Lines:          []db.QuoteLine{singleQuoteLine(inner, meta, denomLabel, beneficiary)},
		IdempotencyKey: idem, ProductUSD: money.FromFloat(liveUSD),
		RequestFingerprint: "admin-test-center/" + idem,
		SupplierRequest:    supplierRequest,
		// Same two fields the customer quote carries: the supplier's own
		// validated amount is the invoice truth, and every simulated artefact
		// below (coin amount, NIM snapshot) is derived from it.
		ValidatedCoinAmount: validateRes.CoinAmount,
		EndUserIP:           loopback.IPv4.String(), EndUserAgent: "admin-test-center",
		CustomerEmail: req.Email, PhoneNumber: req.PhoneNumber,
		BeneficiaryAccount: beneficiary,
		Coin:               coin, Network: network,
		PaymentMethod:       method,
		CashbackDestination: dest,
		Anonymous:           req.Anonymous,
		GiftChannel:         gift.Channel, GiftMessage: gift.Message,
		GifterIdenticonDataURI: req.GifterIdenticon,
		// The whole point of this flag: real pipeline, simulated supplier,
		// no real money downstream.
		TestMode: true,
	}
	if snap := currentRates(); snap.nimUSD > 0 {
		q.NimUsdRate = snap.nimUSD
		q.EstimatedNIM = liveUSD / snap.nimUSD
	}
	// The operator sandbox keeps the production gate but is exempt from the
	// quote-attempt ceiling: an operator testing a flow fifty times in a row
	// is the intended use of the test center, not an abuse pattern.
	if err := h.Store.CreateQuoteWithPurchaseLimits(q, h.Cfg.DailyOrderLimit, money.FromFloat(h.Cfg.DailySpendLimitUSD), money.FromFloat(h.Cfg.MonthlySpendLimitUSD), now,
		h.quoteGateOptions(&q, &cashbackView, false)); err != nil {
		writeError(ctx, fasthttp.StatusConflict, "test quote could not be created: "+err.Error())
		return
	}

	// Simulated supplier attach: a fake order id, a rail-correct fake wallet
	// and the shop's own price as the coin amount — the exact fields the
	// Cryptorefills attach persists.
	supplierOrderID := "TESTSIM-" + uuid.NewString()[:8]
	wallet := simulatedWallet(method, q.ID, coin)
	amount := simulatedCoinAmount(faceUSD, method, q.ValidatedCoinAmount)
	expiry := now.Add(cryptorefills.PaymentWindow - cryptorefills.PaymentSafetyBuffer)
	if err := h.Store.AttachQuotePayment(q.ID, supplierOrderID, wallet, coin, amount, network, expiry); err != nil {
		_ = h.Store.MarkQuoteManualReview(q.ID, "test-center attach failed: "+err.Error())
		writeError(ctx, fasthttp.StatusInternalServerError, "simulated attach failed")
		return
	}
	if method == PaymentMethodNIM {
		if est := h.nimEstimateForBTC(amount); est > 0 {
			_ = h.Store.SetQuoteNIMSnapshot(q.ID, currentRates().nimUSD, est)
		}
	}
	latest, err := h.Store.GetQuote(q.ID)
	if err != nil {
		writeError(ctx, fasthttp.StatusInternalServerError, "could not read the test quote")
		return
	}
	h.audit(identity.User.ID, "admin.test_purchase", ctx,
		"quote="+latest.ID+" product="+req.ProductID+"/"+req.Country+" method="+method+" email="+req.Email)
	h.writeQuoteCreatedExtra(ctx, latest, map[string]any{
		"simulated":   true,
		"test_center": true,
	})
}

// giftChannelFor maps "did the operator write a note?" to the note channel:
// a message means an emailed gift note; no message means none.
func giftChannelFor(message string) string {
	if strings.TrimSpace(message) == "" {
		return ""
	}
	return "email"
}

// simulatedWallet builds the rail-correct fake destination: a checksum-valid
// BOLT11 Lightning invoice for Nimiq Pay, a 0x… address for USDT on Polygon.
func simulatedWallet(method, quoteID, coin string) string {
	if method == PaymentMethodUSDT {
		sum := sha256.Sum256([]byte("test-center-usdt:" + quoteID))
		return "0x" + hex.EncodeToString(sum[:])[:40]
	}
	// A real checksum-valid lnbc invoice (bech32), amount-prefixed like the
	// supplier's — wallets and the QR/lightning: URI path treat it exactly
	// like a real one. Nobody can ever pay it: the test-pay endpoint is the
	// only thing that can settle this order.
	return testutil.Invoice("0.00001", "test-center:"+quoteID, time.Now().UTC())
}

// simulatedCoinAmount prices the cart on the chosen rail: USDT at face value,
// BTC via the always-warm oracle rate (the same snapshot the shop's own
// estimates use).
// simulatedCoinAmount prices the cart on the chosen rail. The FIRST choice
// is the supplier's own validated amount for this exact cart — the real
// price the buyer would be invoiced (fee and live rate included, straight
// from the same validation that gates checkout). Only when no validated
// amount exists (admin test-center quotes that never touched the supplier)
// does it re-derive a price from face USD and the live oracle snapshot.
// A fabricated 1:1 rate is never served while a real price is known.
func simulatedCoinAmount(faceUSD float64, method string, validated string) string {
	if v, err := strconv.ParseFloat(strings.TrimSpace(validated), 64); err == nil && v > 0 && !math.IsInf(v, 0) && !math.IsNaN(v) {
		// Preserve the supplier decimal exactly. Rounding USDT to cents can
		// turn a valid small invoice into zero and desynchronise cashback.
		return strings.TrimSpace(validated)
	}
	if method == PaymentMethodUSDT {
		return strconv.FormatFloat(faceUSD, 'f', 2, 64)
	}
	snap := currentRates()
	if snap.btcUSD <= 0 {
		snap.btcUSD = 1 // oracle cold: keep the test runnable (amount is cosmetic)
	}
	return strconv.FormatFloat(faceUSD/snap.btcUSD, 'f', 8, 64)
}

// finishSimulatedCreation is the TEST_MODE end of the normal checkout: the
// quote is already persisted (real validation, real pricing, real rules) —
// here it gets a SIMULATED payment attached (TESTSIM- supplier id, rail-
// correct fake wallet, the supplier's validated price as the amount) instead
// of a real supplier order, and the same created-quote payload is returned
// to the customer.
func (h *Handlers) finishSimulatedCreation(ctx *fasthttp.RequestCtx, q db.Quote, faceUSD float64, respond func(db.Quote)) {
	method := paymentMethodOf(q)
	supplierOrderID := "TESTSIM-" + uuid.NewString()[:8]
	wallet := simulatedWallet(method, q.ID, q.Coin)
	amount := simulatedCoinAmount(faceUSD, method, q.ValidatedCoinAmount)
	expiry := time.Now().UTC().Add(cryptorefills.PaymentWindow - cryptorefills.PaymentSafetyBuffer)
	if err := h.Store.AttachQuotePayment(q.ID, supplierOrderID, wallet, q.Coin, amount, q.Network, expiry); err != nil {
		_ = h.Store.MarkQuoteManualReview(q.ID, "simulated attach failed: "+err.Error())
		writeError(ctx, fasthttp.StatusInternalServerError, "simulated payment could not be attached")
		return
	}
	if method == PaymentMethodNIM {
		if est := h.nimEstimateForBTC(amount); est > 0 {
			_ = h.Store.SetQuoteNIMSnapshot(q.ID, currentRates().nimUSD, est)
		}
	}
	latest, err := h.Store.GetQuote(q.ID)
	if err != nil {
		writeError(ctx, fasthttp.StatusInternalServerError, "could not read the quote")
		return
	}
	respond(latest)
}

// driveTestQuote walks a test quote through the payment ladder on the SAME
// state machine the real tracker path uses (ApplySupplierOrder) and fires
// the same settlement hooks on fulfillment. Shared by the admin test center
// and the TEST_MODE customer pay button. "auto" walks the whole rail:
// broadcast → confirmed → delivering → delivered.
func (h *Handlers) driveTestQuote(q db.Quote, action string) (db.Quote, []string, error) {
	action = strings.ToLower(strings.TrimSpace(action))
	if action == "" {
		action = "auto"
	}
	type step struct {
		supplier string
		sent     bool
	}
	var ladder []step
	switch action {
	case "broadcast":
		ladder = []step{{supplier: cryptorefills.StatusPaymentStarted}}
	case "confirm":
		ladder = []step{{supplier: cryptorefills.StatusPaymentReceived, sent: true}}
	case "deliver":
		ladder = []step{{supplier: cryptorefills.StatusWaitingForDelivery}, {supplier: cryptorefills.StatusDone, sent: true}}
	case "auto":
		ladder = []step{
			{supplier: cryptorefills.StatusPaymentStarted},
			{supplier: cryptorefills.StatusPaymentReceived, sent: true},
			{supplier: cryptorefills.StatusWaitingForDelivery},
			{supplier: cryptorefills.StatusDone, sent: true},
		}
	default:
		return q, nil, errBadTestAction
	}

	applied := make([]string, 0, len(ladder))
	var latest db.Quote
	for _, st := range ladder {
		order := &cryptorefills.Order{
			ID:            q.SupplierOrderID,
			Status:        st.supplier,
			Coin:          q.Coin,
			CoinAmount:    q.CoinAmount,
			WalletAddress: q.WalletAddress,
			Network:       q.Network,
			// Timestamps stay zero: the state machine's stale-observation
			// check then never interferes with the driven transitions.
		}
		if st.sent {
			// The supplier reports the money that landed — exactly the
			// invoice amount, like a fully-paid real order.
			order.SentCoinAmount = q.CoinAmount
		}
		if st.supplier == cryptorefills.StatusDone {
			order.Deliveries = simulatedDeliveries(q)
		}
		changed, err := h.Store.ApplySupplierOrder(q.ID, order)
		if err != nil {
			break // an illegal transition is not an error to hide: report it
		}
		var gerr error
		latest, gerr = h.Store.GetQuote(q.ID)
		if gerr != nil {
			break
		}
		if !changed {
			// The state machine refused the step (already past it, or the
			// quote is terminal): not a transition, so it is not reported
			// as one. A fully-driven quote therefore answers "no
			// transition applied" instead of pretending to move again.
			if latest.Status == cryptorefills.QuoteFulfilled {
				break
			}
			continue
		}
		applied = append(applied, st.supplier)
		if latest.Status == cryptorefills.QuoteFulfilled {
			// The SAME post-commit hook the tracker and the webhook path
			// fire: gift mail (Mailtrap), feed, admin views, cashback row.
			settlement.NotifyFulfilled(latest)
			break
		}
	}
	if len(applied) == 0 {
		return q, nil, errNoTestTransition
	}
	if latest.ID == "" {
		latest = q
	}
	return latest, applied, nil
}

var (
	errBadTestAction    = errors.New("action must be broadcast, confirm, deliver or auto")
	errNoTestTransition = errors.New("no transition applied")
)

// simulatedDeliveries is the fake redemption payload a delivered test order
// carries — shaped exactly like a real supplier delivery so the order detail
// and the customer code views render identically.
func simulatedDeliveries(q db.Quote) []cryptorefills.Delivery {
	d := cryptorefills.Delivery{
		Kind:               "giftcard",
		BrandName:          q.ProductID,
		FamilyName:         q.ProductID,
		CountryCode:        q.ProductCountry,
		Denomination:       q.Denomination,
		BeneficiaryAccount: q.BeneficiaryAccount,
	}
	d.Deliverable.BrandName = q.ProductID
	d.Deliverable.Denomination = q.Denomination
	d.Deliverable.CountryCode = q.ProductCountry
	// FulfillmentPayload maps PinCode→code and PinSerial→pin — the same
	// fields a real gift-card delivery carries.
	d.PinCode = "TEST-CODE-" + strings.ToUpper(q.ID[len(q.ID)-6:])
	d.PinSerial = "4321"
	d.RedeemInstructions = "This is a simulated delivery — no real product was purchased (test mode)."
	d.HowToRedeem = "Nothing to redeem: this code exists only to exercise the delivery views."
	return []cryptorefills.Delivery{d}
}

// ---- test-pay: the fake auto-pay -------------------------------------

type adminTestPayRequest struct {
	QuoteID string `json:"quote_id"`
	Action  string `json:"action"` // broadcast | confirm | deliver | auto (default)
}

// AdminTestPay drives a test quote through the payment lifecycle on the SAME
// state machine the real tracker path uses (ApplySupplierOrder), and fires
// the same settlement hooks on fulfillment.
//
//	POST /api/admin/test-pay  { "quote_id":"…", "action":"auto" }
func (h *Handlers) AdminTestPay(ctx *fasthttp.RequestCtx) {
	identity := adminIdentity(ctx)
	var req adminTestPayRequest
	if err := readJSON(ctx, &req); err != nil || req.QuoteID == "" {
		writeError(ctx, fasthttp.StatusBadRequest, "quote_id is required")
		return
	}
	q, err := h.Store.GetQuote(req.QuoteID)
	if err != nil {
		writeError(ctx, fasthttp.StatusNotFound, "quote not found")
		return
	}
	if !q.TestMode || !strings.HasPrefix(q.SupplierOrderID, "TESTSIM-") {
		writeError(ctx, fasthttp.StatusForbidden, "only admin test-center quotes can be driven by test-pay")
		return
	}
	latest, applied, derr := h.driveTestQuote(q, req.Action)
	if derr != nil {
		if errors.Is(derr, errBadTestAction) {
			writeError(ctx, fasthttp.StatusBadRequest, derr.Error())
			return
		}
		writeError(ctx, fasthttp.StatusConflict, "no transition applied (current status: "+q.Status+")")
		return
	}
	h.audit(identity.User.ID, "admin.test_pay", ctx,
		"quote="+q.ID+" action="+strings.ToLower(strings.TrimSpace(req.Action))+" applied="+strings.Join(applied, ">"))
	writeJSON(ctx, fasthttp.StatusOK, testQuoteView(h.Store, latest, applied))
}

// AdminTestQuoteStatus reports a test quote's current state (the panel polls
// this while auto-pay settles).
//
//	GET /api/admin/test-quote/{id}
func (h *Handlers) AdminTestQuoteStatus(ctx *fasthttp.RequestCtx) {
	id, _ := ctx.UserValue("id").(string)
	q, err := h.Store.GetQuote(id)
	if err != nil {
		writeError(ctx, fasthttp.StatusNotFound, "quote not found")
		return
	}
	if !q.TestMode {
		writeError(ctx, fasthttp.StatusForbidden, "not a test-center quote")
		return
	}
	writeJSON(ctx, fasthttp.StatusOK, testQuoteView(h.Store, q, nil))
}

func testQuoteView(store *db.Store, q db.Quote, applied []string) map[string]any {
	out := map[string]any{
		"ok":                 true,
		"simulated":          true,
		"quote_id":           q.ID,
		"status":             q.Status,
		"supplier_status":    q.SupplierStatus,
		"supplier_order_id":  q.SupplierOrderID,
		"payment_method":     paymentMethodOf(q),
		"coin":               q.Coin,
		"coin_amount":        q.CoinAmount,
		"network":            q.Network,
		"payment_expires_at": q.PaymentExpiry,
		"payment_observed":   q.PaymentObserved,
		"can_pay":            q.CanPay(time.Now().UTC()),
		"product_id":         q.ProductID,
		"country":            q.ProductCountry,
		"denomination":       q.Denomination,
		"quantity":           q.Quantity,
		"customer_email":     q.CustomerEmail,
		"gift_channel":       q.GiftChannel,
		"gift_message":       q.GiftMessage,
		"anonymous":          q.Anonymous,
		"gift_notified_at":   q.GiftNotifiedAt,
		"test_mode":          q.TestMode,
	}
	if q.WalletAddress != "" && (q.CanPay(time.Now().UTC()) || q.Status == "awaiting_payment") {
		out["wallet_address"] = q.WalletAddress
		if cryptorefills.IsBOLT11(q.WalletAddress) {
			out["lightning_invoice"] = q.WalletAddress
			out["payment_uri"] = cryptorefills.LightningURI(q.WalletAddress)
		}
	}
	if len(q.Fulfillment) > 0 {
		out["fulfillment"] = q.Fulfillment
	}
	if len(applied) > 0 {
		out["applied"] = applied
	}
	// The cashback row (real pipeline; the worker pays TestMode rows with a
	// simulated TESTTX- hash) so the panel can watch queued → paid.
	if cb, err := store.GetCashbackByQuote(q.ID); err == nil {
		out["cashback"] = map[string]any{
			"status":      cb.Status,
			"amount_nim":  float64(cb.AmountLuna) / 100000.0,
			"amount_luna": cb.AmountLuna,
			"bps":         cb.Bps,
			"tx_hash":     cb.TxHash,
			"destination": cb.CashbackDestination,
			"test_mode":   cb.TestMode,
			"skip_reason": cb.SkipReason,
			"last_error":  cb.LastError,
		}
	}
	return out
}
