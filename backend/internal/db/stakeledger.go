package db

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"strconv"
	"time"

	"github.com/dgraph-io/badger/v4"

	"nimiqshop/internal/stakeledger"
)

/* Single-ledger staker cashback persistence (Tek Defter).
 *
 * One stakeledger.Ledger JSON blob per canonical Nimiq address, plus a
 * global feed watermark. All money math lives in internal/stakeledger;
 * this file only makes mutations atomic and idempotent:
 *
 *  - a purchase debits the ledger INSIDE the fulfillment transaction, so
 *    two racing deliveries for the same wallet can never both read the
 *    pre-debit balance (Badger retries the loser on conflict);
 *  - the batch-feed path is retired: GoPool replaced GET /api/cashback/feed
 *    with the per-address GET /api/cashback/profit, whose cumulative deltas
 *    (ApplyProfitSnapshot) are idempotent by construction. The watermark key
 *    remains for pre-existing ledgers and shows in the admin console.
 */

const stakeLedgerWatermarkKey = "sl:watermark"

func stakeLedgerKey(address string) []byte {
	return []byte(prefixStakeLedger + canonicalStakeAddr(address))
}

// ledgerParamsFn returns the live, normalized programme parameters. Wired
// at boot from the admin settings; the safe defaults stand in until then.
type ledgerParamsFn func() stakeledger.Params

// SetStakeLedgerParams installs the parameter resolver (admin settings).
func (s *Store) SetStakeLedgerParams(fn ledgerParamsFn) {
	s.ledgerMu.Lock()
	s.ledgerParams = fn
	s.ledgerMu.Unlock()
}

func (s *Store) stakeLedgerParams() stakeledger.Params {
	s.ledgerMu.RLock()
	fn := s.ledgerParams
	s.ledgerMu.RUnlock()
	if fn == nil {
		return stakeledger.Normalize(stakeledger.Defaults)
	}
	return stakeledger.Normalize(fn())
}

// ReadStakeLedger returns a copy of the ledger with any pending day/month
// boundary applied (NOT persisted — the next write or the sweep persists
// it). ok is false when the address has no ledger yet. now is the UTC wall
// clock used for pending-rollover application; callers pass time.Now().UTC().
func (s *Store) ReadStakeLedger(address string, now time.Time) (stakeledger.Ledger, bool, error) {
	address = canonicalStakeAddr(address)
	var l stakeledger.Ledger
	ok := false
	err := s.View(func(tx *badger.Txn) error {
		e := getJSON(tx, stakeLedgerKey(address), &l)
		if e != nil {
			if e == ErrNotFound {
				return nil
			}
			return e
		}
		ok = true
		return nil
	})
	if err != nil || !ok {
		return l, ok, err
	}
	l.Rollover(s.stakeLedgerParams(), now.UTC())
	return l, true, nil
}

// ObserveStakeLedger records an observed stake change in the ledger:
// increase dilutes the loyalty age, decrease shrinks the book pro rata,
// full withdrawal empties it (r = 0). No fee is credited here — only the
// pool's settled batches do that. Safe to call with stake 0 (that IS the
// withdrawal event).
func (s *Store) ObserveStakeLedger(address string, stakeLuna int64, now time.Time) error {
	address = canonicalStakeAddr(address)
	if address == "" {
		return nil
	}
	p := s.stakeLedgerParams()
	return s.Update(func(tx *badger.Txn) error {
		var l stakeledger.Ledger
		err := getJSON(tx, stakeLedgerKey(address), &l)
		if err != nil && err != ErrNotFound {
			return err
		}
		if err == ErrNotFound {
			l = *stakeledger.New(address, 0, now)
		}
		l.OnStakeChange(float64(stakeLuna)/100_000.0, p, now)
		raw, err := marshal(l)
		if err != nil {
			return err
		}
		return tx.Set(stakeLedgerKey(address), raw)
	})
}

