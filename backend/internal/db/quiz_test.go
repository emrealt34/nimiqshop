package db

import (
	"errors"
	"fmt"
	"github.com/dgraph-io/badger/v4"
	"strings"
	"sync"
	"testing"
	"time"
)

func quizFixture(t *testing.T) (*Store, QuizCompetition, time.Time) {
	t.Helper()
	s, err := New(t.TempDir(), Options{SyncWrites: false, ValueThresholdKB: 1024})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	now := time.Now().UTC()
	c, err := s.PutQuizCompetition(QuizCompetition{ID: "quiz-test", Title: "Nimiq test", Prize: QuizPrize{Title: "Amazon gift card", Value: "5 USD"}, StartsAt: now.Add(-time.Hour), EndsAt: now.Add(time.Hour), Questions: []QuizQuestion{
		{ID: "q1", Prompt: "Which ticker?", Options: []string{"BTC", "NIM"}, CorrectIndex: 1},
		{ID: "q2", Prompt: "Which wallet?", Options: []string{"Nimiq Pay", "Other"}, CorrectIndex: 0},
	}}, true, now)
	if err != nil {
		t.Fatal(err)
	}
	c, err = s.PublishQuiz(c.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	return s, c, now
}
func TestQuizOneAttemptResumeAndServerScore(t *testing.T) {
	s, c, now := quizFixture(t)
	_, a, err := s.StartQuiz(c.ID, "u1", now)
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Answers) != 2 || a.Answers[0] != -1 {
		t.Fatalf("initial answers: %+v", a)
	}
	if _, err = s.SaveQuizAnswers(c.ID, "u1", []int{0, -1}, false, now); err != nil {
		t.Fatal(err)
	}
	_, again, err := s.StartQuiz(c.ID, "u1", now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if !again.StartedAt.Equal(a.StartedAt) || again.Answers[0] != 0 {
		t.Fatalf("did not resume same attempt: %+v", again)
	}
	loaded, _ := s.GetQuizCompetition(c.ID)
	if loaded.Participants != 1 {
		t.Fatalf("participants=%d", loaded.Participants)
	}
	if _, err = s.SaveQuizAnswers(c.ID, "u1", []int{1, -1}, true, now); !errors.Is(err, ErrQuizAnswers) {
		t.Fatalf("incomplete: %v", err)
	}
	scored, err := s.SaveQuizAnswers(c.ID, "u1", []int{1, 0}, true, now)
	if err != nil || scored.Score != 2 || scored.SubmittedAt == nil {
		t.Fatalf("scoring: %+v %v", scored, err)
	}
	replay, err := s.SaveQuizAnswers(c.ID, "u1", []int{0, 1}, true, c.EndsAt.Add(time.Minute))
	if err != nil || replay.Score != 2 || replay.Answers[0] != 1 {
		t.Fatalf("replay changed score: %+v %v", replay, err)
	}
	if _, err = s.SaveQuizAnswers(c.ID, "u1", []int{0, 1}, false, now); !errors.Is(err, ErrQuizLocked) {
		t.Fatalf("edited submission: %v", err)
	}
}
func TestQuizConcurrentStartCreatesOneAttempt(t *testing.T) {
	s, c, now := quizFixture(t)
	var wg sync.WaitGroup
	errs := make(chan error, 24)
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, _, err := s.StartQuiz(c.ID, "same-user", now); errs <- err }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	got, _ := s.GetQuizCompetition(c.ID)
	entries, _ := s.ListQuizEntries(c.ID)
	if got.Participants != 1 || len(entries) != 1 {
		t.Fatalf("repeat reservation: participants=%d entries=%d", got.Participants, len(entries))
	}
}
func TestQuizPublishedRulesFrozenAndDeadlinesEnforced(t *testing.T) {
	s, c, now := quizFixture(t)
	c.Questions[0].CorrectIndex = 0
	if _, err := s.PutQuizCompetition(c, false, now); !errors.Is(err, ErrQuizLocked) {
		t.Fatalf("edit published: %v", err)
	}
	if err := s.DeleteQuizDraft(c.ID); !errors.Is(err, ErrQuizLocked) {
		t.Fatalf("delete published: %v", err)
	}
	if _, _, err := s.StartQuiz(c.ID, "u1", c.StartsAt.Add(-time.Second)); !errors.Is(err, ErrQuizClosed) {
		t.Fatalf("early start: %v", err)
	}
	if _, _, err := s.StartQuiz(c.ID, "u1", c.EndsAt); !errors.Is(err, ErrQuizClosed) {
		t.Fatalf("late start: %v", err)
	}
	if _, _, err := s.StartQuiz(c.ID, "u1", now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveQuizAnswers(c.ID, "u1", []int{1, 0}, true, c.EndsAt); !errors.Is(err, ErrQuizClosed) {
		t.Fatalf("late submission: %v", err)
	}
	if _, err := s.SaveQuizAnswers(c.ID, "different-user", []int{1, 0}, true, now); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign attempt: %v", err)
	}
	if _, err := s.CancelQuiz(c.ID, "Competition withdrawn", now); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.StartQuiz(c.ID, "u1", now); !errors.Is(err, ErrQuizClosed) {
		t.Fatalf("cancelled start: %v", err)
	}
}
func TestQuizWinnerMustLeadAndManualAwardIsIdempotent(t *testing.T) {
	s, c, now := quizFixture(t)
	for i, answers := range [][]int{{1, 0}, {1, 1}, {1, 0}} {
		user := fmt.Sprintf("u%d", i+1)
		if _, _, err := s.StartQuiz(c.ID, user, now); err != nil {
			t.Fatal(err)
		}
		if _, err := s.SaveQuizAnswers(c.ID, user, answers, true, now); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.FinalizeQuiz(c.ID, "u3", "", now); !errors.Is(err, ErrQuizClosed) {
		t.Fatalf("early result: %v", err)
	}
	end := c.EndsAt.Add(time.Second)
	if _, err := s.FinalizeQuiz(c.ID, "u2", "", end); !errors.Is(err, ErrQuizWinner) {
		t.Fatalf("nonleader chosen: %v", err)
	}
	finished, err := s.FinalizeQuiz(c.ID, "u3", "Admin resolved equal scores", end)
	if err != nil || finished.WinnerUserID != "u3" {
		t.Fatalf("tie resolution: %+v %v", finished, err)
	}
	if _, err := s.ClaimQuizPrize(c.ID, "u1", "not-winner@example.com", end); !errors.Is(err, ErrQuizWinner) {
		t.Fatalf("foreign claim: %v", err)
	}
	claimed, err := s.ClaimQuizPrize(c.ID, "u3", "winner@example.com", end)
	if err != nil || claimed.PrizeEmail != "winner@example.com" {
		t.Fatalf("claim: %+v %v", claimed, err)
	}
	delivered, err := s.MarkQuizPrizeDelivered(c.ID, "private external receipt", end)
	if err != nil || delivered.AwardDeliveredAt == nil {
		t.Fatalf("delivery: %+v %v", delivered, err)
	}
	again, err := s.MarkQuizPrizeDelivered(c.ID, "changed note", end.Add(time.Minute))
	if err != nil || again.AwardNote != delivered.AwardNote || !again.AwardDeliveredAt.Equal(*delivered.AwardDeliveredAt) {
		t.Fatalf("repeated delivery changed record: %+v %v", again, err)
	}
	if _, err := s.ClaimQuizPrize(c.ID, "u3", "changed@example.com", end); !errors.Is(err, ErrQuizWinner) {
		t.Fatalf("postdelivery claim change: %v", err)
	}
}
func TestQuizNoEntriesAndShopReset(t *testing.T) {
	s, c, now := quizFixture(t)
	result, err := s.FinalizeQuiz(c.ID, "", "No completed attempts", c.EndsAt)
	if err != nil || result.WinnerUserID != "" {
		t.Fatalf("empty result: %+v %v", result, err)
	}
	if _, err := s.MarkQuizPrizeDelivered(c.ID, "", now); !errors.Is(err, ErrQuizWinner) {
		t.Fatalf("nonexistent winner paid: %v", err)
	}
	if _, err := s.ResetShopData(); err != nil {
		t.Fatal(err)
	}
	list, err := s.ListQuizCompetitions(100)
	if err != nil || len(list) != 0 {
		t.Fatalf("reset left quiz index: %+v %v", list, err)
	}
}
func TestQuizDraftValidation(t *testing.T) {
	s, c, now := quizFixture(t)
	c.ID = "new-draft"
	c.Questions[0].Options = []string{"same", "same"}
	if _, err := s.PutQuizCompetition(c, true, now); err == nil {
		t.Fatal("duplicate options accepted")
	}
	c.Questions[0].Options = []string{"a", "b"}
	c.Prize.ImageURL = "javascript:alert(1)"
	if _, err := s.PutQuizCompetition(c, true, now); err == nil {
		t.Fatal("unsafe image accepted")
	}
	c.Prize.ImageURL = ""
	c.Questions = nil
	draft, err := s.PutQuizCompetition(c, true, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.PublishQuiz(draft.ID, now); !errors.Is(err, ErrQuizClosed) {
		t.Fatalf("empty quiz published: %v", err)
	}
	if err = s.DeleteQuizDraft(draft.ID); err != nil {
		t.Fatal(err)
	}
}

func TestQuizQuotaResumptionAndInvalidDraftBoundaries(t *testing.T) {
	s, c, now := quizFixture(t)
	if _, _, err := s.StartQuiz(c.ID, "", now); !errors.Is(err, ErrNotFound) {
		t.Fatalf("empty identity entered: %v", err)
	}
	if _, _, err := s.StartQuiz(c.ID, "existing", now); err != nil {
		t.Fatal(err)
	}
	if err := s.Update(func(txn *badger.Txn) error {
		c.Participants = quizMaxParticipants
		return putQuizJSON(txn, quizKey(c.ID), c)
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.StartQuiz(c.ID, "new-user", now); !errors.Is(err, ErrLimit) {
		t.Fatalf("quota exceeded: %v", err)
	}
	if _, _, err := s.StartQuiz(c.ID, "existing", now); err != nil {
		t.Fatalf("quota blocked own resume: %v", err)
	}
	if _, err := s.GetQuizAttempt(c.ID, "new-user"); !errors.Is(err, ErrNotFound) {
		t.Fatal("refused entry left an attempt")
	}
	if _, err := s.PutQuizCompetition(c, true, now); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate ID: %v", err)
	}
	fresh := c
	fresh.ID = "missing-draft"
	if _, err := s.PutQuizCompetition(fresh, false, now); !errors.Is(err, ErrNotFound) {
		t.Fatalf("update fabricated a draft: %v", err)
	}
	if _, err := s.PublishQuiz(c.ID, now); !errors.Is(err, ErrQuizLocked) {
		t.Fatalf("republished frozen rules: %v", err)
	}
	if _, err := s.ListQuizCompetitions(1000); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*QuizCompetition){
		func(q *QuizCompetition) { q.ID = "invalid/id" }, func(q *QuizCompetition) { q.Title = "" }, func(q *QuizCompetition) { q.EndsAt = q.StartsAt },
		func(q *QuizCompetition) { q.Questions = make([]QuizQuestion, 51) }, func(q *QuizCompetition) { q.Prize.ImageURL = "https://user:secret@example.com/logo.png" },
	} {
		candidate := c
		candidate.ID = "invalid-draft"
		change(&candidate)
		if _, err := s.PutQuizCompetition(candidate, true, now); err == nil {
			t.Fatalf("invalid draft accepted: %+v", candidate)
		}
	}
	if _, err := s.CancelQuiz(c.ID, "", now); err == nil {
		t.Fatal("empty public reason accepted")
	}
	if _, err := s.FinalizeQuiz(c.ID, "existing", strings.Repeat("x", 1001), now); err == nil {
		t.Fatal("unbounded decision note accepted")
	}
	if _, err := s.MarkQuizPrizeDelivered(c.ID, strings.Repeat("x", 2001), now); err == nil {
		t.Fatal("unbounded delivery note accepted")
	}
}
