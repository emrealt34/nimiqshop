package settlement

import (
	"fmt"
	"sort"
	"testing"
	"time"

	"nimiqshop/internal/cryptorefills"
	"nimiqshop/internal/db"
)

/*
 * poll_priority_test.go — the live "paid but stuck at awaiting_payment" bug.
 *
 * The tracker caps each pass at MaxPollsPerTick supplier GETs. Two defects
 * combined so that a buyer's own purchase never got one of them:
 *
 *  1. db.ListQuotesByStatuses sorted ASCENDING by UpdatedAt while its comment
 *     (and this file's caller) claimed newest-first, so the freshest quote —
 *     the one somebody is paying right now — arrived LAST.
 *  2. sortQuotesByPriority gave `awaiting_payment` the SAME rank as the
 *     background reconciliation rows (manual_review / expired / failed), which
 *     NeedsSupplierPoll keeps alive for a long time. They are permanent
 *     residents of the tracked set, so once MaxPollsPerTick of them were due
 *     the live quote was truncated out of every pass, forever.
 *
 * The fix serves live payment states first and lets background rows take only
 * the remainder.
 */

func trackedQuote(id, status string, updated time.Time, observed bool) db.Quote {
	return db.Quote{
		ID: id, Status: status, SupplierOrderID: "CR-" + id,
		PaymentObserved: observed, UpdatedAt: updated,
	}
}

func TestLivePurchaseIsNeverTruncatedOutByBackgroundRows(t *testing.T) {
	now := time.Now().UTC()

	var due []db.Quote
	// More background residents than the per-pass cap, all older than the
	// live purchase: expired/failed with observed money (polled forever) and
	// manual_review rows.
	for i := 0; i < MaxPollsPerTick+4; i++ {
		due = append(due, trackedQuote(
			fmt.Sprintf("resident-%02d", i), cryptorefills.QuoteExpired,
			now.Add(-time.Duration(i+1)*time.Hour), true))
	}
	due = append(due, trackedQuote("resident-manual", cryptorefills.QuoteManualReview, now.Add(-30*time.Minute), false))

	// The purchase the buyer is waiting on.
	live := trackedQuote("live-purchase", cryptorefills.QuoteAwaitingPay, now, false)
	due = append(due, live)

	// Mirror what the store hands the tracker: newest-first.
	sort.SliceStable(due, func(i, j int) bool { return due[i].UpdatedAt.After(due[j].UpdatedAt) })

	sortQuotesByPriority(due)
	pass := capPollPass(due)

	if len(pass) != MaxPollsPerTick {
		t.Fatalf("pass must be capped at %d, got %d", MaxPollsPerTick, len(pass))
	}
	found := false
	for _, q := range pass {
		if q.ID == "live-purchase" {
			found = true
		}
	}
	if !found {
		t.Fatalf("the live awaiting_payment quote was truncated out of the poll pass; pass=%v", ids(pass))
	}
}

func TestLivePurchaseSurvivesEvenWhenItArrivesLast(t *testing.T) {
	// Defence in depth for defect 1: even if a caller hands the tracker an
	// oldest-first slice, a live payment state must still win the cap.
	now := time.Now().UTC()
	var due []db.Quote
	for i := 0; i < MaxPollsPerTick+2; i++ {
		due = append(due, trackedQuote(
			fmt.Sprintf("resident-%02d", i), cryptorefills.QuoteFailed,
			now.Add(-time.Duration(i+1)*time.Hour), true))
	}
	due = append(due, trackedQuote("live-purchase", cryptorefills.QuoteAwaitingPay, now, false))
	sort.SliceStable(due, func(i, j int) bool { return due[i].UpdatedAt.Before(due[j].UpdatedAt) })

	sortQuotesByPriority(due)
	for _, q := range capPollPass(due) {
		if q.ID == "live-purchase" {
			return
		}
	}
	t.Fatal("an oldest-first input must not be able to starve a live purchase any more")
}

