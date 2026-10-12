package db

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestLightningSubmissionSurvivesRestartAndRejectsReplacement(t *testing.T) {
	dir := t.TempDir()
	store, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	q := Quote{ID: "submission", UserID: "buyer", Coin: "BTC", Network: "Lightning", Status: "awaiting_payment"}
	if err := store.CreateQuoteWithDailyLimits(q, 0, 0, time.Now().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	hash := "0x" + strings.Repeat("ab", 32)
	if err := store.SaveLightningSubmission(q.ID, q.UserID, hash, "wallet-swap-private"); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveLightningSubmission(q.ID, q.UserID, hash, "wallet-swap-private"); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveLightningFundingHint(q.ID, q.UserID, strings.Repeat("cd", 32)); !errors.Is(err, ErrConflict) {
		t.Fatalf("replacement: %v", err)
	}
	if err := store.SaveLightningFundingHint(q.ID, "other", hash); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ownership: %v", err)
	}
	if err := store.SaveLightningSubmission(q.ID, q.UserID, hash, "replacement-swap"); !errors.Is(err, ErrConflict) {
		t.Fatalf("swap replacement: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = New(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	saved, err := store.GetQuote(q.ID)
	if err != nil || saved.LightningFundingHint != hash || saved.LightningSwapID != "wallet-swap-private" || saved.PaymentObserved || saved.PaidAt != nil {
		t.Fatalf("submission lost or settlement invented: %#v %v", saved, err)
	}
	if saved.PublicQuote(time.Now()).LightningSwapID != "" {
		t.Fatal("public quote exposed private swap id")
	}
}
