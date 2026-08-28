// Package db is the BadgerDB-backed persistence layer, replacing the
// previous Postgres/pgx implementation.
//
// Badger is an embedded key/value store: there is no server, no connection
// pool, and no SQL. That changes three things this package has to account
// for:
//
//  1. No schema, so there are no migrations — the store opens a directory
//     and is immediately usable. The old internal/db/migrations/*.sql is
//     obsolete and the keyspace is documented in keys.go instead.
//  2. No secondary indexes or UNIQUE constraints, so uniqueness and lookups
//     are maintained by hand as extra keys written inside the same
//     transaction as the record (see keys.go).
//  3. No numeric type, so money is int64 micro-USD via internal/money.
//
// Badger's transactions are serializable-snapshot-isolation with optimistic
// concurrency control: a txn that read a key another committed txn wrote
// fails at Commit with ErrConflict. That is *stronger* than the read
// committed isolation the Postgres code was running under, but it means
// conflicts surface at commit rather than being resolved by row locks, so
// Update() below retries them automatically.
package db

import (
	"errors"
	"fmt"
	"time"

	"github.com/dgraph-io/badger/v4"
)

// ErrNotFound is returned when a record does not exist. It replaces
// pgx.ErrNoRows at every call site.
var ErrNotFound = errors.New("record not found")

// ErrConflict is returned when a uniqueness constraint we maintain by hand
// is violated — the equivalent of a Postgres unique-violation error.
var ErrConflict = errors.New("conflicting record")

// ErrLimit is returned when an atomic per-user purchase budget would be
// exceeded. It is separate from ErrConflict so HTTP handlers can return 429
// instead of treating a legitimate limit hit as a database failure.
var ErrLimit = errors.New("user purchase limit reached")

// maxRetries bounds the automatic retry loop for optimistic-concurrency
// conflicts. Badger returns ErrConflict when two transactions touched the
// same key; retrying is the documented remedy.
const maxRetries = 10

// Store wraps the Badger handle and exposes typed accessors for users,
// quotes and orders. There is no balance, deposit or ledger: nim.shop is
// non-custodial — customers pay the supplier's Lightning invoice directly.
type Store struct {
	db *badger.DB
}

// New opens (or creates) the Badger database at dir.
//
// This replaces db.New(ctx, databaseURL): there is no URL, no ping, and no
// migration step, just a directory on disk. Callers should still defer
// Close().
func New(dir string) (*Store, error) {
	if dir == "" {
		return nil, fmt.Errorf("badger: empty data directory")
	}

	opts := badger.DefaultOptions(dir).
		// The default logger is extremely chatty at INFO; keep warnings
		// and errors so real problems still surface in the service log.
		WithLogger(badgerLogger{}).
		// Values here are small JSON blobs, so keeping them in the LSM
		// tree rather than the value log avoids a second read on Get and
		// sidesteps value-log GC entirely for this workload.
		WithValueThreshold(1024)

	bdb, err := badger.Open(opts)
	if err != nil {
		return nil, fmt.Errorf("open badger at %s: %w", dir, err)
	}

	s := &Store{db: bdb}
	go s.runValueLogGC()
	return s, nil
}

// Close flushes and closes the underlying database. Unlike a connection
// pool, this must complete before the process exits or recent writes can be
// left in the WAL for recovery on next boot.
func (s *Store) Close() error { return s.db.Close() }

// DB exposes the raw handle for callers that need it (e.g. backups).
func (s *Store) DB() *badger.DB { return s.db }

// Update runs fn inside a read-write transaction, retrying automatically on
// the optimistic-concurrency conflicts that Badger surfaces at commit time.
//
// This is the direct analogue of the old ledger.withTx helper, and it is
// what makes multi-key invariants (record + all of its index keys) atomic.
func (s *Store) Update(fn func(txn *badger.Txn) error) error {
	var err error
	for attempt := 0; attempt < maxRetries; attempt++ {
		err = s.db.Update(fn)
		if !errors.Is(err, badger.ErrConflict) {
			return err
		}
		// Brief, growing backoff so a hot key (e.g. one user's balance
		// under concurrent orders) doesn't livelock.
		time.Sleep(time.Duration(attempt+1) * time.Millisecond)
	}
	return fmt.Errorf("badger: transaction conflict after %d retries: %w", maxRetries, err)
}

// View runs fn inside a read-only transaction.
func (s *Store) View(fn func(txn *badger.Txn) error) error {
	return s.db.View(fn)
}

// runValueLogGC reclaims space from the value log. Badger never does this
// on its own; without it the on-disk footprint grows monotonically as
// records are updated. Postgres's autovacuum was the equivalent chore.
func (s *Store) runValueLogGC() {
	ticker := time.NewTicker(10 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		// RunValueLogGC returns ErrNoRewrite when there is nothing worth
		// reclaiming; loop so a single pass can free multiple files.
		for {
			if err := s.db.RunValueLogGC(0.5); err != nil {
				break
			}
		}
	}
}

// getJSON is the shared "read one record" helper; it converts Badger's
// ErrKeyNotFound into our ErrNotFound so handlers keep a single error
// vocabulary.
func getJSON(txn *badger.Txn, key []byte, out interface{}) error {
	item, err := txn.Get(key)
	if errors.Is(err, badger.ErrKeyNotFound) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	return item.Value(func(val []byte) error {
		return unmarshal(val, out)
	})
}

// getString reads an index key whose value is a plain record id.
func getString(txn *badger.Txn, key []byte) (string, error) {
	item, err := txn.Get(key)
	if errors.Is(err, badger.ErrKeyNotFound) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	var out string
	err = item.Value(func(val []byte) error {
		out = string(val)
		return nil
	})
	return out, err
}

// badgerLogger silences Badger's INFO/DEBUG chatter while preserving
// warnings and errors on the standard service log.
type badgerLogger struct{}

func (badgerLogger) Errorf(f string, v ...interface{})   { logf("badger ERROR: "+f, v...) }
func (badgerLogger) Warningf(f string, v ...interface{}) { logf("badger WARN: "+f, v...) }
func (badgerLogger) Infof(string, ...interface{})        {}
func (badgerLogger) Debugf(string, ...interface{})       {}
