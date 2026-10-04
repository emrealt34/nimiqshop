package db

import (
	"encoding/json"
	"log"
	"time"

	"nimiqshop/internal/money"
)

func marshal(v interface{}) ([]byte, error)   { return json.Marshal(v) }
func unmarshal(b []byte, v interface{}) error { return json.Unmarshal(b, v) }
func logf(f string, v ...interface{})         { log.Printf(f, v...) }

// The structs below are the stored form of what used to be table rows.
// Differences from the SQL schema, all forced by Badger having no types:
//
//   - NUMERIC(18,6) columns become money.Micros (int64), not float64.
//   - TIMESTAMPTZ columns become time.Time, serialized as RFC3339 by JSON.
//   - Defaults that Postgres applied (gen_random_uuid(), now(), 'pending')
//     are applied in Go by the Create* methods instead.
//   - JSONB columns become json.RawMessage, stored inline.
//   - Foreign keys are not enforced by the store; the handlers already
//     only ever write ids they just read or created.

// User was the `users` table.
type User struct {
	ID           string    `json:"id"`
	NimiqAddress string    `json:"nimiq_address"`
	CreatedAt    time.Time `json:"created_at"`
	// LastIP/LastCountry/LastSeenAt are operational fields shown in the
	// admin console (IP↔country next to the Identicon avatar). They are
	// updated on login and on purchase attempts — best-effort, never a
	// substitute for the payment identity (the Nimiq address).
	LastIP      string    `json:"last_ip,omitempty"`
	LastCountry string    `json:"last_country,omitempty"`
	LastSeenAt  time.Time `json:"last_seen_at,omitempty"`
}

