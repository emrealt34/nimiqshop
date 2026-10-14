package db

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/dgraph-io/badger/v4"
	"nimiqshop/internal/cashbackcode"
	"strings"
	"time"
)

// Zero MaxRecipients means unlimited accounts; every account has one redemption.
type QuizCashbackReward struct {
	Code            string `json:"code"`
	CodeMode        string `json:"code_mode,omitempty"`
	CashbackBps     int    `json:"cashback_bps"`
	MaxRecipients   int    `json:"max_recipients"`
	MinScorePercent int    `json:"min_score_percent"`
}
type quizCashbackDefinition struct {
	Reward  QuizCashbackReward `json:"reward"`
	Granted int                `json:"granted"`
}
type QuizCashbackGrant struct {
	Code          string    `json:"code"`
	CompetitionID string    `json:"competition_id"`
	GrantedAt     time.Time `json:"granted_at"`
}

func quizCodeKey(code string) []byte {
	return []byte("quiz:cashback-code:" + strings.ToUpper(strings.TrimSpace(code)))
}
func quizGrantKey(code, user string) []byte {
	return []byte("quiz:cashback-grant:" + strings.ToUpper(strings.TrimSpace(code)) + ":" + user)
}
func validateQuizCashback(r *QuizCashbackReward) error {
	if r == nil {
		return nil
	}
	if r.CodeMode != "" && r.CodeMode != "shared" && r.CodeMode != "unique" {
		return fmt.Errorf("code_mode must be shared or unique")
	}
	if err := cashbackcode.ValidateCode(r.Code); err != nil {
		return err
	}
	if r.CashbackBps < 1 || r.CashbackBps > 10000 || r.MaxRecipients < 0 || r.MinScorePercent < 0 || r.MinScorePercent > 100 {
		return fmt.Errorf("invalid quiz cashback rate, recipient limit or score threshold")
	}
	return nil
}
func saveQuizCashbackDefinition(tx *badger.Txn, r *QuizCashbackReward) error {
	if r == nil {
		return nil
	}
	var d quizCashbackDefinition
	if err := getJSON(tx, quizCodeKey(r.Code), &d); err != nil && !errors.Is(err, ErrNotFound) {
		return err
	}
	d.Reward = *r
	return putQuizJSON(tx, quizCodeKey(r.Code), d)
}

