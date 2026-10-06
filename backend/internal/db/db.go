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
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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

// ErrMonthlyLimit identifies the calendar-month spend ceiling separately.
var ErrMonthlyLimit = errors.New("monthly purchase limit reached")

// maxRetries bounds the automatic retry loop for optimistic-concurrency
// conflicts. Badger returns ErrConflict when two transactions touched the
// same key; retrying is the documented remedy.
const maxRetries = 10

// StakerLookupDetailed is the pool resolver installed at boot from
// internal/poolstake and read on the fulfillment path. One resolver, one
// contract — the pool's answer (stake, staked, its cashback base) applied
// verbatim.
//
// Contract:
//   - It performs network I/O, so it MUST be called outside any Badger
//     transaction. transitionQuote does that; do not move it inside.
//   - err != nil means "the pool could not be asked" (outage / timeout):
//     callers must leave every clock untouched — an unverifiable stake is
//     never upgraded into a boost, and never downgraded into a withdrawal.
//   - err == nil with Staked=false is a DEFINITIVE answer from the pool
//     (the address is not staked): callers record it, which resets the
//     single-ledger book (a full withdrawal is a full reset).
type StakerLookupDetailed func(ctx context.Context, address string) (StakerStake, error)

// Store wraps the Badger handle and exposes typed accessors for users,
// quotes and orders. There is no balance, deposit or ledger: shop.nimiqbase.com is
// non-custodial — customers pay the supplier's Lightning invoice directly.
type Store struct {
	db *badger.DB

	// stakerMu guards stakerLookup, which is set once at boot and read on
	// every fulfillment.
	stakerMu     sync.RWMutex
	stakerLookup StakerLookupDetailed

	// ledgerMu guards ledgerParams (the single-ledger cashback parameter
	// resolver, wired at boot from the admin settings).
	ledgerMu     sync.RWMutex
	ledgerParams ledgerParamsFn

	// cashbackEnrichment holds the burn NIM address (set at boot).
	enrichMu           sync.RWMutex
	cashbackEnrichment CashbackEnrichment

	// Write-path counters, exported through HealthSnapshot. A rising
	// write_conflicts rate is the earliest visible sign of a hot key; a
	// rising pending_writes value is the earliest sign the disk cannot keep
	// up with the commit rate. Both are invisible in a request log.
	pendingWrites  atomic.Int64
	writeConflicts atomic.Int64

	// closed is flipped by Close(). Background maintenance (the
	// checkout-safety index build, value-log GC) must stop touching Badger
	// the moment the handle is gone: badger PANICS on a closed DB rather
	// than returning an error, so a graceful shutdown would otherwise take
	// the process down with it.
	closed atomic.Bool
}

// SetCashbackEnrichment configures runtime cashback tweaks (burn wallet
// address). Empty BurnAddr defaults to the canonical Nimiq burn wallet.
func (s *Store) SetCashbackEnrichment(burnAddr string) {
	if strings.TrimSpace(burnAddr) == "" {
		burnAddr = BurnNIMAddress
	}
	s.enrichMu.Lock()
	s.cashbackEnrichment = CashbackEnrichment{BurnAddr: burnAddr}
	s.enrichMu.Unlock()
}

func (s *Store) cashbackEnrich() CashbackEnrichment {
	s.enrichMu.RLock()
	defer s.enrichMu.RUnlock()
	e := s.cashbackEnrichment
	if strings.TrimSpace(e.BurnAddr) == "" {
		e.BurnAddr = BurnNIMAddress
	}
	return e
}

// GetCashbackEnrichment returns the boot-configured cashback enrichment (burn
// wallet address).
func (s *Store) GetCashbackEnrichment() CashbackEnrichment { return s.cashbackEnrich() }

// SetStakerLookupDetailed installs the pool resolver. The pool's answer —
// stake, staked, and its cashback base — is applied verbatim; passing nil
// turns the feature off (every buyer keeps the base rate).
func (s *Store) SetStakerLookupDetailed(fn StakerLookupDetailed) {
	s.stakerMu.Lock()
	s.stakerLookup = fn
	s.stakerMu.Unlock()
}

// HasStakerLookup reports whether a pool resolver is installed.
func (s *Store) HasStakerLookup() bool {
	s.stakerMu.RLock()
	defer s.stakerMu.RUnlock()
	return s.stakerLookup != nil
}

