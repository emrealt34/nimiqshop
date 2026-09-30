package db

import (
	"strings"
	"time"

	"github.com/dgraph-io/badger/v4"

	adminmodel "nimiqshop/internal/admin"
)

// canonicalStakeAddr mirrors poolstake.CanonicalAddress so one buyer is one
// key regardless of spacing/casing ("NQ07 0000…" vs "NQ070000…").
func canonicalStakeAddr(addr string) string {
	return strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(addr), " ", ""))
}

// StakeWatch tracks, per canonical Nimiq address, the first moment the shop
// observed a positive stake in the operator's pool. That first-seen timestamp
// is the anchor for locked-time loyalty bonuses: "locked for N days" means the
// stake has stayed positive for N days since FirstSeenAt.
//
// The pool itself does not expose a "staked since" field, so the shop derives
// it from its own observations. The clock resets when the stake drops to zero
// (the buyer fully unstaked), because a withdrawn stake was not locked.
type StakeWatch struct {
	Address     string    `json:"address"`
	FirstSeenAt time.Time `json:"first_seen_at"`
	LastSeenAt  time.Time `json:"last_seen_at"`
	StakeLuna   int64     `json:"stake_luna"`
}

// stakeZeroGrace is how long a "not observed" state must persist before the
// shop treats it as a real, definitive unstake (and resets the loyalty clock).
// The pool does not index an on-chain stake instantly; between the buyer
// sending the delegation and the pool reporting it there is a latency window.
// During that window a lookup legitimately returns 0 even though the buyer IS
// staked. Resetting the clock on that transient 0 would silently wipe the
// buyer's locked-time loyalty. So we keep FirstSeenAt for this grace period and
// only clear it once the zero state has been continuous for longer than the
// pool's real unstake signal reasonably takes.
const stakeZeroGrace = 48 * time.Hour

// observeStake records a pool-stake observation for address and returns the
// resulting watch. It runs OUTSIDE any open fulfillment transaction (it is a
// read-modify-write of a separate key), so callers prefetch it alongside the
// pool lookup.
//
// A stake of 0 (pool says "not staked") does NOT blindly clear the clock. A
// buyer who is provably staked — per the local pending-stake record or the
// watch itself — can look staked=false for a short window while the pool
// indexes their fresh delegation. So:
//   - a 0 that follows a previously-positive watch keeps FirstSeenAt (the
//     loyalty clock is not lost), and only resets it once the zero state has
//     been continuous for stakeZeroGrace. A short blip is ignored.
//   - a 0 with no prior positive history is still recorded as zero (no clock
//     to lose).
//   - a positive observation always advances LastSeenAt / a fresh FirstSeenAt.
func (s *Store) observeStake(address string, stakeLuna int64, at time.Time) (StakeWatch, error) {
	address = canonicalStakeAddr(address)
	key := stakeWatchKey(address)
	var watch StakeWatch
	err := s.Update(func(txn *badger.Txn) error {
		existing, err := readStakeWatch(txn, address)
		if err != nil && err != ErrNotFound {
			return err
		}
		if stakeLuna <= 0 {
			// Pool reports "not staked". Preserve FirstSeenAt (the loyalty
			// clock) for a transient blip; only a zero state that has lasted
			// the whole grace window is a real settled unstake.
			if existing.FirstSeenAt.IsZero() {
				// Never had a clock to lose.
				watch = StakeWatch{Address: address, LastSeenAt: at, StakeLuna: 0}
				if existing.StakeLuna <= 0 {
					return nil // nothing new to write
				}
			} else if existing.StakeLuna > 0 {
				// Was positive, now 0 — likely the pool's indexing lag.
				watch = StakeWatch{Address: address, FirstSeenAt: existing.FirstSeenAt, LastSeenAt: at, StakeLuna: 0}
			} else {
				// Already zero. Keep FirstSeenAt unless the zero state has been
				// continuous for the whole grace window (real unstake).
				if at.Sub(existing.LastSeenAt) >= stakeZeroGrace {
					watch = StakeWatch{Address: address, LastSeenAt: at, StakeLuna: 0}
				} else {
					watch = StakeWatch{Address: address, FirstSeenAt: existing.FirstSeenAt, LastSeenAt: at, StakeLuna: 0}
				}
			}
			raw, err := marshal(watch)
			if err != nil {
				return err
			}
			return txn.Set(key, raw)
		}
		first := existing.FirstSeenAt
		// Positive observation. Only reset the clock when the zero state has
		// been continuous for the whole grace window (a real, settled unstake +
		// re-stake). A short blip keeps the original FirstSeenAt.
		if first.IsZero() {
			first = at
		} else if existing.StakeLuna <= 0 && at.Sub(existing.LastSeenAt) >= stakeZeroGrace {
			first = at
		}
		watch = StakeWatch{Address: address, FirstSeenAt: first, LastSeenAt: at, StakeLuna: stakeLuna}
		raw, err := marshal(watch)
		if err != nil {
			return err
		}
		return txn.Set(key, raw)
	})
	return watch, err
}