// Called inside the answer commit: cap checks and grants cannot race or replay.
func grantQuizCashback(tx *badger.Txn, c QuizCompetition, a QuizAttempt, now time.Time) error {
	if c.CashbackReward == nil || a.SubmittedAt == nil {
		return nil
	}
	r := c.CashbackReward
	if QuizPoints(a)*100 < int64(r.MinScorePercent)*int64(len(c.Questions))*1000000 {
		return nil
	}
	if _, err := tx.Get(quizGrantKey(r.Code, a.UserID)); err == nil {
		return nil
	} else if !errors.Is(err, badger.ErrKeyNotFound) {
		return err
	}
	var d quizCashbackDefinition
	if err := getJSON(tx, quizCodeKey(r.Code), &d); err != nil {
		return err
	}
	if d.Reward.MaxRecipients > 0 && d.Granted >= d.Reward.MaxRecipients {
		return nil
	}
	d.Granted++
	if err := putQuizJSON(tx, quizCodeKey(r.Code), d); err != nil {
		return err
	}
	code := r.Code
	if r.CodeMode == "unique" {
		sum := sha256.Sum256([]byte(c.ID + ":" + a.UserID))
		prefix := r.Code
		if len(prefix) > 23 {
			prefix = prefix[:23]
		}
		code = prefix + "-" + strings.ToUpper(hex.EncodeToString(sum[:8]))
		individual := *r
		individual.Code = code
		if err := saveQuizCashbackDefinition(tx, &individual); err != nil {
			return err
		}
	}
	grant := QuizCashbackGrant{Code: code, CompetitionID: c.ID, GrantedAt: now.UTC()}
	if err := putQuizJSON(tx, quizGrantKey(code, a.UserID), grant); err != nil {
		return err
	}
	return putQuizJSON(tx, quizGrantKey(r.Code, a.UserID), grant)
}
func (s *Store) GetQuizCashbackReward(code string) (QuizCashbackReward, bool, error) {
	var d quizCashbackDefinition
	err := s.View(func(tx *badger.Txn) error { return getJSON(tx, quizCodeKey(code), &d) })
	if errors.Is(err, ErrNotFound) {
		return QuizCashbackReward{}, false, nil
	}
	return d.Reward, err == nil, err
}
func (s *Store) QuizCashbackUnlocked(code, user string) bool {
	if user == "" {
		return false
	}
	var grant QuizCashbackGrant
	return s.View(func(tx *badger.Txn) error { return getJSON(tx, quizGrantKey(code, user), &grant) }) == nil && grant.Code == strings.ToUpper(strings.TrimSpace(code))
}
func (s *Store) GetQuizCashbackGrant(code, user string) (QuizCashbackGrant, error) {
	var g QuizCashbackGrant
	err := s.View(func(tx *badger.Txn) error { return getJSON(tx, quizGrantKey(code, user), &g) })
	return g, err
}
func (s *Store) HasQuizCashbackRewards() bool {
	found := false
	_ = s.View(func(tx *badger.Txn) error {
		it := tx.NewIterator(badger.DefaultIteratorOptions)
		defer it.Close()
		it.Seek([]byte("quiz:cashback-code:"))
		found = it.ValidForPrefix([]byte("quiz:cashback-code:"))
		return nil
	})
	return found
}
func (s *Store) EnsureQuizCashbackGrant(id, user string, now time.Time) error {
	return s.Update(func(tx *badger.Txn) error {
		var c QuizCompetition
		var a QuizAttempt
		if err := getJSON(tx, quizKey(id), &c); err != nil {
			return err
		}
		if err := getJSON(tx, quizAttemptKey(id, user), &a); err != nil {
			return err
		}
		return grantQuizCashback(tx, c, a, now)
	})
}

// Convert open legacy contests only. Historical awards and queued transfers
// remain durable; no existing payout record is deleted or rewritten.
func (s *Store) ConvertOpenQuizzesToCashbackCodes(now time.Time) error {
	items, err := s.ListQuizCompetitions(200)
	if err != nil {
		return err
	}
	for _, c := range items {
		if c.CashbackReward != nil || (c.Status != "draft" && c.Status != "published") || !now.Before(c.EndsAt) {
			continue
		}
		legacy := c
		reward, exists, lookupErr := s.GetQuizCashbackReward("ILOVENIM")
		if lookupErr != nil {
			return lookupErr
		}
		if !exists {
			reward = QuizCashbackReward{Code: "ILOVENIM", CashbackBps: 100}
		}
		c.CashbackReward = &reward
		if err = s.Update(func(tx *badger.Txn) error {
			key := []byte("quiz:legacy-prize:" + c.ID)
			if _, err := tx.Get(key); err == nil {
				return nil
			} else if !errors.Is(err, badger.ErrKeyNotFound) {
				return err
			}
			return putQuizJSON(tx, key, legacy)
		}); err != nil {
			return err
		}
		c.Payout = QuizPayoutConfig{}
		c.Prize = QuizPrize{Title: "Cashback code", Value: "ILOVENIM"}
		c.Description = "Complete the quiz to unlock an account-only, single-use cashback code."
		for lang, text := range c.Translations {
			text.Description = map[string]string{
				"en": "Complete the quiz to unlock a single-use cashback code for your account.",
				"tr": "Quizi tamamla, hesabında tek kullanımlık cashback kodunu aç.",
				"de": "Beantworte das Quiz und schalte einen einmaligen Cashback-Code für dein Konto frei.",
				"fr": "Réponds au quiz pour débloquer un code cashback à usage unique pour ton compte.",
				"es": "Completa el quiz para desbloquear un código de cashback de un solo uso para tu cuenta.",
				"pt": "Completa o quiz para desbloquear um código cashback de utilização única na tua conta.",
			}[lang]
			c.Translations[lang] = text
		}
		if _, err = s.PutQuizCompetition(c, false, now); err != nil {
			return err
		}
	}
	return nil
}
