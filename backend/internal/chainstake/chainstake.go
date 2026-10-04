// Package chainstake answers ONE question from the chain itself: "does this
// address currently delegate a positive stake to OUR validator?"
//
// Why this exists (2026-10-04): the shop's staker cashback used to depend
// entirely on the pool API (POOL_API_URL). That is fine while the pool's own
// index is healthy, but the pool is a separate service with its own database:
// when its staker table is empty or simply behind (observed live: the pool
// reported num_stakers 0 while the chain showed 10 delegators and 124 279 NIM
// delegated to the very validator the pool runs), every real staker looked
// unstaked and the shop paid and displayed 0 cashback. The buyer had staked,
// the chain knew it, and the shop said "no stake".
//
// So the pool stays the source of truth for everything the pool OWNS — the
// profit feed, loyalty days, pool fee — while the raw DELEGATION FACT is now
// verified against the chain as a fallback. Verification is:
//
//	chain.GetStaker(address).Delegation == our validator && Balance > 0
//
// Both inputs are public: no wallet, no signature, no new dependency (the RPC
// client is the one already used for payment verification).
//
// A short TTL cache keeps this off the critical path: the answer is asked at
// most once per address per TTL, and a negative answer is cached for a shorter
// time so a fresh delegation (which submits on-chain within seconds) shows up
// quickly. Errors are never cached: an RPC outage must not be mistaken for
// "not staked".
package chainstake

import (
	"context"
	"strings"
	"sync"
	"time"

	"nimiqshop/internal/nimiq"
)

// Defaults mirror the pool client's shape so the two fallbacks feel alike.
const (
	// DefaultTTL is how long a POSITIVE answer is trusted.
	DefaultTTL = 60 * time.Second
	// DefaultNegativeTTL is deliberately shorter: a buyer who just staked
	// must see it soon. 20 s is short enough to feel immediate and long
	// enough to absorb a burst of page views / recheck ticks.
	DefaultNegativeTTL = 20 * time.Second
	// DefaultTimeout bounds one RPC call.
	DefaultTimeout = 5 * time.Second
)

// Staker is one verified position.
type Staker struct {
	Staked    bool
	StakeLuna int64
	// Delegation is the validator the chain reports (canonical, no spaces).
	Delegation string
	CheckedAt  time.Time
	FromCache  bool
}

type entry struct {
	staker Staker
	expiry time.Time
}

// Verifier is a caching, chain-backed staking lookup. The zero value is ready
// but useless (no client, no validator): Verify then reports "not staked" and
// which prerequisite is missing through Ready().
type Verifier struct {
	rpc       *nimiq.Client
	validator string // canonical (no spaces, upper case)

	ttl         time.Duration
	negativeTTL time.Duration

	mu    sync.Mutex
	cache map[string]entry
}

// New builds a verifier for the operator's validator address. A nil RPC client
// or an empty validator disables it (Ready() false, Verify always not-staked);
// callers then keep the previous pool-only behaviour.
func New(rpc *nimiq.Client, validator string, ttl, negativeTTL time.Duration) *Verifier {
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	if negativeTTL <= 0 {
		negativeTTL = DefaultNegativeTTL
	}
	return &Verifier{
		rpc:         rpc,
		validator:   canonical(validator),
		ttl:         ttl,
		negativeTTL: negativeTTL,
		cache:       make(map[string]entry),
	}
}

// Ready reports whether the verifier can answer at all.
func (v *Verifier) Ready() bool {
	return v != nil && v.rpc != nil && v.validator != ""
}

// Validator is the canonical validator address this verifier accepts.
func (v *Verifier) Validator() string {
	if v == nil {
		return ""
	}
	return v.validator
}

// Verify reports whether address delegates a positive stake to our validator,
// and how much. A malformed address is a definitive "no" without touching the
// network: it can never be a staker here, so callers must not treat it as an
// outage (that is what kept retrying an address the pool rejects with 400).
//
// An RPC failure is returned as an error so callers keep their existing
// outage semantics (do not downgrade a known staker, do not reset a clock).
func (v *Verifier) Verify(ctx context.Context, address string) (Staker, error) {
	if !v.Ready() {
		return Staker{}, nil
	}
	addr := canonical(address)
	if addr == "" || nimiq.ValidateAddress(addr) != nil {
		// Not a chain address at all — no lookup can ever succeed.
		return Staker{StakeLuna: 0, Staked: false, CheckedAt: time.Now().UTC()}, nil
	}

	now := time.Now().UTC()
	v.mu.Lock()
	if e, ok := v.cache[addr]; ok && now.Before(e.expiry) {
		v.mu.Unlock()
		e.staker.FromCache = true
		return e.staker, nil
	}
	v.mu.Unlock()

	cctx, cancel := context.WithTimeout(ctx, DefaultTimeout)
	defer cancel()
	info, err := v.rpc.GetStaker(cctx, addr)
	if err != nil {
		if isNoStaker(err) {
			// The RPC has no staking record for this address: definitive.
			st := Staker{CheckedAt: now}
			v.store(addr, st, now)
			return st, nil
		}
		return Staker{}, err
	}

	st := Staker{
		Staked:     info.HasStake() && canonical(info.Delegation) == v.validator,
		StakeLuna:  info.Balance,
		Delegation: canonical(info.Delegation),
		CheckedAt:  now,
	}
	if !st.Staked {
		st.StakeLuna = 0
	}
	v.store(addr, st, now)
	return st, nil
}

// Forget drops one address' cached answer (used right after a stake tx).
func (v *Verifier) Forget(address string) {
	if v == nil {
		return
	}
	addr := canonical(address)
	v.mu.Lock()
	delete(v.cache, addr)
	v.mu.Unlock()
}

func (v *Verifier) store(addr string, st Staker, now time.Time) {
	ttl := v.ttl
	if !st.Staked {
		ttl = v.negativeTTL
	}
	v.mu.Lock()
	if v.cache == nil {
		v.cache = make(map[string]entry)
	}
	v.cache[addr] = entry{staker: st, expiry: now.Add(ttl)}
	if len(v.cache) > 4096 {
		for k, e := range v.cache {
			if !now.Before(e.expiry) {
				delete(v.cache, k)
			}
		}
	}
	v.mu.Unlock()
}

// isNoStaker recognizes the RPC's "this address has no staking position"
// answer, which arrives as a JSON-RPC error. Matching on the text keeps the
// client dependency-free; anything else stays an error (a real outage).
func isNoStaker(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, needle := range []string{
		"no staker",
		"not a staker",
		"no account",
		"account not found",
		"not found",
		"does not exist",
	} {
		if strings.Contains(msg, needle) {
			return true
		}
	}
	return false
}

func canonical(addr string) string {
	return strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(addr), " ", ""))
}
