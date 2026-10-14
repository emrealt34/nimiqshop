package db

import (
	"errors"
	"github.com/dgraph-io/badger/v4"
)

var testActivityKey = []byte("admin:test-activity-visible")

func testActivityVisible(tx *badger.Txn) (bool, error) {
	var enabled bool
	err := getJSON(tx, testActivityKey, &enabled)
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	return enabled, err
}
func (s *Store) TestActivityVisible() (bool, error) {
	var enabled bool
	err := s.View(func(tx *badger.Txn) error { var err error; enabled, err = testActivityVisible(tx); return err })
	return enabled, err
}

// Index prior simulations before enabling their display. Payment and cashback
// state are never changed. Visibility is persisted independently of payments.
func (s *Store) SetTestActivityVisible(enabled bool) error {
	if enabled {
		var ids []string
		if err := s.View(func(tx *badger.Txn) error {
			it := tx.NewIterator(badger.DefaultIteratorOptions)
			defer it.Close()
			for it.Seek([]byte(prefixQuote)); it.ValidForPrefix([]byte(prefixQuote)); it.Next() {
				var q Quote
				if err := getJSON(tx, it.Item().Key(), &q); err != nil {
					return err
				}
				if q.TestMode && q.Status == "fulfilled" {
					ids = append(ids, q.ID)
				}
			}
			return nil
		}); err != nil {
			return err
		}
		for _, id := range ids {
			if err := s.Update(func(tx *badger.Txn) error {
				var q Quote
				if err := getJSON(tx, quoteKey(id), &q); err != nil {
					return err
				}
				return publishToFeed(tx, &q)
			}); err != nil {
				return err
			}
		}
	}
	return s.Update(func(tx *badger.Txn) error { return putQuizJSON(tx, testActivityKey, enabled) })
}
