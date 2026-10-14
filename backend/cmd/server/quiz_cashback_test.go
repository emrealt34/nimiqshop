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
	shopper, _ := s.signIn()
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