// lookupStake asks the pool through the wired resolver. ok=false means no
// resolver at all (feature off). The pool's numbers are never adjusted
// here — its base is its base, including an explicit 0.
func (s *Store) lookupStake(ctx context.Context, address string) (st StakerStake, ok bool, err error) {
	s.stakerMu.RLock()
	fn := s.stakerLookup
	s.stakerMu.RUnlock()
	if fn == nil {
		return StakerStake{}, false, nil
	}
	st, err = fn(ctx, address)
	return st, true, err
}

// Options carries the two Badger settings that are genuinely a policy
// decision rather than a sizing decision, so the caller (cmd/server) can
// wire them from env instead of this package reaching into the environment
// for them. Everything else stays env-tunable inline below.
type Options struct {
	// SyncWrites fsyncs every commit. true is the safe default: a power
	// loss cannot drop a committed cashback row or a quote WAL entry.
	SyncWrites bool
	// ValueThresholdKB is the size above which a value is written to the
	// value log instead of staying inline in the LSM tree.
	//
	// The previous hardcoded 1 KB was actively harmful and contradicted its
	// own comment: a Quote record carries the marshalled supplier request,
	// the delivery manifest and the fulfillment payload, so essentially
	// EVERY quote exceeded 1 KB and was pushed into the value log. That
	// costs a second disk read on every Get and keeps the value-log GC
	// running forever. 1024 KB (Badger's own LSMOnlyOptions value) keeps
	// this workload LSM-only, which is what the store has always wanted.
	ValueThresholdKB int
}

// DefaultOptions is the production profile.
func DefaultOptions() Options { return Options{SyncWrites: true, ValueThresholdKB: 1024} }

// New opens (or creates) the Badger database at dir.
//
// This replaces db.New(ctx, databaseURL): there is no URL, no ping, and no
// migration step, just a directory on disk. Callers should still defer
// Close().
func New(dir string, cfgs ...Options) (*Store, error) {
	if dir == "" {
		return nil, fmt.Errorf("badger: empty data directory")
	}
	cfg := DefaultOptions()
	if len(cfgs) > 0 {
		cfg = cfgs[0]
	}
	valueThreshold := int64(cfg.ValueThresholdKB) * 1024
	if valueThreshold <= 0 {
		// Badger's own maximum: every value stays inline in the LSM.
		valueThreshold = 1 << 20
	}
	if valueThreshold > 1<<20 {
		valueThreshold = 1 << 20
	}

	opts := badger.DefaultOptions(dir).
		// The default logger is extremely chatty at INFO; keep warnings
		// and errors so real problems still surface in the service log.
		WithLogger(badgerLogger{}).
		// Values here are JSON blobs that routinely exceed 1 KB, so they
		// stay in the LSM tree rather than the value log: one read per Get
		// instead of two, and no value-log GC for this workload.
		WithValueThreshold(valueThreshold).
		// fsync every commit by default. Without it a power loss can drop
		// the last milliseconds of writes (quote WAL, cashback hex,
		// attach). Money beats throughput, so the safe default stands and
		// BADGER_SYNC_WRITES=false is an explicit, measured opt-out.
		WithSyncWrites(cfg.SyncWrites)

	// DetectConflicts MUST stay on. Several invariants here are implemented
	// as "read an index key, then set it" inside a transaction — the
	// idempotency key, the cashback quote index, the supplier-dispatch
	// claim. Badger's optimistic conflict detection is what turns two
	// racing writers into one ErrConflict instead of two committed rows
	// (i.e. a double order or a double cashback payout). Turning it off
	// would be a large speed win and a correctness disaster.
	opts = opts.WithDetectConflicts(true)

	// Memory envelope (env-tunable, BADGER_*): the defaults fit a 2-4 GB
	// VPS. A bigger box serving six-figure concurrent users can raise
	// BADGER_MEMTABLE_MB / BADGER_BLOCK_CACHE_MB for more write + read
	// headroom without touching code.
	if v := envPositiveInt("BADGER_MEMTABLE_MB"); v > 0 {
		opts = opts.WithMemTableSize(v * 1024 * 1024)
	} else {
		opts = opts.WithMemTableSize(16 * 1024 * 1024)
	}
	if v := envPositiveInt("BADGER_NUM_MEMTABLES"); v > 0 && v <= 16 {
		opts = opts.WithNumMemtables(int(v))
	} else {
		opts = opts.WithNumMemtables(1)
	}
	if v := envPositiveInt("BADGER_BLOCK_CACHE_MB"); v > 0 {
		opts = opts.WithBlockCacheSize(v * 1024 * 1024)
	} else {
		opts = opts.WithBlockCacheSize(16 * 1024 * 1024)
	}
	if v := envPositiveInt("BADGER_INDEX_CACHE_MB"); v > 0 {
		opts = opts.WithIndexCacheSize(v * 1024 * 1024)
	} else {
		opts = opts.WithIndexCacheSize(16 * 1024 * 1024)
	}
	if v := envPositiveInt("BADGER_NUM_COMPACTORS"); v > 0 && v <= 16 {
		opts = opts.WithNumCompactors(int(v))
	} else {
		opts = opts.WithNumCompactors(1)
	}

	bdb, err := badger.Open(opts)
	if err != nil {
		return nil, fmt.Errorf("open badger at %s: %w", dir, err)
	}

	s := &Store{db: bdb}
	go s.runValueLogGC()

	// Build the checkout-safety index in the background. Reads keep taking
	// the correct full-scan path until the marker is written, so this never
	// delays start-up and never changes an answer — only the cost of one.
	// The sleep between batches keeps a first-boot build over a large
	// existing database from starving live commits of disk bandwidth.
	go func() {
		// A panic here would take the whole process down for a maintenance
		// task that has a correct fallback, so it is guarded: on any failure
		// the marker stays unwritten and reads keep using the full scan.
		defer func() {
			if r := recover(); r != nil {
				log.Printf("db: checkout-safety index build aborted (%v) — reads stay on the full-scan path", r)
			}
		}()
		if err := s.EnsureBlockingIndex(50 * time.Millisecond); err != nil {
			log.Printf("db: checkout-safety index build stopped (%v) — reads stay on the full-scan path", err)
		}
	}()
	return s, nil
}

