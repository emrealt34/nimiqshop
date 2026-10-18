package db

import (
	"errors"
	"github.com/dgraph-io/badger/v4"
)

// testActivityKey is the operator's ONE switch for admin test-center
// purchases on the public site. Off (the default): a test order appears
// nowhere a visitor can see — not in the activity feed, not in the cashback
// totals or leaderboard, not in the star summary or the recent comments, and
// not in the delivery statistics. On: it is listed with a TEST label and
// counted like any other purchase, so the operator can check every public
// surface end to end before switching it off again. The switch is display
// only; payments and cashback rows are never touched.
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
