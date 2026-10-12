package db

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/dgraph-io/badger/v4"
)

func TestPurchaseAndReviewRetentionAfterEditsAndRestart(t *testing.T) {
	dir := t.TempDir()
	store, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateOrder(Order{ID: "retained", UserID: "buyer", Status: "delivered"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Update(func(tx *badger.Txn) error {
		var record map[string]any
		if err := getJSON(tx, orderKey("retained"), &record); err != nil {
			return err
		}
		record["legacy_wallet_result"] = map[string]any{"hash": strings.Repeat("ef", 32), "swapId": "historic-swap"}
		raw, err := json.Marshal(record)
		if err != nil {
			return err
		}
		return tx.Set(orderKey("retained"), raw)
	}); err != nil {
		t.Fatal(err)
	}
	for index, comment := range []string{"first review", "updated review"} {
		_, err := store.ApplyRating(RatingWrite{Kind: RatingKindOrder, ID: "retained", UserID: "buyer", Stars: 5 - index, Comment: comment, TxHash: strings.Repeat(string(rune('a'+index)), 64), MaxRatings: 5, Now: time.Now()})
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := store.SetRatingCommentHidden(RatingKindOrder, "retained", true); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = New(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	if err := store.View(func(tx *badger.Txn) error {
		var order map[string]any
		if err := getJSON(tx, orderKey("retained"), &order); err != nil {
			return err
		}
		if order["legacy_wallet_result"] == nil || order["rating_comment"] != "updated review" || order["user_id"] != "buyer" {
			t.Fatal("purchase extension or review lost")
		}
		for index, comment := range []string{"first review", "updated review"} {
			var history RatingWrite
			if err := getJSON(tx, []byte("rating:history:"+strings.Repeat(string(rune('a'+index)), 64)), &history); err != nil {
				return err
			}
			if history.Comment != comment || history.UserID != "buyer" {
				t.Fatal("accepted review revision lost")
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestKnownFieldsCanClearWhileLegacyQuoteMetadataSurvives(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	q := Quote{ID: "quote-retention", UserID: "buyer", CustomerEmail: "old@example.test"}
	if err := store.CreateQuoteWithDailyLimits(q, 0, 0, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := store.Update(func(tx *badger.Txn) error {
		var record map[string]any
		if err := getJSON(tx, quoteKey(q.ID), &record); err != nil {
			return err
		}
		record["legacy_swap_id"] = "keep-me"
		raw, err := json.Marshal(record)
		if err != nil {
			return err
		}
		if err := tx.Set(quoteKey(q.ID), raw); err != nil {
			return err
		}
		q.CustomerEmail = ""
		return putQuoteTx(tx, &q)
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.View(func(tx *badger.Txn) error {
		var record map[string]any
		if err := getJSON(tx, quoteKey(q.ID), &record); err != nil {
			return err
		}
		if record["legacy_swap_id"] != "keep-me" || record["customer_email"] != nil {
			t.Fatal("unknown metadata lost or intentionally cleared field resurrected")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