// Order was the `orders` table.
type Order struct {
	ID     string `json:"id"`
	UserID string `json:"user_id"`
	// Anonymous keeps this purchase in the public activity feed while hiding
	// the buyer wallet identity and payment transaction details. The buyer
	// still sees the complete order in their own Orders list.
	Anonymous         bool            `json:"anonymous,omitempty"`
	Kind              string          `json:"kind"`
	SupplierOrderID   *string         `json:"supplier_order_id,omitempty"`
	SupplierInvoiceID *string         `json:"supplier_invoice_id,omitempty"`
	CategoryID        string          `json:"category_id"`
	ProductID         string          `json:"product_id"`
	Quantity          int             `json:"quantity"`
	PriceUSD          money.Micros    `json:"price_usd"`
	Status            string          `json:"status"`
	IdempotencyKey    string          `json:"idempotency_key"`
	Payload           json.RawMessage `json:"payload,omitempty"`
	Fulfillment       json.RawMessage `json:"fulfillment,omitempty"`
	// NimUsdRate is the NIM/USD market rate snapshotted when the order was
	// placed. It lets the public feed express the USD price as an approximate
	// NIM figure at the rate that was live at purchase time.
	NimUsdRate float64 `json:"nim_usd_rate,omitempty"`
	// Refund is the supplier-side refund record, persisted when an order
	// ends in 'refunded'. shop.nimiqbase.com never holds funds: the supplier
	// (CryptoRefills, merchant of record) returns the paid amount.
	Refund json.RawMessage `json:"refund,omitempty"`
	// Rating is the 1-5 star rating the buyer left after delivery (0 = unrated).
	Rating    int        `json:"rating,omitempty"`
	RatedAt   *time.Time `json:"rated_at,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
}

// RatingAggregate is the running global star-rating summary. It is stored as a
// single meta record (meta:rating_aggregate) and updated atomically inside the
// same transaction that writes each rating, so the public average can never
// disagree with the individual ratings. Dist is 1-indexed: Dist[1..5] hold the
// count of each star value (Dist[0] unused).
type RatingAggregate struct {
	Count int    `json:"count"`
	Sum   int    `json:"sum"` // sum of all star values
	Dist  [6]int `json:"dist"`
}

// Average returns the mean rating to one decimal, or 0 when there are none.
func (a RatingAggregate) Average() float64 {
	if a.Count == 0 {
		return 0
	}
	return float64(a.Sum) / float64(a.Count)
}

// Quote is a Cryptorefills-backed purchase in any lifecycle state.
//
// Product identity is the supplier's family (brand) + country +
// denomination: e.g. ("Airbnb", "US", "100 USD"). The customer pays the
// supplier's one-time wallet address directly with the quoted coin amount
// (BTC Lightning, funded through the buyer’s Nimiq Pay conversion); Cryptorefills delivers the product
// to CustomerEmail (gift cards/eSIMs) or PhoneNumber (top-ups).
// QuoteLine is ONE cart line inside a quote, as the buyer sees it. It exists
// so the frontend never has to re-derive "how is this delivered?" by parsing a
// joined label: a cart can mix email-delivered gift cards with phone
// top-ups, and saying "lands in your email" for a cart that also contains a
// mobile top-up is simply wrong. The server owns this manifest.
type QuoteLine struct {
	ProductValue    float64 `json:"product_value,omitempty"`
	ProductCurrency string  `json:"product_currency,omitempty"`
	ProductID       string  `json:"product_id"`
	Country         string  `json:"country,omitempty"`
	Denomination    string  `json:"denomination,omitempty"`
	Quantity        int     `json:"quantity"`
	// Kind is the product family: "gift_card", "esim" or "topup".
	Kind string `json:"kind,omitempty"`
	// DeliveryChannel is how THIS line reaches the buyer: "email" (gift card
	// code / eSIM QR by mail) or "phone" (top-up credit onto a number).
	DeliveryChannel string `json:"delivery_channel,omitempty"`
	// DeliveryTarget is the concrete destination (masked for display by the
	// frontend when needed): the email address or the E.164 number.
	DeliveryTarget string `json:"delivery_target,omitempty"`
	// FaceLabel is the per-unit face value label ("50 USD", "30").
	FaceLabel string `json:"face_label,omitempty"`
}

type Quote struct {
	IsBatch         bool     `json:"is_batch,omitempty"`
	BatchItems      int      `json:"batch_items,omitempty"`
	FaceValueTotals []string `json:"face_value_totals,omitempty"`
	// Lines is the per-item delivery manifest (see QuoteLine). Present for
	// batch AND single quotes so every surface can render one honest
	// "delivered by email / by phone / email & phone" sentence.
	Lines            []QuoteLine `json:"lines,omitempty"`
	PaymentHandoffAt time.Time   `json:"payment_handoff_at,omitempty"`
	ID               string      `json:"id"`
	UserID           string      `json:"user_id"`
	// ProductID is the supplier family/brand name ("Airbnb", "t-mobile").
	ProductID      string `json:"product_id"`
	ProductCountry string `json:"product_country,omitempty"`
	// Denomination is the face value label ("100 USD") or "range".
	Denomination string `json:"denomination,omitempty"`
	// ProductValue is the face value in the product currency (USD for US).
	ProductCurrency     string  `json:"product_currency,omitempty"`
	ProductValue        float64 `json:"product_value,omitempty"`
	Quantity            int     `json:"quantity"`
	IdempotencyKey      string  `json:"idempotency_key,omitempty"`
	RequestFingerprint  string  `json:"request_fingerprint,omitempty"`
	PurchaseFingerprint string  `json:"purchase_fingerprint,omitempty"`
	// Exact flat supplier request, committed before the one-shot dispatch claim.
	SupplierRequest     json.RawMessage `json:"supplier_request,omitempty"`
	ValidatedCoinAmount string          `json:"validated_coin_amount,omitempty"`
	EndUserIP           string          `json:"end_user_ip,omitempty"`
	EndUserAgent        string          `json:"end_user_agent,omitempty"`
	SupplierUpdatedAt   time.Time       `json:"supplier_updated_at,omitempty"`
	PaymentObserved     bool            `json:"payment_observed,omitempty"`
	// PaidAt is the moment money was FIRST observed for this purchase — the
	// start of the public "avg delivery" window (paid → delivered).
	PaidAt               *time.Time `json:"paid_at,omitempty"`
	PaymentBlocked       bool       `json:"payment_blocked,omitempty"`
	LightningPaymentHash string     `json:"lightning_payment_hash,omitempty"`
	// ProductUSD is the cart's USD-equivalent value in micro-USD (approximate
	// FX at purchase time; used for order display and daily-limit accounting).
	// The LOCAL-currency face value lives in ProductValue/Denomination.
	ProductUSD money.Micros `json:"product_usd"`
	// CustomerEmail is the delivery recipient (mandatory: Cryptorefills
	// delivers the product to this address). PhoneNumber (strict E.164,
	// normalized server-side) is the top-up target.
	CustomerEmail string `json:"customer_email,omitempty"`
	PhoneNumber   string `json:"phone_number,omitempty"`
	// BeneficiaryAccount is the EXACT value sent to the supplier as
	// beneficiary_account for every delivery: the normalized E.164 phone
	// for mobile top-ups, the customer email for gift cards/eSIMs. It is
	// fixed at quote creation so crash recovery re-sends the identical
	// beneficiary instead of re-deriving it (legacy rows without it fall
	// back to phone-if-present, else email).
	BeneficiaryAccount string `json:"beneficiary_account,omitempty"`
	// Lang is the buyer's selected locale at quote creation time ("en",
	// "es", "de", "fr", "pt", "tr"). Drives the language of the gift
	// notification email (see mailtrap.GiftNote.Lang) so retries/async
	// fulfillment preserve the buyer's language. Empty means "unknown"
	// (legacy rows / defaulted to English by the builder).
	Lang string `json:"lang,omitempty"`
	// Gift notification metadata. Channel is "email" — the only channel the
	// shop has; the SMS note never existed in this build and the phone
	// recipient field is gone with it. When empty, this is a regular
	// self-purchase (no gift notification). The message is buyer-authored
	// personal text, capped once at quote time (giftMessageMax) and sent as
	// written.
	GiftChannel string `json:"gift_channel,omitempty"`
	GiftMessage string `json:"gift_message,omitempty"`
	// TestMode marks an ADMIN TEST CENTER purchase: created through the same
	// checkout pipeline, but against the simulated supplier (no Cryptorefills
	// call, no real money anywhere). Test quotes drive the REAL state
	// machine, feed, gift mail and admin views — and are excluded from
	// everything that would move real money: the cashback payout queue, the
	// 1-Luna wallet memo, the settlement tracker's supplier polls, the
	// active-checkout hold and the daily-order budget.
	TestMode bool `json:"test_mode,omitempty"`
	// GifterIdenticonDataURI is the buyer's REAL Nimiq identicon, rasterized
	// to a PNG data URI by the checkout (the same @nimiq/identicons face the
	// site renders) and carried so the gift email can draw it as a
	// bgcolor-cell mosaic — Gmail blocks data: images, so the avatar cannot
	// ride as an <img>. Never stored for an anonymous gift.
	GifterIdenticonDataURI string `json:"gifter_identicon,omitempty"`
	// GiftNotifiedAt is the durable "gift notification dispatched" marker:
	// once set, the same quote never re-sends the email. Combined with
	// the supplier-side fulfilled transition this gives us at-most-once
	// delivery without a separate queue worker.
	GiftNotifiedAt time.Time `json:"gift_notified_at,omitempty"`
	// CashbackCode is a buyer-entered promo/redeem code whose cashback rate is
	// locked onto this quote at creation time. When present it is EXCLUSIVE:
	// no staker or loyalty bonus may stack on top of CashbackCodeBps.
	// CashbackCodeMaxUSD caps the promo cashback BASE (the AmountLuna part):
	// codes with a max order USD give the promo rate only ON the capped
	// portion of the cart (e.g. $50 cart with a $20 cap earns promo% on
	// $20 only; the $30 over-cap part earns the base rate). Set to 0 for
	// no cap.
	CashbackCode       string  `json:"cashback_code,omitempty"`
	CashbackCodeBps    int     `json:"cashback_code_bps,omitempty"`
	CashbackCodeMaxUSD float64 `json:"cashback_code_max_usd,omitempty"`
	// Payment payload issued by the supplier (one-time wallet address).
	Coin          string `json:"coin,omitempty"`
	Network       string `json:"network,omitempty"`
	CoinAmount    string `json:"coin_amount,omitempty"`
	WalletAddress string `json:"wallet_address,omitempty"`
	// SentCoinAmount records the supplier's received rail amount for payment
	// reconciliation/audit only. It is not the wallet's NIM debit and never
	// increases the cashback base (including when the buyer overpays).
	SentCoinAmount string `json:"sent_coin_amount,omitempty"`
	// PaymentMethod: "nimiq_pay" (BTC Lightning through Nimiq Pay, default)
	// or "usdt_polygon" (direct USDT on Polygon; same cashback rate as Nimiq Pay).
	PaymentMethod string `json:"payment_method,omitempty"`
	// CashbackDestination: buyer's preference locked at quote time
	// ("cashback" to their wallet, "trees" to the tree planting wallet).
	// Applies to both Nimiq Pay and stablecoin orders.
	CashbackDestination string `json:"cashback_destination,omitempty"`
	// Anonymous keeps this purchase in the public activity feed while hiding
	// the buyer wallet identity and payment transaction details. The buyer
	// still sees the complete quote in their own Orders.
	Anonymous bool `json:"anonymous,omitempty"`
	// SupplierOrderID is the Cryptorefills order id; SupplierStatus the last
	// observed raw supplier state.
	SupplierOrderID string `json:"supplier_order_id,omitempty"`
	SupplierStatus  string `json:"supplier_status,omitempty"`
	// SupplierRequestAt is the durable "supplier request started / supplier
	// order id awaited" marker of the order-creation phase. It is committed
	// immediately BEFORE the CreateOrder call begins, which gives the crash
	// tracker a sound invariant:
	//
	//	zero  => no supplier request was ever dispatched (a crash in that
	//	       window means no supplier order exists; safe to re-dispatch)
	//	set   => a supplier order may exist (the request left, or left and
	//	       its response was lost); only the stale/manual_review path
	//	       applies, never an automatic re-send.
	SupplierRequestAt time.Time `json:"supplier_request_at,omitempty"`
	// PaymentExpiry is the supplier's 30-minute payment window end.
	PaymentExpiry time.Time `json:"payment_expiry,omitempty"`
	// OrderAttempts counts CreateOrder attempts (write-ahead crash intent).
	OrderAttempts int             `json:"order_attempts,omitempty"`
	Status        string          `json:"status"`
	ExpiresAt     time.Time       `json:"expires_at"`
	CreatedAt     time.Time       `json:"created_at"`
	Fulfillment   json.RawMessage `json:"fulfillment,omitempty"`
	// Refund carries the supplier's refund info for Refunded orders
	// (Cryptorefills is merchant of record; this shop never signs refunds).
	Refund       json.RawMessage `json:"refund,omitempty"`
	RefundReason string          `json:"refund_reason,omitempty"`
	// Rating is the 1-5 star rating the buyer left after delivery (0 = unrated).
	Rating    int        `json:"rating,omitempty"`
	RatedAt   *time.Time `json:"rated_at,omitempty"`
	UpdatedAt time.Time  `json:"updated_at"`
	// NimUsdRate / EstimatedNIM are snapshotted at quote time so cashback
	// (1% of the product's NIM price) is computed from the rate the buyer
	// actually saw, not a later oracle tick. Zero means "unknown — skip".
	NimUsdRate   float64 `json:"nim_usd_rate,omitempty"`
	EstimatedNIM float64 `json:"estimated_nim,omitempty"`
}

// Cashback is a one-shot NIM payout from the shop wallet to the buyer,
// queued only when the quote becomes fulfilled. AmountLuna and Memo are
// locked at enqueue time so a later admin % change cannot rewrite a
// pending send, and the quote-id index makes a second enqueue impossible.
type Cashback struct {
	ID         string  `json:"id"`
	QuoteID    string  `json:"quote_id"`
	UserID     string  `json:"user_id"`
	Recipient  string  `json:"recipient"`
	ProductID  string  `json:"product_id"`
	Bps        int     `json:"bps"`
	ProductNIM float64 `json:"product_nim"`
	AmountLuna int64   `json:"amount_luna"`
	Memo       string  `json:"memo"`
	Status     string  `json:"status"` // queued | sending | broadcast | paid | skipped
	SkipReason string  `json:"skip_reason,omitempty"`
	TxHash     string  `json:"tx_hash,omitempty"`
	// Boosted records that this row was paid at a pool-staker rate instead
	// of the base rate. StakeLuna is the buyer's active delegation measured
	// at fulfillment time and TierMinStakeLuna the rung that granted the
	// rate — together they let an operator audit "why did this order pay
	// 3%?" long after the fact.
	Boosted          bool  `json:"boosted,omitempty"`
	StakeLuna        int64 `json:"stake_luna,omitempty"`
	TierMinStakeLuna int64 `json:"tier_min_stake_luna,omitempty"`
	// BoostLuna is the v2 single-ledger portion of this payout: the exact
	// whole-Luna amount debited from the buyer's realized-fee ledger (A).
	// AmountLuna - BoostLuna is the base (or exclusive-code) part paid from
	// the shop wallet. Together they let an operator audit "why was this
	// order paid X?" long after the fact.
	BoostLuna      int64  `json:"boost_luna,omitempty"`
	CashbackSource string `json:"cashback_source,omitempty"`
	CashbackCode   string `json:"cashback_code,omitempty"`
	// TestMode marks a cashback row born from an admin test-center order:
	// the REAL pipeline runs (rates, queue, worker, views, tree
	// contributions) and only the on-chain broadcast is simulated — the
	// worker pays it with a TESTTX-… hash and never signs or broadcasts.
	TestMode bool `json:"test_mode,omitempty"`
	// CashbackDestination is "cashback" (send to buyer's wallet, default) or
	// "burn" (send directly to the Nimiq burn wallet).
	CashbackDestination string `json:"cashback_destination,omitempty"`
	// PaidBaseNIM records the actual cashback basis the row was paid on.
	// BaseSource says where it came from ("priced" = persisted shop-price
	// snapshot, "paid" = realized supplier amount).
	PaidBaseNIM float64 `json:"paid_base_nim,omitempty"`
	BaseSource  string  `json:"base_source,omitempty"`
	// SignedTxHex is the exact wire bytes we will (re)broadcast. Persisted
	// BEFORE the first RPC send so a crash can never produce a second
	// signature for the same cashback row — double-pay is structurally
	// impossible. Empty means we have not signed yet.
	SignedTxHex   string     `json:"signed_tx_hex,omitempty"`
	ValidityStart uint32     `json:"validity_start,omitempty"`
	LastError     string     `json:"last_error,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
	SendingAt     *time.Time `json:"sending_at,omitempty"`
	PaidAt        *time.Time `json:"paid_at,omitempty"`
	// StakeReconciled is a durable idempotency marker for the rare recovery
	// payout created after a pool outage hid a fresh stake at fulfillment.
	StakeReconciled bool `json:"stake_reconciled,omitempty"`
}

