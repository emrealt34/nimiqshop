package db

import (
	"testing"
	"time"

	"github.com/dgraph-io/badger/v4"
	"nimiqshop/internal/money"
)

func TestPublicFeedExcludesLegacySimulationsAndPreservesRealPurchases(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	ids := []string{"29760a02-9a43-40a2-8d5c-71277624df94", "0ac3ca80-a9cd-4524-a1fd-0c2fc95c583b", "real-carrefour", "real-amazon"}
	for _, id := range ids {
		if err := s.CreateOrder(Order{ID: id, UserID: "same-buyer", ProductID: "Carrefour", Status: "pending", PriceUSD: money.FromFloat(5)}); err != nil {
			t.Fatal(err)
		}
		if err := s.SetOrderStatus(id, "delivered"); err != nil {
			t.Fatal(err)
		}
	}
	// Emulate the old production index, with excluded rows newest and a
	// duplicate entry. The requested page still has to contain real rows.
	if err := s.Update(func(tx *badger.Txn) error {
		for i, id := range []string{"real-amazon", "real-carrefour", "real-carrefour", ids[0], ids[1], "missing"} {
			if err := tx.Set(feedOrderIndexKey(time.Now().Add(time.Duration(i)*time.Hour).UnixNano(), id), []byte(id)); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	rows, err := s.ListFeedOrders(2)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].ID != "real-carrefour" || rows[1].ID != "real-amazon" {
		t.Fatalf("excluded or duplicate rows displaced real orders: %#v", rows)
	}
	for _, id := range ids {
		o, err := s.GetOrderForUser(id, "same-buyer")
		if err != nil || o.Status != "delivered" {
			t.Fatalf("activity moderation changed order history: %s: %#v %v", id, o, err)
		}
	}
}

func TestPublicFeedRejectsOldAndNewTestQuotes(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	for _, q := range []Quote{
		{ID: "real-carrefour", UserID: "buyer", ProductID: "Carrefour", ProductCountry: "FR", Status: "awaiting_payment"},
		{ID: "future-test", UserID: "buyer", TestMode: true, Status: "awaiting_payment"},
		{ID: "old-test", UserID: "buyer", TestMode: true, Status: "awaiting_payment"},
		{ID: "29760a02-9a43-40a2-8d5c-71277624df94", UserID: "buyer", Status: "awaiting_payment"},
	} {
		if err := s.CreateQuoteWithDailyLimits(q, 0, 0, time.Now().Add(-time.Hour)); err != nil {
			t.Fatal(err)
		}
		if err := s.CompleteQuoteWithFulfillment(q.ID, nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Update(func(tx *badger.Txn) error {
		return tx.Set(feedQuoteIndexKey(time.Now().Add(time.Hour).UnixNano(), "old-test"), []byte("old-test"))
	}); err != nil {
		t.Fatal(err)
	}
	rows, err := s.ListFeedQuotes(1)
	if err != nil || len(rows) != 1 || rows[0].ID != "real-carrefour" {
		t.Fatalf("expected real Carrefour only: %#v %v", rows, err)
	}
	for _, id := range []string{"future-test", "old-test", "29760a02-9a43-40a2-8d5c-71277624df94"} {
		if q, err := s.GetQuoteForUser(id, "buyer"); err != nil || q.Status != "fulfilled" {
			t.Fatalf("test record was lost: %s %v", id, err)
		}
	}
}

func TestActivitySwitchShowsAndHidesSimulationsWithoutChangingPayments(t *testing.T) {
	dir := t.TempDir()
	s, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	q := Quote{ID: "test-switch", UserID: "buyer", TestMode: true, Status: "awaiting_payment"}
	if err = s.CreateQuoteWithDailyLimits(q, 0, 0, time.Now().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err = s.CompleteQuoteWithFulfillment(q.ID, nil); err != nil {
		t.Fatal(err)
	}
	if rows, err := s.ListFeedQuotes(10); err != nil || len(rows) != 0 {
		t.Fatal("test visible by default", err)
	}
	for _, enabled := range []bool{true, false, true} {
		if err = s.SetTestActivityVisible(enabled); err != nil {
			t.Fatal(err)
		}
		rows, err := s.ListFeedQuotes(10)
		if err != nil {
			t.Fatal(err)
		}
		want := 0
		if enabled {
			want = 1
		}
		if len(rows) != want {
			t.Fatalf("toggle=%v rows=%d", enabled, len(rows))
		}
		stored, err := s.GetQuote(q.ID)
		if err != nil || !stored.TestMode || stored.Status != "fulfilled" {
			t.Fatal("toggle changed payment", err)
		}
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = New(dir)
	if err != nil {
		t.Fatal(err)
	}
	if enabled, err := s.TestActivityVisible(); err != nil || !enabled {
		t.Fatal("setting lost after restart", err)
	}
}

// The operator's switch covers EVERY public surface, not just the feed: the
// cashback totals and leaderboard, the star summary and the recent comments
// all leave a hidden test purchase out and all show it once switched on.
func TestTestOrderSwitchCoversCashbackTotalsRatingSummaryAndComments(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	now := time.Now().UTC()
	for _, u := range []User{{ID: "real-user", NimiqAddress: "NQ07 0000 0000 0000 0000 0000 0000 0000 0000"}, {ID: "test-user", NimiqAddress: "NQ23 E6AH UL22 YN31 RNTV GS97 JJA4 TKT7 8YSK"}} {
		if _, err := s.GetOrCreateUserByID(u.ID, u.NimiqAddress); err != nil {
			t.Fatal(err)
		}
	}
	quotes := []Quote{
		{ID: "real-q", UserID: "real-user", ProductID: "Carrefour", Status: "awaiting_payment"},
		{ID: "test-q", UserID: "test-user", ProductID: "Test product", TestMode: true, Status: "awaiting_payment"},
	}
	for _, q := range quotes {
		if err := s.CreateQuoteWithDailyLimits(q, 0, 0, now.Add(-time.Hour)); err != nil {
			t.Fatal(err)
		}
		if err := s.CompleteQuoteWithFulfillment(q.ID, nil); err != nil {
			t.Fatal(err)
		}
	}
	// One paid cashback row per purchase; the test one carries TestMode the
	// way enqueueCashbackOnFulfill writes it.
	if err := s.Update(func(tx *badger.Txn) error {
		for _, cb := range []Cashback{
			{ID: "cb-real", QuoteID: "real-q", UserID: "real-user", Recipient: "NQ07 0000 0000 0000 0000 0000 0000 0000 0000", AmountLuna: 200000, Status: CashbackPaid, CreatedAt: now, UpdatedAt: now},
			{ID: "cb-test", QuoteID: "test-q", UserID: "test-user", Recipient: "NQ23 E6AH UL22 YN31 RNTV GS97 JJA4 TKT7 8YSK", AmountLuna: 900000, Status: CashbackPaid, TestMode: true, CreatedAt: now, UpdatedAt: now},
		} {
			raw, err := marshal(cb)
			if err != nil {
				return err
			}
			if err := tx.Set(cashbackKey(cb.ID), raw); err != nil {
				return err
			}
			if err := tx.Set(cashbackStatusIndexKey(cb.Status, cb.ID), []byte(cb.ID)); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// One on-chain rating per purchase: 4 stars real, 1 star with words on the test.
	for i, w := range []RatingWrite{
		{Kind: RatingKindQuote, ID: "real-q", UserID: "real-user", Stars: 4, Comment: "great", MaxRatings: 5, Now: now},
		{Kind: RatingKindQuote, ID: "test-q", UserID: "test-user", Stars: 1, Comment: "just testing", MaxRatings: 5, Now: now.Add(time.Second)},
	} {
		w.TxHash = "tx" + string(rune('a'+i))
		if _, err := s.ApplyRating(w); err != nil {
			t.Fatal(err)
		}
	}

	check := func(shown bool) {
		t.Helper()
		totals, board, err := s.CashbackLeaderboardAndTotals("all", 50)
		if err != nil {
			t.Fatal(err)
		}
		agg, err := s.PublicRatingAggregate()
		if err != nil {
			t.Fatal(err)
		}
		comments, err := s.ListRatingComments(10, false)
		if err != nil {
			t.Fatal(err)
		}
		feed, err := s.ListFeedQuotes(10)
		if err != nil {
			t.Fatal(err)
		}
		if shown {
			if totals.Orders != 2 || totals.Earners != 2 || len(board) != 2 || board[0].User != "NQ23 E6AH UL22 YN31 RNTV GS97 JJA4 TKT7 8YSK" {
				t.Fatalf("shown: cashback should include the test purchase: %+v %+v", totals, board)
			}
			if agg.Count != 2 || agg.Sum != 5 || agg.Dist[1] != 1 || agg.Dist[4] != 1 {
				t.Fatalf("shown: star summary should include the test rating: %+v", agg)
			}
			if len(comments) != 2 || comments[0].Comment != "just testing" {
				t.Fatalf("shown: comments should include the test comment: %+v", comments)
			}
			if len(feed) != 2 {
				t.Fatalf("shown: feed should list both: %d", len(feed))
			}
			return
		}
		if totals.Orders != 1 || totals.Earners != 1 || totals.TotalNIM != 2 || len(board) != 1 || board[0].User != "NQ07 0000 0000 0000 0000 0000 0000 0000 0000" {
			t.Fatalf("hidden: test cashback leaked into the public totals: %+v %+v", totals, board)
		}
		if agg.Count != 1 || agg.Sum != 4 || agg.Dist[1] != 0 || agg.Dist[4] != 1 {
			t.Fatalf("hidden: test rating leaked into the star summary: %+v", agg)
		}
		if len(comments) != 1 || comments[0].Comment != "great" {
			t.Fatalf("hidden: test comment leaked: %+v", comments)
		}
		if len(feed) != 1 || feed[0].ID != "real-q" {
			t.Fatalf("hidden: feed should list the real purchase only: %+v", feed)
		}
	}
	check(false) // default: hidden
	if err := s.SetTestActivityVisible(true); err != nil {
		t.Fatal(err)
	}
	check(true)
	if err := s.SetTestActivityVisible(false); err != nil {
		t.Fatal(err)
	}
	check(false)

	// The admin view and the global counter keep everything.
	if all, err := s.GetRatingAggregate(); err != nil || all.Count != 2 {
		t.Fatalf("global aggregate changed: %+v %v", all, err)
	}
	if adminComments, err := s.ListRatingComments(10, true); err != nil || len(adminComments) != 2 || !adminComments[0].TestMode || adminComments[1].TestMode {
		t.Fatalf("admin comments should keep the test flag: %+v %v", adminComments, err)
	}

	// A database written before the test-only counter existed: the global
	// counter already holds the test rating but the test counter is empty.
	// The boot-time rebuild recounts both from the records.
	if err := s.Update(func(tx *badger.Txn) error { return tx.Delete([]byte(metaRatingAggregateTestKey)) }); err != nil {
		t.Fatal(err)
	}
	if agg, _ := s.PublicRatingAggregate(); agg.Count != 2 {
		t.Fatalf("precondition: stale counter should leak: %+v", agg)
	}
	all, test, err := s.RebuildRatingAggregates()
	if err != nil || all.Count != 2 || all.Sum != 5 || test.Count != 1 || test.Sum != 1 {
		t.Fatalf("rebuild: all=%+v test=%+v err=%v", all, test, err)
	}
	check(false)
	// A changed rating on the test purchase moves both counters.
	if _, err := s.ApplyRating(RatingWrite{Kind: RatingKindQuote, ID: "test-q", UserID: "test-user", Stars: 5, Comment: "", TxHash: "txc", MaxRatings: 5, Now: now.Add(2 * time.Second)}); err != nil {
		t.Fatal(err)
	}
	if agg, _ := s.PublicRatingAggregate(); agg.Count != 1 || agg.Sum != 4 {
		t.Fatalf("re-rated test purchase leaked: %+v", agg)
	}
	if all, _ := s.GetRatingAggregate(); all.Count != 2 || all.Sum != 9 || all.Dist[5] != 1 || all.Dist[1] != 0 {
		t.Fatalf("global aggregate wrong after re-rating: %+v", all)
	}
}
