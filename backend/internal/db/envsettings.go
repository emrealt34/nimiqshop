package db

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/dgraph-io/badger/v4"
)

// envOverridePrefix holds operator-managed environment values:
//
//	env:<KEY> -> EnvOverride (JSON)
//
// Why the database and not a rewritten .env file: a host may redeploy the
// process onto a fresh filesystem (Railway does), so anything written next
// to the binary is gone on the very next deploy — while a value an operator
// saved in the console has to survive exactly that. These rows are exported
// into the process environment at boot and applied to the loaded config (see
// config.ApplyEnvOverrides), so a saved value behaves like the same variable
// set on the host, with one deliberate difference: a host value still wins,
// and removing an override here falls back to it instead of leaving nothing.
//
// The values are secrets in exactly the sense the process environment is:
// they live in the shop's database on this host, never in the repository,
// and the console never prints one back in full.
const envOverridePrefix = "env:"

// envKeyRe is the shape of a manageable variable name: SCREAMING_SNAKE_CASE.
// It only guards the keyspace here; which variables MAY be managed, and what
// a legal value looks like, is decided in config (see config.ManagedEnvKeys).
var envKeyRe = regexp.MustCompile(`^[A-Z][A-Z0-9_]{2,63}$`)

// EnvOverride is one environment value the operator saved in the console.
type EnvOverride struct {
	Key   string `json:"key"`
	Value string `json:"value"`
	// UpdatedAt/UpdatedBy are the audit trail of the console edit itself,
	// not of the variable: they say who last changed it and when.
	UpdatedAt string `json:"updated_at,omitempty"`
	UpdatedBy string `json:"updated_by,omitempty"`
}

func envOverrideStoreKey(key string) []byte { return []byte(envOverridePrefix + key) }

// ListEnvOverrides returns every saved override, sorted by name. A row that
// cannot be parsed is skipped rather than failing the whole list: an operator
// must always be able to open the console and fix or remove what is there.
func (s *Store) ListEnvOverrides() ([]EnvOverride, error) {
	out := make([]EnvOverride, 0, 8)
	err := s.View(func(txn *badger.Txn) error {
		prefix := []byte(envOverridePrefix)
		opts := badger.DefaultIteratorOptions
		opts.PrefetchValues = true
		opts.Prefix = prefix
		it := txn.NewIterator(opts)
		defer it.Close()
		for it.Rewind(); it.ValidForPrefix(prefix); it.Next() {
			blob, err := it.Item().ValueCopy(nil)
			if err != nil {
				return err
			}
			var o EnvOverride
			if err := json.Unmarshal(blob, &o); err != nil || o.Key == "" {
				continue
			}
			out = append(out, o)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

// GetEnvOverride returns one saved override. ErrNotFound means there is no
// override — the variable may still be set by the host.
func (s *Store) GetEnvOverride(key string) (EnvOverride, error) {
	var o EnvOverride
	err := s.View(func(txn *badger.Txn) error { return getJSON(txn, envOverrideStoreKey(key), &o) })
	return o, err
}

// SetEnvOverride saves one value. The name is shape-checked here because it
// becomes part of a Badger key; its meaning and its value are validated in
// config before they ever reach this call.
func (s *Store) SetEnvOverride(o EnvOverride) error {
	key := strings.TrimSpace(o.Key)
	if !envKeyRe.MatchString(key) {
		return fmt.Errorf("%q is not an environment variable name (use SCREAMING_SNAKE_CASE, e.g. CRYPTOREFILLS_PARTNER_KEY)", key)
	}
	o.Key = key
	o.Value = strings.TrimSpace(o.Value)
	if o.Value == "" {
		return fmt.Errorf("%s needs a value — to stop overriding it, remove the key instead", key)
	}
	if strings.ContainsAny(o.Value, "\r\n") {
		return fmt.Errorf("%s must be a single line", key)
	}
	blob, err := marshal(o)
	if err != nil {
		return err
	}
	return s.Update(func(txn *badger.Txn) error { return txn.Set(envOverrideStoreKey(key), blob) })
}

// DeleteEnvOverride removes one override. Deleting something that was never
// stored is not an error: the console lists host variables too, and removing
// one of those simply means "stop overriding it".
func (s *Store) DeleteEnvOverride(key string) error {
	key = strings.TrimSpace(key)
	if !envKeyRe.MatchString(key) {
		return fmt.Errorf("%q is not an environment variable name (use SCREAMING_SNAKE_CASE, e.g. CRYPTOREFILLS_PARTNER_KEY)", key)
	}
	return s.Update(func(txn *badger.Txn) error { return txn.Delete(envOverrideStoreKey(key)) })
}
