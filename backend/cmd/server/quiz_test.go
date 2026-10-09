package main

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestQuizAuthenticationPrivacyScoringAndManualAward(t *testing.T) {
	s := bootStack(t, stackOptions{testMode: true})
	shopper, _ := s.signIn()
	other, _ := s.signIn()
	admin := s.adminHeaders()
	for _, request := range []struct{ method, path string }{
		{"GET", "/api/quiz"}, {"GET", "/api/quiz/missing"}, {"POST", "/api/quiz/missing/start"},
		{"PUT", "/api/quiz/missing/answers"}, {"POST", "/api/quiz/missing/submit"}, {"POST", "/api/quiz/missing/claim"},
	} {
		if res := s.do(request.method, request.path, map[string]any{}, nil); res.status != 401 {
			t.Fatalf("anonymous %s %s: %d %s", request.method, request.path, res.status, res.body)
		}
	}
	if res := s.do("GET", "/api/admin/quiz", nil, shopper); res.status != 401 {
		t.Fatalf("customer reached admin quiz: %d %s", res.status, res.body)
	}
	end := time.Now().UTC().Add(2 * time.Second)
	draft := s.do("POST", "/api/admin/quiz", map[string]any{
		"title": "Nimiq competition", "prize": map[string]any{"title": "Amazon gift card", "value": "5 USD"},
		"starts_at": time.Now().UTC().Add(-time.Hour), "ends_at": end,
		"questions": []map[string]any{{"prompt": "Which ticker?", "options": []string{"BTC", "NIM"}, "correct_index": 1}, {"prompt": "Which wallet?", "options": []string{"Nimiq Pay", "Other"}, "correct_index": 0}},
	}, admin)
	if draft.status != 200 {
		t.Fatalf("create: %d %s", draft.status, draft.body)
	}
	id := draft.json(t)["id"].(string)
	root := "/api/quiz/" + id
	aroot := "/api/admin/quiz/" + id
	if res := s.do("GET", root, nil, shopper); res.status != 404 {
		t.Fatalf("draft exposed: %d %s", res.status, res.body)
	}
	if res := s.do("POST", aroot+"/publish", nil, admin); res.status != 200 {
		t.Fatalf("publish: %d %s", res.status, res.body)
	}
	for _, path := range []string{"/api/quiz", root} {
		res := s.do("GET", path, nil, shopper)
		if res.status != 200 || strings.Contains(string(res.body), "correct_index") || strings.Contains(string(res.body), `"questions":`) {
			t.Fatalf("unsafe quiz view: %d %s", res.status, res.body)
		}
		if !strings.Contains(res.headers.Get("Cache-Control"), "no-store") {
			t.Fatalf("private view cached: %v", res.headers)
		}
	}
	game := s.do("POST", root+"/start", nil, shopper)
	if game.status != 200 || strings.Contains(string(game.body), "correct_index") {
		t.Fatalf("unsafe game: %d %s", game.status, game.body)
	}
	if res := s.do("POST", root+"/submit", map[string]any{"answers": []int{1, -1}}, shopper); res.status != 400 {
		t.Fatalf("incomplete accepted: %d %s", res.status, res.body)
	}
	// Extra client-supplied identity/score fields are ignored. The server scores 1, not 999.
	result := s.do("POST", root+"/submit", map[string]any{"answers": []int{0, 0}, "score": 999, "user_id": "another-account"}, shopper)
	if result.status != 200 || result.json(t)["score"] != float64(1) {
		t.Fatalf("client controlled scoring: %d %s", result.status, result.body)
	}
	retry := s.do("POST", root+"/submit", map[string]any{"answers": []int{1, 0}}, shopper)
	if retry.status != 200 || retry.json(t)["score"] != float64(1) {
		t.Fatalf("replayed attempt rescored: %d %s", retry.status, retry.body)
	}
	if res := s.do("PUT", root+"/answers", map[string]any{"answers": []int{1, 0}}, other); res.status != 404 {
		t.Fatalf("foreign progress changed: %d %s", res.status, res.body)
	}
	entries := s.do("GET", aroot+"/entries", nil, admin)
	if entries.status != 200 {
		t.Fatalf("entries: %d %s", entries.status, entries.body)
	}
	winner := entries.json(t)["items"].([]any)[0].(map[string]any)["user_id"].(string)
	if res := s.do("POST", aroot+"/finalize", map[string]any{"winner_user_id": winner}, admin); res.status != 409 {
		t.Fatalf("early finalize: %d %s", res.status, res.body)
	}
	time.Sleep(time.Until(end) + 10*time.Millisecond)
	if res := s.do("POST", aroot+"/finalize", map[string]any{"winner_user_id": winner, "note": "Highest score confirmed"}, admin); res.status != 200 {
		t.Fatalf("finalize: %d %s", res.status, res.body)
	}
	if res := s.do("POST", root+"/claim", map[string]any{"email": "other@example.com"}, other); res.status != 409 {
		t.Fatalf("nonwinner claimed: %d %s", res.status, res.body)
	}
	if res := s.do("POST", root+"/claim", map[string]any{"email": "Winner <winner@example.com>"}, shopper); res.status != 400 {
		t.Fatalf("display-name email accepted: %d %s", res.status, res.body)
	}
	if res := s.do("POST", root+"/claim", map[string]any{"email": "winner@example.com"}, shopper); res.status != 200 {
		t.Fatalf("claim: %d %s", res.status, res.body)
	}
	if res := s.do("POST", aroot+"/delivered", map[string]any{"note": "PRIVATE-PRIZE-REFERENCE"}, admin); res.status != 200 {
		t.Fatalf("manual hand-off: %d %s", res.status, res.body)
	}
	public := s.do("GET", root, nil, other)
	if public.status != 200 || strings.Contains(string(public.body), "winner@example.com") || strings.Contains(string(public.body), "PRIVATE-PRIZE-REFERENCE") || strings.Contains(string(public.body), winner) {
		t.Fatalf("private winner data leaked: %d %s", public.status, public.body)
	}
	if public.json(t)["award_delivered_at"] == nil {
		t.Fatalf("manual delivery not visible: %s", public.body)
	}
	if res := s.do(http.MethodPost, root+"/start", nil, other); res.status != 409 {
		t.Fatalf("closed quiz restarted: %d %s", res.status, res.body)
	}
}

