package db

import (
	"time"

	"github.com/dgraph-io/badger/v4"
)

// TouchUserPresence records the caller's best-effort IP + country on the
// user record for the admin console. Failures are deliberately swallowed
// by the caller: presence is operational metadata, never a gate.
func (s *Store) TouchUserPresence(userID, ip, country string) error {
	if userID == "" {
		return nil
	}
	return s.Update(func(txn *badger.Txn) error {
		var u User
		if err := getJSON(txn, userKey(userID), &u); err != nil {
			return err // missing user: nothing to touch
		}
		if ip != "" {
			u.LastIP = ip
		}
		if country != "" {
			u.LastCountry = country
		}
		u.LastSeenAt = time.Now().UTC()
		blob, err := marshal(u)
		if err != nil {
			return err
		}
		return txn.Set(userKey(userID), blob)
	})
}
