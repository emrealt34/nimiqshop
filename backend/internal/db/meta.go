package db

import (
	"time"

	"github.com/dgraph-io/badger/v4"
)

// Small durable key/value store for operational metadata (last known rate
// snapshot, feature hints). Keys are namespaced under "meta:"; entries
// survive restarts with a generous TTL.
func metaKey(key string) []byte { return []byte("meta:" + key) }

// SaveMeta persists a metadata value under key.
func (s *Store) SaveMeta(key string, val []byte, ttl time.Duration) error {
	return s.Update(func(txn *badger.Txn) error {
		e := badger.NewEntry(metaKey(key), val)
		if ttl > 0 {
			e = e.WithTTL(ttl)
		}
		return txn.SetEntry(e)
	})
}

// LoadMeta reads a metadata value. (nil, nil) when absent.
func (s *Store) LoadMeta(key string) ([]byte, error) {
	var out []byte
	err := s.View(func(txn *badger.Txn) error {
		item, err := txn.Get(metaKey(key))
		if err != nil {
			if err == badger.ErrKeyNotFound {
				return nil
			}
			return err
		}
		out, err = item.ValueCopy(nil)
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
