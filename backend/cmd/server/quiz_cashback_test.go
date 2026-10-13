package main

import (
	"nimiqshop/internal/db"
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
