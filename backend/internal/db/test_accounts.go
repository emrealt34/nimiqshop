package db

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/dgraph-io/badger/v4"
)

// testAccountsKey holds the operator-managed list of test wallets. Addresses
// are public, so the list lives in the store (edited from the admin console)
// rather than in an env var.
const testAccountsKey = "admin:test_accounts"

// ErrTestAccountExists is returned when an address is already on the list.
var ErrTestAccountExists = errors.New("test account already listed")

// TestAccount is one wallet allowed to run the simulated checkout.
type TestAccount struct {
	Address string    `json:"address"` // normalized: upper case, no spaces
	Label   string    `json:"label,omitempty"`
	AddedAt time.Time `json:"added_at"`
}

// ListTestAccounts returns the listed test wallets (empty when none).
func (s *Store) ListTestAccounts() ([]TestAccount, error) {
	var out []TestAccount
	err := s.View(func(txn *badger.Txn) error {
		err := getJSON(txn, []byte(testAccountsKey), &out)
		if errors.Is(err, ErrNotFound) {
			out = nil
			return nil
		}
		return err
	})
	return out, err
}

// AddTestAccount appends a wallet and returns the updated list.
func (s *Store) AddTestAccount(a TestAccount) ([]TestAccount, error) {
	var out []TestAccount
	err := s.Update(func(txn *badger.Txn) error {
		var list []TestAccount
		err := getJSON(txn, []byte(testAccountsKey), &list)
		if err != nil && !errors.Is(err, ErrNotFound) {
			return err
		}
		for _, e := range list {
			if e.Address == a.Address {
				return ErrTestAccountExists
			}
		}
		list = append(list, a)
		b, err := json.Marshal(list)
		if err != nil {
			return err
		}
		if err := txn.Set([]byte(testAccountsKey), b); err != nil {
			return err
		}
		out = list
		return nil
	})
	return out, err
}

// RemoveTestAccount drops a wallet and returns the updated list plus whether
// the address was on it.
func (s *Store) RemoveTestAccount(address string) ([]TestAccount, bool, error) {
	var out []TestAccount
	found := false
	err := s.Update(func(txn *badger.Txn) error {
		var list []TestAccount
		err := getJSON(txn, []byte(testAccountsKey), &list)
		if errors.Is(err, ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		kept := make([]TestAccount, 0, len(list))
		for _, e := range list {
			if e.Address == address {
				found = true
				continue
			}
			kept = append(kept, e)
		}
		if !found {
			out = list
			return nil
		}
		b, err := json.Marshal(kept)
		if err != nil {
			return err
		}
		if err := txn.Set([]byte(testAccountsKey), b); err != nil {
			return err
		}
		out = kept
		return nil
	})
	return out, found, err
}

// IsTestAccount reports whether the normalized address is a listed test wallet.
func (s *Store) IsTestAccount(address string) bool {
	if address == "" {
		return false
	}
	list, err := s.ListTestAccounts()
	if err != nil {
		return false
	}
	for _, e := range list {
		if e.Address == address {
			return true
		}
	}
	return false
}
