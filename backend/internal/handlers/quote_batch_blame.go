package handlers

// WHICH ITEM BROKE THE CART?
//
// The supplier validates a batch as a WHOLE: one bad line and the entire
// order is refused with a problem list that does NOT say which delivery
// caused it ("INVALID_BENEFICIARY_ACCOUNT" — of which item?). The buyer was
// therefore told "the cart could not be validated" and left to guess, with
// only the blunt "pay one by one" escape hatch.
//
// diagnoseBatch finds the culprit(s): when the whole-cart dry-run fails, it
// re-runs the SAME free dry-run for each line on its own and reports exactly
// which ones the supplier refuses and why. The frontend turns that into
// "Remove <product> and continue" — one tap, no guessing.
//
// Cost: dry-runs are free and create no order; at most batchMaxItems (10)
// extra calls, only on a path that has already failed. They run in parallel
// behind the supplier queue, and the whole diagnosis is bounded by
// blameBudget so a slow supplier can never hang the checkout.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"nimiqshop/internal/cryptorefills"
)

// blameBudget caps the total time spent finding the culprit. On timeout the
// caller falls back to the generic "cart refused" message.
const blameBudget = 12 * time.Second

// blockedItem is one cart line the supplier will not accept.
type blockedItem struct {
	Index     int    `json:"index"`      // position in the request's items[]
	ProductID string `json:"product_id"` // what the buyer sees in the cart
	Country   string `json:"country,omitempty"`
	Reason    string `json:"reason"` // human-readable, already de-jargonised
	// Fixable marks a problem the BUYER can correct (a bad phone number or
	// email) as opposed to one only removal can solve (out of stock, not
	// sold here). Telling someone to delete a product they simply mistyped
	// a phone number for would be a terrible answer.
	Fixable bool `json:"fixable,omitempty"`
}

// problemReason turns a supplier problem list into one buyer-readable line.
func problemReason(err error) string {
	var pe *cryptorefills.ProblemError
	if errors.As(err, &pe) && pe != nil && len(pe.Problems) > 0 {
		parts := make([]string, 0, len(pe.Problems))
		for _, p := range pe.Problems {
			parts = append(parts, humanProblem(p))
		}
		return strings.Join(parts, "; ")
	}
	var se *cryptorefills.SupplierError
	if errors.As(err, &se) && se != nil {
		// Compatibility for old/raw adapters; new client responses are typed.
		if reasons := reasonsFromBody(se.Detail); len(reasons) > 0 {
			return strings.Join(reasons, "; ")
		}
		if c := strings.TrimSpace(se.Code); c != "" {
			return humanProblemCode(c)
		}
	}
	return "the supplier refused this item"
}

// Legacy raw-error support; moreDetails has no fixed schema and is never
// trusted as customer copy. All reasons use the shared reviewed catalogue.
func reasonsFromBody(body string) []string {
	var doc struct {
		Problems []cryptorefills.Problem `json:"problems"`
		Detail   string                  `json:"detail"`
		Error    string                  `json:"error"`
	}
	if json.Unmarshal([]byte(body), &doc) != nil {
		return nil
	}
	var out []string
	for _, p := range doc.Problems {
		out = append(out, humanProblem(p))
	}
	if len(out) == 0 {
		if doc.Detail != "" {
			out = append(out, humanProblemCode(doc.Detail))
		} else if doc.Error != "" {
			out = append(out, humanProblemCode(doc.Error))
		}
	}
	return out
}
func humanProblem(p cryptorefills.Problem) string { return cryptorefills.ProblemMessage(p.Code) }
func humanProblemCode(code string) string         { return cryptorefills.ProblemMessage(code) }