func TestQuizAdminDraftLifecycleAndScheduledCancellation(t *testing.T) {
	s := bootStack(t, stackOptions{testMode: true})
	admin := s.adminHeaders()
	shopper, _ := s.signIn()
	if res := s.do("GET", "/api/admin/quiz", nil, admin); res.status != 200 {
		t.Fatalf("operator list=%d", res.status)
	}
	body := map[string]any{"title": "Scheduled quiz", "prize": map[string]any{"title": "Manual prize"}, "starts_at": time.Now().UTC().Add(time.Hour), "ends_at": time.Now().UTC().Add(2 * time.Hour), "questions": []any{}}
	if res := s.do("POST", "/api/admin/quiz", "bad body", admin); res.status != 400 {
		t.Fatalf("malformed create=%d", res.status)
	}
	invalid := map[string]any{"title": ""}
	if res := s.do("POST", "/api/admin/quiz", invalid, admin); res.status != 400 {
		t.Fatalf("invalid draft=%d", res.status)
	}
	created := s.do("POST", "/api/admin/quiz", body, admin)
	if created.status != 200 {
		t.Fatalf("empty draft: %d %s", created.status, created.body)
	}
	id := created.json(t)["id"].(string)
	root := "/api/admin/quiz/" + id
	body["title"] = "Edited scheduled quiz"
	if res := s.do("PUT", root, body, admin); res.status != 200 {
		t.Fatalf("edit draft: %d %s", res.status, res.body)
	}
	if res := s.do("PUT", "/api/admin/quiz/missing", body, admin); res.status != 404 {
		t.Fatalf("missing update=%d", res.status)
	}
	if res := s.do("PUT", root, "bad body", admin); res.status != 400 {
		t.Fatalf("malformed update=%d", res.status)
	}
	if res := s.do("POST", root+"/publish", nil, admin); res.status != 409 {
		t.Fatalf("questionless draft published=%d", res.status)
	}
	if res := s.do("GET", "/api/quiz", nil, shopper); len(res.json(t)["items"].([]any)) != 0 {
		t.Fatalf("draft listed to customers: %s", res.body)
	}
	if res := s.do("DELETE", root, nil, admin); res.status != 200 {
		t.Fatalf("delete=%d", res.status)
	}
	if res := s.do("DELETE", root, nil, admin); res.status != 404 {
		t.Fatalf("deleted draft still exists=%d", res.status)
	}
	body["questions"] = []map[string]any{{"prompt": "Nimiq ticker?", "options": []string{"NIM", "Other"}, "correct_index": 0}}
	created = s.do("POST", "/api/admin/quiz", body, admin)
	if created.status != 200 {
		t.Fatalf("create scheduled: %d %s", created.status, created.body)
	}
	id = created.json(t)["id"].(string)
	root = "/api/admin/quiz/" + id
	public := "/api/quiz/" + id
	if res := s.do("POST", root+"/publish", nil, admin); res.status != 200 {
		t.Fatalf("publish scheduled=%d", res.status)
	}
	if res := s.do("GET", public, nil, shopper); res.status != 200 || res.json(t)["phase"] != "scheduled" {
		t.Fatalf("scheduled view: %d %s", res.status, res.body)
	}
	if res := s.do("POST", public+"/start", nil, shopper); res.status != 409 {
		t.Fatalf("early entry=%d", res.status)
	}
	for _, bad := range []any{"bad body", map[string]any{"reason": ""}} {
		if res := s.do("POST", root+"/cancel", bad, admin); res.status != 400 {
			t.Fatalf("invalid cancellation=%d", res.status)
		}
	}
	if res := s.do("POST", root+"/cancel", map[string]any{"reason": "Rescheduled by operator"}, admin); res.status != 200 {
		t.Fatalf("cancel=%d", res.status)
	}
	if res := s.do("GET", public, nil, shopper); res.status != 200 || res.json(t)["phase"] != "cancelled" || res.json(t)["cancel_reason"] != "Rescheduled by operator" {
		t.Fatalf("cancelled view: %d %s", res.status, res.body)
	}
	if res := s.do("PUT", root, body, admin); res.status != 409 {
		t.Fatalf("published rules changed=%d", res.status)
	}
	if res := s.do("POST", root+"/delivered", map[string]any{"note": "not actually delivered"}, admin); res.status != 409 {
		t.Fatalf("manual delivery before winner=%d", res.status)
	}
}
