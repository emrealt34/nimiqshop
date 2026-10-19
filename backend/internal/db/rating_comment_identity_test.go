package db

import (
	"testing"
	"time"

	"github.com/dgraph-io/badger/v4"
)

/* rating_comment_identity_test.go — what a public buyer comment is allowed
 * to say about the person who wrote it.
 *
 * The public comment list carries no purchase id (ListRatingComments blanks
 * kind and id for public callers), so nothing about the buyer can be
 * resolved when the list is READ. The identity is therefore captured when
 * the rating is WRITTEN — and the one rule that matters is that an
 * anonymous buyer's wallet is never copied anywhere, because the feed does
 * not publish that buyer's wallet beside their purchase either.
 */

func TestACommentRemembersWhoLeftItUnlessTheBuyerWasPrivate(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()

	public, err := store.FindOrCreateUserByAddress("NQ24 2XCD PK1C 7DR5 25U5 3PER U64V USR2 LPRK")
	if err != nil {
		t.Fatal(err)
	}
	private, err := store.FindOrCreateUserByAddress("NQ73SE1XYRRFQ8NCDQCPHLJMNR858P7V2HPD")
	if err != nil {
		t.Fatal(err)
	}

	seed := func(id, userID string, anonymous bool) Quote {
		now := time.Now().UTC()
		q := Quote{
			ID: id + "-quote", UserID: userID, Status: "fulfilled",
			ProductID: "amazon", ProductCountry: "TR", Denomination: "100 TRY", Quantity: 1,
			CustomerEmail: "buyer@example.test", Anonymous: anonymous,
			CreatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(30 * time.Minute),
		}
		if err := store.CreateQuoteWithDailyLimits(q, 0, 0, now); err != nil {
			t.Fatal(err)
		}
		return q
	}
	rate := func(q Quote, stars int, comment string) {
		if _, err := store.ApplyRating(RatingWrite{
			Kind: RatingKindQuote, ID: q.ID, UserID: q.UserID,
			Stars: stars, Comment: comment, TxHash: "tx-" + q.ID,
			MaxRatings: 3, Now: time.Now().UTC(),
		}); err != nil {
			t.Fatalf("rate %s: %v", q.ID, err)
		}
	}

	openQuote := seed("open", public.ID, false)
	hiddenQuote := seed("hidden", private.ID, true)
	rate(openQuote, 5, "Fast delivery, the code arrived in seconds.")
	rate(hiddenQuote, 4, "Good.")

	byID := func(list []RatingComment, id string) RatingComment {
		for _, c := range list {
			if c.ID == id {
				return c
			}
		}
		t.Fatalf("no comment for %s", id)
		return RatingComment{}
	}
	list, err := store.ListRatingComments(50, true)
	if err != nil {
		t.Fatal(err)
	}

	// A public purchase: the wallet the feed already publishes, and the
	// product the comment is about.
	open := byID(list, openQuote.ID)
	if open.Address != public.NimiqAddress {
		t.Fatalf("address = %q, want the buyer's wallet %q", open.Address, public.NimiqAddress)
	}
	if open.Anonymous {
		t.Fatal("a public purchase must not be marked anonymous")
	}
	if open.Product != "amazon" {
		t.Fatalf("product = %q, want amazon", open.Product)
	}
	// A private purchase keeps its secret: the wallet is not even copied.
	hidden := byID(list, hiddenQuote.ID)
	if hidden.Address != "" {
		t.Fatalf("an anonymous buyer's wallet must never be stored on the comment, got %q", hidden.Address)
	}
	if !hidden.Anonymous {
		t.Fatal("an anonymous purchase must be recorded as anonymous, not merely as addressless")
	}
	if hidden.Product != "amazon" {
		t.Fatalf("product = %q, the product of a private purchase is still its own", hidden.Product)
	}
	// And the public list never carries the purchase id to begin with.
	publicList, err := store.ListRatingComments(50, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range publicList {
		if c.ID != "" || c.Kind != "" {
			t.Fatalf("a public comment row must not carry a purchase id, got kind=%q id=%q", c.Kind, c.ID)
		}
	}
}

// Comments written before the identity was captured get filled in from the
// purchase they belong to — the one place that may still read the id.
func TestAnOldCommentGainsItsFaceOnTheBackfillPass(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	user, err := store.FindOrCreateUserByAddress("NQ38AU4HYEPNXCHLPBYDPF4CEYMCC92HRHEJ")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	q := Quote{
		ID: "old-quote", UserID: user.ID, Status: "fulfilled",
		ProductID: "carrefour", ProductCountry: "TR", Denomination: "50 TRY", Quantity: 1,
		CustomerEmail: "buyer@example.test", CreatedAt: now, UpdatedAt: now,
		ExpiresAt: now.Add(30 * time.Minute),
	}
	if err := store.CreateQuoteWithDailyLimits(q, 0, 0, now); err != nil {
		t.Fatal(err)
	}
	// A comment as the old code wrote it: no identity at all.
	if err := store.Update(func(tx *badger.Txn) error {
		return upsertRecentComment(tx, RatingComment{
			Kind: RatingKindQuote, ID: q.ID, Stars: 5, Comment: "Written before faces existed.", At: now,
		})
	}); err != nil {
		t.Fatal(err)
	}

	fixed, err := store.BackfillRatingCommentIdentity(0)
	if err != nil {
		t.Fatal(err)
	}
	if fixed != 1 {
		t.Fatalf("backfilled %d comment(s), want 1", fixed)
	}
	list, err := store.ListRatingComments(50, true)
	if err != nil {
		t.Fatal(err)
	}
	var got RatingComment
	for _, c := range list {
		if c.ID == q.ID {
			got = c
		}
	}
	if got.Address != user.NimiqAddress {
		t.Fatalf("address = %q, want %q after the backfill", got.Address, user.NimiqAddress)
	}
	if got.Product != "carrefour" {
		t.Fatalf("product = %q, want carrefour", got.Product)
	}
	if got.Comment != "Written before faces existed." {
		t.Fatalf("comment = %q, the backfill must not touch the words", got.Comment)
	}
	// A second pass has nothing left to do: the work disappears by itself.
	again, err := store.BackfillRatingCommentIdentity(0)
	if err != nil {
		t.Fatal(err)
	}
	if again != 0 {
		t.Fatalf("second pass filled %d comment(s), want 0 (idempotent)", again)
	}
}
