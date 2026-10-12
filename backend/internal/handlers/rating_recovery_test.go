package handlers

import (
	"strings"
	"testing"
	"time"

	"github.com/valyala/fasthttp"
	"nimiqshop/internal/db"
)

func TestSavedRatingAcknowledgementIsIdempotentAndOwnerOnly(t *testing.T) {
	store, err := db.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	if err := store.CreateOrder(db.Order{ID: "saved-rating", UserID: "buyer", Status: "delivered"}); err != nil {
		t.Fatal(err)
	}
	hash := strings.Repeat("ab", 32)
	if _, err := store.ApplyRating(db.RatingWrite{Kind: db.RatingKindOrder, ID: "saved-rating", UserID: "buyer", Stars: 5, Comment: "excellent", TxHash: hash, MaxRatings: 5, Now: time.Now()}); err != nil {
		t.Fatal(err)
	}
	h := &Handlers{Store: store}
	for _, user := range []string{"buyer", "other"} {
		var ctx fasthttp.RequestCtx
		ctx.SetUserValue("id", "saved-rating")
		ctx.SetUserValue("user_id", user)
		ctx.Request.SetBodyString(`{"stars":5,"comment":"excellent","tx_hash":"` + hash + `"}`)
		h.RateOrder(&ctx)
		if (ctx.Response.StatusCode() == 200) != (user == "buyer") {
			t.Fatalf("recovery ownership: %q %s", user, ctx.Response.Body())
		}
	}
	state, err := store.GetRatingState(db.RatingKindOrder, "saved-rating", "buyer")
	if err != nil || state.Edits != 1 {
		t.Fatal("acknowledgement counted a duplicate rating")
	}
}
