package db

import (
	"errors"

	"github.com/dgraph-io/badger/v4"
)

var errFeedLimit = errors.New("public feed limit reached")

// Count visible purchases, not index entries: excluded legacy rows, missing
// records and duplicate index keys must never crowd real purchases off a page.
func scanPublicFeed(txn *badger.Txn, prefix []byte, limit int, accept func(string) (bool, error)) error {
	seen := map[string]bool{}
	count := 0
	err := scanIndex(txn, prefix, 0, func(id string) error {
		if seen[id] {
			return nil
		}
		seen[id] = true
		visible, err := accept(id)
		if err != nil {
			return err
		}
		if visible {
			count++
			if limit > 0 && count >= limit {
				return errFeedLimit
			}
		}
		return nil
	})
	if errors.Is(err, errFeedLimit) {
		return nil
	}
	return err
}
