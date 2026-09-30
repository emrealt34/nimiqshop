package db

import (
	"errors"
	"log"
	"sync/atomic"
	"time"

	"github.com/dgraph-io/badger/v4"
)

/*
quote_index.go — the O(1) checkout-safety index.

THE PROBLEM IT FIXES

Every purchase used to answer "does this buyer already have an unresolved
checkout?" by scanning the buyer's ENTIRE quote history and JSON-decoding each
record, inside a read-write Badger transaction. The same scan ran twice per
checkout (once in the handler's pre-check, once in the atomic gate), plus once
more for the rolling daily/monthly budget.

For a buyer with a handful of orders that is invisible. It does not stay
invisible:

  - An abandoned checkout is FREE to the buyer. countsAgainstDailyBudget
    deliberately excludes order_creating / awaiting_payment, so the daily
    order and spend ceilings never counted them.
  - The settlement tracker expires an unpaid quote within seconds, and an
    expired-unpaid quote does not block the next one.
  - Therefore one client can open an unbounded number of quotes: create,
    abandon, wait for the sweep, repeat — a few per second, forever.
  - Each new attempt then scans every record that client has ever created.
    After a week that is >100k JSON decodes inside a write transaction, per
    attempt. The attacker's own checkout gets slow, and — because Badger
    commits are serialised and DetectConflicts is on — so does everyone
    else's.

That is a one-client, no-privilege denial of service. It is fixed here in two
independent ways, because either alone leaves a gap:

1. A per-user BLOCKING index (below). "Do I have an unresolved checkout?"
   becomes a scan of the handful of quotes that are actually unresolved,
   maintained in the same transaction as every quote write, so it can never
   drift from the record it indexes.

2. A bounded window for the budget scan. The user index is ordered
   newest-first by reversed timestamp, so the iterator can SEEK straight to
   the window boundary and stop there instead of walking to the beginning of
   the buyer's history.

MIGRATION SAFETY

An index that only exists for new writes would be wrong on an existing
database: a quote that was already in manual_review before the upgrade would
be invisible to it, and the buyer could open a second live checkout. So the
index is built at start-up by EnsureBlockingIndex, and until that build has
finished and written its marker, every read falls back to the old full scan.
Correctness first, speed once it is proven. The build runs in the background,
so an upgrade never delays start-up.
*/

// blockingIndexMarker is written once the start-up build has seen every
// quote. Its presence is what licenses the fast path.
const blockingIndexMarker = "meta:ix:q:blocking:v1"

func quoteBlockingIndexKey(userID, quoteID string) []byte {
	return []byte("ix:q:block:" + userID + ":" + quoteID)
}

func quoteBlockingIndexPrefix(userID string) []byte {
	return []byte("ix:q:block:" + userID + ":")
}

// blockingIndexReady is a process-local mirror of the marker so the hot path
// does not read Badger to decide how to read Badger.
var blockingIndexReady atomic.Bool

// BlockingIndexReady reports whether the fast checkout-safety path is live.
func BlockingIndexReady() bool { return blockingIndexReady.Load() }

// putQuoteTx is the ONE way a quote record is written. Every call site in this
// package goes through it, which is what makes the blocking index impossible
// to forget: adding a new write path cannot leave the index stale, because
// there is no write path that does not maintain it.
func putQuoteTx(tx *badger.Txn, q *Quote) error {
	if q == nil || q.ID == "" {
		return errors.New("db: cannot persist a quote without an id")
	}
	raw, err := marshal(q)
	if err != nil {
		return err
	}
	if err := tx.Set(quoteKey(q.ID), raw); err != nil {
		return err
	}
	return syncBlockingIndexTx(tx, q)
}

// syncBlockingIndexTx makes the index agree with the record: present when the
// quote can block a new purchase, absent otherwise. Idempotent, so a replayed
// Badger transaction (Update retries on conflict) cannot corrupt it.
func syncBlockingIndexTx(tx *badger.Txn, q *Quote) error {
	if q == nil || q.ID == "" || q.UserID == "" {
		return nil
	}
	key := quoteBlockingIndexKey(q.UserID, q.ID)
	// Admin test-center quotes never hold a checkout hostage, matching the
	// long-standing exclusion in the full-scan version. Keeping the rule in
	// one predicate means the two paths cannot disagree.
	if q.TestMode || !BlocksNewPurchase(*q) {
		return tx.Delete(key)
	}
	return tx.Set(key, []byte(q.ID))
}

