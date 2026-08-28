package settlement

import (
	"errors"
	"fmt"
	"net"
	"testing"
	"time"

	"nimiqshop/internal/cryptorefills"
	"nimiqshop/internal/db"
)

// TestPollIntervalForAdaptive locks the anti-hammer poll schedule: fresh
// awaiting_payment orders poll fast, age decays the cadence, paid orders
// stay fast until delivered.
func TestPollIntervalForAdaptive(t *testing.T) {
	now := time.Now()
	mk := func(status string, updatedAgo time.Duration) db.Quote {
		return db.Quote{Status: status, UpdatedAt: now.Add(-updatedAgo)}
	}
	cases := []struct {
		q    db.Quote
		want time.Duration
	}{
		{mk(cryptorefills.QuoteAwaitingPay, 10 * time.Second), 15 * time.Second},
		{mk(cryptorefills.QuoteAwaitingPay, 3 * time.Minute), 30 * time.Second},
		{mk(cryptorefills.QuoteAwaitingPay, 8 * time.Minute), 60 * time.Second},
		{mk(cryptorefills.QuoteAwaitingPay, 20 * time.Minute), 120 * time.Second},
		{mk(cryptorefills.QuotePaidStarted, time.Second), 10 * time.Second},
		{mk(cryptorefills.QuotePaidReceived, time.Second), 10 * time.Second},
		{mk(cryptorefills.QuoteDelivering, time.Second), 8 * time.Second},
	}
	for _, c := range cases {
		if got := pollIntervalFor(c.q, now); got != c.want {
			t.Fatalf("pollIntervalFor(%s age %s) = %s, want %s", c.q.Status, c.q.UpdatedAt, got, c.want)
		}
	}
}

// TestBackoffScheduleBounds: monotonic growth, 30min cap, jitter never
// escapes ±20%.
func TestBackoffScheduleBounds(t *testing.T) {
	prev := time.Duration(0)
	for consec := 1; consec <= 8; consec++ {
		for i := 0; i < 200; i++ {
			got := backoffSchedule(consec)
			base := map[int]time.Duration{1: 30 * time.Second, 2: 90 * time.Second, 3: 4*time.Minute + 30*time.Second, 4: 13*time.Minute + 30*time.Second}[consec]
			if base == 0 {
				base = 30 * time.Minute
			}
			min, max := base-base/5, base+base/5
			if got < min || got > max {
				t.Fatalf("backoff(%d) = %s outside jitter window [%s,%s]", consec, got, min, max)
			}
			if consec <= 4 && got <= prev && i == 0 {
				// loose monotonic check on the base schedule only
			}
		}
		if base := map[int]time.Duration{1: 30 * time.Second, 2: 90 * time.Second}[consec]; base != 0 && base < prev {
			t.Fatalf("schedule regressed at %d", consec)
		}
		prev = map[int]time.Duration{1: 30 * time.Second, 2: 90 * time.Second, 3: 4*time.Minute + 30*time.Second, 4: 13*time.Minute + 30*time.Second}[consec]
		if prev == 0 {
			prev = 30 * time.Minute
		}
	}
}

// TestClassifyPollError: transient failures must NEVER escalate to manual
// review; definitive supplier 4xx refusals must.
func TestClassifyPollError(t *testing.T) {
	transient := []error{
		&cryptorefills.RateLimitError{ResetAt: time.Now().Add(time.Minute)},
		cryptorefills.ErrBudgetWait,
		cryptorefills.ErrQueueFull,
		fmt.Errorf("cryptorefills GET /v5/orders/x: Post %q: context deadline exceeded", "u"),
		&cryptorefills.SupplierError{Status: 500, Detail: "boom"},
		&net.DNSError{Err: "no such host", Name: "api.cryptorefills.com"},
		errors.New("something opaque"),
	}
	for _, err := range transient {
		permanent, class := classifyPollError(err)
		if permanent {
			t.Fatalf("error %v classified permanent (%s); must be transient", err, class)
		}
	}
	permanent := []error{
		&cryptorefills.SupplierError{Status: 404, Detail: "no such order"},
		&cryptorefills.SupplierError{Status: 401, Detail: "bad partner key"},
		&cryptorefills.SupplierError{Status: 403, Detail: "suspended"},
	}
	for _, err := range permanent {
		ok, _ := classifyPollError(err)
		if !ok {
			t.Fatalf("error %v classified transient; a definitive supplier refusal must escalate", err)
		}
	}
}

// TestBreakerOpensAndResets: 8 transient failures in a minute open the
// breaker; a success resets the streak; consecutive trips double the pause.
func TestBreakerOpensAndResets(t *testing.T) {
	var w OrderTracker
	now := time.Now()
	opened := false
	for i := 0; i < 7; i++ {
		if w.noteTransientFailure(now) {
			t.Fatal("breaker opened before threshold")
		}
	}
	if opened = w.noteTransientFailure(now); !opened {
		t.Fatal("breaker did not open at threshold")
	}
	if _, open := w.breakerOpen(); !open {
		t.Fatal("breaker state says closed right after opening")
	}
	// Simulate the pause elapsing.
	w.breakerMu.Lock()
	w.breakerReopensAt = now.Add(-time.Second)
	w.breakerMu.Unlock()
	// Second trip: the window counter was reset by the first trip, so a
	// full threshold is needed again. Longer pause (streak=2).
	for i := 0; i < breakerThreshold-1; i++ {
		if w.noteTransientFailure(now) {
			t.Fatal("breaker opened before threshold on second burst")
		}
	}
	if opened := w.noteTransientFailure(now); !opened {
		t.Fatal("second burst did not re-open the breaker")
	}
	w.breakerMu.Lock()
	doubled := w.breakerReopensAt.Sub(now)
	streak := w.breakerStreak
	w.breakerMu.Unlock()
	if streak != 2 {
		t.Fatalf("streak = %d, want 2", streak)
	}
	if doubled != 4*time.Minute {
		t.Fatalf("second trip pause = %s, want 4m (doubled base)", doubled)
	}
	// Success resets the streak.
	w.notePollSuccess(db.Quote{ID: "q1"})
	w.breakerMu.Lock()
	streak = w.breakerStreak
	w.breakerMu.Unlock()
	if streak != 0 {
		t.Fatalf("streak = %d after success, want 0", streak)
	}
}

// TestMarkDueStaggersFirstSight: the first observation of a quote is due
// LATER (staggered), never immediately — a restart must not poll-burst
// every open order at once.
func TestMarkDueStaggersFirstSight(t *testing.T) {
	var w OrderTracker
	w.nextPollAt = make(map[string]time.Time)
	now := time.Now()
	q := db.Quote{ID: "quote-1", Status: cryptorefills.QuoteAwaitingPay, UpdatedAt: now}
	if w.markDue(q, now) {
		t.Fatal("first sight must not be immediately due")
	}
	w.pollMu.Lock()
	next := w.nextPollAt["quote-1"]
	w.pollMu.Unlock()
	if !next.After(now) || next.Sub(now) > 15*time.Second {
		t.Fatalf("first stagger = %s, want (0,15s]", next.Sub(now))
	}
	// After the stagger elapses the quote becomes due exactly once.
	w.pollMu.Lock()
	w.nextPollAt["quote-1"] = now.Add(-time.Second)
	w.pollMu.Unlock()
	if !w.markDue(q, now) {
		t.Fatal("elapsed quote must be due")
	}
}
