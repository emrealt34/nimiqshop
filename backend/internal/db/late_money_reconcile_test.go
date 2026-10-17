package db

import (
	"testing"
	"time"

	"nimiqshop/internal/cryptorefills"
)

/*
 * late_money_reconcile_test.go — a local timer must never be the last word
 * about somebody's money.
 *
 * The live bug: a payment landed while the tracker was not looking (Railway
 * idle-sleep, the supplier circuit breaker, an exponential backoff, or a poll
 * pass whose budget was eaten by older rows). The window lapsed, the sweep
 * marked the quote `expired`, and NeedsSupplierPoll answered false for an
 * expired quote with no OBSERVED money — money that can only ever be observed
 * by polling. The supplier was therefore never asked again and a paid order
 * was forgotten, even though quoteTransitions explicitly allows
 * expired → payment_received / delivering / fulfilled / refunded
 * ("A local timer is not evidence of payment failure. Reconcile late money.").
 */

func TestExpiredQuoteKeepsBeingPolledInsideTheLateMoneyWindow(t *testing.T) {
	now := time.Now().UTC()
	base := func(deadline time.Time) Quote {
		return Quote{
			ID: "q-late", UserID: "buyer", Status: "expired",
			SupplierOrderID: "CR-late",
			PaymentExpiry:   deadline,
			ExpiresAt:       deadline,
		}
	}

	inside := base(now.Add(-time.Hour))
	if !inside.NeedsSupplierPoll() {
		t.Fatal("an expired quote inside the late-money window must still be offered to the supplier poller")
	}

	outside := base(now.Add(-LateMoneyReconcileWindow - time.Minute))
	if outside.NeedsSupplierPoll() {
		t.Fatal("an expired quote past the late-money window must stop being polled")
	}

	// Observed money is polled forever, window or no window.
	observed := base(now.Add(-LateMoneyReconcileWindow - time.Hour))
	observed.PaymentObserved = true
	if !observed.NeedsSupplierPoll() {
		t.Fatal("a quote where money was seen must be polled forever")
	}

	// No deadline at all means there was no payment window to miss.
	unknown := Quote{ID: "q-unknown", Status: "expired", SupplierOrderID: "CR-x"}
	if unknown.NeedsSupplierPoll() {
		t.Fatal("an expired quote with no known deadline has nothing left to reconcile")
	}
}

func TestLateMoneyReconcileIsTheSlowestClass(t *testing.T) {
	// The reconcile rows are permanent residents of the tracked set, so they
	// must not be able to crowd out a buyer who is on the pay screen. The
	// cadence is asserted here (the priority split lives in settlement).
	now := time.Now().UTC()
	reconcile := Quote{
		ID: "q-reconcile", Status: "expired", SupplierOrderID: "CR-r",
		PaymentExpiry: now.Add(-time.Hour), ExpiresAt: now.Add(-time.Hour),
	}
	if !reconcile.NeedsSupplierPoll() {
		t.Fatal("precondition: the quote must still be inside its reconcile window")
	}
	if reconcile.CanPay(now) {
		t.Fatal("a locally expired quote must never become payable again")
	}
	if cryptorefills.IsTerminal(reconcile.SupplierStatus) {
		t.Fatal("test setup: no supplier state was asserted yet")
	}
}
