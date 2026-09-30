package db

import (
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
// it). ok is false when the address has no ledger yet.
func (s *Store) ReadStakeLedger(address string) (stakeledger.Ledger, bool, error) {
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
	l.Rollover(s.stakeLedgerParams(), time.Now().UTC())
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
	if address == "" || month == "" || totalFeeLuna < 0 || nimUsd <= 0 {
		return 0, nil
	}
	p := s.stakeLedgerParams()
	var credited float64
	err := s.Update(func(tx *badger.Txn) error {
		var l stakeledger.Ledger
		err := getJSON(tx, stakeLedgerKey(address), &l)
		if err != nil && err != ErrNotFound {
			return err
		}
		if err == ErrNotFound {
			l = *stakeledger.New(address, 0, now)
		}
		previous := int64(0)
		if l.ProfitMonth == month {
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
		l.ProfitMonth = month
		l.ProfitLuna = totalFeeLuna
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

// ApplyLedgerPurchaseInTx is the fulfillment-side half of the ledger: it
// runs INSIDE the open fulfillment transaction so the boost debit and the
// cashback row commit together (or not at all). It resolves
//
//	eligible = min(xUSD, day remaining, month remaining)
//	rate     = min(CB_MAX, A*P/basis)     (the "next $100" rate)
//	cbNIM    = min(A, rate*eligible/P)
//
// and debits the ledger by the exact whole Luna paid. Returns the boost in
// Luna (0 when there is nothing to pay — no ledger, no price, no balance,
// or the caps are spent) plus the rate (bps) that produced it, for the
// audit row. Params are resolved by the caller (one stable value per
// fulfillment, outside the txn body).
func ApplyLedgerPurchaseInTx(tx *badger.Txn, address string, xUSD, nimUsd float64, p stakeledger.Params, now time.Time) (boostLuna int64, boostBps int, err error) {
	if xUSD <= 0 || nimUsd <= 0 {
		return 0, 0, nil
	}
	addr := canonicalStakeAddr(address)
	if addr == "" {
		return 0, 0, nil
	}
	var l stakeledger.Ledger
	if e := getJSON(tx, stakeLedgerKey(addr), &l); e != nil {
		if e == ErrNotFound {
			return 0, 0, nil // no ledger → no boost, base rate stands
		}
		return 0, 0, e
	}
	bps := stakeledger.BoostBps(&l, nimUsd, p)
	if bps <= 0 {
		return 0, 0, nil
	}
	_, cbNIM := l.Purchase(xUSD, nimUsd, p, now) // debits the float from A
	if cbNIM <= 0 {
		return 0, 0, nil
	}
	luna := int64(cbNIM*100_000.0 + 0.5)
	if luna < 1 {
		return 0, 0, nil
	}
	// Purchase debited the float; correct to the exact whole Luna actually
	// paid so the book matches what goes on-chain. Rounding down (luna <
	// cbNIM in Luna terms) leaves the dust in the ledger — under-pay,
	// never over.
	l.A -= (float64(luna) - cbNIM*100_000.0) / 100_000.0
	if l.A < 0 {
		l.A = 0
	}
	raw, err := marshal(l)
	if err != nil {
		return 0, 0, err
	}
	if err := tx.Set(stakeLedgerKey(addr), raw); err != nil {
		return 0, 0, err
	}
	return luna, bps, nil
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
	return scanIndex(tx, []byte(prefixStakeLedger), 0, func(id string) error {
		if id == "watermark" {
			return nil
		}
		var l stakeledger.Ledger
		if err := getJSON(tx, stakeLedgerKey(id), &l); err != nil {
			if err == ErrNotFound {
				return nil
			}
			return err
		}
		return fn(&l)
	})
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
