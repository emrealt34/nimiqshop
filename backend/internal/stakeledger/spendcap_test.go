package stakeledger

import (
	"testing"
	"time"
)

var spendcapNow = time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)

func bookWithA(address string, a float64) Ledger {
	return Ledger{
		Address:   address,
		S:         1000,
		A:         a,
		D:         100,
		DayKey:    dayKeyOf(spendcapNow),
		MonthKey:  monthKeyOf(spendcapNow),
		UpdatedAt: spendcapNow,
	}
}

func TestPlanChargesTheSharedBudgetOncePerOrder(t *testing.T) {
	books := []Ledger{bookWithA("NQAAAA", 10), bookWithA("NQBBBB", 10)}
	ss := NewSharedSpend(spendcapNow)
	plan := PlanPurchaseDebits(books, ss, 80, 0.05, Defaults, spendcapNow)

	// rate per book: A*P/basis = 10*0.05/100 = 0.005 → 50 bps each → 100 bps total
	if plan.BpsSum != 100 {
		t.Fatalf("BpsSum = %d, want 100", plan.BpsSum)
	}
	// each book pays rate * eligible / P = 0.005 * 80 / 0.05 = 8 NIM
	if plan.PaidNIM[0] != 8 || plan.PaidNIM[1] != 8 {
		t.Fatalf("paid per book = %v / %v, want 8 / 8", plan.PaidNIM[0], plan.PaidNIM[1])
	}
	// The order's $80 must be charged from the ACCOUNT budget exactly once,
	// not once per book.
	if plan.SharedSpend.HDay != 8000 || plan.SharedSpend.HMonth != 8000 {
		t.Fatalf("shared budget spent day=%d month=%d, want 8000 / 8000 (charged once)", plan.SharedSpend.HDay, plan.SharedSpend.HMonth)
	}
	if !plan.ChargedSharedBudget {
		t.Fatal("ChargedSharedBudget = false, want true")
	}
	// Books still keep their own counters for display.
	if books[0].HDay != 8000 || books[1].HDay != 8000 {
		t.Fatalf("book counters = %d / %d, want 8000 each", books[0].HDay, books[1].HDay)
	}
}

func TestSharedRemainingCapsTheNextOrder(t *testing.T) {
	// Budget almost gone: only $1 of today's allowance left.
	ss := NewSharedSpend(spendcapNow)
	ss.HDay = int64((500 - 1) * 100)
	ss.HMonth = ss.HDay
	books := []Ledger{bookWithA("NQAAAA", 1), bookWithA("NQBBBB", 1)}
	plan := PlanPurchaseDebits(books, ss, 80, 0.05, Defaults, spendcapNow)
	if plan.EligibleUSD != 1 {
		t.Fatalf("EligibleUSD = %v, want 1 (remaining budget, not the order size)", plan.EligibleUSD)
	}
	// rate per book: A*P/basis = 1*0.05/100 = 0.0005 → 5 bps; eligible=1 →
	// cbNIM = 0.0005 * 1 / 0.05 = 0.01 NIM per book.
	for i, paid := range plan.PaidNIM {
		if paid < 0.009 || paid > 0.011 {
			t.Fatalf("book %d paid %v NIM, want ~0.01", i, paid)
		}
	}
	if plan.SharedSpend.HDay != 50000 {
		t.Fatalf("shared budget day = %d, want exactly 50000 ($500)", plan.SharedSpend.HDay)
	}
}

func TestExhaustedSharedBudgetPaysNothing(t *testing.T) {
	ss := NewSharedSpend(spendcapNow)
	ss.HDay = 50000 // $500 spent today already
	ss.HMonth = 50000
	books := []Ledger{bookWithA("NQAAAA", 10)}
	plan := PlanPurchaseDebits(books, ss, 80, 0.05, Defaults, spendcapNow)
	if plan.LunaSum != 0 || plan.BpsSum != 0 {
		t.Fatalf("exhausted budget paid %d luna / %d bps, want 0 / 0", plan.LunaSum, plan.BpsSum)
	}
	if books[0].A != 10 {
		t.Fatal("book was debited although the shared budget was exhausted")
	}
}

