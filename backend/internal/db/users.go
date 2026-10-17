package db

import (
	"errors"
	"strings"
	"time"

	"github.com/dgraph-io/badger/v4"
	"github.com/google/uuid"

	"nimiqshop/internal/nimiq"
)

// MaxLinkedStakeAddresses bounds how many EXTRA wallets one account may link.
// Each earning wallet costs a pool and/or chain lookup on every rate read, so
// this is a latency and abuse bound as much as a product rule; five linked
// wallets plus the sign-in wallet already covers a buyer who splits a stake
// over a hardware wallet, a phone and a couple of old addresses.
const MaxLinkedStakeAddresses = 5

// ErrTooManyStakeAddresses is what AddStakeAddress returns once the cap is
// reached. The handler turns it into a 409 that names the limit, because
// "could not link" would send the buyer to support for a rule they can act on
// themselves (unlink a wallet they no longer use).
var ErrTooManyStakeAddresses = errors.New("stake wallet limit reached")

// LinkedStakeWallets returns the wallets this account proved control of, in
// link order, canonicalised and deduplicated. The sign-in address is filtered
// out even if a caller stored it: it always earns, so keeping it in the list
// would double-count its stake and show the buyer a "link" they never made.
func (u User) LinkedStakeWallets() []StakeAddressLink {
	own := canonicalStakeAddr(u.NimiqAddress)
	out := make([]StakeAddressLink, 0, len(u.StakeAddresses))
	seen := make(map[string]bool, len(u.StakeAddresses))
	for _, link := range u.StakeAddresses {
		addr := canonicalStakeAddr(link.Address)
		if addr == "" || addr == own || seen[addr] {
			continue
		}
		seen[addr] = true
		out = append(out, StakeAddressLink{Address: nimiq.NormalizeAddress(link.Address), LinkedAt: link.LinkedAt})
	}
	return out
}

// EarningStakeAddresses is the ONE place that answers "whose stake earns this
// account's cashback?". It is the wallet the buyer signed in with, followed by
// every linked staking wallet — deduplicated, own address first so the primary
// never changes when a wallet is added or removed.
//
// Every stake read in the shop (pool lookup, chain verification, loyalty
// clocks, the ledger, the post-delivery re-ask) must go through this. Reading
// NimiqAddress directly is how a linked wallet gets silently ignored, and the
// failure mode is invisible: the buyer simply earns the base rate forever.
//
// The stakes behind these addresses are SUMMED into one rate (see
// Store.aggregateStakerStake) while the staker base is applied ONCE per
// account. It never affects the payout recipient: cashback is paid to
// NimiqAddress, the wallet the buyer authenticated with. A link says where the
// stake lives, never where money goes.
func (u User) EarningStakeAddresses() []string {
	out := make([]string, 0, len(u.StakeAddresses)+1)
	if own := strings.TrimSpace(u.NimiqAddress); own != "" {
		out = append(out, own)
	}
	for _, link := range u.LinkedStakeWallets() {
		out = append(out, link.Address)
	}
	return out
}

// EffectiveStakeAddress is the PRIMARY earning wallet: the first linked
// staking wallet when the account has one, otherwise the sign-in address.
//
// Use it where a single address has to be named — the label on a card, an
// admin row, a cache key — and EarningStakeAddresses wherever stake is READ or
// cashback is COMPUTED. Naming one wallet of several as "the" stake is fine;
// summing only one of them is not.
func (u User) EffectiveStakeAddress() string {
	if links := u.LinkedStakeWallets(); len(links) > 0 {
		return links[0].Address
	}
	return u.NimiqAddress
}

// HasLinkedStakeAddress reports whether any wallet other than the sign-in
// wallet earns for this account. The UI uses it to say which addresses are
// earning, and the admin console to show why a rate came out the way it did.
func (u User) HasLinkedStakeAddress() bool {
	return len(u.LinkedStakeWallets()) > 0
}

