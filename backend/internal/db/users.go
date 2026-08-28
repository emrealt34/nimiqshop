package db

import (
	"errors"
	"time"

	"github.com/dgraph-io/badger/v4"
	"github.com/google/uuid"
)

// FindOrCreateUserByAddress replaces the upsert the login handler used:
//
//	INSERT INTO users (nimiq_address) VALUES ($1)
//	ON CONFLICT (nimiq_address) DO UPDATE SET ...
//	RETURNING id
//
// Badger has no ON CONFLICT, so the read-check-write happens inside one
// transaction. Badger's SSI guarantees that if a concurrent transaction
// creates the same address between our read and our commit, this commit
// fails with ErrConflict and Store.Update retries it — at which point the
// read finds the existing user. The uniqueness of nimiq_address is
// therefore preserved without a UNIQUE constraint.
func (s *Store) FindOrCreateUserByAddress(normalizedAddr string) (User, error) {
	var user User

	err := s.Update(func(txn *badger.Txn) error {
		idxKey := userAddrIndexKey(normalizedAddr)

		existingID, err := getString(txn, idxKey)
		if err == nil {
			return getJSON(txn, userKey(existingID), &user)
		}
		if !errors.Is(err, ErrNotFound) {
			return err
		}

		// Postgres supplied these defaults; now we do.
		user = User{
			ID:           uuid.NewString(),
			NimiqAddress: normalizedAddr,
			CreatedAt:    time.Now().UTC(),
		}

		blob, err := marshal(user)
		if err != nil {
			return err
		}
		if err := txn.Set(userKey(user.ID), blob); err != nil {
			return err
		}
		return txn.Set(idxKey, []byte(user.ID))
	})

	return user, err
}

// GetUser replaces SELECT ... FROM users WHERE id = $1.
func (s *Store) GetUser(id string) (User, error) {
	var user User
	err := s.View(func(txn *badger.Txn) error {
		return getJSON(txn, userKey(id), &user)
	})
	return user, err
}

// UserAddresses resolves a set of user ids to their Nimiq addresses in a single
// read transaction. Missing users are simply absent from the map. Used by the
// public activity feed to label each entry with the buyer's address.
func (s *Store) UserAddresses(userIDs []string) (map[string]string, error) {
	out := make(map[string]string, len(userIDs))
	err := s.View(func(txn *badger.Txn) error {
		for _, id := range userIDs {
			if id == "" {
				continue
			}
			if _, seen := out[id]; seen {
				continue
			}
			var u User
			if e := getJSON(txn, userKey(id), &u); e == nil {
				out[id] = u.NimiqAddress
			}
		}
		return nil
	})
	return out, err
}

// GetOrCreateUserByID ensures a user record exists for the given id (used by the
// test-purchase flow so test buyers have a real record and can open support
// tickets like any logged-in user). In production users are created by login.
func (s *Store) GetOrCreateUserByID(userID, address string) (User, error) {
	var u User
	err := s.Update(func(txn *badger.Txn) error {
		if e := getJSON(txn, userKey(userID), &u); e == nil {
			return nil
		}
		u = User{ID: userID, NimiqAddress: address, CreatedAt: time.Now().UTC()}
		blob, e := marshal(u)
		if e != nil {
			return e
		}
		if e := txn.Set(userKey(userID), blob); e != nil {
			return e
		}
		return txn.Set(userAddrIndexKey(address), []byte(userID))
	})
	return u, err
}
