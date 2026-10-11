package db

import (
	"encoding/json"
	"testing"
	"time"

	"nimiqshop/internal/cryptorefills"
)

func TestDeliveryObservationsSurviveEmailRatingAndSnapshotUpdates(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	created := time.Now().UTC().Add(-10 * time.Minute).Truncate(time.Second)
	paid := created.Add(time.Minute)
	delivered := paid.Add(61 * time.Second)
	q := Quote{ID: "timing", UserID: "buyer", Status: "awaiting_payment", SupplierOrderID: "supplier", CreatedAt: created}
	if err := s.CreateQuoteWithDailyLimits(q, 0, 0, created); err != nil {
		t.Fatal(err)
	}
	apply := func(status string, at time.Time) {
		var order cryptorefills.Order
		blob, _ := json.Marshal(map[string]string{"order_id": "supplier", "order_state": status, "updated_at": at.Format(time.RFC3339)})
		if err := json.Unmarshal(blob, &order); err != nil {
			t.Fatal(err)
		}
		if changed, err := s.ApplySupplierOrder(q.ID, &order); err != nil || !changed {
			t.Fatalf("%s: %v/%v", status, changed, err)
		}
	}
	apply(cryptorefills.StatusPaymentReceived, paid)
	apply(cryptorefills.StatusDone, delivered)
	if err := s.MarkGiftNotified(q.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.SetQuoteNIMSnapshot(q.ID, .002, 5000); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetQuote(q.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.PaidAt == nil || !got.PaidAt.Equal(paid) || !got.DeliveryTime().Equal(delivered) || !got.UpdatedAt.After(delivered) {
		t.Fatalf("lost immutable observations: %+v", got)
	}
	if sec, ok := DeliverySeconds(got.CreatedAt, got.PaidAt, got.DeliveryTime()); !ok || sec != 61 {
		t.Fatalf("duration changed: %v/%v", sec, ok)
	}
	// Historical rows without the new field already retain the supplier stamp.
	got.DeliveredAt = nil
	got.RatedAt = deliveryPtr(time.Now().UTC())
	if !got.DeliveryTime().Equal(delivered) {
		t.Fatal("rating changed historical delivery")
	}
}

func deliveryPtr(t time.Time) *time.Time { return &t }

func TestDeliverySecondsIncludesZeroAndFractionsButNeverHandoffOrNegativeTime(t *testing.T) {
	start := time.Now().UTC().Add(-time.Hour)
	for _, secs := range []float64{0, .25, 61.75} {
		end := start.Add(time.Duration(secs * float64(time.Second)))
		if got, ok := DeliverySeconds(start.Add(-time.Minute), &start, end); !ok || got != secs {
			t.Fatalf("%v: %v/%v", secs, got, ok)
		}
	}
	for _, pair := range []struct {
		paid *time.Time
		end  time.Time
	}{{nil, start}, {&start, start.Add(-time.Second)}, {&start, time.Time{}}, {&start, time.Now().Add(time.Hour)}} {
		if _, ok := DeliverySeconds(start.Add(-time.Minute), pair.paid, pair.end); ok {
			t.Fatal("unrecorded or impossible timing accepted")
		}
	}
}

func TestLegacyOrderDeliveryStampIsNotMovedByDuplicateUpdates(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if err := s.CreateOrder(Order{ID: "legacy-timing", UserID: "buyer"}); err != nil {
		t.Fatal(err)
	}
	for _, status := range []string{"processing", "delivered", "delivered"} {
		if err := s.SetOrderStatus("legacy-timing", status); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.GetOrder("legacy-timing")
	if err != nil {
		t.Fatal(err)
	}
	if got.PaidAt == nil || got.DeliveredAt == nil || got.DeliveredAt.Before(*got.PaidAt) {
		t.Fatalf("missing lifecycle stamps: %+v", got)
	}
	first := *got.DeliveredAt
	if err := s.SetOrderStatus(got.ID, "delivered"); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetOrder(got.ID)
	if !got.DeliveredAt.Equal(first) {
		t.Fatal("duplicate delivery moved completion")
	}
	old := time.Now().UTC().Add(-time.Hour)
	if err := s.CreateOrder(Order{ID: "historical-terminal", UserID: "buyer", Status: "delivered", CreatedAt: old}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetOrderStatus("historical-terminal", "delivered"); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetOrder("historical-terminal")
	if got.PaidAt != nil || !got.DeliveryTime().Equal(old) {
		t.Fatal("duplicate invented historical payment or changed delivery")
	}
}