// ApplyProfitSnapshot consumes the cumulative per-address profit returned by
// GET /api/cashback/profit. Repeating the same total credits zero; a larger
// total credits only its positive delta. On a calendar-month change callers
// first pass last_month, then this_month, so fees settled while the buyer was
// inactive are not lost. Pool resets/regressions never create a negative debit.
func (s *Store) ApplyProfitSnapshot(address, month string, totalFeeLuna int64, nimUsd, loyaltyDays, loyaltyMultiplier float64, stakeLuna int64, now time.Time) (float64, error) {
	address = canonicalStakeAddr(address)
	period, periodErr := time.Parse("2006-01", month)
	if address == "" || periodErr != nil || period.Format("2006-01") != month || month > now.UTC().Format("2006-01") || totalFeeLuna < 0 || stakeLuna < 0 || nimUsd <= 0 || math.IsNaN(nimUsd) || math.IsInf(nimUsd, 0) || loyaltyDays < 0 || math.IsNaN(loyaltyDays) || math.IsInf(loyaltyDays, 0) || loyaltyMultiplier < 0 || loyaltyMultiplier > 1 || math.IsNaN(loyaltyMultiplier) || math.IsInf(loyaltyMultiplier, 0) {
		return 0, fmt.Errorf("invalid pool profit snapshot")
	}
	p := s.stakeLedgerParams()
	var credited float64
	err := s.Update(func(tx *badger.Txn) error {
		credited = 0
		var l stakeledger.Ledger
		err := getJSON(tx, stakeLedgerKey(address), &l)
		if err != nil && err != ErrNotFound {
			return err
		}
		if err == ErrNotFound {
			l = *stakeledger.New(address, 0, now)
		}
		// Retain a high watermark for every month. A pool reset, late response,
		// or replay of an older month cannot turn already consumed fees into
		// a new balance. Seed the latest legacy snapshot during migration.
		key := []byte("slp:" + address + ":" + month)
		var previous int64
		if e := getJSON(tx, key, &previous); e != nil && e != ErrNotFound {
			return e
		}
		if l.ProfitMonth == month && l.ProfitLuna > previous {
			previous = l.ProfitLuna
		}
		// NimiqBase is authoritative for both current stake and loyalty. A
		// definitive zero means full exit: balance and loyalty reset.
		l.S = float64(stakeLuna) / 100_000.0
		l.D = loyaltyDays
		l.LoyaltyMultiplier = loyaltyMultiplier
		if stakeLuna <= 0 {
			l.A, l.D, l.AccM, l.LoyaltyMultiplier = 0, 0, 0, 0
		} else if totalFeeLuna > previous {
			credited = l.AccrueAuthoritative(float64(totalFeeLuna-previous)/100_000.0, nimUsd, loyaltyMultiplier, p, now)
		}
		if totalFeeLuna > previous {
			previous = totalFeeLuna
		}
		watermark, e := marshal(previous)
		if e != nil {
			return e
		}
		if e = tx.Set(key, watermark); e != nil {
			return e
		}
		if month >= l.ProfitMonth {
			l.ProfitMonth = month
			l.ProfitLuna = previous
		}
		l.UpdatedAt = now.UTC()
		raw, err := marshal(l)
		if err != nil {
			return err
		}
		return tx.Set(stakeLedgerKey(address), raw)
	})
	return credited, err
}

func ledgerWatermarkInTx(tx *badger.Txn) (int64, error) {
	v, err := getString(tx, []byte(stakeLedgerWatermarkKey))
	if err != nil {
		if err == ErrNotFound {
			return 0, nil
		}
		return 0, err
	}
	return strconv.ParseInt(v, 10, 64)
}

// LedgerWatermark returns the last fully-applied pool-fee batch (0 = none).
func (s *Store) LedgerWatermark() (int64, error) {
	var wm int64
	err := s.View(func(tx *badger.Txn) error {
		var e error
		wm, e = ledgerWatermarkInTx(tx)
		return e
	})
	return wm, err
}

// sharedSpendKey is the account-level boost-eligible spend budget:
// es:<canonical_signin_address>. One record per ACCOUNT, spent by every order the
// account earns boost on, however many linked wallets earn alongside the
// sign-in one.
func sharedSpendKey(account string) []byte {
	return []byte(prefixEligibleSpend + canonicalStakeAddr(account))
}

// readStakeLedgerInTx loads a book inside an open transaction. ok is false
// when the address has no ledger yet (normal for a fresh buyer).
func readStakeLedgerInTx(tx *badger.Txn, address string) (stakeledger.Ledger, bool, error) {
	addr := canonicalStakeAddr(address)
	var l stakeledger.Ledger
	if addr == "" {
		return l, false, nil
	}
	err := getJSON(tx, stakeLedgerKey(addr), &l)
	if err == ErrNotFound {
		return l, false, nil
	}
	if err != nil {
		return l, false, err
	}
	return l, true, nil
}

