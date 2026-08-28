package db

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"nimiqshop/internal/money"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestMoneyRoundTrip(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"12.50", "12.500000"},
		{"0.000001", "0.000001"},
		{"-3", "-3.000000"},
		{"1000000", "1000000.000000"},
	}
	for _, c := range cases {
		m, err := money.Parse(c.in)
		if err != nil {
			t.Fatalf("parse %q: %v", c.in, err)
		}
		if got := m.String(); got != c.want {
			t.Errorf("Parse(%q).String() = %q, want %q", c.in, got, c.want)
		}
	}

	// The classic float trap: 0.1 + 0.2 must be exactly 0.3.
	a := money.FromFloat(0.1)
	b := money.FromFloat(0.2)
	if got := (a + b).String(); got != "0.300000" {
		t.Errorf("0.1+0.2 = %s, want 0.300000", got)
	}
}

func TestFindOrCreateUserIsIdempotent(t *testing.T) {
	s := newTestStore(t)

	u1, err := s.FindOrCreateUserByAddress("NQ07TEST")
	if err != nil {
		t.Fatal(err)
	}
	u2, err := s.FindOrCreateUserByAddress("NQ07TEST")
	if err != nil {
		t.Fatal(err)
	}
	if u1.ID != u2.ID {
		t.Fatalf("address uniqueness broken: %s != %s", u1.ID, u2.ID)
	}
}

func TestOrderIdempotencyAndListing(t *testing.T) {
	s := newTestStore(t)
	u, _ := s.FindOrCreateUserByAddress("NQ07ORD")

	o := Order{
		ID:             uuid.NewString(),
		UserID:         u.ID,
		Kind:           "gift_card",
		CategoryID:     "gift_card",
		ProductID:      "amazon-us",
		Quantity:       1,
		PriceUSD:       money.MustParse("50"),
		IdempotencyKey: "idem-1",
	}
	if err := s.CreateOrder(o); err != nil {
		t.Fatal(err)
	}
	// Duplicate HTTP retry with the same key.
	dup := o
	dup.ID = uuid.NewString()
	if err := s.CreateOrder(dup); err != ErrConflict {
		t.Fatalf("expected ErrConflict on duplicate idempotency key, got %v", err)
	}

	if err := s.AttachSupplierIDs(o.ID, "ord_xyz", "inv_abc", "processing"); err != nil {
		t.Fatal(err)
	}

	byBR, err := s.GetOrderBySupplierID("ord_xyz")
	if err != nil {
		t.Fatal(err)
	}
	if byBR.ID != o.ID {
		t.Error("supplier index lookup returned wrong order")
	}

	// Webhook transition, then a repeat delivery.
	_, changed, err := s.UpdateOrderFromWebhook("ord_xyz", "completed", nil)
	if err != nil || !changed {
		t.Fatalf("first webhook: changed=%v err=%v", changed, err)
	}
	_, changed, err = s.UpdateOrderFromWebhook("ord_xyz", "completed", nil)
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Error("repeat webhook delivery reported a change")
	}

	orders, err := s.ListOrders(u.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(orders) != 1 || orders[0].Status != "completed" {
		t.Errorf("unexpected orders: %+v", orders)
	}
}

