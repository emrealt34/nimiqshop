package db

import (
	"errors"
	"testing"
	"time"
)

func rangeQuizFixture(t *testing.T, winners int) (*Store, QuizCompetition, time.Time) {
	t.Helper()
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	now := time.Now().UTC()
	correct := 500
	c, err := s.PutQuizCompetition(QuizCompetition{Title: "Closest", Prize: QuizPrize{Title: "Manual prize"}, WinnerCount: &winners, StartsAt: now.Add(-time.Minute), EndsAt: now.Add(time.Hour), Questions: []QuizQuestion{{ID: "range", Prompt: "Guess", Type: "range", Min: 0, Max: 1000, CorrectValue: &correct}}}, true, now)
	if err != nil {
		t.Fatal(err)
	}
	c, err = s.PublishQuiz(c.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	return s, c, now
}
func rangeEntry(t *testing.T, s *Store, c QuizCompetition, user string, answer int, now time.Time) QuizAttempt {
	t.Helper()
	if _, _, err := s.StartQuiz(c.ID, user, now); err != nil {
		t.Fatal(err)
	}
	a, err := s.SaveQuizAnswers(c.ID, user, []int{answer}, true, now)
	if err != nil {
		t.Fatal(err)
	}
	return a
}
func TestQuizRangeProximityBoundsAndZero(t *testing.T) {
	s, c, now := rangeQuizFixture(t, 1)
	a := rangeEntry(t, s, c, "near", 499, now)
	b := rangeEntry(t, s, c, "far", 0, now)
	if QuizPoints(a) != 999000 || QuizPoints(b) != 500000 {
		t.Fatalf("scores: %d %d", QuizPoints(a), QuizPoints(b))
	}
	_, _, _ = s.StartQuiz(c.ID, "invalid", now)
	for _, answer := range []int{-1, 1001} {
		if _, err := s.SaveQuizAnswers(c.ID, "invalid", []int{answer}, true, now); !errors.Is(err, ErrQuizAnswers) {
			t.Fatalf("accepted %d: %v", answer, err)
		}
	}
	if _, err := s.SaveQuizAnswers(c.ID, "invalid", []int{-1}, false, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.FinalizeQuiz(c.ID, "far", "", c.EndsAt); !errors.Is(err, ErrQuizWinner) {
		t.Fatal("lower proximity won", err)
	}
	if _, err := s.FinalizeQuiz(c.ID, "near", "", c.EndsAt); err != nil {
		t.Fatal(err)
	}
}
func TestQuizThreeWinnersTiesAndIndependentDelivery(t *testing.T) {
	s, c, now := rangeQuizFixture(t, 3)
	for user, value := range map[string]int{"first": 500, "tie-a": 450, "tie-b": 550, "tie-c": 450, "lower": 100} {
		rangeEntry(t, s, c, user, value, now)
	}
	for _, ids := range [][]string{{"first", "tie-a"}, {"tie-a", "tie-b", "tie-c"}, {"first", "tie-a", "lower"}, {"first", "tie-a", "tie-a"}} {
		if _, err := s.FinalizeQuizWinners(c.ID, ids, "", c.EndsAt); !errors.Is(err, ErrQuizWinner) {
			t.Fatalf("accepted %v: %v", ids, err)
		}
	}
	got, err := s.FinalizeQuizWinners(c.ID, []string{"tie-b", "first", "tie-a"}, "Tie decided", c.EndsAt)
	if err != nil {
		t.Fatal(err)
	}
	if len(QuizWinners(got)) != 3 || got.WinnerUserID != "first" {
		t.Fatal(got)
	}
	for _, user := range []string{"first", "tie-a", "tie-b"} {
		if _, err := s.ClaimQuizPrize(c.ID, user, user+"@example.com", c.EndsAt); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.ClaimQuizPrize(c.ID, "tie-c", "loser@example.com", c.EndsAt); !errors.Is(err, ErrQuizWinner) {
		t.Fatal("non-winner claimed")
	}
	got, err = s.MarkQuizWinnerPrizeDelivered(c.ID, "first", "private first reference", c.EndsAt)
	if err != nil {
		t.Fatal(err)
	}
	if got.AwardDeliveredAt != nil || QuizAwardTime(got, "tie-a") != nil {
		t.Fatal("one prize completed all prizes")
	}
	if _, err := s.ClaimQuizPrize(c.ID, "tie-a", "updated@example.com", c.EndsAt); err != nil {
		t.Fatal(err)
	}
	for _, user := range []string{"tie-a", "tie-b"} {
		got, err = s.MarkQuizWinnerPrizeDelivered(c.ID, user, "private", c.EndsAt)
		if err != nil {
			t.Fatal(err)
		}
	}
	if got.AwardDeliveredAt == nil {
		t.Fatal("all delivery completion missing")
	}
}
func TestQuizEveryoneOnlySubmittedAttemptsAndRangeValidation(t *testing.T) {
	s, c, now := rangeQuizFixture(t, 0)
	rangeEntry(t, s, c, "exact", 500, now)
	rangeEntry(t, s, c, "far", 0, now)
	_, _, _ = s.StartQuiz(c.ID, "unfinished", now)
	got, err := s.FinalizeQuizWinners(c.ID, nil, "Everyone", c.EndsAt)
	if err != nil {
		t.Fatal(err)
	}
	if len(QuizWinners(got)) != 2 || QuizIsWinner(got, "unfinished") {
		t.Fatal(got)
	}
	if _, err := s.ClaimQuizPrize(c.ID, "far", "far@example.com", c.EndsAt); err != nil {
		t.Fatal(err)
	}
	draft := QuizCompetition{Title: "Validation", Prize: QuizPrize{Title: "Prize"}, StartsAt: now, EndsAt: now.Add(time.Hour), Questions: []QuizQuestion{{Prompt: "Number", Type: "range", Min: 0, Max: 10}}}
	if _, err := s.PutQuizCompetition(draft, true, now); err == nil {
		t.Fatal("missing target accepted")
	}
	zero := 0
	draft.Questions[0].CorrectValue = &zero
	draft.Questions[0].Options = []string{"A", "B"}
	if _, err := s.PutQuizCompetition(draft, true, now); err == nil {
		t.Fatal("range options accepted")
	}
	draft.Questions[0].Options = nil
	got, err = s.PutQuizCompetition(draft, true, now)
	if err != nil {
		t.Fatal(err)
	}
	if got.Questions[0].Options == nil {
		t.Fatal("nil range options")
	}
}

func TestQuizTranslationsPersistValidateAndFreeze(t *testing.T) {
	s, c, now := rangeQuizFixture(t, 1)
	c.ID = ""
	c.Translations = map[string]QuizText{"fr": {Title: "Boutique", Description: "Règles"}}
	c.Prize.Translations = map[string]QuizText{"fr": {Title: "Lot"}}
	c.Questions[0].Translations = map[string]QuizQuestionText{"fr": {Prompt: "Montant ?"}}
	draft, err := s.PutQuizCompetition(c, true, now)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := s.GetQuizCompetition(draft.ID)
	if err != nil || loaded.Translations["fr"].Title != "Boutique" || loaded.Prize.Translations["fr"].Title != "Lot" || loaded.Questions[0].Translations["fr"].Prompt != "Montant ?" {
		t.Fatal("lost translations", loaded, err)
	}
	if _, err := s.PublishQuiz(draft.ID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutQuizCompetition(loaded, false, now); !errors.Is(err, ErrQuizLocked) {
		t.Fatal("translated quiz not frozen", err)
	}
	c.Translations["xx"] = QuizText{Title: "bad"}
	if _, err := s.PutQuizCompetition(c, true, now); err == nil {
		t.Fatal("unsupported language accepted")
	}
	delete(c.Translations, "xx")
	c.Questions[0].Translations["fr"] = QuizQuestionText{Prompt: "Bad range", Options: []string{"secret"}}
	if _, err := s.PutQuizCompetition(c, true, now); err == nil {
		t.Fatal("range options accepted")
	}
}

func TestQuizTopFiveScoreTiesUseServerSubmissionTime(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Now().UTC()
	correct := 500
	count := 5
	c, err := s.PutQuizCompetition(QuizCompetition{Title: "Top five", TieBreak: "submitted_at", WinnerCount: &count, Prize: QuizPrize{Title: "10000 NIM total", Value: "10 000 NIM"}, StartsAt: now.Add(-time.Minute), EndsAt: now.Add(time.Hour), Questions: []QuizQuestion{{ID: "guess", Prompt: "Closest", Type: "range", Min: 0, Max: 1000, CorrectValue: &correct}}}, true, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.PublishQuiz(c.ID, now); err != nil {
		t.Fatal(err)
	}
	// Names deliberately oppose chronological order; starting first is not the tiebreaker.
	users := []string{"z-first", "y-second", "x-third", "w-fourth", "v-fifth", "u-sixth"}
	for i, user := range users {
		rangeEntry(t, s, c, user, 500, now.Add(time.Duration(i)*time.Second))
	}
	rangeEntry(t, s, c, "a-late-high", 500, now.Add(10*time.Second))
	rangeEntry(t, s, c, "b-low", 0, now.Add(-time.Second))
	_, _, _ = s.StartQuiz(c.ID, "unfinished", now.Add(-time.Second))
	// Retrying a completed submission must not replace the original server time.
	a, err := s.SaveQuizAnswers(c.ID, users[0], []int{0}, true, now.Add(20*time.Second))
	if err != nil || !a.SubmittedAt.Equal(now) {
		t.Fatal("submission time changed", a, err)
	}
	entries, err := s.ListQuizEntries(c.ID)
	if err != nil {
		t.Fatal(err)
	}
	eligible, slots := QuizEligibility(c, entries)
	if slots != 5 || len(eligible) != 5 {
		t.Fatal("wrong winner count", slots, eligible)
	}
	for i, user := range users {
		if eligible[user] != (i < 5) {
			t.Fatal("wrong tie eligibility", user, eligible)
		}
	}
	if eligible["a-late-high"] || eligible["b-low"] || eligible["unfinished"] {
		t.Fatal("ineligible participant selected", eligible)
	}
	if _, err := s.FinalizeQuizWinners(c.ID, []string{users[0], users[1], users[2], users[3], users[5]}, "", c.EndsAt); !errors.Is(err, ErrQuizWinner) {
		t.Fatal("later tied participant allowed", err)
	}
	finalized, err := s.FinalizeQuizWinners(c.ID, nil, "Server-ranked winners", c.EndsAt)
	if err != nil || len(finalized.WinnerUserIDs) != 5 {
		t.Fatal("finalize", finalized, err)
	}
	for i, user := range finalized.WinnerUserIDs {
		if user != users[i] {
			t.Fatal("wrong winner order", finalized.WinnerUserIDs)
		}
	}
}