// diagnoseBatch re-validates every line on its own and returns the ones the
// supplier refuses, in cart order. An empty result means no single line is
// individually at fault (the combination is — e.g. a total limit), which the
// caller must report differently.
func diagnoseBatch(ctx context.Context, cr supplierValidator, items []batchQuoteItem, perItem [][]cryptorefills.Delivery, email string, payment cryptorefills.OrderPayment) []blockedItem {
	budget, cancel := context.WithTimeout(ctx, blameBudget)
	defer cancel()

	found := make([]*blockedItem, len(items))
	var wg sync.WaitGroup
	for i := range items {
		if len(perItem[i]) == 0 {
			continue
		}
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			req := &cryptorefills.CreateOrderRequest{
				Deliveries: perItem[i],
				Payment:    payment,
				Lang:       "en",
			}
			if email != "" {
				req.Email = email
				req.User = &cryptorefills.OrderUser{Email: email}
			}
			if _, err := cr.ValidateOrder(budget, req); err != nil {
				// A transport/timeout failure is NOT the item's fault —
				// blaming a good product would be worse than saying nothing.
				if budget.Err() != nil || !supplierBlamesItem(err) {
					return
				}
				reason := problemReason(err)
				// A line the supplier reports sold out is flagged here too, so
				// the storefront shows it as out of stock on the next load.
				noteSupplierOutOfStock(err, items[i].ProductID, items[i].Country)
				found[i] = &blockedItem{
					Index:     i,
					ProductID: items[i].ProductID,
					Country:   items[i].Country,
					Reason:    reason,
					Fixable:   supplierInputOnly(err),
				}
			}
		}(i)
	}
	wg.Wait()

	out := make([]blockedItem, 0, len(items))
	for _, b := range found {
		if b != nil {
			out = append(out, *b)
		}
	}
	return out
}

// supplierBlamesItem reports whether the error is a verdict ABOUT the item
// (a problem list or a policy code) rather than an infrastructure hiccup.
func supplierBlamesItem(err error) bool {
	problems := itemProblems(err)
	if len(problems) == 0 {
		return false
	}
	for _, p := range problems {
		info, ok := cryptorefills.LookupProblem(p.Code)
		if !ok || (info.Scope != "item" && info.Scope != "input") {
			return false
		}
	}
	return true
}
func itemProblems(err error) []cryptorefills.Problem {
	var pe *cryptorefills.ProblemError
	if errors.As(err, &pe) {
		if pe.HTTPStatus >= 500 || pe.HTTPStatus == 429 {
			return nil
		}
		return pe.Problems
	}
	var se *cryptorefills.SupplierError
	if errors.As(err, &se) && se.Status >= 400 && se.Status < 500 && se.Status != 429 {
		if se.Code != "" {
			return []cryptorefills.Problem{{Code: se.Code, Details: se.MoreDetails}}
		}
		var doc struct {
			Problems []cryptorefills.Problem `json:"problems"`
		}
		if json.Unmarshal([]byte(se.Detail), &doc) == nil {
			return doc.Problems
		}
	}
	return nil
}
func supplierInputOnly(err error) bool {
	problems := itemProblems(err)
	if len(problems) == 0 {
		return false
	}
	for _, p := range problems {
		info, ok := cryptorefills.LookupProblem(p.Code)
		if !ok || info.Scope != "input" {
			return false
		}
	}
	return true
}

// supplierValidator is the slice of the supplier client this file needs,
// so the blame logic is unit-testable without a live client.
type supplierValidator interface {
	ValidateOrder(ctx context.Context, req *cryptorefills.CreateOrderRequest) (*cryptorefills.ValidationResult, error)
}

// blockedItemsMessage renders the buyer-facing summary line.
func blockedItemsMessage(blocked []blockedItem) string {
	if len(blocked) == 0 {
		return ""
	}
	if len(blocked) == 1 {
		if blocked[0].Fixable {
			return fmt.Sprintf("%s needs a correction: %s. Fix it (or remove the item), then recheck the cart.",
				blocked[0].ProductID, blocked[0].Reason)
		}
		return fmt.Sprintf("%s can't be ordered right now: %s. Remove it, then recheck the remaining cart.",
			blocked[0].ProductID, blocked[0].Reason)
	}
	names := make([]string, 0, len(blocked))
	for _, b := range blocked {
		names = append(names, b.ProductID)
	}
	return fmt.Sprintf("%d items can't be ordered right now (%s). Remove them, then recheck the remaining cart.",
		len(blocked), strings.Join(names, ", "))
}