// readBooksInTx loads every book of an earning set, deduplicated and
// canonicalized. Missing books are skipped (their stake contributes nothing),
// a storage failure aborts the read.
func readBooksInTx(tx *badger.Txn, addresses []string) ([]stakeledger.Ledger, error) {
	seen := make(map[string]bool, len(addresses))
	books := make([]stakeledger.Ledger, 0, len(addresses))
	for _, address := range addresses {
		addr := canonicalStakeAddr(address)
		if addr == "" || seen[addr] {
			continue
		}
		seen[addr] = true
		l, ok, err := readStakeLedgerInTx(tx, addr)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		books = append(books, l)
	}
	return books, nil
}

// Each shard belongs to the wallet that incurred it. Splits retain the same
// historical shards on both sides; merges take a per-shard maximum, so neither
// a budget reset nor double counting is possible after unlink/relink.
type stakeSpendState struct {
	Components map[string]stakeledger.SharedSpend `json:"components"`
}

func stakeSpendStateInTx(tx *badger.Txn, account string, now time.Time) (stakeSpendState, error) {
	state := stakeSpendState{}
	group, err := stakeGroupInTx(tx, account)
	if err != nil {
		return state, err
	}
	groupID := group.ID
	key := standaloneSpendStateKey(account)
	if groupID != "" {
		key = stakeGroupSpendKey(groupID)
	}
	err = getJSON(tx, key, &state)
	if errors.Is(err, ErrNotFound) {
		if groupID != "" {
			return state, errors.New("linked stake group budget missing")
		}
		var old stakeledger.SharedSpend
		err = getJSON(tx, sharedSpendKey(account), &old)
		if errors.Is(err, ErrNotFound) {
			state.Components = map[string]stakeledger.SharedSpend{}
			return state, nil
		}
		if err != nil {
			return state, err
		}
		state.Components = map[string]stakeledger.SharedSpend{canonicalStakeAddr(account): old}
	} else if err != nil {
		return state, err
	}
	if state.Components == nil {
		return state, errors.New("invalid stake group budget")
	}
	for key, component := range state.Components {
		component.Rollover(now)
		state.Components[key] = component
	}
	return state, nil
}

func (s stakeSpendState) total(now time.Time) stakeledger.SharedSpend {
	total := stakeledger.NewSharedSpend(now)
	for _, component := range s.Components {
		component.Rollover(now)
		total.HDay = addCents(total.HDay, component.HDay)
		total.HMonth = addCents(total.HMonth, component.HMonth)
	}
	return total
}

func sharedSpendInTx(tx *badger.Txn, account string, now time.Time) (stakeledger.SharedSpend, error) {
	state, err := stakeSpendStateInTx(tx, account, now)
	if err != nil {
		return stakeledger.SharedSpend{}, err
	}
	return state.total(now), nil
}

// Persist only the increase in the signed-in wallet's shard. Copying full
// totals into every wallet would double-count them on a later group merge.
func persistSharedSpendInTx(tx *badger.Txn, account string, ss stakeledger.SharedSpend) error {
	account = canonicalStakeAddr(account)
	if account == "" {
		return nil
	}
	now, err := time.Parse("2006-01-02", ss.DayKey)
	if err != nil {
		return err
	}
	state, err := stakeSpendStateInTx(tx, account, now)
	if err != nil {
		return err
	}
	old := state.total(now)
	component := state.Components[account]
	component.Rollover(now)
	if ss.HDay > old.HDay {
		component.HDay = addCents(component.HDay, ss.HDay-old.HDay)
	}
	if ss.HMonth > old.HMonth {
		component.HMonth = addCents(component.HMonth, ss.HMonth-old.HMonth)
	}
	state.Components[account] = component
	raw, err := marshal(state)
	if err != nil {
		return err
	}
	id, err := getString(tx, stakeGroupIndexKey(account))
	if err == nil {
		return tx.Set(stakeGroupSpendKey(id), raw)
	}
	if !errors.Is(err, ErrNotFound) {
		return err
	}
	return tx.Set(standaloneSpendStateKey(account), raw)
}

