package cryptorefills

import (
	"encoding/json"
	"strings"
	"testing"
)

// The production answer of 2026-10-20 for Amazon.fr / FR / €200 (verbatim
// apart from the USD block, which is irrelevant to the message).
const liveLightningRefusal = `{"problems":[{"problem":"LIGHTNING_ORDER_LIMIT_EXCEEDED","moreDetails":{"kyc_level":"NOT_REGISTERED","limits":{"daily":{"amount":100,"currency":"EUR"},"monthly":{"amount":300,"currency":"EUR"},"per_order":{"amount":100,"currency":"EUR"}},"next_level_limits":{"daily":{"amount":250,"currency":"EUR"},"monthly":{"amount":1000,"currency":"EUR"},"per_order":{"amount":100,"currency":"EUR"}},"order_total":{"amount":208.0707,"currency":"EUR"},"spent_this_month":{"amount":0,"currency":"EUR"},"spent_today":{"amount":0,"currency":"EUR"}}}]}`

func TestLightningLimitRefusalIsAProblemError(t *testing.T) {
	err := responseProblems([]byte(liveLightningRefusal), 422)
	pe, ok := err.(*ProblemError)
	if !ok || len(pe.Problems) != 1 || pe.HTTPStatus != 422 {
		t.Fatalf("want one ProblemError at 422, got %T %v", err, err)
	}
	p := pe.Problems[0]
	if p.Code != ProblemLightningOrderLimit {
		t.Fatalf("code %q", p.Code)
	}
	d, ok := ParseLightningLimit(p)
	if !ok {
		t.Fatal("details must parse")
	}
	if d.KYCLevel != "NOT_REGISTERED" || d.Limits.PerOrder.Amount != 100 || d.Limits.Daily.Amount != 100 || d.Limits.Monthly.Amount != 300 || d.OrderTotal.Amount != 208.0707 || d.OrderTotal.Currency != "EUR" {
		t.Fatalf("parsed %+v", d)
	}
	if got := d.Exceeded(); got != "per_order" {
		t.Fatalf("Exceeded() = %q, want per_order", got)
	}
}

func TestLightningLimitExceededPicksTheCrossedCeiling(t *testing.T) {
	var d LightningLimitDetails
	d.Limits.PerOrder.Amount, d.Limits.Daily.Amount, d.Limits.Monthly.Amount = 100, 100, 300
	d.OrderTotal.Amount = 60
	if got := d.Exceeded(); got != "" {
		t.Fatalf("60 under every ceiling, got %q", got)
	}
	d.SpentToday.Amount = 50
	if got := d.Exceeded(); got != "daily" {
		t.Fatalf("50 spent + 60 crosses the daily 100, got %q", got)
	}
	d.SpentToday.Amount, d.SpentThisMonth.Amount = 0, 260
	if got := d.Exceeded(); got != "monthly" {
		t.Fatalf("260 spent + 60 crosses the monthly 300, got %q", got)
	}
	var nilDetails *LightningLimitDetails
	if nilDetails.Exceeded() != "" {
		t.Fatal("nil details")
	}
}

func TestLightningLimitMessagesCarryTheNumbers(t *testing.T) {
	generic := ProblemMessage("SOMETHING_NEW")
	if msg := ProblemMessage(ProblemLightningOrderLimit); msg == generic || !strings.Contains(msg, "Lightning") {
		t.Fatalf("the catalogue must know the code: %q", msg)
	}
	info, ok := LookupProblem(ProblemLightningOrderLimit)
	if !ok || info.Scope != "order" {
		t.Fatalf("catalogue entry %+v %v", info, ok)
	}
	var env struct {
		Problems []Problem `json:"problems"`
	}
	if err := json.Unmarshal([]byte(liveLightningRefusal), &env); err != nil {
		t.Fatal(err)
	}
	msg := ProblemDetailMessage(env.Problems[0])
	for _, want := range []string{"order total 208.07 EUR", "per-order limit 100 EUR", "daily limit 100 EUR", "monthly limit 300 EUR"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("%q lacks %q", msg, want)
		}
	}
	// Any other problem, or one without details, keeps the plain sentence.
	if got := ProblemDetailMessage(Problem{Code: "OUT_OF_STOCK"}); got != ProblemMessage("OUT_OF_STOCK") {
		t.Fatalf("unexpected decoration: %q", got)
	}
	if got := ProblemDetailMessage(Problem{Code: ProblemLightningOrderLimit}); got != ProblemMessage(ProblemLightningOrderLimit) {
		t.Fatalf("no details, no numbers: %q", got)
	}
	if _, ok := ParseLightningLimit(Problem{Code: ProblemLightningOrderLimit, Details: "garbage"}); ok {
		t.Fatal("unreadable details must not parse")
	}
}
