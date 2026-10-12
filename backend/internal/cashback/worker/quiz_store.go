package worker

import (
	"log"
	"nimiqshop/internal/db"
	"nimiqshop/internal/nimiq"
	"strings"
	"time"
)

// One sender handles both queues from the same configured cashback wallet.
// Prize accounting stays separate from purchase cashback accounting.
type quizStore struct {
	primary *db.Store
	prizes  db.QuizPayoutStore
}

func WithQuizPayouts(store *db.Store) PayoutStore {
	return quizStore{primary: store, prizes: db.QuizPayoutStore{Store: store}}
}
func (s quizStore) ListCashbacksByStatus(status string, limit int) ([]db.Cashback, error) {
	if status == db.CashbackBroadcast {
		if err := s.primary.FinalizeDueQuizPayouts(time.Now().UTC()); err != nil {
			log.Printf("quiz: automatic prize scheduling failed: %v", err)
		}
	}
	cashbacks, err := s.primary.ListCashbacksByStatus(status, limit)
	if err != nil {
		return nil, err
	}
	prizes, err := s.prizes.ListCashbacksByStatus(status, limit)
	if err != nil {
		return nil, err
	}
	return append(cashbacks, prizes...), nil
}
func (s quizStore) target(id string) PayoutStore {
	if strings.HasPrefix(id, "quiz_") {
		return s.prizes
	}
	return s.primary
}
func (s quizStore) ClaimCashbackSend(id string) (db.Cashback, error) {
	return s.target(id).ClaimCashbackSend(id)
}
func (s quizStore) AttachCashbackSignedTx(id, raw, hash string, height uint32) error {
	return s.target(id).AttachCashbackSignedTx(id, raw, hash, height)
}
func (s quizStore) MarkCashbackBroadcast(id, hash string) error {
	return s.target(id).MarkCashbackBroadcast(id, hash)
}
func (s quizStore) MarkCashbackPaid(id, hash string) error {
	return s.target(id).MarkCashbackPaid(id, hash)
}
func (s quizStore) FailCashbackSend(id, reason string) error {
	return s.target(id).FailCashbackSend(id, reason)
}

func (s quizStore) RetireExpiredPayout(id, raw string, proof nimiq.PayoutExpiryProof) error {
	return s.target(id).RetireExpiredPayout(id, raw, proof)
}