// ReadSharedSpend is the buyer/admin-side accessor: the account's shared
// boost-eligible spend budget with pending boundaries applied, WITHOUT
// persisting the rollover. A missing record reads as a fresh (full) budget.
func (s *Store) ReadSharedSpend(account string, now time.Time) (stakeledger.SharedSpend, error) {
	account = canonicalStakeAddr(account)
	fresh := stakeledger.NewSharedSpend(now)
	if account == "" {
		return fresh, ErrNotFound
	}
	var ss stakeledger.SharedSpend
	err := s.View(func(tx *badger.Txn) error {
		var e error
		ss, e = sharedSpendInTx(tx, account, now)
		return e
	})
	return ss, err
}

// purchaseEarningSetInTx is the fulfillment-side half of the ledger for a
// WHOLE earning set, run INSIDE the fulfillment transaction so the boost
// debits and the cashback row commit together (or not at all).
//
// ONE order spends ONE eligible-spend charge against the ACCOUNT's shared
// budget ($500/day, $1000/month by default), however many linked wallets
// earn on it — before this, every book carried its own copy of the caps and
// linking N wallets multiplied the account's eligible spend N-fold. The books
// stay separate (own stake, own loyalty age, own balance): no book can pay
// more than its own stake earned. Only the limit is shared.
//
// Returns the boost in whole Luna (0 when there is nothing to pay — no
// ledgers, no balances, no price, or the shared budget is spent) plus the
// rate (bps) that produced it, for the audit row. Params are resolved by the
// caller (one stable value per fulfillment, outside the txn body).
func purchaseEarningSetInTx(tx *badger.Txn, account string, addresses []string, xUSD, nimUsd float64, p stakeledger.Params, now time.Time) (boostLuna int64, boostBps int, err error) {
	return purchaseEarningSet(tx, account, addresses, xUSD, nimUsd, p, now, true)
}

// purchaseEarningSet is the shared implementation. persist=true writes the
// debited books and the (possibly) charged budget; persist=false is the
// read-only preview twin — the plan is computed on in-memory copies and
// thrown away, so the buyer sees exactly what fulfillment would pay.
func purchaseEarningSet(tx *badger.Txn, account string, addresses []string, xUSD, nimUsd float64, p stakeledger.Params, now time.Time, persist bool) (boostLuna int64, boostBps int, err error) {
	if xUSD <= 0 || nimUsd <= 0 {
		return 0, 0, nil
	}
	account = canonicalStakeAddr(account)
	if account == "" {
		return 0, 0, nil
	}
	norm := stakeledger.Normalize(p)

	ss, err := sharedSpendInTx(tx, account, now)
	if err != nil {
		if !persist {
			return 0, 0, nil
		}
		// Fail closed: never pay a boost we cannot meter against the shared
		// budget. The base rate still enqueues; the row is the audit trail.
		log.Printf("cashback: could not read shared spend budget for account %s; paying no ledger boost this order: %v", account, err)
		return 0, 0, nil
	}
	books, err := readBooksInTx(tx, addresses)
	if err != nil {
		return 0, 0, err
	}
	plan := stakeledger.PlanPurchaseDebits(books, ss, xUSD, nimUsd, norm, now)
	if !persist {
		return plan.LunaSum, plan.BpsSum, nil
	}
	for i := range books {
		// Only books that paid are written back. A rolled-over book whose
		// buyer paid nothing this order keeps its stale boundary keys until
		// its next real write — the lazy rollover makes that a no-op.
		if plan.PaidNIM[i] <= 0 {
			continue
		}
		raw, mErr := marshal(books[i])
		if mErr != nil {
			return 0, 0, mErr
		}
		if wErr := tx.Set(stakeLedgerKey(books[i].Address), raw); wErr != nil {
			return 0, 0, wErr
		}
	}
	if plan.SharedSpendChanged {
		if wErr := persistSharedSpendInTx(tx, account, plan.SharedSpend); wErr != nil {
			return 0, 0, wErr
		}
	}
	return plan.LunaSum, plan.BpsSum, nil
}

