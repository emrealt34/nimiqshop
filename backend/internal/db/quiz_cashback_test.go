package db

import (
	"errors"
	"fmt"
	"github.com/dgraph-io/badger/v4"
	"sync"
	"testing"
	"time"
)

func TestQuizCashbackUnlockCapAndAccountOnlyRedemption(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	}()
	now := time.Now().UTC()
	c, err := store.PutQuizCompetition(QuizCompetition{Title: "Account code", Prize: QuizPrize{Title: "Code"}, CashbackReward: &QuizCashbackReward{Code: "ILOVENIM", CashbackBps: 750, MaxRecipients: 2}, StartsAt: now.Add(-time.Minute), EndsAt: now.Add(time.Hour), Questions: []QuizQuestion{{Prompt: "Choose", Options: []string{"Yes", "No"}, CorrectIndex: 0}}}, true, now)
	if err != nil {
		t.Fatal(err)
	}
	c, err = store.PublishQuiz(c.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	for _, user := range []string{"a", "b", "c"} {
		if _, _, err = store.StartQuiz(c.ID, user, now); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = store.SaveQuizAnswers(c.ID, "a", []int{0}, false, now); err != nil {
		t.Fatal(err)
	}
	if store.QuizCashbackUnlocked("ILOVENIM", "a") {
		t.Fatal("partial answers unlocked code")
	}
	denied := store.Update(func(tx *badger.Txn) error {
		return ReserveCodeRedemption(tx, "ILOVENIM", "a", "early", CodeLimits{}, 0, now)
	})
	var limit *CodeLimitDetail
	if !errors.As(denied, &limit) || limit.Limit != "quiz" {
		t.Fatalf("code usable before completion: %v", denied)
	}
	for _, user := range []string{"a", "a", "b", "c"} {
		if _, err = store.SaveQuizAnswers(c.ID, user, []int{0}, true, now); err != nil {
			t.Fatal(err)
		}
	}
	if !store.QuizCashbackUnlocked("ILOVENIM", "a") || !store.QuizCashbackUnlocked("ILOVENIM", "b") || store.QuizCashbackUnlocked("ILOVENIM", "c") {
		t.Fatal("award cap or replay failed")
	}
	if err = store.Update(func(tx *badger.Txn) error {
		return ReserveCodeRedemption(tx, "ILOVENIM", "a", "one", CodeLimits{}, 0, now)
	}); err != nil {
		t.Fatal(err)
	}
	denied = store.Update(func(tx *badger.Txn) error {
		return ReserveCodeRedemption(tx, "ILOVENIM", "a", "two", CodeLimits{}, 0, now)
	})
	if !errors.As(denied, &limit) || limit.Limit != "per_user" {
		t.Fatalf("same code reserved twice: %v", denied)
	}
	if err = store.Update(func(tx *badger.Txn) error {
		return ReserveCodeRedemption(tx, "ILOVENIM", "b", "other", CodeLimits{}, 0, now)
	}); err != nil {
		t.Fatal("shared code refused other earned account", err)
	}
	if c.Payout.Enabled {
		t.Fatal("coupon quiz enabled NIM payouts")
	}
}

func TestCouponQuizHasNoLegacyParticipantLimitAndKeepsGrantAfterReopen(t *testing.T) {
	dir := t.TempDir()
	store, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	c, err := store.PutQuizCompetition(QuizCompetition{Title: "Unlimited", Prize: QuizPrize{Title: "Code"}, CashbackReward: &QuizCashbackReward{Code: "UNLIMITED", CashbackBps: 100}, StartsAt: now.Add(-time.Minute), EndsAt: now.Add(time.Hour), Questions: []QuizQuestion{{Prompt: "Choose", Options: []string{"Yes", "No"}, CorrectIndex: 0}}}, true, now)
	if err != nil {
		t.Fatal(err)
	}
	c, err = store.PublishQuiz(c.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	c.Participants = quizMaxParticipants
	if err = store.Update(func(tx *badger.Txn) error { return putQuizJSON(tx, quizKey(c.ID), c) }); err != nil {
		t.Fatal(err)
	}
	if _, _, err = store.StartQuiz(c.ID, "later-account", now); err != nil {
		t.Fatal("legacy cap blocked participation", err)
	}
	if _, err = store.SaveQuizAnswers(c.ID, "later-account", []int{0}, true, now); err != nil {
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = New(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	}()
	if !store.QuizCashbackUnlocked("UNLIMITED", "later-account") {
		t.Fatal("earned code lost after restart")
	}
}

func TestQuizCodeMigrationPreservesAttemptAndScoreThreshold(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	}()
	now := time.Now().UTC()
	c, err := s.PutQuizCompetition(QuizCompetition{Title: "Legacy", Prize: QuizPrize{Title: "Old reward"}, StartsAt: now.Add(-time.Minute), EndsAt: now.Add(time.Hour), Questions: []QuizQuestion{{Prompt: "Pick", Options: []string{"Yes", "No"}, CorrectIndex: 0}}}, true, now)
	if err != nil {
		t.Fatal(err)
	}
	c, err = s.PublishQuiz(c.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.StartQuiz(c.ID, "previous", now); err != nil {
		t.Fatal(err)
	}
	if _, err = s.SaveQuizAnswers(c.ID, "previous", []int{0}, true, now); err != nil {
		t.Fatal(err)
	}
	if err = s.ConvertOpenQuizzesToCashbackCodes(now); err != nil {
		t.Fatal(err)
	}
	c, err = s.GetQuizCompetition(c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if c.CashbackReward == nil || c.Payout.Enabled {
		t.Fatal("legacy quiz not converted")
	}
	if err = s.EnsureQuizCashbackGrant(c.ID, "previous", now); err != nil {
		t.Fatal(err)
	}
	if !s.QuizCashbackUnlocked("ILOVENIM", "previous") {
		t.Fatal("existing completed attempt lost")
	}
	c.CashbackReward = &QuizCashbackReward{Code: "SCOREONLY", CashbackBps: 100, MinScorePercent: 100}
	c, err = s.PutQuizCompetition(c, false, now)
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range []string{"wrong", "correct"} {
		if _, _, err = s.StartQuiz(c.ID, u, now); err != nil {
			t.Fatal(err)
		}
		answer := 0
		if u == "wrong" {
			answer = 1
		}
		if _, err = s.SaveQuizAnswers(c.ID, u, []int{answer}, true, now); err != nil {
			t.Fatal(err)
		}
	}
	if s.QuizCashbackUnlocked("SCOREONLY", "wrong") || !s.QuizCashbackUnlocked("SCOREONLY", "correct") {
		t.Fatal("score threshold failed")
	}
}

func TestQuizCashbackConcurrentCap(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	}()
	now := time.Now().UTC()
	c, err := s.PutQuizCompetition(QuizCompetition{Title: "Concurrent", Prize: QuizPrize{Title: "Code"}, CashbackReward: &QuizCashbackReward{Code: "RACECODE", CashbackBps: 100, MaxRecipients: 3}, StartsAt: now.Add(-time.Minute), EndsAt: now.Add(time.Hour), Questions: []QuizQuestion{{Prompt: "Pick", Options: []string{"Yes", "No"}, CorrectIndex: 0}}}, true, now)
	if err != nil {
		t.Fatal(err)
	}
	c, err = s.PublishQuiz(c.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		if _, _, err = s.StartQuiz(c.ID, fmt.Sprint(i), now); err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(user string) {
			defer wg.Done()
			if _, err := s.SaveQuizAnswers(c.ID, user, []int{0}, true, now); err != nil {
				t.Error(err)
			}
		}(fmt.Sprint(i))
	}
	wg.Wait()
	count := 0
	for i := 0; i < 10; i++ {
		if s.QuizCashbackUnlocked("RACECODE", fmt.Sprint(i)) {
			count++
		}
	}
	if count != 3 {
		t.Fatalf("cap violated: %d grants", count)
	}
}

func TestUniqueQuizCodesArePrivateStableAndSingleUse(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	now := time.Now().UTC()
	c, err := s.PutQuizCompetition(QuizCompetition{Title: "Unique codes", CashbackReward: &QuizCashbackReward{Code: "ILOVENIM", CodeMode: "unique", CashbackBps: 500}, StartsAt: now.Add(-time.Minute), EndsAt: now.Add(time.Hour), Questions: []QuizQuestion{{Prompt: "Pick", Options: []string{"Yes", "No"}, CorrectIndex: 0}}}, true, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.PublishQuiz(c.ID, now); err != nil {
		t.Fatal(err)
	}
	codes := map[string]string{}
	for _, u := range []string{"a", "b"} {
		if _, _, err = s.StartQuiz(c.ID, u, now); err != nil {
			t.Fatal(err)
		}
		if _, err = s.SaveQuizAnswers(c.ID, u, []int{0}, true, now); err != nil {
			t.Fatal(err)
		}
		g, err := s.GetQuizCashbackGrant("ILOVENIM", u)
		if err != nil {
			t.Fatal(err)
		}
		codes[u] = g.Code
		deniedPrefix := s.Update(func(tx *badger.Txn) error {
			return ReserveCodeRedemption(tx, "ILOVENIM", u, "prefix-attempt", CodeLimits{}, 0, now)
		})
		if deniedPrefix == nil {
			t.Fatal("unique prefix reserved as a coupon")
		}
		if g.Code == "ILOVENIM" || !s.QuizCashbackUnlocked(g.Code, u) || s.QuizCashbackUnlocked("ILOVENIM", u) {
			t.Fatal("unique prefix bypass or missing grant")
		}
		if _, err = s.SaveQuizAnswers(c.ID, u, []int{0}, true, now); err != nil {
			t.Fatal(err)
		}
		again, err := s.GetQuizCashbackGrant("ILOVENIM", u)
		if err != nil || again.Code != g.Code {
			t.Fatal("retry changed code")
		}
	}
	if codes["a"] == codes["b"] || s.QuizCashbackUnlocked(codes["a"], "b") {
		t.Fatal("code leaked between accounts")
	}
	for i := 0; i < 2; i++ {
		err = s.Update(func(tx *badger.Txn) error {
			return ReserveCodeRedemption(tx, codes["a"], "a", fmt.Sprint(i), CodeLimits{}, 0, now)
		})
		if (i == 0 && err != nil) || (i == 1 && err == nil) {
			t.Fatal("single use failed", err)
		}
	}
}