// envPositiveInt reads a positive integer from the environment; absent or
// malformed values return 0 (meaning "use the Badger default").
func envPositiveInt(key string) int64 {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return 0
	}
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || v <= 0 {
		return 0
	}
	return v
}

// Close flushes and closes the underlying database. Unlike a connection
// pool, this must complete before the process exits or recent writes can be
// left in the WAL for recovery on next boot.
//
// The closed flag is set FIRST so background maintenance observes it before
// the handle disappears; badger panics rather than erroring on a closed DB,
// so ordering here is the difference between a clean shutdown and a crash on
// the way out.
func (s *Store) Close() error {
	s.closed.Store(true)
	return s.db.Close()
}

// Closed reports whether the store has been shut down.
func (s *Store) Closed() bool { return s == nil || s.closed.Load() }

// DB exposes the raw handle for callers that need it (e.g. backups).
func (s *Store) DB() *badger.DB { return s.db }

// Update runs fn inside a read-write transaction, retrying automatically on
// the optimistic-concurrency conflicts that Badger surfaces at commit time.
//
// This is the direct analogue of the old ledger.withTx helper, and it is
// what makes multi-key invariants (record + all of its index keys) atomic.
func (s *Store) Update(fn func(txn *badger.Txn) error) error {
	s.pendingWrites.Add(1)
	defer s.pendingWrites.Add(-1)
	var err error
	for attempt := 0; attempt < maxRetries; attempt++ {
		err = s.db.Update(fn)
		if !errors.Is(err, badger.ErrConflict) {
			return err
		}
		s.writeConflicts.Add(1)
		// Brief, growing backoff so a hot key (e.g. one user's budget lock
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
	ticker := time.NewTicker(30 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		if s.closed.Load() {
			return
		}
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


// WipeUsersCompletely permanently deletes all user data.
func (s *Store) WipeUsersCompletely() error {
	prefixes := [][]byte{
		[]byte("u:"), []byte("ix:u:"), []byte("lock:q:user:"),
		[]byte("o:"), []byte("ix:o:"),
		[]byte("q:"), []byte("ix:q:"),
		[]byte("st:"), []byte("ix:st:"),
		[]byte("stm:"), []byte("ix:stm:"),
		[]byte("ix:feed:"),
		[]byte("cb:"), []byte("ix:cb:"),
		[]byte("sw:"), []byte("sl:"), []byte("sr:"),
	}
	for _, p := range prefixes {
		if err := s.db.DropPrefix(p); err != nil {
			return fmt.Errorf("wipe %s: %w", string(p), err)
		}
	}
	return nil
}
