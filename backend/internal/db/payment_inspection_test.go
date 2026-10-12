package db

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/dgraph-io/badger/v4"
)

func TestPaymentInspectionPreservesLegacyCandidatesWithoutReadingOtherPurchases(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	invoice := strings.Repeat("ab", 32)
	funding := strings.Repeat("cd", 32)
	if err := store.CreateQuoteWithDailyLimits(Quote{ID: "target", UserID: "buyer", SupplierOrderID: "supplier", LightningPaymentHash: invoice}, 0, 0, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := store.Update(func(tx *badger.Txn) error {
		write := func(key []byte, value any) error {
			data, err := json.Marshal(value)
			if err != nil {
				return err
			}
			return tx.Set(key, data)
		}
		var raw map[string]any
		if err := getJSON(tx, quoteKey("target"), &raw); err != nil {
			return err
		}
		raw["legacy_payment"] = map[string]any{"tx_hash": funding, "swap": map[string]any{"id": "old-wallet-swap"}, "email": "private@example.test", "password_hash": strings.Repeat("ee", 32), "gift_code": "private-code"}
		if err := write(quoteKey("target"), raw); err != nil {
			return err
		}
		if err := write(orderKey("related"), map[string]any{"id": "related", "supplier_order_id": "supplier", "payload": map[string]any{"swap_id": "linked-swap"}}); err != nil {
			return err
		}
		return write(orderKey("unrelated"), map[string]any{"id": "unrelated", "swap_id": "other-customer-swap"})
	}); err != nil {
		t.Fatal(err)
	}
	got, err := store.InspectQuotePayment("target")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Records) != 2 || got.Identifiers["quote.legacy_payment.tx_hash"] != funding || got.Identifiers["quote.legacy_payment.swap.id"] != "old-wallet-swap" || got.Identifiers["o:related.payload.swap_id"] != "linked-swap" {
		t.Fatalf("legacy data lost: %#v", got)
	}
	for path, value := range got.Identifiers {
		if strings.Contains(path, "password") || strings.Contains(value, "private") || value == "other-customer-swap" {
			t.Fatalf("unrelated or sensitive data escaped: %s", path)
		}
	}
	q, err := store.GetQuote("target")
	if err != nil || q.LightningFundingHint != "" || q.LightningSwapID != "" || q.PaymentObserved {
		t.Fatal("diagnostics mutated payment state")
	}
}