func TestSupportTicketsAndMessaging(t *testing.T) {
	s := newTestStore(t)
	u, _ := s.FindOrCreateUserByAddress("NQ07SUPPORT")
	orderID := uuid.NewString()

	ticket := SupportTicket{
		UserID:      u.ID,
		UserAddress: u.NimiqAddress,
		OrderID:     orderID,
		OrderKind:   "gift_card",
		ProductID:   "amazon-us",
		Subject:     "Code delivery delayed",
	}

	createdTicket, firstMsg, err := s.CreateSupportTicket(ticket, "Hello, my order was confirmed but the code is not showing yet.")
	if err != nil {
		t.Fatalf("create ticket: %v", err)
	}
	if createdTicket.ID == "" || firstMsg.ID == "" {
		t.Fatal("expected non-empty IDs")
	}
	if createdTicket.Status != "open" || createdTicket.MessageCount != 1 {
		t.Fatalf("expected open status and message count 1, got %s / %d", createdTicket.Status, createdTicket.MessageCount)
	}

	// Lookup by order
	byOrder, err := s.GetSupportTicketForOrder(orderID)
	if err != nil || byOrder.ID != createdTicket.ID {
		t.Fatalf("lookup by order failed: %v", err)
	}

	// Admin replies
	adminMsg, err := s.AddSupportMessage(createdTicket.ID, "admin", "admin1", "We checked your order, your code has been prepared.", "waiting_user")
	if err != nil {
		t.Fatalf("admin message: %v", err)
	}
	if adminMsg.Sender != "admin" {
		t.Fatalf("expected sender admin, got %s", adminMsg.Sender)
	}

	// User replies
	userMsg2, err := s.AddSupportMessage(createdTicket.ID, "user", u.ID, "Thank you, I received the code!", "resolved")
	if err != nil {
		t.Fatalf("user message: %v", err)
	}
	if userMsg2.Sender != "user" {
		t.Fatalf("expected sender user, got %s", userMsg2.Sender)
	}

	// Check messages in chronological order
	messages, err := s.GetTicketMessages(createdTicket.ID)
	if err != nil {
		t.Fatalf("get messages: %v", err)
	}
	if len(messages) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(messages))
	}
	if messages[0].Message != "Hello, my order was confirmed but the code is not showing yet." ||
		messages[1].Message != "We checked your order, your code has been prepared." ||
		messages[2].Message != "Thank you, I received the code!" {
		t.Fatalf("messages order or content mismatch: %+v", messages)
	}

	// Check user list
	userTickets, err := s.ListSupportTicketsForUser(u.ID, 10)
	if err != nil || len(userTickets) != 1 {
		t.Fatalf("user tickets: %v", err)
	}
	if userTickets[0].MessageCount != 3 || userTickets[0].Status != "resolved" {
		t.Fatalf("ticket state mismatch: %+v", userTickets[0])
	}
}

