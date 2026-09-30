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
	// badger's own not-found must map to "not sent" too: txn.Get returns
	// badger.ErrKeyNotFound ("Key not found"), NOT db.ErrNotFound — missing
	// this made every FIRST memo check fail with an error, so the wallet
	// memo channel could never send anything (dormant: the channel ships
	// disabled by default).
	if errors.Is(err, ErrNotFound) || errors.Is(err, badger.ErrKeyNotFound) {
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
