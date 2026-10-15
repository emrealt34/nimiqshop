package db

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/dgraph-io/badger/v4"
)

// The intent identifies a purchase, never establishes its review contents.
// Only a verified chain transaction can supply the saved stars and comment.
type RatingIntent struct {
	Key       string
	Kind      string
	ID        string
	UserID    string
	Buyer     string
	Recipient string
	CreatedAt time.Time
}

var ErrRatingIntentBusy = errors.New("finish the pending rating for your other purchase first")

func RatingIntentKey(buyer, recipient, memo string) string {
	sum := sha256.Sum256([]byte(canonicalStakeAddr(buyer) + "\x00" + canonicalStakeAddr(recipient) + "\x00" + memo))
	return hex.EncodeToString(sum[:])
}
func ratingIntentKey(key string) []byte { return []byte("rating:watch:intent:" + key) }
func ratingInboxKey(hash string) []byte { return []byte("rating:watch:inbox:" + strings.ToLower(hash)) }
func ratingReadyKey(entry RatingChainInbox) []byte {
	return []byte(fmt.Sprintf("rating:watch:ready:%020d:%s", max(int64(0), entry.ChainAt.UnixMilli()), strings.ToLower(entry.Hash)))
}
func ratingMatchKey(key, hash string) []byte {
	return []byte("rating:watch:unmatched:" + key + ":" + strings.ToLower(hash))
}

func (s *Store) SaveRatingIntent(intent RatingIntent) error {
	return s.Update(func(txn *badger.Txn) error {
		st, owner, err := loadRatingRecord(txn, intent.Kind, intent.ID)
		if err != nil {
			return err
		}
		if owner != intent.UserID {
			return ErrNotFound
		}
		if !st.Delivered {
			return ErrConflict
		}
		var existing RatingIntent
		err = getJSON(txn, ratingIntentKey(intent.Key), &existing)
		if err == nil {
			if existing.Kind != intent.Kind || existing.ID != intent.ID || existing.UserID != intent.UserID {
				return ErrRatingIntentBusy
			}
			return nil
		}
		if !errors.Is(err, ErrNotFound) {
			return err
		}
		raw, err := marshal(intent)
		if err != nil {
			return err
		}
		if err := txn.Set(ratingIntentKey(intent.Key), raw); err != nil {
			return err
		}
		// An earlier payment can have arrived before this intent was persisted
		// (old clients). Wake its durable inbox record without another RPC lookup.
		prefix := []byte("rating:watch:unmatched:" + intent.Key + ":")
		opts := badger.DefaultIteratorOptions
		opts.Prefix = prefix
		it := txn.NewIterator(opts)
		defer it.Close()
		for it.Seek(prefix); it.ValidForPrefix(prefix); it.Next() {
			var hash string
			if err := it.Item().Value(func(v []byte) error { hash = string(v); return nil }); err != nil {
				return err
			}
			var entry RatingChainInbox
			if err := getJSON(txn, ratingInboxKey(hash), &entry); err != nil {
				return err
			}
			if entry.Status != "unmatched" {
				continue
			}
			entry.Status = "pending"
			entry.NextAttempt = time.Time{}
			if err := saveRatingInbox(txn, entry); err != nil {
				return err
			}
		}
		return nil
	})
}
func (s *Store) GetRatingIntent(key string) (RatingIntent, error) {
	var out RatingIntent
	err := s.View(func(txn *badger.Txn) error { return getJSON(txn, ratingIntentKey(key), &out) })
	return out, err
}
func (s *Store) CancelRatingIntent(key, kind, id, userID string) error {
	return s.Update(func(txn *badger.Txn) error {
		var intent RatingIntent
		if err := getJSON(txn, ratingIntentKey(key), &intent); errors.Is(err, ErrNotFound) {
			return nil
		} else if err != nil {
			return err
		}
		if intent.Kind != kind || intent.ID != id || intent.UserID != userID {
			return ErrNotFound
		}
		return txn.Delete(ratingIntentKey(key))
	})
}

