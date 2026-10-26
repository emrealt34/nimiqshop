package db

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/dgraph-io/badger/v4"
	"nimiqshop/internal/nimiq"
)

// Prize records use a separate namespace: they never inflate cashback totals,
// staking ledgers, tree contributions or purchase volume.
type QuizPayout struct {
	Cashback
	CompetitionID string `json:"competition_id"`
}
type QuizPayoutStore struct{ Store *Store }

func quizPayoutStatusPrefix(status string) []byte { return []byte("quiz:pay-status:" + status + ":") }
func quizPayoutStatusKey(status, id string) []byte {
	return append(quizPayoutStatusPrefix(status), []byte(id)...)
}
func quizPayoutKey(id string) []byte { return []byte("quiz:pay:" + id) }
func quizMemoReference(id string) string {
	return id
}
func quizPayoutID(competition, user string) string {
	hash := sha256.Sum256([]byte(competition + ":" + user))
	return "quiz_" + hex.EncodeToString(hash[:])
}
func (s *Store) FinalizeDueQuizPayouts(now time.Time) error {
	competitions := []QuizCompetition{}
	err := s.View(func(tx *badger.Txn) error {
		return scanIndex(tx, []byte(quizIndexPrefix), 0, func(id string) error {
			var c QuizCompetition
			if err := getJSON(tx, quizKey(id), &c); err != nil {
				return err
			}
			if c.Status == "published" && c.Payout.Enabled && !now.Before(c.EndsAt) {
				competitions = append(competitions, c)
			}
			return nil
		})
	})
	if err != nil {
		return err
	}
	var failures []error
	for _, candidate := range competitions {
		if candidate.Status != "published" || !candidate.Payout.Enabled || now.Before(candidate.EndsAt) {
			continue
		}
		err = s.Update(func(tx *badger.Txn) error {
			var c QuizCompetition
			if err := getJSON(tx, quizKey(candidate.ID), &c); err != nil {
				return err
			}
			if c.Status != "published" || !c.Payout.Enabled || now.Before(c.EndsAt) {
				return nil
			}
			if err := validateQuiz(c); err != nil {
				return err
			}
			entries, err := quizEntries(tx, c.ID)
			if err != nil {
				return err
			}
			eligible, _ := QuizEligibility(c, entries)
			winners := []string{}
			for _, entry := range entries {
				if !eligible[entry.UserID] {
					continue
				}
				var user User
				if err := getJSON(tx, userKey(entry.UserID), &user); err != nil {
					return err
				}
				if err := nimiq.ValidateAddress(user.NimiqAddress); err != nil {
					return fmt.Errorf("quiz %s: winner wallet is invalid", c.ID)
				}
				id := quizPayoutID(c.ID, user.ID)
				var existing QuizPayout
				if err := getJSON(tx, quizPayoutKey(id), &existing); err == nil {
					return ErrConflict
				} else if !errors.Is(err, ErrNotFound) {
					return err
				}
				row := QuizPayout{CompetitionID: c.ID, Cashback: Cashback{ID: id, QuoteID: c.ID, UserID: user.ID, Recipient: nimiq.NormalizeAddress(user.NimiqAddress), ProductID: c.Title, CashbackSource: "quiz_prize", AmountLuna: c.Payout.PerWinnerNIM * 100_000, Memo: fmt.Sprintf("nimshop #%d %d NIM %s", len(winners)+1, c.Payout.PerWinnerNIM, quizMemoReference(c.ID)), Status: CashbackQueued, CreatedAt: now.UTC(), UpdatedAt: now.UTC()}}
				if err := putQuizJSON(tx, quizPayoutKey(id), row); err != nil {
					return err
				}
				if err := tx.Set([]byte("quiz:pay-user:"+user.ID+":"+id), []byte(id)); err != nil {
					return err
				}
				if err := tx.Set(quizPayoutStatusKey(row.Status, id), []byte(id)); err != nil {
					return err
				}
				winners = append(winners, user.ID)
			}
			c.Status = "finalized"
			c.WinnerUserIDs = winners
			c.WinnerUserID = ""
			if len(winners) > 0 {
				c.WinnerUserID = winners[0]
			}
			c.UpdatedAt = now.UTC()
			return putQuizJSON(tx, quizKey(c.ID), c)
		})
		if err != nil {
			failures = append(failures, fmt.Errorf("quiz %s: %w", candidate.ID, err))
		}
	}
	return errors.Join(failures...)
}
func (s *Store) ListQuizPayouts(competition string) ([]QuizPayout, error) {
	rows := []QuizPayout{}
	err := s.View(func(tx *badger.Txn) error {
		options := badger.DefaultIteratorOptions
		options.Prefix = []byte("quiz:pay:")
		it := tx.NewIterator(options)
		defer it.Close()
		for it.Rewind(); it.ValidForPrefix(options.Prefix); it.Next() {
			var row QuizPayout
			if err := getJSON(tx, it.Item().KeyCopy(nil), &row); err != nil {
				return err
			}
			if competition == "" || row.CompetitionID == competition {
				rows = append(rows, row)
			}
		}
		return nil
	})
	return rows, err
}
func (q QuizPayoutStore) ListCashbacksByStatus(status string, limit int) ([]Cashback, error) {
	out := []Cashback{}
	err := q.Store.View(func(tx *badger.Txn) error {
		return scanIndex(tx, quizPayoutStatusPrefix(status), 0, func(id string) error {
			var row QuizPayout
			if err := getJSON(tx, quizPayoutKey(id), &row); err != nil {
				return err
			}
			out = append(out, row.Cashback)
			return nil
		})
	})
	sort.Slice(out, func(i, j int) bool {
		if out[i].UpdatedAt.Equal(out[j].UpdatedAt) {
			return out[i].ID < out[j].ID
		}
		return out[i].UpdatedAt.Before(out[j].UpdatedAt)
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, err
}
func (q QuizPayoutStore) patch(id string, change func(*QuizPayout, *badger.Txn) error) error {
	return q.Store.Update(func(tx *badger.Txn) error {
		var row QuizPayout
		if err := getJSON(tx, quizPayoutKey(id), &row); err != nil {
			return err
		}
		oldStatus := row.Status
		if err := change(&row, tx); err != nil {
			return err
		}
		if oldStatus != row.Status {
			if err := tx.Delete(quizPayoutStatusKey(oldStatus, row.ID)); err != nil {
				return err
			}
			if err := tx.Set(quizPayoutStatusKey(row.Status, row.ID), []byte(row.ID)); err != nil {
				return err
			}
		}
		row.UpdatedAt = time.Now().UTC()
		return putQuizJSON(tx, quizPayoutKey(id), row)
	})
}
func (q QuizPayoutStore) ClaimCashbackSend(id string) (Cashback, error) {
	var result Cashback
	err := q.patch(id, func(row *QuizPayout, _ *badger.Txn) error {
		if row.Status == CashbackSending && row.SignedTxHex != "" {
			result = row.Cashback
			return nil
		}
		if row.Status == CashbackSending && row.SignedTxHex == "" && row.SendingAt != nil && time.Since(*row.SendingAt) > 2*time.Minute {
			row.Status = CashbackQueued
		}
		if row.Status != CashbackQueued {
			return ErrConflict
		}
		now := time.Now().UTC()
		row.Status = CashbackSending
		row.SendingAt = &now
		row.LastError = ""
		result = row.Cashback
		return nil
	})
	return result, err
}
func (q QuizPayoutStore) AttachCashbackSignedTx(id, raw, hash string, height uint32) error {
	return q.patch(id, func(row *QuizPayout, _ *badger.Txn) error {
		for _, attempt := range row.ExpiredAttempts {
			if attempt.SignedTxHex == raw || attempt.Proof.Hash == hash {
				return ErrConflict
			}
		}
		if strings.TrimSpace(raw) == "" || (row.Status != CashbackSending && row.Status != CashbackQueued) {
			return ErrConflict
		}
		if row.SignedTxHex != "" {
			if row.SignedTxHex != raw || row.TxHash != hash {
				return ErrConflict
			}
			return nil
		}
		row.SignedTxHex = raw
		row.TxHash = hash
		row.ValidityStart = height
		return nil
	})
}
func (q QuizPayoutStore) MarkCashbackBroadcast(id, hash string) error {
	return q.patch(id, func(row *QuizPayout, _ *badger.Txn) error {
		if row.Status != CashbackSending && row.Status != CashbackBroadcast {
			return ErrConflict
		}
		if hash == "" || (row.TxHash != "" && row.TxHash != hash) {
			return ErrConflict
		}
		row.Status = CashbackBroadcast
		row.TxHash = hash
		row.LastError = ""
		return nil
	})
}
func (q QuizPayoutStore) MarkCashbackPaid(id, hash string) error {
	return q.patch(id, func(row *QuizPayout, tx *badger.Txn) error {
		if row.Status == CashbackPaid {
			if row.TxHash != hash {
				return ErrConflict
			}
			return nil
		}
		if row.Status != CashbackBroadcast || hash == "" || row.TxHash != hash {
			return ErrConflict
		}
		now := time.Now().UTC()
		row.Status = CashbackPaid
		row.PaidAt = &now
		row.LastError = ""
		var c QuizCompetition
		if err := getJSON(tx, quizKey(row.CompetitionID), &c); err != nil {
			return err
		}
		if c.Awards == nil {
			c.Awards = map[string]QuizAward{}
		}
		c.Awards[row.UserID] = QuizAward{DeliveredAt: &now, Note: "Confirmed automatic NIM prize"}
		allPaid := true
		for _, winner := range QuizWinners(c) {
			if c.Awards[winner].DeliveredAt == nil {
				allPaid = false
			}
		}
		if allPaid {
			c.AwardDeliveredAt = &now
		}
		return putQuizJSON(tx, quizKey(c.ID), c)
	})
}
func (q QuizPayoutStore) FailCashbackSend(id, reason string) error {
	return q.patch(id, func(row *QuizPayout, _ *badger.Txn) error {
		if row.Status != CashbackSending && row.Status != CashbackBroadcast {
			return nil
		}
		row.LastError = reason
		if row.SignedTxHex == "" && row.Status == CashbackSending {
			row.Status = CashbackQueued
			row.SendingAt = nil
		}
		return nil
	})
}

// Only the owner's prize rows are exposed in the cashback payment history;
// signed bytes, errors and expiry audit records remain server-side.
func (s *Store) ListQuizPayoutsByUser(user string) ([]Cashback, error) {
	rows := []Cashback{}
	err := s.View(func(tx *badger.Txn) error {
		return scanIndex(tx, []byte("quiz:pay-user:"+user+":"), 0, func(id string) error {
			var row QuizPayout
			if err := getJSON(tx, quizPayoutKey(id), &row); err != nil {
				return err
			}
			if row.UserID != user {
				return ErrConflict
			}
			rows = append(rows, row.Cashback)
			return nil
		})
	})
	sort.Slice(rows, func(i, j int) bool { return rows[i].CreatedAt.After(rows[j].CreatedAt) })
	return rows, err
}