// StakeAddressLinkedAt returns when a wallet was linked, or the zero time when
// it is not linked to this account.
func (u User) StakeAddressLinkedAt(address string) time.Time {
	want := canonicalStakeAddr(address)
	for _, link := range u.LinkedStakeWallets() {
		if canonicalStakeAddr(link.Address) == want {
			return link.LinkedAt
		}
	}
	return time.Time{}
}

// AddStakeAddress links another earning wallet to an existing account.
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
// Adding an address that is already linked (or that IS the sign-in address) is
// a successful no-op that returns the current record, so a double click or a
// re-link after a page refresh cannot fail the buyer. Exceeding
// MaxLinkedStakeAddresses returns ErrTooManyStakeAddresses.
func (s *Store) AddStakeAddress(userID, address string, now time.Time) (User, error) {
	var user User
	if strings.TrimSpace(userID) == "" {
		return user, errors.New("user id required")
	}
	if err := nimiq.ValidateAddress(address); err != nil {
		return user, err
	}
	normalized := nimiq.NormalizeAddress(address)
	want := canonicalStakeAddr(normalized)

	err := s.Update(func(txn *badger.Txn) error {
		if err := getJSON(txn, userKey(userID), &user); err != nil {
			return err
		}
		if want == canonicalStakeAddr(user.NimiqAddress) {
			return nil // the sign-in wallet always earns; nothing to store
		}
		for _, link := range user.LinkedStakeWallets() {
			if canonicalStakeAddr(link.Address) == want {
				return nil // already linked: idempotent
			}
		}
		if len(user.LinkedStakeWallets()) >= MaxLinkedStakeAddresses {
			return ErrTooManyStakeAddresses
		}
		user.StakeAddresses = append(user.LinkedStakeWallets(), StakeAddressLink{
			Address:  normalized,
			LinkedAt: now.UTC(),
		})
		blob, err := marshal(user)
		if err != nil {
			return err
		}
		return txn.Set(userKey(user.ID), blob)
	})
	return user, err
}

// RemoveStakeAddress unlinks ONE earning wallet; the rest keep earning and the
// sign-in wallet always does. The ledger and loyalty clocks are keyed by
// address and are left untouched on purpose: they record what was observed for
// that wallet, and unlinking must not rewrite history or forfeit cashback that
// was already earned and queued. Returns ErrNotFound when the account never
// linked that wallet, so a stale UI cannot report a success it did not cause.
func (s *Store) RemoveStakeAddress(userID, address string) (User, error) {
	var user User
	if strings.TrimSpace(userID) == "" {
		return user, errors.New("user id required")
	}
	want := canonicalStakeAddr(address)
	if want == "" {
		return user, ErrNotFound
	}
	err := s.Update(func(txn *badger.Txn) error {
		if err := getJSON(txn, userKey(userID), &user); err != nil {
			return err
		}
		kept := make([]StakeAddressLink, 0, len(user.StakeAddresses))
		removed := false
		for _, link := range user.LinkedStakeWallets() {
			if canonicalStakeAddr(link.Address) == want {
				removed = true
				continue
			}
			kept = append(kept, link)
		}
		if !removed {
			return ErrNotFound
		}
		user.StakeAddresses = kept
		blob, err := marshal(user)
		if err != nil {
			return err
		}
		return txn.Set(userKey(user.ID), blob)
	})
	return user, err
}

// ClearStakeAddresses unlinks every wallet, putting the account back to
// earning from the wallet it signs in with. As with RemoveStakeAddress this
// changes future reads only — ledger entries and queued cashback stay put.
func (s *Store) ClearStakeAddresses(userID string) (User, error) {
	var user User
	if strings.TrimSpace(userID) == "" {
		return user, errors.New("user id required")
	}
	err := s.Update(func(txn *badger.Txn) error {
		if err := getJSON(txn, userKey(userID), &user); err != nil {
			return err
		}
		user.StakeAddresses = nil
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
			for _, link := range u.LinkedStakeWallets() {
				if canonicalStakeAddr(link.Address) == want {
					found = u
					return stop
				}
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