// Scan progress is checkpointed with each page. A restart resumes the older
// page, then returns to the captured head; newer arrivals are read next pass.
type RatingChainScan struct {
	Head   string
	Target string
	Before string
}
type RatingChainInbox struct {
	Hash        string
	Fingerprint string
	Status      string // pending, unmatched, applied, ignored
	Verified    bool
	Buyer       string
	Recipient   string
	Stars       int
	Comment     string
	ChainAt     time.Time
	NextAttempt time.Time
}

func (s *Store) GetRatingChainScan(wallet string) (RatingChainScan, error) {
	var out RatingChainScan
	err := s.View(func(txn *badger.Txn) error {
		return getJSON(txn, []byte("rating:watch:scan:"+canonicalStakeAddr(wallet)), &out)
	})
	if errors.Is(err, ErrNotFound) {
		err = nil
	}
	return out, err
}
func (s *Store) SaveRatingChainPage(wallet string, scan RatingChainScan, entries []RatingChainInbox) error {
	return s.Update(func(txn *badger.Txn) error {
		for _, entry := range entries {
			if _, err := txn.Get(ratingInboxKey(entry.Hash)); err == nil {
				continue
			} else if !errors.Is(err, badger.ErrKeyNotFound) {
				return err
			}
			if err := saveRatingInbox(txn, entry); err != nil {
				return err
			}
		}
		raw, err := marshal(scan)
		if err != nil {
			return err
		}
		return txn.Set([]byte("rating:watch:scan:"+canonicalStakeAddr(wallet)), raw)
	})
}
func saveRatingInbox(txn *badger.Txn, entry RatingChainInbox) error {
	raw, err := marshal(entry)
	if err != nil {
		return err
	}
	if err := txn.Set(ratingInboxKey(entry.Hash), raw); err != nil {
		return err
	}
	if entry.Status == "pending" {
		return txn.Set(ratingReadyKey(entry), []byte(entry.Hash))
	}
	if err := txn.Delete(ratingReadyKey(entry)); err != nil {
		return err
	}
	if entry.Status == "unmatched" {
		return txn.Set(ratingMatchKey(entry.Fingerprint, entry.Hash), []byte(entry.Hash))
	}
	return nil
}
func (s *Store) SaveRatingChainInbox(entry RatingChainInbox) error {
	return s.Update(func(txn *badger.Txn) error {
		if entry.Status == "unmatched" {
			var intent RatingIntent
			if err := getJSON(txn, ratingIntentKey(entry.Fingerprint), &intent); err == nil {
				entry.Status = "pending"
				entry.NextAttempt = time.Time{}
			} else if !errors.Is(err, ErrNotFound) {
				return err
			}
		}
		return saveRatingInbox(txn, entry)
	})
}
func (s *Store) PendingRatingChainInbox(now time.Time, limit int) ([]RatingChainInbox, error) {
	out := make([]RatingChainInbox, 0)
	err := s.View(func(txn *badger.Txn) error {
		prefix := []byte("rating:watch:ready:")
		opts := badger.DefaultIteratorOptions
		opts.Prefix = prefix
		it := txn.NewIterator(opts)
		defer it.Close()
		for it.Seek(prefix); it.ValidForPrefix(prefix) && len(out) < limit; it.Next() {
			var hash string
			if err := it.Item().Value(func(v []byte) error { hash = string(v); return nil }); err != nil {
				return err
			}
			var entry RatingChainInbox
			if err := getJSON(txn, ratingInboxKey(hash), &entry); err != nil {
				return err
			}
			if entry.Status == "pending" && !entry.NextAttempt.After(now) {
				out = append(out, entry)
			}
		}
		return nil
	})
	return out, err
}

// Finish an uncounted duplicate without retaining a retrying reservation.
func (s *Store) IgnoreRatingChainInbox(entry RatingChainInbox, intent RatingIntent) error {
	return s.Update(func(txn *badger.Txn) error {
		var current RatingIntent
		err := getJSON(txn, ratingIntentKey(intent.Key), &current)
		if err == nil && current.Kind == intent.Kind && current.ID == intent.ID && current.UserID == intent.UserID && current.CreatedAt.Equal(intent.CreatedAt) {
			if err := txn.Delete(ratingIntentKey(intent.Key)); err != nil {
				return err
			}
		} else if err != nil && !errors.Is(err, ErrNotFound) {
			return err
		}
		entry.Status = "ignored"
		return saveRatingInbox(txn, entry)
	})
}
