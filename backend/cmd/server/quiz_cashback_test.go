package main

import (
	"nimiqshop/internal/db"
	"strings"
	"testing"
	"time"
)

func TestQuizCashbackCodeIsServerGatedAndSingleUse(t *testing.T) {
	s := bootStack(t, stackOptions{})
	auth, address := s.signIn()
	user, err := s.h.Store.GetUserByAddress(address)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	response := s.do("POST", "/api/admin/quiz", db.QuizCompetition{Title: "Learn shop", CashbackReward: &db.QuizCashbackReward{Code: "ILOVENIM", CashbackBps: 750}, StartsAt: now.Add(-time.Minute), EndsAt: now.Add(time.Hour), Questions: []db.QuizQuestion{{Prompt: "Choose", Options: []string{"Yes", "No"}, CorrectIndex: 0}}}, s.adminHeaders())
	if response.status != 200 {
		t.Fatalf("admin create %d %s", response.status, response.body)
	}
	c, err := s.h.Store.GetQuizCompetition(response.json(t)["id"].(string))
	if err != nil {
		t.Fatal(err)
	}
	if response := s.do("POST", "/api/admin/quiz/"+c.ID+"/publish", nil, s.adminHeaders()); response.status != 200 {
		t.Fatalf("publish %d %s", response.status, response.body)
	}
	for _, headers := range []map[string]string{nil, auth} {
		r := s.do("GET", "/api/cashback/code?code=ILOVENIM", nil, headers)
		if r.status != 403 {
			t.Fatalf("unearned coupon accepted: %d %s", r.status, r.body)
		}
	}
	order := map[string]any{"product_id": "test-steam", "country": "US", "quantity": 1, "denomination": "50 USD", "product_value": 50, "email": "buyer@example.com", "cashback_code": "ILOVENIM"}
	if r := s.do("POST", "/api/quotes", order, auth); r.status != 400 {
		t.Fatalf("unearned coupon checkout: %d %s", r.status, r.body)
	}
	if r := s.do("POST", "/api/quiz/"+c.ID+"/start", map[string]any{}, auth); r.status != 200 {
		t.Fatalf("start %d %s", r.status, r.body)
	}
	if r := s.do("POST", "/api/quiz/"+c.ID+"/submit", map[string]any{"answers": []int{0}}, auth); r.status != 200 {
		t.Fatalf("submit %d %s", r.status, r.body)
	}
	if !s.h.Store.QuizCashbackUnlocked("ILOVENIM", user.ID) {
		t.Fatal("completed quiz did not activate account code")
	}
	if r := s.do("GET", "/api/cashback/code?code=ILOVENIM", nil, auth); r.status != 200 || r.json(t)["cashback_bps"] != float64(750) {
		t.Fatalf("earned coupon %d %s", r.status, r.body)
	}
	first := s.do("POST", "/api/quotes", order, auth)
	id := quoteIDFrom(t, first)
	q, err := s.h.Store.GetQuote(id)
	if err != nil || q.CashbackCode != "ILOVENIM" || q.CashbackCodeBps != 750 {
		t.Fatalf("coupon rate not persisted %+v %v", q, err)
	}
	order["product_id"] = "test-airbnb"
	order["denomination"] = "range"
	if r := s.do("POST", "/api/quotes", order, auth); r.status < 400 {
		t.Fatalf("coupon used in two checkouts: %d %s", r.status, r.body)
	}
	other, _ := s.signIn()
	if r := s.do("GET", "/api/cashback/code?code=ILOVENIM", nil, other); r.status != 403 {
		t.Fatal("grant leaked to another account")
	}
}

