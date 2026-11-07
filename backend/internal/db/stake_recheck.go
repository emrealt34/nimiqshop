package db

// stake_recheck.go — the post-fulfillment pool re-ask.
//
// THE PROBLEM THIS SOLVES: a buyer can stake with our pool directly in the
// Nimiq wallet (the shop sees no broadcast, no announce — there is no signal
// of its own), or the pool's index can simply lag the chain. If the order is
// delivered in that window, the fulfillment path resolves "not staked" and
// the cashback row is booked at the non-staker base (0 by default).
//
// THE ANSWER: no trust, no claims, no buyer-declared anything. The shop
// keeps asking ITS OWN SINGLE SOURCE — the pool's API — for that buyer,
// every 30 seconds for an hour after the delivery. The moment the pool says
// "staked", ReconcileStakerCashback upgrades the still-unpaid rows (and
// tops up already-paid ones). After the hour the row honestly stays at the
// base rate: the buyer was not staked in any window the shop can verify.
//
// Records are written in the SAME transaction that marks the quote
// fulfilled, so a crash between delivery and scheduling is impossible.
// They live in Badger ("sr:"), so restarts keep the clock running.

import (
	"context"
	"errors"
	"time"

	"github.com/dgraph-io/badger/v4"
)

// StakeRecheckWindow is how long after a delivery the shop keeps re-asking
// the pool about the buyer, and StakeRecheckInterval how often. The window
// comfortably covers the pool's index latency plus a wallet stake made
// right after (or right before) the purchase.
const (
	StakeRecheckWindow   = time.Hour
	StakeRecheckInterval = 30 * time.Second
	stakeRecheckTimeout  = 5 * time.Second
)