// TestListingIsNewestFirst verifies the reversed-timestamp index reproduces
// ORDER BY created_at DESC.
func TestListingIsNewestFirst(t *testing.T) {
	s := newTestStore(t)
	u, _ := s.FindOrCreateUserByAddress("NQ07SORT")

	for i := 0; i < 5; i++ {
		err := s.CreateOrder(Order{
			ID:             uuid.NewString(),
			UserID:         u.ID,
			Kind:           "gift_card",
			ProductID:      "p",
			Quantity:       1,
			PriceUSD:       money.MustParse("1"),
			IdempotencyKey: uuid.NewString(),
			CreatedAt:      time.Now().UTC().Add(time.Duration(i) * time.Second),
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	orders, err := s.ListOrders(u.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(orders) != 5 {
		t.Fatalf("got %d orders, want 5", len(orders))
	}
	for i := 1; i < len(orders); i++ {
		if orders[i].CreatedAt.After(orders[i-1].CreatedAt) {
			t.Errorf("orders not newest-first at index %d", i)
		}
	}

	// LIMIT must be honoured.
	limited, _ := s.ListOrders(u.ID, 2)
	if len(limited) != 2 {
		t.Errorf("limit ignored: got %d, want 2", len(limited))
	}
}

func TestPersistenceAcrossReopen(t *testing.T) {
	dir := t.TempDir()

	s1, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	u, _ := s1.FindOrCreateUserByAddress("NQ07PERSIST")
	if err := s1.CreateOrder(Order{
		ID: uuid.NewString(), UserID: u.ID, Kind: "gift_card",
		CategoryID: "gift_card", ProductID: "amazon-us", Quantity: 1,
		PriceUSD: money.MustParse("42.5"), Status: "delivered",
	}); err != nil {
		t.Fatal(err)
	}
	if err := s1.Close(); err != nil {
		t.Fatal(err)
	}

	s2, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()

	again, err := s2.FindOrCreateUserByAddress("NQ07PERSIST")
	if err != nil {
		t.Fatal(err)
	}
	if again.ID != u.ID {
		t.Error("user identity not preserved across reopen")
	}
	orders, err := s2.ListOrders(u.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(orders) != 1 || orders[0].PriceUSD.String() != "42.500000" {
		t.Errorf("order not preserved across reopen: %+v", orders)
	}
}

// TestExpiredQuoteStillCompletes is a regression guard for a paid-order data
// loss: the local expiry sweeper releases the daily-limit slot while the
// customer's BOLT11 payment is still in flight; the verified delivery
// webhook then arrives after the quote is "expired". The supplier re-fetch
// already happened in the webhook handler, so the delivery data MUST land.
func TestExpiredQuoteStillCompletes(t *testing.T) {
	s := newTestStore(t)
	now := time.Now().UTC()
	id := uuid.NewString()
	q := Quote{
		ID: uuid.NewString(), UserID: "NQ07EXPIRE", ProductID: "test-gift-card", ProductCountry: "US",
		Denomination: "100 USD", ProductValue: 100,
		Quantity: 1, ProductUSD: money.FromFloat(100), Status: "order_creating",
		CustomerEmail: "buyer@example.com", Coin: "USDT",
		ExpiresAt: now.Add(time.Minute), CreatedAt: now, UpdatedAt: now,
	}
	q.ID = id
	if err := s.CreateQuoteWithDailyLimits(q, 3, money.FromFloat(500), now.Add(-24*time.Hour)); err != nil {
		t.Fatalf("create quote: %v", err)
	}
	if err := s.AttachQuotePayment(id, "ord-1", "MOCKWALLETADDR", "USDT", "100.39", "Solana", now.Add(30*time.Minute)); err != nil {
		t.Fatalf("attach payment: %v", err)
	}
	// The local sweeper expires the quote (releases the limit slot).
	if err := s.SetQuoteStatus(id, "expired"); err != nil {
		t.Fatalf("expire quote: %v", err)
	}
	// The verified delivery webhook arrives late: must still complete.
	if err := s.CompleteQuoteWithFulfillment(id, []byte(`{"code":"TEST-ABC"}`)); err != nil {
		t.Fatalf("complete expired quote with verified delivery: %v", err)
	}
	got, err := s.GetQuote(id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "fulfilled" {
		t.Fatalf("status = %q, want fulfilled", got.Status)
	}
	if string(got.Fulfillment) != `{"code":"TEST-ABC"}` {
		t.Fatalf("fulfillment lost: %s", got.Fulfillment)
	}
	// A duplicate delivery webhook is still an idempotent no-op.
	if err := s.CompleteQuoteWithFulfillment(id, []byte(`{"code":"TEST-ABC"}`)); err == nil {
		t.Fatal("duplicate completion should conflict (already fulfilled)")
	}
	_ = id
}

// TestCompleteQuoteRejectedFromWrongStatus pins the rest of the state
// machine: arbitrary statuses must still be rejected.
func TestCompleteQuoteRejectedFromWrongStatus(t *testing.T) {
	s := newTestStore(t)
	now := time.Now().UTC()
	id := uuid.NewString()
	q := Quote{
		ID: id, UserID: "NQ07WRONG", ProductID: "test-gift-card", ProductCountry: "US",
		Denomination: "10 USD", ProductValue: 10,
		Quantity: 1, ProductUSD: money.FromFloat(10), Status: "order_creating",
		CustomerEmail: "buyer@example.com",
		ExpiresAt:     now.Add(time.Minute), CreatedAt: now, UpdatedAt: now,
	}
	if err := s.CreateQuoteWithDailyLimits(q, 3, money.FromFloat(500), now.Add(-24*time.Hour)); err != nil {
		t.Fatalf("create quote: %v", err)
	}
	if err := s.CompleteQuoteWithFulfillment(id, []byte(`{"code":"X"}`)); err == nil {
		t.Fatal("completing an invoice_creating quote must conflict")
	}
}

// TestLabelOnlyQuoteCreation: fixed products priced by their EXACT label
// ("Java & Bedrock Ed") carry ProductUSD = 0 — the store must accept them
// (the old `ProductUSD <= 0 → ErrConflict` invariant silently 409'd every
// such purchase with "quote idempotency key already used").
func TestLabelOnlyQuoteCreation(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	now := time.Now().UTC()
	q := Quote{
		ID: "q-label-only", UserID: "NQTEST", ProductID: "test-minecraft",
		ProductCountry: "US", Denomination: "Java & Bedrock Ed", ProductValue: 0,
		Quantity: 1, IdempotencyKey: "idem-label-only-123456", ProductUSD: 0,
		CustomerEmail: "buyer@example.com", Coin: "BTC", Network: "Lightning",
	}
	if err := store.CreateQuoteWithDailyLimits(q, 3, money.FromFloat(50), now.Add(-24*time.Hour)); err != nil {
		t.Fatalf("label-only quote rejected: %v", err)
	}
	got, err := store.GetQuote("q-label-only")
	if err != nil || got.Denomination != "Java & Bedrock Ed" {
		t.Fatalf("stored quote = %+v, %v", got, err)
	}
	// Same idempotency key again → conflict, existing quote returned.
	if err := store.CreateQuoteWithDailyLimits(q, 3, money.FromFloat(50), now.Add(-24*time.Hour)); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate idempotency key: want ErrConflict, got %v", err)
	}
}