// previewEarningPurchaseInTx is the READ-ONLY twin of purchaseEarningSetInTx:
// the identical plan, computed on throwaway copies, nothing written. The
// buyer's preview and the fulfillment payout cannot disagree about the caps,
// the bps split, or the luna total.
func previewEarningPurchaseInTx(tx *badger.Txn, account string, addresses []string, xUSD, nimUsd float64, p stakeledger.Params, now time.Time) (boostLuna int64, boostBps int) {
	luna, bps, err := purchaseEarningSet(tx, account, addresses, xUSD, nimUsd, p, now, false)
	if err != nil {
		return 0, 0
	}
	return luna, bps
}

// StakeLedgerParamsForTx resolves the live normalized params. Call it
// BEFORE opening the fulfillment transaction so the txn body reads one
// stable value.
func (s *Store) StakeLedgerParamsForTx() stakeledger.Params {
	return s.stakeLedgerParams()
}

// SweepLedgers applies pending day/month boundaries (incl. the month-end
// haircut) to every ledger. Called at boot and hourly; the lazy rollover in
// every read/write path means a missed sweep only delays a haircut by the
// sweep interval, never applies it twice.
func (s *Store) SweepLedgers(now time.Time) (int, error) {
	p := s.stakeLedgerParams()
	var swept int
	err := s.Update(func(tx *badger.Txn) error {
		return scanAllLedgers(tx, func(l *stakeledger.Ledger) error {
			if !l.Rollover(p, now) {
				return nil
			}
			raw, err := marshal(*l)
			if err != nil {
				return err
			}
			if err := tx.Set(stakeLedgerKey(l.Address), raw); err != nil {
				return err
			}
			swept++
			return nil
		})
	})
	return swept, err
}

func scanAllLedgers(tx *badger.Txn, fn func(*stakeledger.Ledger) error) error {
	prefix := []byte(prefixStakeLedger)
	options := badger.DefaultIteratorOptions
	options.Prefix, options.PrefetchSize = prefix, 32
	it := tx.NewIterator(options)
	defer it.Close()
	for it.Rewind(); it.ValidForPrefix(prefix); it.Next() {
		item := it.Item()
		if string(item.Key()) == prefixStakeLedger+"watermark" {
			continue
		}
		var l stakeledger.Ledger
		if err := item.Value(func(raw []byte) error { return json.Unmarshal(raw, &l) }); err != nil {
			return err
		}
		if err := fn(&l); err != nil {
			return err
		}
	}
	return nil
}

// StakeLedgerRow is the admin table's per-wallet view (raw state; the
// handler converts NIM→USD with the live price).
type StakeLedgerRow struct {
	stakeledger.Ledger
}

// ListStakeLedgers returns every ledger (newest-updated first within the
// prefix scan is not guaranteed — the admin table sorts client-side).
func (s *Store) ListStakeLedgers(limit int) ([]StakeLedgerRow, error) {
	if limit <= 0 || limit > 1000 {
		limit = 500
	}
	var out []StakeLedgerRow
	err := s.View(func(tx *badger.Txn) error {
		return scanAllLedgers(tx, func(l *stakeledger.Ledger) error {
			if len(out) < limit {
				out = append(out, StakeLedgerRow{Ledger: *l})
			}
			return nil
		})
	})
	return out, err
}

// LedgerCount is the operator's "how many wallets are in the programme?"
func (s *Store) LedgerCount() (int, error) {
	var n int
	err := s.View(func(tx *badger.Txn) error {
		return scanAllLedgers(tx, func(*stakeledger.Ledger) error {
			n++
			return nil
		})
	})
	return n, err
}

// ResetStakeLedger is the operator's per-wallet "start over" (fraud /
// chargeback / goodwill). A, D and the month accrual are wiped; the
// observed stake and the day/month spend counters survive.
func (s *Store) ResetStakeLedger(address string) error {
	address = canonicalStakeAddr(address)
	if address == "" {
		return ErrNotFound
	}
	p := s.stakeLedgerParams()
	return s.Update(func(tx *badger.Txn) error {
		var l stakeledger.Ledger
		if err := getJSON(tx, stakeLedgerKey(address), &l); err != nil {
			return err
		}
		l.Reset(p, time.Now().UTC())
		raw, err := marshal(l)
		if err != nil {
			return err
		}
		return tx.Set(stakeLedgerKey(address), raw)
	})
}
