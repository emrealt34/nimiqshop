package db

// stake_aggregate.go — one buyer, several wallets, ONE cashback rate.
//
// A buyer's stake rarely lives in a single wallet: some keep a hardware wallet
// for the bulk of it, some stake from the phone wallet they used years ago, and
// anyone shopping through Nimiq Pay signs in with a wallet that may hold no
// stake at all. Every wallet they proved ownership of therefore earns for the
// account, and this file is the only place that turns several per-wallet facts
// into the one StakerStake the cashback math consumes.
//
// The aggregation rules, in the order they matter:
//
//   - stakes are SUMMED (StakeLuna), so splitting 300 NIM over three wallets
//     earns exactly what 300 NIM in one wallet earns;
//   - the staker BASE is counted ONCE per account, never once per wallet. It is
//     a flat operator rate (EffectiveBaseBps picks the higher of the staker base
//     and the universal base), so summing it would pay a multi-wallet buyer a
//     multiple of the advertised rate for the same order;
//   - loyalty (LockedDays) is the LONGEST streak among the wallets that are
//     staked, i.e. the day the buyer first started staking with us through any
//     of their wallets. A wallet linked today does not reset a streak that
//     began a year ago, and a stale wallet does not extend one that ended;
//   - a wallet that could not be asked (pool outage) is SKIPPED, never treated
//     as zero. Only a definitive "not staked" answer observes a zero, which is
//     what keeps an outage from erasing loyalty clocks — the same circuit
//     breaker the single-wallet path had, now per wallet.

import (
	"context"
	"time"
)

// aggregateStakeTimeout bounds the whole fan-out rather than each lookup: with
// up to MaxLinkedStakeAddresses+1 wallets a per-wallet timeout could add up to
// a minute on the fulfillment path, which delivers gift cards. One budget for
// all of them means a slow pool costs the same whether the buyer has one
// wallet or six, and whatever was answered still counts.
const aggregateStakeTimeout = 10 * time.Second

// StakeAggregateReport says how the total was reached, which is what lets the
// callers distinguish "definitively not staked" from "could not ask about some
// wallets". Failed counts wallets whose lookup errored; Answered counts the
// ones the resolver gave a definitive answer for.
type StakeAggregateReport struct {
	Answered int
	Failed   int
	// PerWallet keeps the individual answers in the order they were asked, so
	// a UI or an admin view can show the breakdown instead of only the total.
	PerWallet []WalletStake
}

// WalletStake is one earning wallet's own standing inside an aggregate.
type WalletStake struct {
	Address    string
	StakeLuna  int64
	Staked     bool
	LockedDays int
	// Failed is true when the resolver could not be asked about this wallet,
	// in which case the other fields describe the last durable observation
	// (or nothing at all), not a fresh answer.
	Failed bool
}

// AggregateStakerStake resolves every earning wallet and folds the answers into
// one StakerStake using the rules at the top of this file. It also advances each
// wallet's own loyalty and ledger clocks, because those books are keyed by
// address and a buyer who stakes from three wallets has three of them.
//
// It never invents stake: a wallet contributes only what the resolver reported
// for it, or what a previous positive observation durably recorded when the
// resolver could not be reached.
func (s *Store) AggregateStakerStake(ctx context.Context, addresses []string) (StakerStake, StakeAggregateReport) {
	return s.aggregateStakerStake(ctx, addresses, true)
}

// CachedStakeAggregate sums the last durable POSITIVE local observations for a
// set of wallets. It is the outage circuit breaker: when the pool could not be
// asked, a buyer who was verifiably staked keeps being treated as staked
// instead of silently dropping to the base rate.
func (s *Store) CachedStakeAggregate(addresses []string, now time.Time) StakerStake {
	var total StakerStake
	for _, addr := range addresses {
		cached := s.CachedStakerStake(addr, now)
		if !cached.Staked || cached.StakeLuna <= 0 {
			continue
		}
		total.StakeLuna += cached.StakeLuna
		total.Staked = true
		if cached.LockedDays > total.LockedDays {
			total.LockedDays = cached.LockedDays
		}
		if total.BaseBps == 0 {
			total.BaseBps = cached.BaseBps
		}
	}
	return total
}

// aggregateStakerStake is the implementation; observeClocks lets a read-only
// caller (a display refresh during an outage, a test) resolve the same total
// without touching the loyalty or ledger books.
func (s *Store) aggregateStakerStake(ctx context.Context, addresses []string, observeClocks bool) (StakerStake, StakeAggregateReport) {
	var total StakerStake
	report := StakeAggregateReport{PerWallet: make([]WalletStake, 0, len(addresses))}
	if len(addresses) == 0 || !s.HasStakerLookup() {
		return total, report
	}

	now := time.Now().UTC()
	seen := make(map[string]bool, len(addresses))
	for _, addr := range addresses {
		canonical := canonicalStakeAddr(addr)
		if canonical == "" || seen[canonical] {
			continue
		}
		seen[canonical] = true

		row := WalletStake{Address: addr}
		st, _, err := s.lookupStake(ctx, addr)
		switch {
		case err != nil:
			// Could not ask. Leave this wallet's clocks exactly as they are:
			// observing a zero here would erase a loyalty streak over a
			// network failure. The caller decides what an unanswered wallet
			// means for the total (see CachedStakeAggregate).
			row.Failed = true
			report.Failed++
		case !st.Staked || st.StakeLuna <= 0:
			// A DEFINITIVE "not staked" for this wallet: record it. The v1
			// watch clears its clock only after the zero state has been
			// continuous for the grace window, and the ledger applies r = 0
			// → A = 0, so a full withdrawal is a full reset for that wallet
			// alone — the others keep earning.
			report.Answered++
			if observeClocks {
				_, _ = s.observeStake(addr, 0, now)
				_ = s.ObserveStakeLedger(addr, 0, now)
			}
		default:
			report.Answered++
			row.Staked = true
			row.StakeLuna = st.StakeLuna
			if observeClocks {
				if watch, werr := s.observeStake(addr, st.StakeLuna, now); werr == nil {
					row.LockedDays = watch.stakeLockedDays(now)
				}
				// The single-ledger book keeps its own stake-weighted clock
				// per wallet: an increase dilutes that wallet's loyalty age,
				// a decrease shrinks its book pro rata.
				_ = s.ObserveStakeLedger(addr, st.StakeLuna, now)
			}
			// Sum the stake, keep the longest streak, and take the base rate
			// from the first wallet that reported one — the base is a flat
			// operator number, so "first" and "any" are the same value.
			total.StakeLuna += st.StakeLuna
			total.Staked = true
			if row.LockedDays > total.LockedDays {
				total.LockedDays = row.LockedDays
			}
			if total.BaseBps == 0 {
				total.BaseBps = st.BaseBps
			}
		}
		report.PerWallet = append(report.PerWallet, row)
	}
	return total, report
}

// AggregateStakerStakeReadOnly resolves the same total without advancing any
// clock. Display paths use it when they must not mutate state (an admin view,
// a page render during a pool outage).
func (s *Store) AggregateStakerStakeReadonly(ctx context.Context, addresses []string) (StakerStake, StakeAggregateReport) {
	return s.aggregateStakerStake(ctx, addresses, false)
}
