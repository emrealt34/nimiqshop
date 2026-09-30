package notification

// SEND POLICY for the 1-Luna memo channel.
//
// A memo notification is NOT an email. Every one of them is a real on-chain
// transaction that is written into the recipient's wallet history FOREVER —
// they cannot delete it, mark it read, or filter it. That single fact
// decides the entire design:
//
//   - Marketing blasts are forbidden by construction. There is no
//     "send to everyone" path in this package, and the admin endpoint that
//     uses it must pass a reason that maps to a real, user-owned event.
//   - Every send needs a Reason. The reason decides whether it is allowed
//     at all, and reasons are a closed set — you cannot invent
//     "summer_sale" without editing this file and reading this comment.
//   - Frequency is capped PER USER, not per campaign: at most one message
//     per reason per cooldown window, and a hard ceiling across all reasons
//     per rolling 30 days. A shop that spams a wallet gets uninstalled.
//   - Users can switch the channel off entirely, and off means off — the
//     opt-out is checked before the rate limit, not after.
//
// The rule of thumb for adding a reason: would the user be ANNOYED to learn
// this cost them a line in their wallet ledger forever? If yes, it belongs
// in the in-app feed instead, where there is no such cost.

import (
	"fmt"
	"strings"
	"time"
)

// Reason is the closed set of events that may spend a user's wallet ledger
// line. Each one is something the user OWNS: their order, their money,
// their question. None of them is promotional.
type Reason string

const (
	// ReasonOrderFulfilled — the thing they paid for is ready. This is the
	// notification users actively wait for.
	ReasonOrderFulfilled Reason = "order_fulfilled"
	// ReasonCashbackPaid — real money arrived in their wallet.
	ReasonCashbackPaid Reason = "cashback_paid"
	// ReasonSupportReply — they asked a question and it was answered.
	ReasonSupportReply Reason = "support_reply"
	// ReasonOrderProblem — an order needs their attention (manual review,
	// refund, delivery failure). Silence here is worse than a message.
	ReasonOrderProblem Reason = "order_problem"
)

// reasonPolicy is the per-reason frequency contract.
type reasonPolicy struct {
	// Cooldown is the minimum gap between two sends of THIS reason to the
	// same user. Zero means "no cooldown" — used for per-object events that
	// are already naturally unique (one order is fulfilled exactly once).
	Cooldown time.Duration
	// CountsToBudget marks reasons that consume the global monthly budget.
	// Things the user is actively waiting for (their order, their money,
	// their answer) are exempt: throttling those would be user-hostile.
	// Anything WE initiate must be budgeted.
	CountsToBudget bool
	// Description is shown in the admin UI and audit log.
	Description string
}

var reasonPolicies = map[Reason]reasonPolicy{
	ReasonOrderFulfilled: {Cooldown: 0, CountsToBudget: false, Description: "their order is ready"},
	ReasonCashbackPaid:   {Cooldown: 0, CountsToBudget: false, Description: "cashback landed in their wallet"},
	ReasonSupportReply:   {Cooldown: 0, CountsToBudget: false, Description: "their support ticket was answered"},
	ReasonOrderProblem:   {Cooldown: 0, CountsToBudget: false, Description: "an order needs their attention"},
}

// MonthlyBudget is the ceiling on WE-initiated messages per user per rolling
// 30 days. Deliberately tiny: this channel's value comes from being rare
// enough that a memo from the shop is always worth reading.
//
// NOTE: right now NO reason is shop-initiated — every entry in
// reasonPolicies is an event the user owns and is waiting for. The budget
// machinery stays because it is the guard rail: any future reason that sets
// CountsToBudget is capped by it from the moment it is added.
const MonthlyBudget = 2

// ValidReason reports whether r is a known, allowed reason.
func ValidReason(r Reason) bool {
	_, ok := reasonPolicies[r]
	return ok
}

// Reasons lists the allowed reasons (admin UI, docs, tests).
func Reasons() []Reason {
	out := make([]Reason, 0, len(reasonPolicies))
	for r := range reasonPolicies {
		out = append(out, r)
	}
	return out
}