func TestAdminTestActivityToggleRequiresAdminAndLabelsPublicRows(t *testing.T) {
	s := bootStack(t, stackOptions{testMode: true})
	shopper, shopperAddress := s.signIn()
	for _, headers := range []map[string]string{nil, shopper} {
		if r := s.do("POST", "/api/admin/test-activity", map[string]any{"enabled": true}, headers); r.status != 401 {
			t.Fatalf("unauthorized toggle %d", r.status)
		}
	}
	q := db.Quote{ID: "toggle-api-test", UserID: "buyer", ProductID: "Test product", TestMode: true, Status: "awaiting_payment"}
	if err := s.h.Store.CreateQuoteWithDailyLimits(q, 0, 0, time.Now().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := s.h.Store.CompleteQuoteWithFulfillment(q.ID, nil); err != nil {
		t.Fatal(err)
	}
	// The same switch governs the cashback page and the star ratings. A
	// delivered test purchase of the shopper earns (simulated) cashback at
	// the operator's 1% base rate, and the quote owner leaves a comment.
	if r := s.do("POST", "/api/admin/settings/cashback", map[string]any{"cashback_bps": 100}, s.adminHeaders()); r.status != 200 {
		t.Fatalf("cashback settings %d %s", r.status, r.body)
	}
	owner, err := s.h.Store.GetUserByAddress(shopperAddress)
	if err != nil {
		t.Fatal(err)
	}
	paid := db.Quote{ID: "toggle-api-cashback", UserID: owner.ID, ProductID: "Test product", TestMode: true, Status: "awaiting_payment", EstimatedNIM: 100, NimUsdRate: 0.01, ProductUSD: 1_000_000}
	if err := s.h.Store.CreateQuoteWithDailyLimits(paid, 0, 0, time.Now().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := s.h.Store.CompleteQuoteWithFulfillment(paid.ID, nil); err != nil {
		t.Fatal(err)
	}
	if cb, err := s.h.Store.GetCashbackByQuote(paid.ID); err != nil || !cb.TestMode || cb.AmountLuna <= 0 || cb.Status != db.CashbackQueued {
		t.Fatalf("precondition: simulated cashback row expected: %+v %v", cb, err)
	}
	if _, err := s.h.Store.ApplyRating(db.RatingWrite{Kind: db.RatingKindQuote, ID: q.ID, UserID: "buyer", Stars: 2, Comment: "only a simulation", TxHash: "toggle-rating-tx", MaxRatings: 5, Now: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	for _, enabled := range []bool{true, false} {
		if r := s.do("POST", "/api/admin/test-activity", map[string]any{"enabled": enabled}, s.adminHeaders()); r.status != 200 {
			t.Fatalf("toggle %d %s", r.status, r.body)
		}
		r := s.do("GET", "/api/activity", nil, nil)
		if strings.Contains(string(r.body), q.ID) != enabled {
			t.Fatalf("public visibility %v %s", enabled, r.body)
		}
		if enabled && (!strings.Contains(string(r.body), `"test_mode":true`) || !strings.Contains(string(r.body), `"test_activity_visible":true`)) {
			t.Fatal("test marker missing")
		}
		wantCount := float64(0)
		if enabled {
			wantCount = 1
		}
		if summary, _ := r.json(t)["summary"].(map[string]any); summary["count"] != wantCount {
			t.Fatalf("enabled=%v: activity star summary should count %v test ratings: %v", enabled, wantCount, summary)
		}
		if r := s.do("GET", "/api/ratings/summary", nil, nil); r.status != 200 || r.json(t)["count"] != wantCount {
			t.Fatalf("enabled=%v: ratings summary %d %s", enabled, r.status, r.body)
		}
		if r := s.do("GET", "/api/ratings/comments", nil, nil); r.status != 200 || strings.Contains(string(r.body), "only a simulation") != enabled {
			t.Fatalf("enabled=%v: ratings comments %d %s", enabled, r.status, r.body)
		}
		lb := s.do("GET", "/api/cashback/leaderboard", nil, nil)
		if lb.status != 200 {
			t.Fatalf("leaderboard %d %s", lb.status, lb.body)
		}
		totals, _ := lb.json(t)["totals"].(map[string]any)
		rows, _ := lb.json(t)["leaderboard"].([]any)
		if totals["orders"] != wantCount || len(rows) != int(wantCount) || strings.Contains(strings.ReplaceAll(string(lb.body), " ", ""), strings.ReplaceAll(shopperAddress, " ", "")) != enabled {
			t.Fatalf("enabled=%v: cashback page should count %v test orders: %s", enabled, wantCount, lb.body)
		}
	}
	// The owner still sees their own test order and its cashback regardless.
	if r := s.do("GET", "/api/cashback/me", nil, shopper); r.status != 200 || !strings.Contains(string(r.body), paid.ID) {
		t.Fatalf("owner lost their own cashback row: %d %s", r.status, r.body)
	}
}
func TestUniqueQuizViewReturnsOnlyTheCompletingAccountsCode(t *testing.T) {
	s := bootStack(t, stackOptions{})
	auth, address := s.signIn()
	other, _ := s.signIn()
	user, err := s.h.Store.GetUserByAddress(address)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	c, err := s.h.Store.PutQuizCompetition(db.QuizCompetition{Title: "Private code", CashbackReward: &db.QuizCashbackReward{Code: "PERSONAL", CodeMode: "unique", CashbackBps: 500}, StartsAt: now.Add(-time.Minute), EndsAt: now.Add(time.Hour), Questions: []db.QuizQuestion{{Prompt: "Pick", Options: []string{"Yes", "No"}, CorrectIndex: 0}}}, true, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.h.Store.PublishQuiz(c.ID, now); err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.h.Store.StartQuiz(c.ID, user.ID, now); err != nil {
		t.Fatal(err)
	}
	if _, err = s.h.Store.SaveQuizAnswers(c.ID, user.ID, []int{0}, true, now); err != nil {
		t.Fatal(err)
	}
	mine := s.do("GET", "/api/quiz/"+c.ID, nil, auth)
	if mine.status != 200 {
		t.Fatal(mine.status)
	}
	code := mine.json(t)["cashback_reward"].(map[string]any)["code"].(string)
	if code == "PERSONAL" || mine.json(t)["cashback_unlocked"] != true {
		t.Fatal("personal code missing")
	}
	theirs := s.do("GET", "/api/quiz/"+c.ID, nil, other)
	if strings.Contains(string(theirs.body), code) || strings.Contains(string(theirs.body), "PERSONAL") || theirs.json(t)["cashback_unlocked"] != false {
		t.Fatal("personal code exposed to another account")
	}
	if r := s.do("GET", "/api/cashback/code?code="+code, nil, auth); r.status != 200 {
		t.Fatal("owner cannot activate personal code")
	}
	if r := s.do("GET", "/api/cashback/code?code=PERSONAL", nil, auth); r.status != 403 {
		t.Fatal("prefix accepted as a coupon")
	}
}

func TestQuizAnswerReviewIsPrivateAndOnlyRevealedAfterSubmission(t *testing.T) {
	for _, cashback := range []bool{true, false} {
		t.Run(map[bool]string{true: "cashback", false: "ranked"}[cashback], func(t *testing.T) {
			s := bootStack(t, stackOptions{})
			owner, _ := s.signIn()
			other, _ := s.signIn()
			now := time.Now().UTC()
			correct := 20
			c := db.QuizCompetition{Title: "Review quiz", Prize: db.QuizPrize{Title: "Test prize"}, StartsAt: now.Add(-time.Hour), EndsAt: now.Add(time.Hour), Questions: []db.QuizQuestion{{Prompt: "Choose NIM", Options: []string{"NIM", "BTC"}, CorrectIndex: 0}, {Type: "range", Prompt: "2% of 1000", Min: 0, Max: 100, CorrectValue: &correct}}}
			if cashback {
				c.CashbackReward = &db.QuizCashbackReward{Code: "REVIEW", CashbackBps: 100}
			}
			c, err := s.h.Store.PutQuizCompetition(c, true, now)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = s.h.Store.PublishQuiz(c.ID, now); err != nil {
				t.Fatal(err)
			}
			path := "/api/quiz/" + c.ID
			for _, headers := range []map[string]string{owner, other} {
				r := s.do("GET", path, nil, headers)
				if r.status != 200 || strings.Contains(string(r.body), `"review"`) || strings.Contains(string(r.body), `"correct_answer"`) {
					t.Fatalf("premature answer disclosure: %d %s", r.status, r.body)
				}
			}
			r := s.do("POST", path+"/start", map[string]any{}, owner)
			if r.status != 200 || strings.Contains(string(r.body), `"correct_answer"`) || strings.Contains(string(r.body), `"correct_value"`) {
				t.Fatalf("start disclosure: %d %s", r.status, r.body)
			}
			if r = s.do("POST", path+"/submit", map[string]any{"answers": []int{1, 19}}, owner); r.status != 200 {
				t.Fatalf("submit: %d %s", r.status, r.body)
			}
			if !cashback {
				r = s.do("GET", path, nil, owner)
				if strings.Contains(string(r.body), `"review"`) {
					t.Fatal("ranked solutions exposed before closing")
				}
				c, err = s.h.Store.GetQuizCompetition(c.ID)
				if err != nil {
					t.Fatal(err)
				}
				c.EndsAt = now.Add(-time.Minute)
				if _, err = s.h.Store.PutQuizCompetition(c, false, now); err != nil {
					t.Fatal(err)
				}
			}
			r = s.do("GET", path, nil, owner)
			if r.status != 200 {
				t.Fatalf("review: %d %s", r.status, r.body)
			}
			rows, ok := r.json(t)["review"].([]any)
			if !ok || len(rows) != 2 {
				t.Fatalf("missing review: %s", r.body)
			}
			for i, expected := range []float64{0, 20} {
				row := rows[i].(map[string]any)
				if row["correct"] != false || row["correct_answer"] != expected || row["answer"] != []float64{1, 19}[i] {
					t.Fatalf("wrong review: %+v", row)
				}
			}
			r = s.do("GET", path, nil, other)
			if strings.Contains(string(r.body), `"review"`) {
				t.Fatal("review leaked to another account")
			}
			if r = s.do("GET", path, nil, nil); r.status != 401 {
				t.Fatalf("guest review status %d", r.status)
			}
		})
	}
}
