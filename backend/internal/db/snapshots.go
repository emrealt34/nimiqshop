package db

import (
	"time"

	"github.com/dgraph-io/badger/v4"
)

// Catalog snapshots are the LAST-LINE cache layer: every successful
// supplier catalog response (brands, families, prices, payment vias) is
// persisted here. On cache miss + supplier failure — including full
// supplier outages, 429 storms and fresh process restarts — the storefront
// is served from this keyspace instead of erroring.
//
// The payload bytes are plain JSON produced (and enveloped, including the
// saved-at stamp) by the handlers layer; this layer only cares about bytes.
//
// Keyspace: snap:cat:<key>

func catalogSnapshotKey(key string) []byte { return []byte("snap:cat:" + key) }

// SaveCatalogSnapshot persists one catalog payload durably.
func (s *Store) SaveCatalogSnapshot(key string, val []byte) error {
	return s.Update(func(txn *badger.Txn) error {
		return txn.SetEntry(badger.NewEntry(catalogSnapshotKey(key), val).WithTTL(30 * 24 * time.Hour))
	})
}

// LoadCatalogSnapshot returns the persisted payload for key. It returns
// (nil, nil) when there is none — snapshot reads are best-effort by
// design: a missing snapshot must behave exactly like "no fallback
// available", never like a hard error.
func (s *Store) LoadCatalogSnapshot(key string) ([]byte, error) {
	var out []byte
	err := s.View(func(txn *badger.Txn) error {
		item, err := txn.Get(catalogSnapshotKey(key))
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
