package db

import (
	"errors"
	"sync"
	"testing"
	"time"
)

func TestRatingIntentReservationIsOwnerBoundAndConsumedWithProof(t *testing.T) {
	s := ratingFixture(t)
	if err := s.CreateOrder(Order{ID: "other", UserID: "u1", Status: "delivered"}); err != nil {
		t.Fatal(err)
	}
	intent := RatingIntent{Key: RatingIntentKey("buyer", "shop", "5 great"), Kind: "order", ID: "o1", UserID: "u1", Buyer: "buyer", Recipient: "shop", CreatedAt: time.Now()}
	if err := s.SaveRatingIntent(intent); err != nil {
		t.Fatal(err)
	}
	conflicting := intent
	conflicting.ID = "other"
	if err := s.SaveRatingIntent(conflicting); !errors.Is(err, ErrRatingIntentBusy) {
		t.Fatalf("ambiguous intent allowed: %v", err)
	}
	if err := s.CancelRatingIntent(intent.Key, "order", "o1", "attacker"); !errors.Is(err, ErrNotFound) {
		t.Fatal("other user cancelled intent")
	}
	w := write("o1", "u1", 4, "wrong", "hash")
	w.IntentKey = intent.Key
	if _, err := s.ApplyRating(w); !errors.Is(err, ErrConflict) {
		t.Fatal("different memo consumed intent")
	}
	w = write("o1", "u1", 5, "great", "hash")
	w.IntentKey = intent.Key
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, _ = s.ApplyRating(w) }()
	}
	wg.Wait()
	st, _ := s.GetRatingState("order", "o1", "u1")
	if st.Edits != 1 {
		t.Fatalf("duplicate review count: %d", st.Edits)
	}
	if _, err := s.GetRatingIntent(intent.Key); !errors.Is(err, ErrNotFound) {
		t.Fatal("intent not consumed")
	}
	if err := s.SaveRatingIntent(conflicting); err != nil {
		t.Fatalf("completed reservation still blocked: %v", err)
	}
}
func TestRatingIntentCancellationReleasesOnlyTheSelectedPurchase(t *testing.T) {
	s := ratingFixture(t)
	intent := RatingIntent{Key: RatingIntentKey("buyer", "shop", "5 great"), Kind: "order", ID: "o1", UserID: "u1", Buyer: "buyer", Recipient: "shop", CreatedAt: time.Now()}
	if err := s.SaveRatingIntent(intent); err != nil {
		t.Fatal(err)
	}
	if err := s.CancelRatingIntent(intent.Key, "order", "o2", "u1"); !errors.Is(err, ErrNotFound) {
		t.Fatal("wrong order released intent")
	}
	if err := s.CancelRatingIntent(intent.Key, "order", "o1", "u1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetRatingIntent(intent.Key); !errors.Is(err, ErrNotFound) {
		t.Fatal("cancelled reservation remains")
	}
}
