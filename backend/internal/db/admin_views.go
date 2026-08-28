package db

import (
	"errors"
	"sort"

	"github.com/dgraph-io/badger/v4"
)

// The admin console needs cross-customer views. Badger has no ad-hoc query
// language, so these bounded lists walk the record prefixes and sort by the
// immutable timestamps. The HTTP layer caps each request at 100 rows; add
// cursor indexes before using this MVP store for high-volume operations.
func (s *Store) ListAllUsers(limit int) ([]User, error) {
	var out []User
	err := s.View(func(txn *badger.Txn) error {
		return scanJSONPrefix(txn, []byte(prefixUser), func(item *badger.Item) error {
			var u User
			if err := item.Value(func(value []byte) error { return unmarshal(value, &u) }); err != nil {
				return err
			}
			out = append(out, u)
			return nil
		})
	})
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return limitUsers(out, limit), err
}

func (s *Store) ListAllOrders(limit int) ([]Order, error) {
	var out []Order
	err := s.View(func(txn *badger.Txn) error {
		return scanJSONPrefix(txn, []byte(prefixOrder), func(item *badger.Item) error {
			var o Order
			if err := item.Value(func(value []byte) error { return unmarshal(value, &o) }); err != nil {
				return err
			}
			out = append(out, o)
			return nil
		})
	})
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return limitOrders(out, limit), err
}

func (s *Store) ListAllQuotes(limit int) ([]Quote, error) {
	var out []Quote
	err := s.View(func(txn *badger.Txn) error {
		return scanJSONPrefix(txn, []byte(prefixQuote), func(item *badger.Item) error {
			var q Quote
			if err := item.Value(func(value []byte) error { return unmarshal(value, &q) }); err != nil {
				return err
			}
			out = append(out, q)
			return nil
		})
	})
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return limitQuotes(out, limit), err
}

func (s *Store) ListQuotesForUser(userID string, limit int) ([]Quote, error) {
	var out []Quote
	err := s.View(func(txn *badger.Txn) error {
		return scanIndex(txn, []byte("ix:q:user:"+userID+":"), limit, func(id string) error {
			var q Quote
			if err := getJSON(txn, quoteKey(id), &q); err != nil {
				if errors.Is(err, ErrNotFound) {
					return nil
				}
				return err
			}
			out = append(out, q)
			return nil
		})
	})
	return out, err
}

func (s *Store) CountUsers() (int, error) { return s.countPrefix([]byte(prefixUser)) }
func (s *Store) CountOrdersByStatus(status string) (int, error) {
	return s.countPrefix(orderStatusIndexPrefix(status))
}
func (s *Store) CountQuotesByStatus(status string) (int, error) {
	return s.countPrefix([]byte("ix:q:status:" + status + ":"))
}
func (s *Store) countPrefix(prefix []byte) (int, error) {
	count := 0
	err := s.View(func(txn *badger.Txn) error {
		return scanJSONPrefix(txn, prefix, func(*badger.Item) error {
			count++
			return nil
		})
	})
	return count, err
}

func scanJSONPrefix(txn *badger.Txn, prefix []byte, fn func(*badger.Item) error) error {
	opts := badger.DefaultIteratorOptions
	opts.Prefix = prefix
	opts.PrefetchValues = true
	it := txn.NewIterator(opts)
	defer it.Close()
	for it.Rewind(); it.ValidForPrefix(prefix); it.Next() {
		if err := fn(it.Item()); err != nil {
			return err
		}
	}
	return nil
}

func limitUsers(items []User, limit int) []User {
	if limit > 0 && len(items) > limit {
		return items[:limit]
	}
	return items
}
func limitOrders(items []Order, limit int) []Order {
	if limit > 0 && len(items) > limit {
		return items[:limit]
	}
	return items
}
func limitQuotes(items []Quote, limit int) []Quote {
	if limit > 0 && len(items) > limit {
		return items[:limit]
	}
	return items
}