// listBlockingQuoteIDs returns the ids of this buyer's unresolved checkouts.
// It is a scan over a set that is, by construction, tiny: BlocksNewPurchase is
// a transient property, and the settlement tracker drives quotes out of it.
func listBlockingQuoteIDs(tx *badger.Txn, userID string) ([]string, error) {
	if userID == "" {
		return nil, nil
	}
	out := make([]string, 0, 4)
	err := scanIndex(tx, quoteBlockingIndexPrefix(userID), 0, func(id string) error {
		out = append(out, id)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// blockingQuotesFor returns the buyer's blocking quotes. When the index is not
// built yet it falls back to the historical full scan, so the answer is
// identical either way — only the cost differs.
func (s *Store) blockingQuotesFor(tx *badger.Txn, userID string) ([]Quote, error) {
	if blockingIndexReady.Load() {
		ids, err := listBlockingQuoteIDs(tx, userID)
		if err != nil {
			return nil, err
		}
		out := make([]Quote, 0, len(ids))
		for _, id := range ids {
			var q Quote
			if err := getJSON(tx, quoteKey(id), &q); err != nil {
				if errors.Is(err, ErrNotFound) {
					// Index entry outlived its record: heal it as we go.
					_ = tx.Delete(quoteBlockingIndexKey(userID, id))
					continue
				}
				return nil, err
			}
			if q.UserID != userID {
				continue
			}
			// Re-check the predicate against the record itself. The index is
			// maintained transactionally, but a defensive re-check costs
			// nothing on a set of a few and guarantees the two can never
			// disagree even if a future write path forgets putQuoteTx.
			if q.TestMode || !BlocksNewPurchase(q) {
				_ = tx.Delete(quoteBlockingIndexKey(userID, id))
				continue
			}
			out = append(out, q)
		}
		return out, nil
	}

	var out []Quote
	err := scanIndex(tx, quoteUserIndexPrefix(userID), 0, func(id string) error {
		var q Quote
		if err := getJSON(tx, quoteKey(id), &q); err != nil {
			if errors.Is(err, ErrNotFound) {
				return nil
			}
			return err
		}
		if q.TestMode || !BlocksNewPurchase(q) {
			return nil
		}
		out = append(out, q)
		return nil
	})
	return out, err
}

// scanQuotesSince walks the buyer's quote index but STOPS at the window
// boundary instead of reading to the end of their history.
//
// The index key is ix:q:user:<uid>:<reverseTS(created)>:<id> and reverseTS is
// a zero-padded, monotonically DESCENDING encoding of the creation time. So a
// forward iteration is newest-first, and seeking to reverseTS(since) lands
// exactly on the first record that is still inside the window. Everything the
// callback sees satisfies created >= since.
//
// fn receives the decoded quote. Returning a non-nil error aborts the scan.
func scanQuotesSince(txn *badger.Txn, userID string, since time.Time, fn func(q Quote) error) error {
	prefix := quoteUserIndexPrefix(userID)
	opts := badger.DefaultIteratorOptions
	opts.Prefix = prefix
	opts.PrefetchSize = 32

	it := txn.NewIterator(opts)
	defer it.Close()

	// Seek key: the prefix followed by the reversed boundary timestamp. Any
	// record newer than `since` sorts BEFORE this key, so seeking here and
	// iterating backwards is what we want — but Badger iterators only move
	// forward, so instead we seek to the prefix start and rely on the
	// descending encoding: the first key >= prefix+reverseTS(since) is the
	// OLDEST in-window record, and everything before it is newer. Iterating
	// forward from the prefix therefore visits newest-first and we can stop
	// the moment the key reaches the boundary.
	boundary := append(append([]byte{}, prefix...), []byte(reverseTS(since.UnixNano()))...)
	for it.Seek(prefix); it.ValidForPrefix(prefix); it.Next() {
		key := it.Item().Key()
		// Keys are ascending; once we reach the boundary every remaining key
		// is an older record and is outside the window by construction.
		if string(key) >= string(boundary) {
			break
		}
		var id string
		if err := it.Item().Value(func(v []byte) error { id = string(v); return nil }); err != nil {
			return err
		}
		var q Quote
		if err := getJSON(txn, quoteKey(id), &q); err != nil {
			if errors.Is(err, ErrNotFound) {
				continue
			}
			return err
		}
		// Defence in depth: the seek is a key-order optimisation, not a
		// substitute for the actual predicate.
		if q.CreatedAt.Before(since) {
			continue
		}
		if err := fn(q); err != nil {
			return err
		}
	}
	return nil
}

// EnsureBlockingIndex builds the per-user blocking index over the whole quote
// keyspace and then writes the readiness marker. Until it returns, reads take
// the (correct but slower) full-scan path.
//
// It is resumable and idempotent: an interrupted build simply leaves the
// marker unwritten and the next start-up repeats the work. Records already
// indexed are re-written with the same value.
func (s *Store) EnsureBlockingIndex(batchSleep time.Duration) error {
	if blockingIndexReady.Load() {
		return nil
	}
	if s.Closed() {
		return errors.New("db: store closed before the index build ran")
	}
	var done bool
	if err := s.View(func(tx *badger.Txn) error {
		_, err := tx.Get([]byte(blockingIndexMarker))
		if err == nil {
			done = true
		} else if errors.Is(err, badger.ErrKeyNotFound) {
			done = false
		} else {
			return err
		}
		return nil
	}); err != nil {
		return err
	}
	if done {
		blockingIndexReady.Store(true)
		return nil
	}

	start := time.Now()
	var scanned, indexed int64
	opts := badger.DefaultIteratorOptions
	opts.Prefix = []byte(prefixQuote)
	// Values are only needed for the ones that block; prefetching them all
	// would double the memory of the build for no benefit.
	opts.PrefetchValues = false
	opts.PrefetchSize = 256

	// Paginate by last-seen key so one giant iterator does not pin a
	// long-lived read transaction (Badger's GC cannot reclaim versions while
	// a read transaction is open).
	var resume []byte
	for {
		// Re-checked per batch: a shutdown during a long first-boot build
		// must stop the walk immediately rather than panicking on a closed
		// handle (badger panics, it does not return an error).
		if s.Closed() {
			return errors.New("db: store closed during the index build")
		}
		var last []byte
		batch := make([]struct {
			id       string
			userID   string
			blocking bool
		}, 0, 512)
		err := s.View(func(tx *badger.Txn) error {
			it := tx.NewIterator(opts)
			defer it.Close()
			seek := opts.Prefix
			if len(resume) > 0 {
				seek = resume
			}
			n := 0
			for it.Seek(seek); it.ValidForPrefix(opts.Prefix); it.Next() {
				item := it.Item()
				if len(resume) > 0 && string(item.Key()) == string(resume) {
					continue
				}
				key := append([]byte{}, item.Key()...)
				var q Quote
				if err := item.Value(func(v []byte) error { return unmarshal(v, &q) }); err != nil {
					// A record this build cannot decode must not silently
					// drop out of the index: abort and leave the marker
					// unwritten so the slow-but-correct path stays in force.
					return err
				}
				scanned++
				last = key
				n++
				if q.ID == "" || q.UserID == "" {
					continue
				}
				batch = append(batch, struct {
					id       string
					userID   string
					blocking bool
				}{q.ID, q.UserID, !q.TestMode && BlocksNewPurchase(q)})
				if n >= 512 {
					break
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
		if len(batch) == 0 {
			break
		}
		if err := s.Update(func(tx *badger.Txn) error {
			for _, b := range batch {
				key := quoteBlockingIndexKey(b.userID, b.id)
				if b.blocking {
					if err := tx.Set(key, []byte(b.id)); err != nil {
						return err
					}
					indexed++
				} else if err := tx.Delete(key); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			return err
		}
		resume = last
		if len(last) == 0 {
			break
		}
		if batchSleep > 0 {
			time.Sleep(batchSleep)
		}
	}

	if err := s.Update(func(tx *badger.Txn) error {
		return tx.Set([]byte(blockingIndexMarker), []byte(time.Now().UTC().Format(time.RFC3339Nano)))
	}); err != nil {
		return err
	}
	blockingIndexReady.Store(true)
	log.Printf("db: checkout-safety index built — %d quotes scanned, %d blocking, %s (checkout safety checks are now O(active) instead of O(history))",
		scanned, indexed, time.Since(start).Round(time.Millisecond))
	return nil
}

// HealthSnapshot is the store's contribution to /api/health: enough to tell
// whether the database is the bottleneck without exposing any record content.
func (s *Store) HealthSnapshot() map[string]any {
	out := map[string]any{
		"blocking_index_ready": blockingIndexReady.Load(),
	}
	if s == nil || s.db == nil {
		return out
	}
	// Badger exposes LSM/vlog sizes cheaply; a growing value log is the
	// classic "disk fills up slowly" signal.
	lsm, vlog := s.db.Size()
	out["lsm_bytes"] = lsm
	out["value_log_bytes"] = vlog
	out["pending_writes"] = s.pendingWrites.Load()
	out["write_conflicts"] = s.writeConflicts.Load()
	return out
}

// BlockingQuotesForUser is the exported answer to "does this buyer have an
// unresolved checkout?". Handlers use it as the cheap pre-check before the
// atomic gate, which asks the same question inside its transaction.
//
// Both go through the same index and the same BlocksNewPurchase predicate, so
// the pre-check and the gate can never disagree — the failure mode where the
// handler says "clear" and the transaction then refuses is exactly what a
// buyer experiences as a random 409.
func (s *Store) BlockingQuotesForUser(userID string) ([]Quote, error) {
	if userID == "" {
		return nil, nil
	}
	var out []Quote
	err := s.View(func(tx *badger.Txn) error {
		qs, err := s.blockingQuotesFor(tx, userID)
		out = qs
		return err
	})
	return out, err
}
