package settlement

/*
 * track_test.go — the fulfilled-order EMAIL gate.
 *
 * The bug this pins down (owner, 2026-10-05: "satın alımlarda hediye olsun
 * olmasın e-posta gider … şu an sadece hediyelerde gidiyor"): the tracker used to
 * fire the mail hook only for quotes that carried a GiftChannel, so a plain
 * purchase never produced an email at all. The gate is now the RECIPIENT
 * ADDRESS — a gift note and an order confirmation ride the same hook, and the
 * mailtrap builder picks the wording from the quote.
 *
 * The tracker calls the hook in its own goroutine, so the positive cases wait
 * on a channel; the negative cases are the ABSENCE of a send (the gate is
 * evaluated synchronously, before the goroutine exists).
 */

import (
	"testing"
	"time"

	"nimiqshop/internal/db"
)

func TestHasMailRecipient(t *testing.T) {
	for _, tc := range []struct {
		name  string
		email string
		want  bool
	}{
		{"buyer address", "buyer@example.com", true},
		{"padded address", "  buyer@example.com  ", true},
		{"phone-only top-up has no address", "", false},
		{"whitespace is not an address", "   ", false},
	} {
		if got := HasMailRecipient(db.Quote{CustomerEmail: tc.email}); got != tc.want {
			t.Errorf("%s: HasMailRecipient(%q) = %v, want %v", tc.name, tc.email, got, tc.want)
		}
	}
}

func TestFulfilledMailFiresForEveryPurchaseWithAnAddress(t *testing.T) {
	for _, tc := range []struct {
		name  string
		quote db.Quote
		want  bool
	}{
		{"plain purchase — the case that used to be skipped", db.Quote{ID: "q-plain", CustomerEmail: "buyer@example.com"}, true},
		{"gift note for the recipient", db.Quote{ID: "q-gift", CustomerEmail: "recipient@example.com", GiftChannel: "email", GiftMessage: "happy birthday"}, true},
		{"anonymous gift", db.Quote{ID: "q-anon", CustomerEmail: "recipient@example.com", GiftChannel: "email", Anonymous: true}, true},
		{"phone-only top-up — nobody to mail", db.Quote{ID: "q-topup", PhoneNumber: "+905000000000"}, false},
		{"blank address", db.Quote{ID: "q-blank", CustomerEmail: "   "}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			seen := make(chan db.Quote, 4)
			SetMailNotifyFn(func(q db.Quote) { seen <- q })
			t.Cleanup(func() { SetMailNotifyFn(nil) })

			NotifyFulfilled(tc.quote)

			select {
			case got := <-seen:
				if !tc.want {
					t.Fatalf("hook fired for %+v, but there is no address to mail", tc.quote)
				}
				if got.ID != tc.quote.ID {
					t.Fatalf("hook saw quote %q, want %q", got.ID, tc.quote.ID)
				}
			case <-time.After(2 * time.Second):
				if tc.want {
					t.Fatalf("no email hook for %+v — the buyer hears nothing", tc.quote)
				}
			}
		})
	}
}

func TestFulfilledMailWithoutAHookIsSilent(t *testing.T) {
	SetMailNotifyFn(nil)
	defer SetMailNotifyFn(nil)
	if fn := mailNotifyFn(); fn != nil {
		t.Fatal("hook should be detached before the call")
	}
	// The hook is optional: an unwired process (dev box, tests) must fulfil
	// orders without panicking and without mailing anybody.
	NotifyFulfilled(db.Quote{ID: "q-no-hook", CustomerEmail: "buyer@example.com"})
}