// ObserveStake is the exported wrapper used by handlers right after a pool
// lookup so the loyalty clock advances even when the buyer never orders.
func (s *Store) ObserveStake(address string, stakeLuna int64, at time.Time) (StakeWatch, error) {
	return s.observeStake(address, stakeLuna, at)
}

// CachedStakerStake returns the last durable positive local POOL observation.
// It is a circuit breaker for pool outages and process restarts: it never
// creates a new entitlement (only a previously stored positive pool
// observation is eligible), but it prevents a known staker from being
// displayed or paid as a non-staker while the upstream API is unavailable.
func (s *Store) CachedStakerStake(address string, now time.Time) StakerStake {
	address = canonicalStakeAddr(address)
	watch, err := s.GetStakeWatch(address)
	if err != nil || watch.StakeLuna <= 0 {
		return StakerStake{}
	}
	settings := adminmodel.Settings{}
	_ = s.View(func(tx *badger.Txn) error { return getJSON(tx, []byte(adminSettingsKey), &settings) })
	return StakerStake{StakeLuna: watch.StakeLuna, Staked: true, LockedDays: watch.LockedDays(now), BaseBps: settings.EffectiveStakerCashbackBps()}
}

// GetStakeWatch returns the stored watch for address (ErrNotFound when never
// observed or cleared).
func (s *Store) GetStakeWatch(address string) (StakeWatch, error) {
	address = canonicalStakeAddr(address)
	var watch StakeWatch
	err := s.View(func(txn *badger.Txn) error {
		w, err := readStakeWatch(txn, address)
		if err != nil {
			return err
		}
		watch = w
		return nil
	})
	return watch, err
}

// stakeLockedDays returns whole days the stake has been continuously positive,
// or 0 when there is no watch / it was never positive.
func (w StakeWatch) stakeLockedDays(at time.Time) int {
	if w.FirstSeenAt.IsZero() || w.StakeLuna <= 0 {
		return 0
	}
	d := int(at.Sub(w.FirstSeenAt) / (24 * time.Hour))
	if d < 0 {
		d = 0
	}
	return d
}

// LockedDays is the exported loyalty-clock reading: whole days this stake has
// stayed positive in the operator's pool as of at. Handlers use it to resolve
// the SAME effective rate the payout path will resolve, so the rate shown on
// the product page is the rate that gets paid at fulfillment.
func (w StakeWatch) LockedDays(at time.Time) int { return w.stakeLockedDays(at) }

// readStakeWatch reads the stored watch inside the caller's transaction (so it
// can run inside the fulfillment write transaction without a second round
// trip). Returns ErrNotFound when the address was never observed or unstaked.
func readStakeWatch(txn *badger.Txn, address string) (StakeWatch, error) {
	var w StakeWatch
	err := getJSON(txn, stakeWatchKey(address), &w)
	return w, err
}
