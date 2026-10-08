package db

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// ratingFixture opens a store with one delivered order owned by u1 and one
// order that is not yet delivered.
func ratingFixture(t *testing.T) *Store {
	t.Helper()
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	now := time.Now().UTC()
	if err := s.CreateOrder(Order{ID: "o1", UserID: "u1", Status: "delivered", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatalf("create delivered order: %v", err)
	}
	if err := s.CreateOrder(Order{ID: "o2", UserID: "u1", Status: "pending", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatalf("create pending order: %v", err)
	}
	return s
}

func write(id, userID string, stars int, comment, tx string) RatingWrite {
	return RatingWrite{
		Kind: RatingKindOrder, ID: id, UserID: userID, Stars: stars, Comment: comment,
		TxHash: tx, MaxRatings: 5, Now: time.Now().UTC(),
	}
}

func TestApplyRatingFirstThenChange(t *testing.T) {
	s := ratingFixture(t)

	agg, err := s.ApplyRating(write("o1", "u1", 5, "fast delivery", "tx1"))
	if err != nil {
		t.Fatalf("first rating: %v", err)
	}
	if agg.Count != 1 || agg.Sum != 5 || agg.Dist[5] != 1 {
		t.Fatalf("aggregate after first rating = %+v", agg)
	}

	st, err := s.GetRatingState(RatingKindOrder, "o1", "u1")
	if err != nil {
		t.Fatal(err)
	}
	if !st.Rated || st.Stars != 5 || st.Comment != "fast delivery" || st.Edits != 1 || st.RatingTx != "tx1" {
		t.Fatalf("state after first rating = %+v", st)
	}

	// A changed rating needs its own transaction and moves the aggregate by delta.
	agg, err = s.ApplyRating(write("o1", "u1", 3, "", "tx2"))
	if err != nil {
		t.Fatalf("changed rating: %v", err)
	}
	if agg.Count != 1 || agg.Sum != 3 || agg.Dist[5] != 0 || agg.Dist[3] != 1 {
		t.Fatalf("aggregate after change = %+v", agg)
	}
	st, _ = s.GetRatingState(RatingKindOrder, "o1", "u1")
	if st.Stars != 3 || st.Comment != "" || st.Edits != 2 || st.RatingTx != "tx2" {
		t.Fatalf("state after change = %+v", st)
	}
}

func TestRepeatedRatingIsRefused(t *testing.T) {
	s := ratingFixture(t)
	if _, err := s.ApplyRating(write("o1", "u1", 4, "ok", "tx1")); err != nil {
		t.Fatal(err)
	}
	_, err := s.ApplyRating(write("o1", "u1", 4, "ok", "tx2"))
	if !errors.Is(err, ErrRatingRepeat) {
		t.Fatalf("same stars and comment: err = %v, want ErrRatingRepeat", err)
	}
	st, _ := s.GetRatingState(RatingKindOrder, "o1", "u1")
	if st.Edits != 1 {
		t.Fatalf("repeat changed the edit count to %d", st.Edits)
	}
}

func TestTransactionProvesOneRatingOnly(t *testing.T) {
	s := ratingFixture(t)
	if _, err := s.ApplyRating(write("o1", "u1", 5, "", "txA")); err != nil {
		t.Fatal(err)
	}
	if !s.RatingTxUsed("txA") {
		t.Fatal("RatingTxUsed did not see the spent transaction")
	}
	// The same transaction must not prove a rating for another purchase.
	if err := s.CreateOrder(Order{ID: "o3", UserID: "u1", Status: "delivered"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApplyRating(write("o3", "u1", 5, "", "txA")); !errors.Is(err, ErrRatingTxUsed) {
		t.Fatalf("reused transaction: err = %v, want ErrRatingTxUsed", err)
	}
}

func TestRatingLimitPerPurchase(t *testing.T) {
	s := ratingFixture(t)
	stars := []int{1, 2, 3, 4, 5}
	for i, st := range stars {
		if _, err := s.ApplyRating(write("o1", "u1", st, "", "tx"+string(rune('a'+i)))); err != nil {
			t.Fatalf("rating %d: %v", i+1, err)
		}
	}
	// Five ratings used up; the sixth change is refused even with a fresh tx.
	if _, err := s.ApplyRating(write("o1", "u1", 4, "", "txz")); !errors.Is(err, ErrRatingLimit) {
		t.Fatalf("sixth rating: err = %v, want ErrRatingLimit", err)
	}
}

func TestOnlyOwnerDeliveredOrdersAreRateable(t *testing.T) {
	s := ratingFixture(t)
	if _, err := s.ApplyRating(write("o1", "someone-else", 5, "", "tx1")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("other owner: err = %v, want ErrNotFound", err)
	}
	if _, err := s.GetRatingState(RatingKindOrder, "o1", "someone-else"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("other owner state: err = %v, want ErrNotFound", err)
	}
	if _, err := s.ApplyRating(write("o2", "u1", 5, "", "tx2")); !errors.Is(err, ErrConflict) {
		t.Fatalf("pending order: err = %v, want ErrConflict", err)
	}
	// Nothing was written for the refused attempts.
	agg, _ := s.GetRatingAggregate()
	if agg.Count != 0 {
		t.Fatalf("refused ratings changed the aggregate: %+v", agg)
	}
}

func TestCommentsSwitchAndHiding(t *testing.T) {
	s := ratingFixture(t)
	if _, err := s.ApplyRating(write("o1", "u1", 5, "great shop", "tx1")); err != nil {
		t.Fatal(err)
	}

	pub, _ := s.ListRatingComments(10, false)
	if len(pub) != 1 || pub[0].Comment != "great shop" || pub[0].ID != "" || pub[0].Kind != "" {
		t.Fatalf("public list must hold the words but no purchase id: %+v", pub)
	}

	// Admin hides the comment: gone from the public list, stars still counted.
	if err := s.SetRatingCommentHidden(RatingKindOrder, "o1", true); err != nil {
		t.Fatal(err)
	}
	pub, _ = s.ListRatingComments(10, false)
	if len(pub) != 0 {
		t.Fatalf("hidden comment still public: %+v", pub)
	}
	adm, _ := s.ListRatingComments(10, true)
	if len(adm) != 1 || !adm[0].Hidden || adm[0].ID != "o1" {
		t.Fatalf("admin view should keep the hidden entry: %+v", adm)
	}
	if agg, _ := s.GetRatingAggregate(); agg.Count != 1 || agg.Sum != 5 {
		t.Fatalf("hiding a comment must not change the stars: %+v", agg)
	}

	// Switching comments off hides them everywhere except the admin view,
	// and a comment is refused while off.
	if err := s.SetRatingCommentHidden(RatingKindOrder, "o1", false); err != nil {
		t.Fatal(err)
	}
	if err := s.SetRatingCommentsEnabled(false); err != nil {
		t.Fatal(err)
	}
	if s.RatingCommentsEnabled() {
		t.Fatal("comments still enabled after switching off")
	}
	if pub, _ := s.ListRatingComments(10, false); len(pub) != 0 {
		t.Fatalf("public list while comments are off: %+v", pub)
	}
	if _, err := s.ApplyRating(write("o1", "u1", 2, "again", "tx2")); !errors.Is(err, ErrCommentsOff) {
		t.Fatalf("comment while off: err = %v, want ErrCommentsOff", err)
	}
	// A stars-only rating still works while comments are off.
	if _, err := s.ApplyRating(write("o1", "u1", 2, "", "tx3")); err != nil {
		t.Fatalf("stars-only while comments off: %v", err)
	}

	if err := s.SetRatingCommentsEnabled(true); err != nil {
		t.Fatal(err)
	}
	if !s.RatingCommentsEnabled() {
		t.Fatal("comments not re-enabled")
	}
}

func TestClearingTheCommentDropsItFromTheList(t *testing.T) {
	s := ratingFixture(t)
	if _, err := s.ApplyRating(write("o1", "u1", 5, "words", "tx1")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApplyRating(write("o1", "u1", 5, "", "tx2")); err != nil {
		t.Fatal(err)
	}
	if pub, _ := s.ListRatingComments(10, false); len(pub) != 0 {
		t.Fatalf("cleared comment still listed: %+v", pub)
	}
}

func TestPublicListNeverCarriesAddresses(t *testing.T) {
	s := ratingFixture(t)
	if _, err := s.ApplyRating(write("o1", "u1", 5, "good", "tx1")); err != nil {
		t.Fatal(err)
	}
	pub, _ := s.ListRatingComments(10, false)
	for _, c := range pub {
		if strings.Contains(c.Comment, "NQ") || c.ID != "" {
			t.Fatalf("public entry leaks identity: %+v", c)
		}
	}
}
