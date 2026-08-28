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
	ID                string          `json:"id"`
	UserID            string          `json:"user_id"`
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
	// ends in 'refunded'. nim.shop never holds funds: the supplier
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
// (stablecoin on the selected network); Cryptorefills delivers the product
// to CustomerEmail (gift cards/eSIMs) or PhoneNumber (top-ups).
type Quote struct {
	ID     string `json:"id"`
	UserID string `json:"user_id"`
	// ProductID is the supplier family/brand name ("Airbnb", "t-mobile").
	ProductID      string `json:"product_id"`
	ProductCountry string `json:"product_country,omitempty"`
	// Denomination is the face value label ("100 USD") or "range".
	Denomination string `json:"denomination,omitempty"`
	// ProductValue is the face value in the product currency (USD for US).
	ProductValue   float64 `json:"product_value,omitempty"`
	Quantity       int     `json:"quantity"`
	IdempotencyKey string  `json:"idempotency_key,omitempty"`
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
	// Gift notification metadata. Channel is the buyer's choice: "email",
	// "sms", or "both". When empty, this is a regular self-purchase (no
	// gift notification). The message is buyer-authored personal text;
	// Email and SMS limit it independently (see notification package).
	GiftChannel        string `json:"gift_channel,omitempty"`
	GiftMessage        string `json:"gift_message,omitempty"`
	GiftRecipientPhone string `json:"gift_recipient_phone,omitempty"`
	// GiftNotifiedAt is the durable "gift notification dispatched" marker:
	// once set, the same quote never re-sends an email or SMS. Combined with
	// the supplier-side fulfilled transition this gives us at-most-once
	// delivery without a separate queue worker.
	GiftNotifiedAt time.Time `json:"gift_notified_at,omitempty"`
	// Payment payload issued by the supplier (one-time wallet address).
	Coin          string `json:"coin,omitempty"`
	Network       string `json:"network,omitempty"`
	CoinAmount    string `json:"coin_amount,omitempty"`
	WalletAddress string `json:"wallet_address,omitempty"`
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
}

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
