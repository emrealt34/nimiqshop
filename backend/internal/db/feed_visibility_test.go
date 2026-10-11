package db

import (
	"testing"
	"time"

	"github.com/dgraph-io/badger/v4"
	"nimiqshop/internal/money"
)

func TestPublicFeedExcludesLegacySimulationsAndPreservesRealPurchases(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	ids := []string{"29760a02-9a43-40a2-8d5c-71277624df94", "0ac3ca80-a9cd-4524-a1fd-0c2fc95c583b", "real-carrefour", "real-amazon"}
	for _, id := range ids {
		if err := s.CreateOrder(Order{ID: id, UserID: "same-buyer", ProductID: "Carrefour", Status: "pending", PriceUSD: money.FromFloat(5)}); err != nil {
			t.Fatal(err)
		}
		if err := s.SetOrderStatus(id, "delivered"); err != nil {
			t.Fatal(err)
		}
	}
	// Emulate the old production index, with excluded rows newest and a
	// duplicate entry. The requested page still has to contain real rows.
	if err := s.Update(func(tx *badger.Txn) error {
		for i, id := range []string{"real-amazon", "real-carrefour", "real-carrefour", ids[0], ids[1], "missing"} {
			if err := tx.Set(feedOrderIndexKey(time.Now().Add(time.Duration(i)*time.Hour).UnixNano(), id), []byte(id)); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	rows, err := s.ListFeedOrders(2)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].ID != "real-carrefour" || rows[1].ID != "real-amazon" {
		t.Fatalf("excluded or duplicate rows displaced real orders: %#v", rows)
	}
	for _, id := range ids {
		o, err := s.GetOrderForUser(id, "same-buyer")
		if err != nil || o.Status != "delivered" {
			t.Fatalf("activity moderation changed order history: %s: %#v %v", id, o, err)
		}
	}
}

func TestPublicFeedRejectsOldAndNewTestQuotes(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	for _, q := range []Quote{
		{ID: "real-carrefour", UserID: "buyer", ProductID: "Carrefour", ProductCountry: "FR", Status: "awaiting_payment"},
		{ID: "future-test", UserID: "buyer", TestMode: true, Status: "awaiting_payment"},
		{ID: "old-test", UserID: "buyer", TestMode: true, Status: "awaiting_payment"},
		{ID: "29760a02-9a43-40a2-8d5c-71277624df94", UserID: "buyer", Status: "awaiting_payment"},
	} {
		if err := s.CreateQuoteWithDailyLimits(q, 0, 0, time.Now().Add(-time.Hour)); err != nil {
			t.Fatal(err)
		}
		if err := s.CompleteQuoteWithFulfillment(q.ID, nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Update(func(tx *badger.Txn) error {
		return tx.Set(feedQuoteIndexKey(time.Now().Add(time.Hour).UnixNano(), "old-test"), []byte("old-test"))
	}); err != nil {
		t.Fatal(err)
	}
	rows, err := s.ListFeedQuotes(1)
	if err != nil || len(rows) != 1 || rows[0].ID != "real-carrefour" {
		t.Fatalf("expected real Carrefour only: %#v %v", rows, err)
	}
	for _, id := range []string{"future-test", "old-test", "29760a02-9a43-40a2-8d5c-71277624df94"} {
		if q, err := s.GetQuoteForUser(id, "buyer"); err != nil || q.Status != "fulfilled" {
			t.Fatalf("test record was lost: %s %v", id, err)
		}
	}
}
