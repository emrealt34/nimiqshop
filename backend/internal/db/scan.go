package db

import "github.com/dgraph-io/badger/v4"

// scanIndex walks every key under prefix in ascending key order and calls
// fn with the record id stored as each key's value.
//
// This is the general replacement for `SELECT ... WHERE <indexed col> = $1
// ORDER BY created_at DESC LIMIT n`. Because the user-scoped index keys
// embed a reversed timestamp, ascending key order is already newest-first,
// so LIMIT can be honoured by stopping the iteration early instead of
// sorting in memory.
//
// limit <= 0 means "no limit".
func scanIndex(txn *badger.Txn, prefix []byte, limit int, fn func(id string) error) error {
	opts := badger.DefaultIteratorOptions
	opts.Prefix = prefix
	// Index values are short record ids, but we still need them, so
	// prefetching stays on with a small batch.
	opts.PrefetchSize = 32

	it := txn.NewIterator(opts)
	defer it.Close()

	count := 0
	for it.Rewind(); it.ValidForPrefix(prefix); it.Next() {
		var id string
		err := it.Item().Value(func(v []byte) error {
			id = string(v)
			return nil
		})
		if err != nil {
			return err
		}
		if err := fn(id); err != nil {
			return err
		}
		count++
		if limit > 0 && count >= limit {
			break
		}
	}
	return nil
}