// StakeRecheck is one scheduled re-ask: the buyer of quote QuoteID was NOT
// (verifiably) staked at delivery time — re-check them until Deadline.
type StakeRecheck struct {
	QuoteID string `json:"quote_id"`
	UserID  string `json:"user_id"`
	// Address is the PRIMARY earning wallet, kept for the legacy no-user
	// fallback and for logs.
	Address string `json:"address"`
	// Addresses is every wallet that earns this buyer's cashback: the re-ask
	// has to watch all of them, because the stake that was missing at delivery
	// time can appear under any wallet the buyer owns — and one positive
	// answer is enough to upgrade the row (the total is recomputed from the
	// whole set, so a second wallet joining later is picked up too).
	Addresses []string  `json:"addresses,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	Deadline  time.Time `json:"deadline"`
}

// EarningAddresses is the set this re-ask watches, tolerating a record written
// before the list existed (one hour of records, but a restart must not drop a
// buyer's window).
func (rc StakeRecheck) EarningAddresses() []string {
	if len(rc.Addresses) > 0 {
		return rc.Addresses
	}
	if rc.Address != "" {
		return []string{rc.Address}
	}
	return nil
}

// recordStakeRecheckTx writes the re-ask inside the caller's open
// transaction (the fulfillment transaction itself). A crash between
// "delivered" and "scheduled" is therefore impossible.
func recordStakeRecheckTx(tx *badger.Txn, rc StakeRecheck) error {
	if rc.QuoteID == "" || rc.Address == "" {
		return nil
	}
	raw, err := marshal(rc)
	if err != nil {
		return err
	}
	return tx.Set(stakeRecheckKey(rc.QuoteID), raw)
}

// RecordStakeRecheck is the standalone wrapper (tests / manual scheduling).
func (s *Store) RecordStakeRecheck(rc StakeRecheck) error {
	return s.Update(func(tx *badger.Txn) error { return recordStakeRecheckTx(tx, rc) })
}

// ListStakeRechecks returns every scheduled re-ask (the set is tiny: one
// per delivered-to-a-non-staker order, self-clearing within an hour).
func (s *Store) ListStakeRechecks() ([]StakeRecheck, error) {
	var out []StakeRecheck
	err := s.View(func(tx *badger.Txn) error {
		it := tx.NewIterator(badger.DefaultIteratorOptions)
		defer it.Close()
		prefix := []byte(prefixStakeRecheck)
		for it.Seek(prefix); it.ValidForPrefix(prefix); it.Next() {
			var rc StakeRecheck
			if err := it.Item().Value(func(v []byte) error { return unmarshal(v, &rc) }); err == nil && rc.QuoteID != "" {
				out = append(out, rc)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ClearStakeRecheck removes a scheduled re-ask (resolved, or expired).
func (s *Store) ClearStakeRecheck(quoteID string) error {
	return s.Update(func(tx *badger.Txn) error { return tx.Delete(stakeRecheckKey(quoteID)) })
}

// ExtendStakeRecheck pushes the deadline out by one tick. Used when the pool
// could not be asked at all: an outage must not consume the buyer's window.
func (s *Store) ExtendStakeRecheck(quoteID string, by time.Duration) error {
	return s.Update(func(tx *badger.Txn) error {
		var rc StakeRecheck
		if err := getJSON(tx, stakeRecheckKey(quoteID), &rc); err != nil {
			if errors.Is(err, badger.ErrKeyNotFound) {
				return nil
			}
			return err
		}
		rc.Deadline = rc.Deadline.Add(by)
		raw, err := marshal(rc)
		if err != nil {
			return err
		}
		return tx.Set(stakeRecheckKey(quoteID), raw)
	})
}

// RunStakeRechecks is one pass of the re-ask loop: for every scheduled
// re-check, ask the pool (through the ONE wired resolver — the same
// contract the fulfillment path uses) and act on the answer:
//
//	staked            → observe, reconcile the cashback rows UP to the
//	                    staker rate, clear the pending-stake note, done.
//	not staked        → keep asking until the deadline, then drop it (the
//	                    row honestly stays at the base rate).
//	pool unreachable  → skip this tick AND extend the deadline by one tick,
//	                    so an outage never eats the buyer's window.
//
// Returns how many re-checks resolved (staked) and how many expired.
func (s *Store) RunStakeRechecks(now time.Time) (resolved, expired int) {
	if !s.HasStakerLookup() {
		return 0, 0
	}
	recs, err := s.ListStakeRechecks()
	if err != nil {
		return 0, 0
	}
	for _, rc := range recs {
		addrs := rc.EarningAddresses()
		if len(addrs) == 0 {
			_ = s.ClearStakeRecheck(rc.QuoteID)
			expired++
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), stakeRecheckTimeout)
		// Ask about EVERY earning wallet and fold the answers into one total
		// (stakes summed, base once, loyalty the longest streak). The
		// aggregate also advances each wallet's loyalty and ledger clocks,
		// which is what the single-address version of this loop used to do
		// by hand.
		st, report := s.AggregateStakerStake(ctx, addrs)
		cancel()
		if !st.Staked && report.Failed > 0 && report.Answered == 0 {
			// The pool could not be asked about any wallet: this tick does not
			// count, and an outage must not eat the buyer's window.
			_ = s.ExtendStakeRecheck(rc.QuoteID, StakeRecheckInterval)
			continue
		}
		if st.Staked && st.StakeLuna > 0 {
			// User-scoped on purpose. The earning wallets are not account
			// addresses at all — for a linked staking wallet, resolving the
			// user from it returns ErrNotFound, the repair never runs, and the
			// buyer keeps the base rate with no error anywhere. Only rows that
			// carry no user id (none are written any more) fall back to the
			// address form, which can only ever find the primary wallet.
			var rerr error
			if rc.UserID != "" {
				_, rerr = s.ReconcileStakerCashbackForUser(rc.UserID, st, now)
			} else {
				_, rerr = s.ReconcileStakerCashback(addrs[0], st, now)
			}
			if rerr == nil {
				_ = s.ClearStakeRecheck(rc.QuoteID)
				resolved++
			}
			continue
		}
		// A definitive "not staked". Past the deadline the row stays at
		// the base rate — drop the re-ask.
		if now.After(rc.Deadline) {
			_ = s.ClearStakeRecheck(rc.QuoteID)
			expired++
		}
	}
	return resolved, expired
}