// Describe returns the human description of a reason.
func Describe(r Reason) string {
	if p, ok := reasonPolicies[r]; ok {
		return p.Description
	}
	return string(r)
}

// CountsToBudget reports whether the reason spends the user's monthly quota.
func CountsToBudget(r Reason) bool {
	p, ok := reasonPolicies[r]
	return ok && p.CountsToBudget
}

// Cooldown returns the minimum gap between two sends of this reason.
func Cooldown(r Reason) time.Duration {
	if p, ok := reasonPolicies[r]; ok {
		return p.Cooldown
	}
	// Unknown reason: the most restrictive answer, so a bug cannot spam.
	return 30 * 24 * time.Hour
}

// SendDecision is the outcome of the policy check.
type SendDecision struct {
	Allowed bool
	// Reason the send was refused, for logs and the admin UI. Never shown
	// to the recipient (they are, by definition, not receiving anything).
	Blocked string
}

// Allow evaluates the policy for one prospective send.
//
//	optedOut    — the user turned the channel off (checked FIRST: an opt-out
//	              is a boundary, not a preference to be weighed).
//	lastSameAt  — when this reason last went to this user (zero if never).
//	budgetUsed  — WE-initiated sends to this user in the last 30 days.
//	now         — clock (injected so this is testable).
func Allow(r Reason, optedOut bool, lastSameAt time.Time, budgetUsed int, now time.Time) SendDecision {
	if !ValidReason(r) {
		return SendDecision{Blocked: fmt.Sprintf("unknown reason %q — refusing to send", r)}
	}
	if optedOut {
		return SendDecision{Blocked: "the recipient turned wallet notifications off"}
	}
	if cd := Cooldown(r); cd > 0 && !lastSameAt.IsZero() {
		if next := lastSameAt.Add(cd); now.Before(next) {
			return SendDecision{Blocked: fmt.Sprintf("cooldown for %s active until %s", r, next.UTC().Format(time.RFC3339))}
		}
	}
	if CountsToBudget(r) && budgetUsed >= MonthlyBudget {
		return SendDecision{Blocked: fmt.Sprintf("monthly budget spent (%d/%d shop-initiated messages in 30 days)", budgetUsed, MonthlyBudget)}
	}
	return SendDecision{Allowed: true}
}

// Memo renders the on-chain memo for a reason. Memos are hard-capped at 64
// BYTES by the protocol, so this trims on a rune boundary (a byte-sliced
// memo can end in half a UTF-8 character and render as a replacement box in
// the wallet).
func Memo(r Reason, detail string) string {
	var base string
	switch r {
	case ReasonOrderFulfilled:
		base = "Your " + shopHost() + " order is ready"
	case ReasonCashbackPaid:
		base = "Cashback from " + shopHost()
	case ReasonSupportReply:
		base = shopHost() + " replied to your ticket"
	case ReasonOrderProblem:
		base = "Your " + shopHost() + " order needs attention"
	default:
		base = shopHost()
	}
	if d := strings.TrimSpace(detail); d != "" {
		base += ": " + d
	}
	return TrimMemo(base)
}

// TrimMemo cuts a memo to the protocol's 64-byte ceiling without splitting a
// UTF-8 rune.
func TrimMemo(s string) string {
	s = strings.TrimSpace(s)
	if len(s) <= maxMemoLen {
		return s
	}
	cut := s[:maxMemoLen]
	for len(cut) > 0 && !utf8ValidEnd(cut) {
		cut = cut[:len(cut)-1]
	}
	return strings.TrimSpace(cut)
}

// utf8ValidEnd reports whether s ends on a complete rune.
func utf8ValidEnd(s string) bool {
	if s == "" {
		return true
	}
	r := []rune(s)
	return len(string(r)) == len(s)
}

func shopHost() string {
	if s := strings.TrimSpace(ShopName); s != "" {
		return s
	}
	return "shop.nimiqbase.com"
}
