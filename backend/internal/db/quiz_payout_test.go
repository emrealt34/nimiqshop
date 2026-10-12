package db

import (
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"nimiqshop/internal/nimiq"
)

func automaticQuizFixture(t *testing.T) (*Store, QuizCompetition, time.Time) {
	t.Helper()
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	now := time.Now().UTC()
	five := 5
	c, err := s.PutQuizCompetition(QuizCompetition{Title: "Automatic prizes", Prize: QuizPrize{Title: "NIM pool"}, TieBreak: "submitted_at", WinnerCount: &five, StartsAt: now.Add(-time.Minute), EndsAt: now.Add(time.Hour), Payout: QuizPayoutConfig{Enabled: true, Source: "cashback_wallet", PoolNIM: 10000, PerWinnerNIM: 2000}, Questions: []QuizQuestion{{ID: "q", Prompt: "Choose", Options: []string{"Yes", "No"}, CorrectIndex: 0}}}, true, now)
	if err != nil {
		t.Fatal(err)
	}
	c, err = s.PublishQuiz(c.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 7; i++ {
		address, err := nimiq.AddressFromKeyHex(fmt.Sprintf("%064x", i+1))
		if err != nil {
			t.Fatal(err)
		}
		user, err := s.FindOrCreateUserByAddress(address)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err = s.StartQuiz(c.ID, user.ID, now); err != nil {
			t.Fatal(err)
		}
		if _, err = s.SaveQuizAnswers(c.ID, user.ID, []int{0}, true, now.Add(time.Duration(i)*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	return s, c, now
}
func TestAutomaticQuizPayoutsDeadlineIdempotenceMemoAndAccounting(t *testing.T) {
	s, c, now := automaticQuizFixture(t)
	if err := s.FinalizeDueQuizPayouts(now); err != nil {
		t.Fatal(err)
	}
	rows, err := s.ListQuizPayouts(c.ID)
	if err != nil || len(rows) != 0 {
		t.Fatal("paid before deadline", rows, err)
	}
	var wg sync.WaitGroup
	failures := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); failures <- s.FinalizeDueQuizPayouts(c.EndsAt) }()
	}
	wg.Wait()
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	rows, err = s.ListQuizPayouts(c.ID)
	if err != nil || len(rows) != 5 {
		t.Fatal("duplicate or missing payout", len(rows), err)
	}
	finalized, err := s.GetQuizCompetition(c.ID)
	if err != nil || len(QuizWinners(finalized)) != 5 {
		t.Fatal(finalized, err)
	}
	queue := QuizPayoutStore{Store: s}
	var total int64
	for _, row := range rows {
		total += row.AmountLuna
		rank := 0
		for index, userID := range QuizWinners(finalized) {
			if userID == row.UserID {
				rank = index + 1
			}
		}
		wantMemo := fmt.Sprintf("nimshop.io #%d 2000 NIM %s", rank, quizMemoReference(c.ID))
		if rank == 0 || row.AmountLuna != 2000*100000 || row.Memo != wantMemo || len([]byte(row.Memo)) > nimiq.MaxMemoLen {
			t.Fatal("wrong amount/memo", row)
		}
		// Sign only with a local test key; no node or real wallet is contacted.
		signed, hash, err := nimiq.BuildBasicTransaction(strings.Repeat("01", 32), row.Recipient, row.AmountLuna, 0, 100, nimiq.NetworkTestnet, []byte(row.Memo))
		if err != nil {
			t.Fatal(err)
		}
		wire, err := hex.DecodeString(signed)
		if err != nil || !bytes.Contains(wire, []byte(wantMemo)) {
			t.Fatal("memo missing from signed transaction", err)
		}
		if _, err = queue.ClaimCashbackSend(row.ID); err != nil {
			t.Fatal(err)
		}
		if err = queue.AttachCashbackSignedTx(row.ID, signed, hash, 100); err != nil {
			t.Fatal(err)
		}
		if err = queue.FailCashbackSend(row.ID, "temporary timeout"); err != nil {
			t.Fatal(err)
		}
		resumed, err := queue.ClaimCashbackSend(row.ID)
		if err != nil || resumed.SignedTxHex != signed || resumed.Memo != row.Memo {
			t.Fatal("lost signed payment", resumed, err)
		}
		if err = queue.AttachCashbackSignedTx(row.ID, "different-bytes", "different-hash", 101); !errors.Is(err, ErrConflict) {
			t.Fatal("second signed payment accepted", err)
		}
		if err = queue.MarkCashbackBroadcast(row.ID, hash); err != nil {
			t.Fatal(err)
		}
		if err = queue.MarkCashbackPaid(row.ID, hash); err != nil {
			t.Fatal(err)
		}
		if err = queue.MarkCashbackPaid(row.ID, hash); err != nil {
			t.Fatal(err)
		}
		summary, err := s.GetUserCashbackSummary(row.UserID)
		if err != nil || summary.PaidLuna != 0 || summary.PendingLuna != 0 {
			t.Fatal("quiz inflated cashback", summary, err)
		}
	}
	if total != 10000*100000 {
		t.Fatal("pool exceeded", total)
	}
	paid, err := s.GetQuizCompetition(c.ID)
	if err != nil || paid.AwardDeliveredAt == nil {
		t.Fatal("award falsely pending", paid, err)
	}
	if _, err = s.PutQuizCompetition(c, false, c.EndsAt); !errors.Is(err, ErrQuizLocked) {
		t.Fatal("finalized financial records edited", err)
	}
	if err = s.FinalizeDueQuizPayouts(c.EndsAt.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	rows, err = s.ListQuizPayouts(c.ID)
	if err != nil || len(rows) != 5 {
		t.Fatal("paid again", len(rows), err)
	}
}
func TestQuizActiveEditsPreserveAnswersAndArchiveScoringChanges(t *testing.T) {
	s, c, now := rangeQuizFixture(t, 1)
	attempt := rangeEntry(t, s, c, "participant", 499, now)
	c.Title = "New title"
	c.EndsAt = c.EndsAt.Add(time.Hour)
	updated, err := s.PutQuizCompetition(c, false, now)
	if err != nil || updated.ID != c.ID || updated.Status != "published" || updated.Participants != 1 {
		t.Fatal("metadata edit reset entries", updated, err)
	}
	kept, err := s.GetQuizAttempt(c.ID, "participant")
	if err != nil || kept.Points != attempt.Points {
		t.Fatal("answer history lost", kept, err)
	}
	correct := 600
	updated.Questions[0].CorrectValue = &correct
	revised, err := s.PutQuizCompetition(updated, false, now)
	if err != nil || revised.ID == c.ID || revised.Participants != 0 || revised.Status != "published" {
		t.Fatal("scoring update not versioned", revised, err)
	}
	old, err := s.GetQuizCompetition(c.ID)
	if err != nil || old.Status != "cancelled" {
		t.Fatal("old version not archived", old, err)
	}
	kept, err = s.GetQuizAttempt(c.ID, "participant")
	if err != nil || kept.Points != attempt.Points {
		t.Fatal("archived answer altered", kept, err)
	}
}
