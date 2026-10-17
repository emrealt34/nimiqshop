package db

import (
	"errors"
	"strings"
	"time"

	"github.com/dgraph-io/badger/v4"
	"github.com/google/uuid"

	"nimiqshop/internal/nimiq"
)

// EffectiveStakeAddress is the ONE place that answers "whose stake earns this
// account's cashback?". It is the linked stake wallet when the account has one
// and the login address otherwise, so a buyer who staked from a regular wallet
// and shops through Nimiq Pay is still recognised as a staker.
//
// Every stake read in the shop (pool lookup, chain verification, loyalty
// clocks, the single ledger) must go through this. Reading NimiqAddress
// directly is how a linked wallet gets silently ignored — and the failure mode
// is invisible: the buyer simply earns the base rate forever.
//
// It never affects the payout recipient. Cashback is paid to NimiqAddress, the
// wallet the buyer authenticated with; a link only says where the stake lives.
func (u User) EffectiveStakeAddress() string {
	if addr := strings.TrimSpace(u.StakeAddress); addr != "" {
		return addr
	}
	return u.NimiqAddress
}

// HasLinkedStakeAddress reports whether the account's stake is read from a
// wallet other than the one it signed in with. The UI uses it to say which
// address is earning, and the admin console to show why a rate came out the way
// it did.
func (u User) HasLinkedStakeAddress() bool {
	addr := strings.TrimSpace(u.StakeAddress)
	return addr != "" && canonicalStakeAddr(addr) != canonicalStakeAddr(u.NimiqAddress)
}

// SetStakeAddress links a second wallet to an existing account.
//
// The caller is responsible for the two proofs that make this safe, and this
// method deliberately refuses to guess either of them:
//
//  1. control — a Hub/Nimiq Pay signature over a purpose-bound challenge,
//     verified with nimiq.VerifySignedMessage before this is ever called; and
//  2. stake — the pool or the chain must show that address delegating to the
//     operator's validator, otherwise linking buys nothing and only confuses
//     the buyer about which wallet earns.
//
// Linking the address the account already logs in with is allowed and is a
// no-op normalisation: it clears the link rather than storing a duplicate, so
// HasLinkedStakeAddress stays honest.
func (s *Store) SetStakeAddress(userID, address string, now time.Time) (User, error) {
	var user User
	if strings.TrimSpace(userID) == "" {
		return user, errors.New("user id required")
	}
	if err := nimiq.ValidateAddress(address); err != nil {
		return user, err
	}
	normalized := nimiq.NormalizeAddress(address)

	err := s.Update(func(txn *badger.Txn) error {
		if err := getJSON(txn, userKey(userID), &user); err != nil {
			return err
		}
		if canonicalStakeAddr(normalized) == canonicalStakeAddr(user.NimiqAddress) {
			// Same wallet: nothing to link. Clearing keeps the record clean
			// instead of storing an address that duplicates the identity.
			user.StakeAddress = ""
			user.StakeAddressLinkedAt = time.Time{}
		} else {
			user.StakeAddress = normalized
			user.StakeAddressLinkedAt = now.UTC()
		}
		blob, err := marshal(user)
		if err != nil {
			return err
		}
		return txn.Set(userKey(user.ID), blob)
	})
	return user, err
}

// ClearStakeAddress removes the link, putting the account back to reading its
// stake from the wallet it signs in with. The ledger and loyalty clocks are
// keyed by address and are left untouched on purpose: they are a record of what
// was observed for that wallet, and unlinking must not rewrite history or
// forfeit cashback that was already earned and queued.
func (s *Store) ClearStakeAddress(userID string) (User, error) {
	var user User
	if strings.TrimSpace(userID) == "" {
		return user, errors.New("user id required")
	}
	err := s.Update(func(txn *badger.Txn) error {
		if err := getJSON(txn, userKey(userID), &user); err != nil {
			return err
		}
		user.StakeAddress = ""
		user.StakeAddressLinkedAt = time.Time{}
		blob, err := marshal(user)
		if err != nil {
			return err
		}
		return txn.Set(userKey(user.ID), blob)
	})
	return user, err
}

// GetUserByStakeAddress finds the account that linked a given wallet. It is the
// reverse of the login-address index and exists so a stake observation about a
// linked wallet can find the buyer it belongs to (reconciliation after a pool
// outage, admin lookups). Returns ErrNotFound when nobody linked it.
//
// There is no index for this: links are rare and the walk is bounded by the
// user count, the same trade-off the admin cross-customer views already make
// (see ListAllUsers). If linking ever becomes common, add a
// stake-address → user-id index beside userAddrIndexKey instead of growing this
// scan.
func (s *Store) GetUserByStakeAddress(address string) (User, error) {
	var found User
	want := canonicalStakeAddr(address)
	if want == "" {
		return found, ErrNotFound
	}
	// Sentinel to stop the prefix walk early; scanJSONPrefix returns it as-is.
	stop := errors.New("stake address found")
	err := s.View(func(txn *badger.Txn) error {
		return scanJSONPrefix(txn, []byte(prefixUser), func(item *badger.Item) error {
			var u User
			if e := item.Value(func(value []byte) error { return unmarshal(value, &u) }); e != nil {
				return nil // skip an unreadable row rather than failing the lookup
			}
			if canonicalStakeAddr(u.StakeAddress) == want {
				found = u
				return stop
			}
			return nil
		})
	})
	if errors.Is(err, stop) {
		return found, nil
	}
	if err != nil {
		return User{}, err
	}
	return found, ErrNotFound
}

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

// GetUserByAddress resolves the durable address index without creating a
// user. It is used by reconciliation jobs after a pool/API outage so a newly
// detected stake can repair any cashback rows that were conservatively queued
// at the base rate.
func (s *Store) GetUserByAddress(address string) (User, error) {
	var user User
	err := s.View(func(txn *badger.Txn) error {
		id, err := getString(txn, userAddrIndexKey(canonicalStakeAddr(address)))
		if err != nil {
			return err
		}
		return getJSON(txn, userKey(id), &user)
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
