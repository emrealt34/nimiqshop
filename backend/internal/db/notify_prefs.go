package db

// Wallet-notification preferences and send ledger.
//
// The 1-Luna memo channel writes into a user's wallet history permanently,
// so the shop must be able to prove — not assume — that it is not spamming:
//
//   - OPT-OUT is stored per user and is authoritative. There is no
//     "re-engagement" override anywhere in the codebase.
//   - Every send is RECORDED with its reason and timestamp, so the policy
//     layer can enforce a per-reason cooldown and a rolling 30-day budget.
//     The ledger is the only source of truth for "have we bothered this
//     person recently?".
//
// Keys:
//
//	notifpref:<userID>            -> "off" | "on"
//	notifsent:<userID>:<reason>   -> RFC3339 list of recent send times
//
// The per-reason value keeps only the timestamps still inside the widest
// policy window (30 days), so the record cannot grow without bound.

import (
	"strings"
	"time"

	"github.com/dgraph-io/badger/v4"
)

// notifyLedgerWindow is how far back send timestamps are retained. It must
// be >= the largest cooldown/budget window in internal/notification/policy.
const notifyLedgerWindow = 30 * 24 * time.Hour

// maxLedgerEntriesPerReason bounds a single key's value even if something
// upstream misbehaves and sends in a tight loop.
const maxLedgerEntriesPerReason = 64

func notifyPrefKey(userID string) string {
	return "notifpref:" + strings.ToLower(strings.TrimSpace(userID))
}

func notifySentKey(userID, reason string) string {
	return "notifsent:" + strings.ToLower(strings.TrimSpace(userID)) + ":" + strings.TrimSpace(reason)
}

// SetWalletNotifyEnabled stores the user's channel preference.
func (s *Store) SetWalletNotifyEnabled(userID string, enabled bool) error {
	if strings.TrimSpace(userID) == "" {
		return ErrNotFound
	}
	val := []byte("off")
	if enabled {
		val = []byte("on")
	}
	return s.Update(func(txn *badger.Txn) error {
		return txn.Set([]byte(notifyPrefKey(userID)), val)
	})
}

// WalletNotifyEnabled reports the user's preference. Default is ENABLED:
// every reason in the policy is an event the user owns (their order, their
// money, their question), and silence about those would be the bigger
// failure. Marketing is impossible by construction, so an opt-in default
// would only suppress genuinely useful messages.
func (s *Store) WalletNotifyEnabled(userID string) (bool, error) {
	if strings.TrimSpace(userID) == "" {
		return false, nil
	}
	enabled := true
	err := s.View(func(txn *badger.Txn) error {
		item, err := txn.Get([]byte(notifyPrefKey(userID)))
		if err != nil {
			return err
		}
		return item.Value(func(v []byte) error {
			enabled = string(v) != "off"
			return nil
		})
	})
	if err == ErrNotFound || err == badger.ErrKeyNotFound {
		return true, nil // never set: default on
	}
	if err != nil {
		return true, err
	}
	return enabled, nil
}

// RecordNotifySend appends a send timestamp for (user, reason), pruning
// anything outside the retention window.
func (s *Store) RecordNotifySend(userID, reason string, at time.Time) error {
	key := []byte(notifySentKey(userID, reason))
	return s.Update(func(txn *badger.Txn) error {
		times := readTimes(txn, key)
		times = append(times, at.UTC())
		times = pruneTimes(times, at.UTC().Add(-notifyLedgerWindow))
		if len(times) > maxLedgerEntriesPerReason {
			times = times[len(times)-maxLedgerEntriesPerReason:]
		}
		return txn.Set(key, []byte(encodeTimes(times)))
	})
}

// LastNotifySend returns the most recent send time for (user, reason), or
// the zero time when there is none.
func (s *Store) LastNotifySend(userID, reason string) (time.Time, error) {
	var last time.Time
	err := s.View(func(txn *badger.Txn) error {
		for _, t := range readTimes(txn, []byte(notifySentKey(userID, reason))) {
			if t.After(last) {
				last = t
			}
		}
		return nil
	})
	return last, err
}

// NotifyBudgetUsed counts shop-initiated sends to a user inside the rolling
// window. `budgeted` is the set of reasons that consume the budget — the
// policy package owns that list, so the store stays free of policy.
func (s *Store) NotifyBudgetUsed(userID string, budgeted []string, since time.Time) (int, error) {
	count := 0
	err := s.View(func(txn *badger.Txn) error {
		for _, reason := range budgeted {
			for _, t := range readTimes(txn, []byte(notifySentKey(userID, reason))) {
				if t.After(since) {
					count++
				}
			}
		}
		return nil
	})
	return count, err
}

/* ------------------------------ helpers -------------------------------- */

func readTimes(txn *badger.Txn, key []byte) []time.Time {
	item, err := txn.Get(key)
	if err != nil {
		return nil
	}
	var raw string
	if err := item.Value(func(v []byte) error {
		raw = string(v)
		return nil
	}); err != nil {
		return nil
	}
	return decodeTimes(raw)
}

func decodeTimes(raw string) []time.Time {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]time.Time, 0, len(parts))
	for _, p := range parts {
		if t, err := time.Parse(time.RFC3339, strings.TrimSpace(p)); err == nil {
			out = append(out, t.UTC())
		}
	}
	return out
}

func encodeTimes(times []time.Time) string {
	parts := make([]string, 0, len(times))
	for _, t := range times {
		parts = append(parts, t.UTC().Format(time.RFC3339))
	}
	return strings.Join(parts, ",")
}

func pruneTimes(times []time.Time, cutoff time.Time) []time.Time {
	out := times[:0]
	for _, t := range times {
		if t.After(cutoff) {
			out = append(out, t)
		}
	}
	return out
}
