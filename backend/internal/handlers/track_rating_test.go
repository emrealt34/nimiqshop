package handlers

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/valyala/fasthttp"
	"nimiqshop/internal/db"
	"nimiqshop/internal/money"
)

/* track_rating_test.go — the rating a buyer left, read off the PUBLIC track
 * page of the purchase it belongs to.
 *
 * Both halves of it were already public: the stars ride the feed, the words
 * ride the comment list. Putting the two side by side here says nothing the
 * world could not read elsewhere. What it must NOT do is publish anything
 * the owner-only endpoints keep back: a comment an admin hid stays hidden,
 * and this page has never named the buyer — so no wallet travels with the
 * rating.
 */

func trackStatusOf(t *testing.T, h *Handlers, id string) map[string]interface{} {
	t.Helper()
	ctx := newTestCtx()
	ctx.Request.Header.SetMethod("GET")
	ctx.Request.SetRequestURI("/api/track/" + id)
	ctx.SetUserValue("id", id)
	h.TrackStatus(ctx)
	if code := ctx.Response.StatusCode(); code != fasthttp.StatusOK {
		t.Fatalf("track status = %d, want 200: %s", code, ctx.Response.Body())
	}
	var got map[string]interface{}
	if err := json.Unmarshal(ctx.Response.Body(), &got); err != nil {
		t.Fatalf("the track body is not JSON: %v (%s)", err, ctx.Response.Body())
	}
	return got
}

func ratingBlockOf(t *testing.T, body map[string]interface{}) map[string]interface{} {
	t.Helper()
	block, ok := body["rating"].(map[string]interface{})
	if !ok {
		t.Fatalf("the track page carries no rating: %v", body)
	}
	return block
}

const ratingTxHash = "a1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f90"

func TestATrackedPurchaseShowsTheRatingTheBuyerLeftIt(t *testing.T) {
	store, err := db.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	h := &Handlers{Store: store}

	now := time.Now().UTC()
	ratedAt := now.Add(time.Minute)
	q := db.Quote{
		ID: "q-rated", UserID: "buyer-1", Status: "fulfilled",
		ProductID: "amazon-tr", ProductCountry: "TR", Denomination: "100 TRY", Quantity: 1,
		ProductUSD: money.FromFloat(20), CustomerEmail: "buyer@example.test",
		Rating: 5, RatedAt: &ratedAt, RatingTx: ratingTxHash,
		RatingComment: "payment made under 10 seconds",
		CreatedAt:     now, UpdatedAt: now, ExpiresAt: now.Add(30 * time.Minute),
	}
	if err := store.CreateQuoteWithDailyLimits(q, 0, 0, now); err != nil {
		t.Fatal(err)
	}

	body := trackStatusOf(t, h, q.ID)
	block := ratingBlockOf(t, body)
	if block["stars"] != float64(5) {
		t.Fatalf("stars = %v, want 5", block["stars"])
	}
	if block["comment"] != "payment made under 10 seconds" {
		t.Fatalf("comment = %v, want the words the buyer wrote", block["comment"])
	}
	// The rating's own on-chain transaction, so anyone can check it.
	if block["tx"] != ratingTxHash {
		t.Fatalf("tx = %v, want the rating transaction", block["tx"])
	}
	if block["at"] == nil || block["at"] == "" {
		t.Fatalf("at = %v, want when the rating was written", block["at"])
	}
	// And nothing that is not already public: no wallet, no recipient.
	for _, key := range []string{"address", "customer_email", "phone_number", "beneficiary_account"} {
		if body[key] != nil {
			t.Fatalf("the track page published %s = %v", key, body[key])
		}
	}
}

func TestATrackedOrderShowsItsRatingToo(t *testing.T) {
	store, err := db.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	h := &Handlers{Store: store}

	now := time.Now().UTC()
	ratedAt := now.Add(time.Minute)
	o := db.Order{
		ID: "o-rated", UserID: "buyer-2", Kind: "product", Status: "delivered",
		ProductID: "netflix", Quantity: 1, PriceUSD: money.FromFloat(15),
		Rating: 4, RatedAt: &ratedAt, RatingTx: ratingTxHash,
		RatingComment: "worked fine",
		CreatedAt:     now, UpdatedAt: now,
	}
	if err := store.CreateOrder(o); err != nil {
		t.Fatal(err)
	}

	block := ratingBlockOf(t, trackStatusOf(t, h, o.ID))
	if block["stars"] != float64(4) || block["comment"] != "worked fine" {
		t.Fatalf("rating = %v, want the order's own 4 stars and its comment", block)
	}
	if block["tx"] != ratingTxHash {
		t.Fatalf("tx = %v, want the rating transaction", block["tx"])
	}
}

func TestACommentAnAdminHidStaysHiddenOnTheTrackPage(t *testing.T) {
	store, err := db.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	h := &Handlers{Store: store}

	now := time.Now().UTC()
	q := db.Quote{
		ID: "q-hidden", UserID: "buyer-3", Status: "fulfilled",
		ProductID: "amazon-tr", ProductCountry: "TR", Quantity: 1,
		Rating: 3, RatedAt: &now,
		RatingComment:       "an admin took this down",
		RatingCommentHidden: true,
		CreatedAt:           now, UpdatedAt: now, ExpiresAt: now.Add(30 * time.Minute),
	}
	if err := store.CreateQuoteWithDailyLimits(q, 0, 0, now); err != nil {
		t.Fatal(err)
	}

	block := ratingBlockOf(t, trackStatusOf(t, h, q.ID))
	// Hiding a comment is an admin decision about the shop as a whole: the
	// stars stay, the words do not come back on another page.
	if block["stars"] != float64(3) {
		t.Fatalf("stars = %v, want 3 — hiding the comment is not unrating", block["stars"])
	}
	if block["comment"] != "" {
		t.Fatalf("comment = %v, want a hidden comment to stay hidden", block["comment"])
	}
}

func TestAPurchaseNobodyRatedCarriesNoRatingAtAll(t *testing.T) {
	store, err := db.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	h := &Handlers{Store: store}

	now := time.Now().UTC()
	q := db.Quote{
		ID: "q-unrated", UserID: "buyer-4", Status: "fulfilled",
		ProductID: "amazon-tr", ProductCountry: "TR", Quantity: 1,
		CreatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(30 * time.Minute),
	}
	if err := store.CreateQuoteWithDailyLimits(q, 0, 0, now); err != nil {
		t.Fatal(err)
	}
	if body := trackStatusOf(t, h, q.ID); body["rating"] != nil {
		t.Fatalf("an unrated purchase must carry no rating block, got %v", body["rating"])
	}
}