// CashbackDestination is where a buyer wants their cashback to go.
const (
	CashbackDestWallet = "cashback" // default: pay NIM to the buyer's wallet
	CashbackDestBurn   = "burn"     // send NIM directly to the Nimiq burn wallet
	BurnNIMAddress     = "NQ07 0000 0000 0000 0000 0000 0000 0000 0000"
)

// SupportTicket represents a customer support inquiry tied to an order.
type SupportTicket struct {
	ID                 string    `json:"id"`
	UserID             string    `json:"user_id"`
	UserAddress        string    `json:"user_address"`
	OrderID            string    `json:"order_id"`
	OrderKind          string    `json:"order_kind,omitempty"`
	ProductID          string    `json:"product_id,omitempty"`
	Subject            string    `json:"subject"`
	Status             string    `json:"status"` // open | waiting_user | waiting_admin | resolved | closed
	LastMessageSnippet string    `json:"last_message_snippet"`
	LastMessageBy      string    `json:"last_message_by"` // user | admin
	MessageCount       int       `json:"message_count"`
	CreatedAt          time.Time `json:"created_at"`
	UpdatedAt          time.Time `json:"updated_at"`
}

// SupportMessage represents an individual message in a support ticket thread.
type SupportMessage struct {
	ID        string    `json:"id"`
	TicketID  string    `json:"ticket_id"`
	OrderID   string    `json:"order_id"`
	Sender    string    `json:"sender"`    // user | admin
	SenderID  string    `json:"sender_id"` // user_id or admin username
	Message   string    `json:"message"`
	CreatedAt time.Time `json:"created_at"`
}
