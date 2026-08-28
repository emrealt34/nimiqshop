package db

import (
	"errors"
	"time"

	"github.com/dgraph-io/badger/v4"
)

// Notification idempotency: the 1-Luna notification system records each sent
// notification by reference id so a retried/re-delivered event never sends a
// buyer two transactions. Keys live under the meta:notif: prefix.

// IsNotificationSent reports whether a notification for refID was already sent.
func (s *Store) IsNotificationSent(refID string) (bool, error) {
	err := s.View(func(txn *badger.Txn) error {
		_, err := txn.Get([]byte("meta:notif:" + refID))
		return err
	})
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// MarkNotificationSent records that a notification for refID was sent.
func (s *Store) MarkNotificationSent(refID string) error {
	return s.Update(func(txn *badger.Txn) error {
		return txn.Set([]byte("meta:notif:"+refID), []byte(time.Now().UTC().Format(time.RFC3339)))
	})
}