func TestBackgroundRowsStillGetTheLeftoverBudget(t *testing.T) {
	// The split must not starve reconciliation entirely: with no live rows
	// due, the whole cap goes to the background class.
	now := time.Now().UTC()
	var due []db.Quote
	for i := 0; i < MaxPollsPerTick+3; i++ {
		due = append(due, trackedQuote(
			fmt.Sprintf("resident-%02d", i), cryptorefills.QuoteManualReview,
			now.Add(-time.Duration(i+1)*time.Minute), false))
	}
	sortQuotesByPriority(due)
	pass := capPollPass(due)
	if len(pass) != MaxPollsPerTick {
		t.Fatalf("background rows must fill the cap when nothing live is due, got %d", len(pass))
	}
}

func TestPollRankOrdersDeliveryAboveWaitingAboveReconciliation(t *testing.T) {
	order := []string{
		cryptorefills.QuoteDelivering,
		cryptorefills.QuotePaidReceived,
		cryptorefills.QuotePaidStarted,
		cryptorefills.QuoteAwaitingPay,
		cryptorefills.QuoteManualReview,
		cryptorefills.QuoteExpired,
	}
	for i := 1; i < len(order); i++ {
		if pollRank(order[i]) <= pollRank(order[i-1]) {
			t.Fatalf("%s must rank below %s (%d vs %d)", order[i], order[i-1], pollRank(order[i]), pollRank(order[i-1]))
		}
	}
	if pollRank(cryptorefills.QuoteAwaitingPay) > livePollRank {
		t.Fatal("awaiting_payment is a live state a buyer is waiting on")
	}
	if pollRank(cryptorefills.QuoteManualReview) <= livePollRank || pollRank(cryptorefills.QuoteExpired) <= livePollRank {
		t.Fatal("reconciliation states must rank below every live state")
	}
}

func TestLateMoneyReconcilePollsSlowly(t *testing.T) {
	now := time.Now().UTC()
	deadline := now.Add(-time.Hour)

	reconcile := trackedQuote("reconcile", cryptorefills.QuoteExpired, now, false)
	reconcile.PaymentExpiry, reconcile.ExpiresAt = deadline, deadline
	if got := pollIntervalFor(reconcile, now); got != lateMoneyReconcileInterval {
		t.Fatalf("late-money reconcile cadence = %s, want %s", got, lateMoneyReconcileInterval)
	}
	// Owner, 2026-10-17: "expired olsa bile 6 saatte bir bak expired
	// olanlara 3 gün boyunca". Pin both halves of that contract AND the
	// arithmetic that makes it useful — a 6h cadence inside a 72h window
	// gives every stranded order a dozen chances to be picked up.
	if lateMoneyReconcileInterval != 6*time.Hour {
		t.Fatalf("reconcile cadence = %s, want 6h", lateMoneyReconcileInterval)
	}
	if db.LateMoneyReconcileWindow != 72*time.Hour {
		t.Fatalf("reconcile window = %s, want 72h", db.LateMoneyReconcileWindow)
	}
	if chances := db.LateMoneyReconcileWindow / lateMoneyReconcileInterval; chances < 10 {
		t.Fatalf("only %d reconcile chances inside the window; a late supplier confirmation would be missed", chances)
	}
	// The reconcile class must stay far cheaper than a live buyer's order.
	if live := pollIntervalFor(trackedQuote("live", cryptorefills.QuoteAwaitingPay, now, false), now); lateMoneyReconcileInterval <= live {
		t.Fatalf("reconcile cadence %s must be slower than the live awaiting_payment cadence %s", lateMoneyReconcileInterval, live)
	}

	observed := reconcile
	observed.PaymentObserved = true
	if got := pollIntervalFor(observed, now); got != time.Minute {
		t.Fatalf("observed money must reconcile at 1m, got %s", got)
	}

	// The live states are untouched by the new class.
	for status, want := range map[string]time.Duration{
		cryptorefills.QuoteAwaitingPay:  10 * time.Second,
		cryptorefills.QuotePaidStarted:  10 * time.Second,
		cryptorefills.QuotePaidReceived: 10 * time.Second,
		cryptorefills.QuoteDelivering:   8 * time.Second,
		cryptorefills.QuoteManualReview: time.Minute,
	} {
		if got := pollIntervalFor(trackedQuote("q", status, now, false), now); got != want {
			t.Fatalf("%s cadence = %s, want %s", status, got, want)
		}
	}
}

func ids(qs []db.Quote) []string {
	out := make([]string, 0, len(qs))
	for _, q := range qs {
		out = append(out, q.ID)
	}
	return out
}