func TestBoostCapIsSharedAcrossBooks(t *testing.T) {
	// Two enormous books: each alone would rate the full 1000 bps. The
	// account's 1000 bps headroom goes to the first book; the second is capped
	// at zero and pays nothing.
	books := []Ledger{bookWithA("NQAAAA", 100000), bookWithA("NQBBBB", 100000)}
	ss := NewSharedSpend(spendcapNow)
	plan := PlanPurchaseDebits(books, ss, 10, 0.05, Defaults, spendcapNow)
	if plan.BpsSum != 1000 {
		t.Fatalf("BpsSum = %d, want exactly 1000 (cap shared, not per book)", plan.BpsSum)
	}
	if plan.Bps[0] != 1000 || plan.Bps[1] != 0 {
		t.Fatalf("per-book bps = %v / %v, want 1000 / 0", plan.Bps[0], plan.Bps[1])
	}
	if books[1].A != 100000 {
		t.Fatal("second book paid even though the shared boost cap was consumed")
	}
}

func TestNoEarningBooksChargesNoBudget(t *testing.T) {
	books := []Ledger{bookWithA("NQAAAA", 0)}
	ss := NewSharedSpend(spendcapNow)
	plan := PlanPurchaseDebits(books, ss, 80, 0.05, Defaults, spendcapNow)
	if plan.ChargedSharedBudget {
		t.Fatal("budget charged although no book earned")
	}
	if plan.SharedSpend.HDay != 0 || plan.SharedSpendChanged {
		t.Fatalf("budget spent = %d changed=%v, want untouched", plan.SharedSpend.HDay, plan.SharedSpendChanged)
	}
}

func TestSharedSpendRolloverResetsDayAndMonth(t *testing.T) {
	ss := SharedSpend{DayKey: "2026-11-08", MonthKey: "2026-10", HDay: 49999, HMonth: 99999}
	if !ss.Rollover(spendcapNow) {
		t.Fatal("Rollover reported no boundary although both keys were stale")
	}
	if ss.HDay != 0 || ss.HMonth != 0 {
		t.Fatalf("counters not reset: day=%d month=%d", ss.HDay, ss.HMonth)
	}
	if ss.DayKey != "2026-11-09" || ss.MonthKey != "2026-11" {
		t.Fatalf("keys not refreshed: %v %v", ss.DayKey, ss.MonthKey)
	}
	dayRem, monthRem := SharedRemaining(ss, spendcapNow, Normalize(Defaults))
	if dayRem != 500 || monthRem != 1000 {
		t.Fatalf("remaining after rollover = %v / %v, want 500 / 1000", dayRem, monthRem)
	}
}

func TestSharedEligibleNeverExceedsOrderOrCaps(t *testing.T) {
	ss := NewSharedSpend(spendcapNow)
	p := Normalize(Defaults)
	if got := SharedEligible(ss, 0, spendcapNow, p); got != 0 {
		t.Fatalf("zero order eligible = %v, want 0", got)
	}
	if got := SharedEligible(ss, 750, spendcapNow, p); got != 500 {
		t.Fatalf("eligible past the day cap = %v, want 500", got)
	}
	if got := SharedEligible(ss, 80, spendcapNow, p); got != 80 {
		t.Fatalf("eligible for a small order = %v, want 80", got)
	}
}

func TestPlanMergesLegacyBookCountersIntoTheAccountBudget(t *testing.T) {
	// Simulates the upgrade deploy: the account's shared record does not
	// exist yet (fresh) but a book already spent $450 under the old per-book
	// caps earlier today. The shared budget must not refill — the order may
	// only spend the remaining $50.
	ss := NewSharedSpend(spendcapNow)
	book := bookWithA("NQAAAA", 1)
	book.HDay = 45000
	book.HMonth = 45000
	plan := PlanPurchaseDebits([]Ledger{book}, ss, 80, 0.05, Defaults, spendcapNow)
	if plan.EligibleUSD != 50 {
		t.Fatalf("EligibleUSD = %v, want 50 (merged legacy book spend, not a fresh $80/$500)", plan.EligibleUSD)
	}
	if plan.SharedSpend.HDay != 50000 {
		t.Fatalf("shared budget = %d, want 50000 after the $50 charge", plan.SharedSpend.HDay)
	}
}
