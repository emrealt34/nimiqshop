// Package admin contains persistence-safe models for the separate operations
// console identity domain. These records deliberately never cross the public
// user/JWT authentication boundary.
package admin

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
	"unicode"

	"nimiqshop/internal/stakeledger"
)

// User is an operations-console identity. PasswordHash and TOTPSecret are
// intentionally omitted from every HTTP response by admin handlers.
type User struct {
	ID           string    `json:"id"`
	Username     string    `json:"username"`
	PasswordHash string    `json:"password_hash"`
	TOTPSecret   string    `json:"totp_secret"`
	CreatedAt    time.Time `json:"created_at"`
	Disabled     bool      `json:"disabled"`
}

// Session stores only an HMAC of the random cookie credential. The cookie
// credential itself is never persisted or logged.
type Session struct {
	ID        string     `json:"id"`
	AdminID   string     `json:"admin_id"`
	TokenHash string     `json:"token_hash"`
	IP        string     `json:"ip"`
	UserAgent string     `json:"user_agent"`
	CreatedAt time.Time  `json:"created_at"`
	ExpiresAt time.Time  `json:"expires_at"`
	RevokedAt *time.Time `json:"revoked_at,omitempty"`
}

// AuditEvent is append-only. Detail must be an operator-safe description and
// must never contain a password, TOTP code, session cookie, or API secret.
type AuditEvent struct {
	ID        string    `json:"id"`
	AdminID   string    `json:"admin_id,omitempty"`
	Action    string    `json:"action"`
	IP        string    `json:"ip"`
	UserAgent string    `json:"user_agent"`
	Detail    string    `json:"detail,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// LoginFailure is a private lockout record keyed by canonical username.
// It is never returned to an unauthenticated client, which prevents account
// enumeration and lockout-state disclosure.
type LoginFailure struct {
	Username      string     `json:"username"`
	FailedCount   int        `json:"failed_count"`
	FirstFailedAt time.Time  `json:"first_failed_at"`
	LastFailedAt  time.Time  `json:"last_failed_at"`
	LockedUntil   *time.Time `json:"locked_until,omitempty"`
}

// StakerTier is one rung of the "delegate NIM to our validator, earn more
// cashback" ladder. A buyer whose active stake in the operator's pool is at
// least MinStakeLuna earns CashbackBps instead of the base rate.
type StakerTier struct {
	MinStakeLuna int64 `json:"min_stake_luna"`
	CashbackBps  int   `json:"cashback_bps"`
}

// MaxStakerTiers caps the ladder so a typo cannot create an unbounded table.
const MaxStakerTiers = 8

// StakerLoyaltyLock is one locked-time bonus on top of the pool-staker ladder.
// A buyer whose stake has stayed in the operator's pool for at least LockDays
// earns BonusBps extra on every order, on top of whatever rung they reach.
type StakerLoyaltyLock struct {
	LockDays int `json:"lock_days"`
	BonusBps int `json:"bonus_bps"`
}

// MaxStakerLoyalty caps the lock list the same way MaxStakerTiers caps the ladder.
const MaxStakerLoyalty = 8

// CashbackCodeRule is one admin-managed promo/redeem cashback rule. These
// live in the same settings record as the base/staker rates so the operator
// can adjust them at runtime without editing process env or redeploying.
//
// Code is stored normalized (upper-case, trimmed). CashbackBps is exclusive:
// when a buyer applies the code, this rate is the whole cashback rule for that
// order and no pool-staker or loyalty bonus may stack on top.
type CashbackCodeRule struct {
	Code        string `json:"code"`
	CashbackBps int    `json:"cashback_bps"`
	// MaxOrderUSD is the maximum final cart/product USD value accepted by
	// this code. Zero means no price cap. It never changes the payout base:
	// an over-cap cart is rejected rather than partially discounted.
	MaxOrderUSD float64 `json:"max_order_usd,omitempty"`
	// OnlyCategories / OnlyKinds / OnlyCountries restrict the code to a
	// subset of products. Empty means "no restriction". Matching is done
	// against the catalog.GateQuote metadata (category/kind/country). An
	// item outside every filter is refused at quote time with a toast-
	// friendly message listing the valid categories/kinds/countries.
	OnlyCategories []string `json:"only_categories,omitempty"`
	OnlyKinds      []string `json:"only_kinds,omitempty"`
	OnlyCountries  []string `json:"only_countries,omitempty"`

	/* --- Redemption lifecycle (see internal/db/cashback_codes.go) ---
	 *
	 * A cashback code is the ONLY buyer-supplied input that increases what
	 * the shop pays out, so it is the one place a promotion can be drained.
	 * These four fields give the operator a real off switch. All of them
	 * default to "unlimited" (zero / empty) so an existing configuration
	 * keeps behaving exactly as it did before they existed.
	 *
	 * MaxUsesTotal and MaxUsesPerUser count REDEMPTIONS, not quotes: an
	 * in-flight checkout occupies a slot (so a burst cannot overshoot the
	 * cap) and an abandoned one gives the slot back when the settlement
	 * tracker expires it.
	 */

	// MaxUsesTotal is the total number of redemptions this code may ever
	// have. 0 = unlimited.
	MaxUsesTotal int `json:"max_uses_total,omitempty"`
	// MaxUsesPerUser is how many redemptions ONE account may have. 0 =
	// unlimited. Note that a Nimiq identity is free — Hub login is a
	// signature over a fresh keypair — so this alone does not stop a
	// determined attacker; MaxUsesTotal and MaxTotalCashbackUSD are what
	// bound the shop's total exposure.
	MaxUsesPerUser int `json:"max_uses_per_user,omitempty"`
	// MaxTotalCashbackUSD is the ceiling on everything this code may pay
	// out, in USD. 0 = unlimited. This is the field that turns "someone is
	// farming our 20% code" from an open-ended loss into a bounded one.
	MaxTotalCashbackUSD float64 `json:"max_total_cashback_usd,omitempty"`
	// ExpiresAt ends the promotion. Zero = no expiry.
	ExpiresAt time.Time `json:"expires_at,omitempty"`
}

// MaxCashbackCodeRules caps the admin-managed promo code table.
const MaxCashbackCodeRules = 64

// StakeCashbackParams is the single-ledger staker programme (Tek Defter).
// It is the engine's parameter struct itself (an alias): the admin console
// edits ONE parameter set — no tier table, no loyalty-lock list — and what
// the console saves is exactly what internal/stakeledger runs, with no
// translation layer that could drift.
type StakeCashbackParams = stakeledger.Params

// Settings are global operations controls persisted independently of process
// configuration so a margin or cashback update affects newly fulfilled
// orders immediately. CashbackBps is a pointer so a missing field (legacy
// rows) still means "use the published default" rather than guessing.
type Settings struct {
	GlobalMarginBps int  `json:"global_margin_bps"`
	CashbackBps     *int `json:"cashback_bps,omitempty"`
	// StakerCashbackTiers / StakerLoyalty are LEGACY v1 fields. v2 (the
	// single-ledger programme) does not use them: they are still decoded so
	// old settings rows load, but no editor exposes them and the payout
	// resolver ignores them.
	StakerCashbackTiers []StakerTier        `json:"staker_cashback_tiers,omitempty"`
	StakerLoyalty       []StakerLoyaltyLock `json:"staker_loyalty,omitempty"`
	// StakeCashback is the v2 single-ledger parameter set. Nil (legacy rows)
	// means "run with the published defaults" — see EffectiveStakeCashback.
	StakeCashback *StakeCashbackParams `json:"stake_cashback,omitempty"`
	// StakerBaseBps is the operator's STAKER base rate — what a buyer
	// with any positive stake in the pool earns, before the ledger boost.
	// Nil means "use DefaultStakerCashbackBps (1%)". Admin-editable at
	// runtime. Independent of the pool.
	StakerBaseBps *int `json:"staker_base_bps,omitempty"`
	// CashbackCodeRules is nil for legacy/env-managed deployments, non-nil for
	// admin-managed promo codes. A non-nil empty slice means "promo codes are
	// intentionally disabled in admin settings".
	CashbackCodeRules *[]CashbackCodeRule `json:"cashback_code_rules,omitempty"`
	UpdatedAt         time.Time           `json:"updated_at"`
	UpdatedBy         string              `json:"updated_by"`
}

// DefaultStakerTiers is the published fallback when the operator has not
// written a custom programme yet: any positive stake in the operator's pool
// earns 1%. MinStakeLuna=0 is a special "any positive stake" rung.
var DefaultStakerTiers = []StakerTier{
	{MinStakeLuna: 0, CashbackBps: 100},
}

// DefaultStakerLoyalty is intentionally empty: by default the programme is
// just "stake with our pool, earn 1%" with no time-based boosts.
var DefaultStakerLoyalty = []StakerLoyaltyLock{}

// DefaultCashbackBps is the operator's UNIVERSAL base rate — what a buyer
// with NO stake in the pool earns. It is 0 by default: there is no base
// without staking. The staker base (1% for any positive stake) is the
// operator's number, admin-editable at runtime via StakerBaseBps. The pool
// is not consulted for this rate. The single-ledger boost (up to +10%)
// stacks on top of whichever base applies.
const DefaultCashbackBps = 0

// DefaultStakerCashbackBps is the staker base rate the shop advertises and
// pays when the operator has not written a custom one: 1% for any positive
// stake in the operator's pool. Admin can change it at runtime.
const DefaultStakerCashbackBps = 100

// MaxCashbackBps caps the admin slider so a typo cannot drain the shop wallet.
const MaxCashbackBps = 2000

// EffectiveCashbackBps returns the live base cashback rate in basis points:
// the flat rate every buyer earns (1% by default), before any staker boost.
func (s Settings) EffectiveCashbackBps() int {
	if s.CashbackBps == nil {
		return DefaultCashbackBps
	}
	v := *s.CashbackBps
	if v < 0 {
		return 0
	}
	if v > MaxCashbackBps {
		return MaxCashbackBps
	}
	return v
}

// EffectiveStakeCashback returns the live, normalized single-ledger
// programme parameters: the admin set when present, otherwise the published
// defaults. Normalize clamps every field into the safe envelope, so the
// payout path always reads sane values even after a fat-fingered edit.
func (s Settings) EffectiveStakeCashback() stakeledger.Params {
	if s.StakeCashback == nil {
		return stakeledger.Normalize(stakeledger.Defaults)
	}
	return stakeledger.Normalize(*s.StakeCashback)
}

// EffectiveStakerCashbackBps returns the live staker base rate in basis
// points: the admin-set value when present, otherwise DefaultStakerCashbackBps
// (1%). Clamped to [0, MaxCashbackBps] so a fat-fingered edit cannot drain
// the shop wallet.
func (s Settings) EffectiveStakerCashbackBps() int {
	if s.StakerBaseBps == nil {
		return DefaultStakerCashbackBps
	}
	v := *s.StakerBaseBps
	if v < 0 {
		return 0
	}
	if v > MaxCashbackBps {
		return MaxCashbackBps
	}
	return v
}

/* ---------------- Pool-staker cashback ladder ----------------
 *
 * Buyers who delegate NIM to the operator's own validator earn a higher
 * cashback rate. The ladder lives in Settings (admin-editable at runtime) so
 * the tiers can be re-tuned without a deploy.
 *
 * Resolution is deliberately boring: the highest rung whose minimum the
 * buyer's ACTIVE stake meets wins, and it can only ever improve on the base
 * rate. A staker never earns less than a non-staker.
 */

// NormalizeStakerTiers makes an admin-supplied ladder safe to persist and to
// display: it drops rungs with a negative minimum or a zero/negative rate,
// clamps rates to MaxCashbackBps, collapses duplicate minimums (highest rate
// wins) and sorts ascending by stake. A minimum of 0 is allowed and means
// "any positive stake in our pool".
func NormalizeStakerTiers(in []StakerTier) []StakerTier {
	byMin := make(map[int64]int, len(in))
	for _, t := range in {
		if t.MinStakeLuna < 0 || t.CashbackBps <= 0 {
			continue
		}
		bps := t.CashbackBps
		if bps > MaxCashbackBps {
			bps = MaxCashbackBps
		}
		if existing, ok := byMin[t.MinStakeLuna]; !ok || bps > existing {
			byMin[t.MinStakeLuna] = bps
		}
	}
	out := make([]StakerTier, 0, len(byMin))
	for min, bps := range byMin {
		out = append(out, StakerTier{MinStakeLuna: min, CashbackBps: bps})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].MinStakeLuna < out[j].MinStakeLuna })
	if len(out) > MaxStakerTiers {
		// Keep the CHEAPEST rungs: they are the ones every buyer can reach,
		// so a truncation never silently removes a rate someone already earns.
		out = out[:MaxStakerTiers]
	}
	return out
}

// StakerCashbackBps returns the rate earned by an active stake of stakeLuna
// and the rung that granted it. ok is false when the ladder is empty or the
// stake reaches no rung.
func (s Settings) StakerCashbackBps(stakeLuna int64) (bps int, tier StakerTier, ok bool) {
	return stakerBpsOn(s.StakerCashbackTiers, stakeLuna)
}

// stakerBpsOn resolves the richest rung of an explicit ladder that the stake
// meets. Extracted so the effective-ladder path and the raw-configured path
// share one resolver. A 0-minimum rung means "any positive stake"; a buyer
// with no active stake still earns nothing because stakeLuna<=0 exits early.
func stakerBpsOn(tiers []StakerTier, stakeLuna int64) (bps int, tier StakerTier, ok bool) {
	if stakeLuna <= 0 {
		return 0, StakerTier{}, false
	}
	for _, t := range tiers {
		if t.MinStakeLuna < 0 || t.CashbackBps <= 0 {
			continue
		}
		if t.MinStakeLuna > stakeLuna {
			continue // ladder is ascending, so nothing above matches either
		}
		if t.CashbackBps > bps {
			bps, tier, ok = t.CashbackBps, t, true
		}
	}
	if bps > MaxCashbackBps {
		bps = MaxCashbackBps
	}
	return bps, tier, ok
}

// CashbackBpsForBuyer resolves the RAW configured ladder only: no published
// defaults, no locked-time loyalty bonus. It exists for configuration tests
// that assert how an explicit ladder behaves.
//
// DO NOT call this from a request path. Anything that tells a buyer what they
// will earn, or decides what they are paid, must call
// CashbackBpsForBuyerEffective — that is the one resolver the storefront and
// the payout path share, and it is the only one that can keep the advertised
// rate and the paid rate identical.
func (s Settings) CashbackBpsForBuyer(stakeLuna int64, staked bool) (bps int, tier StakerTier, boosted bool) {
	base := s.EffectiveCashbackBps()
	if !staked {
		return base, StakerTier{}, false
	}
	stakerBps, rung, ok := s.StakerCashbackBps(stakeLuna)
	if !ok || stakerBps <= base {
		return base, StakerTier{}, false
	}
	return stakerBps, rung, true
}

// normalizeCashbackCode canonicalises an operator-entered promo code.
func normalizeCashbackCode(raw string) string {
	return strings.ToUpper(strings.TrimSpace(raw))
}

// validateCashbackCode keeps promo codes easy to type on mobile: A-Z, 0-9,
// dash and underscore, 2-40 chars once normalised.
func validateCashbackCode(code string) error {
	code = normalizeCashbackCode(code)
	if code == "" {
		return fmt.Errorf("cashback code is required")
	}
	if len(code) < 2 || len(code) > 40 {
		return fmt.Errorf("cashback codes must be 2-40 characters")
	}
	for _, r := range code {
		if unicode.IsUpper(r) || unicode.IsDigit(r) || r == '-' || r == '_' {
			continue
		}
		return fmt.Errorf("cashback codes may only contain letters, digits, dashes and underscores")
	}
	return nil
}

// NormalizeCashbackCodeRules validates, normalizes and sorts the admin-managed
// promo-code table. Duplicates after normalization are rejected so the active
// rate for a code is never ambiguous.
func NormalizeCashbackCodeRules(in []CashbackCodeRule) ([]CashbackCodeRule, error) {
	if len(in) > MaxCashbackCodeRules {
		return nil, fmt.Errorf("at most %d cashback code rules are allowed", MaxCashbackCodeRules)
	}
	out := make([]CashbackCodeRule, 0, len(in))
	seen := make(map[string]struct{}, len(in))
	for _, r := range in {
		code := normalizeCashbackCode(r.Code)
		if err := validateCashbackCode(code); err != nil {
			return nil, fmt.Errorf("%q: %w", r.Code, err)
		}
		bps := r.CashbackBps
		if bps <= 0 || bps > MaxCashbackBps {
			return nil, fmt.Errorf("%s cashback must be between 1 and %d basis points", code, MaxCashbackBps)
		}
		if math.IsNaN(r.MaxOrderUSD) || math.IsInf(r.MaxOrderUSD, 0) || r.MaxOrderUSD < 0 {
			return nil, fmt.Errorf("%s maximum order USD must be zero or a positive amount", code)
		}
		if r.MaxOrderUSD > 1_000_000 {
			return nil, fmt.Errorf("%s maximum order USD is too large", code)
		}
		if _, dup := seen[code]; dup {
			return nil, fmt.Errorf("duplicate cashback code %s", code)
		}
		seen[code] = struct{}{}
		// Redemption-lifecycle limits. Negative or absurd values are refused
		// rather than clamped: a limit the operator did not mean is worse
		// than no limit, because it silently changes what a promotion pays.
		if r.MaxUsesTotal < 0 || r.MaxUsesTotal > 100_000_000 {
			return nil, fmt.Errorf("%s max_uses_total must be 0 (unlimited) or at most 100000000", code)
		}
		if r.MaxUsesPerUser < 0 || r.MaxUsesPerUser > 1_000_000 {
			return nil, fmt.Errorf("%s max_uses_per_user must be 0 (unlimited) or at most 1000000", code)
		}
		if math.IsNaN(r.MaxTotalCashbackUSD) || math.IsInf(r.MaxTotalCashbackUSD, 0) || r.MaxTotalCashbackUSD < 0 || r.MaxTotalCashbackUSD > 100_000_000 {
			return nil, fmt.Errorf("%s max_total_cashback_usd must be 0 (unlimited) or a finite amount up to 100000000", code)
		}
		if !r.ExpiresAt.IsZero() && r.ExpiresAt.Year() < 2000 {
			return nil, fmt.Errorf("%s expires_at is not a plausible date", code)
		}
		out = append(out, CashbackCodeRule{
			Code: code, CashbackBps: bps, MaxOrderUSD: r.MaxOrderUSD,
			// Preserved verbatim: the previous version of this function
			// rebuilt the rule from three fields and silently dropped
			// everything else, so any future field would have vanished on
			// the first admin save. Copy the whole struct and overwrite the
			// two normalised ones instead.
			OnlyCategories:      r.OnlyCategories,
			OnlyKinds:           r.OnlyKinds,
			OnlyCountries:       r.OnlyCountries,
			MaxUsesTotal:        r.MaxUsesTotal,
			MaxUsesPerUser:      r.MaxUsesPerUser,
			MaxTotalCashbackUSD: r.MaxTotalCashbackUSD,
			ExpiresAt:           r.ExpiresAt,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CashbackBps == out[j].CashbackBps {
			return out[i].Code < out[j].Code
		}
		return out[i].CashbackBps > out[j].CashbackBps
	})
	return out, nil
}

// EffectiveCashbackCodeRules returns the persisted admin-managed promo code
// table. Nil means "this deployment still reads legacy env codes"; a non-nil
// empty slice means "admin explicitly disabled promo codes".
func (s Settings) EffectiveCashbackCodeRules() []CashbackCodeRule {
	if s.CashbackCodeRules == nil {
		return nil
	}
	rules, err := NormalizeCashbackCodeRules(*s.CashbackCodeRules)
	if err != nil {
		return nil
	}
	return rules
}

/* ---------------- Published ladder + locked-time loyalty ---------------- */

// NormalizeStakerLoyalty makes an admin-supplied lock list safe: it drops
// entries with a non-positive day count or bonus, clamps bonuses to
// MaxCashbackBps, collapses duplicate day counts (highest bonus wins) and
// sorts ascending by lock length.
func NormalizeStakerLoyalty(in []StakerLoyaltyLock) []StakerLoyaltyLock {
	byDays := make(map[int]int, len(in))
	for _, l := range in {
		if l.LockDays <= 0 || l.BonusBps <= 0 {
			continue
		}
		bps := l.BonusBps
		if bps > MaxCashbackBps {
			bps = MaxCashbackBps
		}
		if existing, ok := byDays[l.LockDays]; !ok || bps > existing {
			byDays[l.LockDays] = bps
		}
	}
	out := make([]StakerLoyaltyLock, 0, len(byDays))
	for days, bps := range byDays {
		out = append(out, StakerLoyaltyLock{LockDays: days, BonusBps: bps})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LockDays < out[j].LockDays })
	if len(out) > MaxStakerLoyalty {
		out = out[:MaxStakerLoyalty]
	}
	return out
}

// EffectiveStakerTiers is the ladder the programme actually runs on: the
// configured ladder when present, otherwise the published defaults. This is
// the single source both the public rate endpoint and the payout path read,
// so the storefront can never advertise a rate the shop will not pay.
func (s Settings) EffectiveStakerTiers() []StakerTier {
	if tiers := NormalizeStakerTiers(s.StakerCashbackTiers); len(tiers) > 0 {
		return tiers
	}
	return NormalizeStakerTiers(DefaultStakerTiers)
}

// EffectiveStakerLoyalty is the lock-bonus list the storefront shows, falling
// back to the published defaults when the operator has not written one.
func (s Settings) EffectiveStakerLoyalty() []StakerLoyaltyLock {
	if locks := NormalizeStakerLoyalty(s.StakerLoyalty); len(locks) > 0 {
		return locks
	}
	return NormalizeStakerLoyalty(DefaultStakerLoyalty)
}

// LoyaltyBonusBps returns the bonus earned by a stake that has been locked for
// lockedDays: the richest lock whose day count the buyer has met. ok is false
// when no lock is met or the list is empty.
func (s Settings) LoyaltyBonusBps(lockedDays int) (bps int, lock StakerLoyaltyLock, ok bool) {
	for _, l := range s.EffectiveStakerLoyalty() {
		if l.LockDays <= lockedDays && l.BonusBps > bps {
			bps, lock, ok = l.BonusBps, l, true
		}
	}
	return bps, lock, ok
}

// NextLoyaltyLock is the cheapest lock the buyer has not yet reached, so the
// UI can say "N days to the +0.3% lock". ok is false once every lock is met.
func (s Settings) NextLoyaltyLock(lockedDays int) (StakerLoyaltyLock, bool) {
	for _, l := range s.EffectiveStakerLoyalty() {
		if l.LockDays > lockedDays {
			return l, true
		}
	}
	return StakerLoyaltyLock{}, false
}

// CashbackBpsForBuyerEffective is the honest answer to "what base rate does
// this buyer earn": in v2 (single-ledger model) that is simply the flat base
// rate for everyone — the v1 tier ladder and locked-time loyalty bonuses are
// gone, and the staker boost is no longer a rate the settings can express.
// It is a WALLET-BOUND ledger balance: it is resolved separately from each
// buyer's realized pool-fee ledger (store.ApplyLedgerPurchaseInTx on
// fulfillment, store.ReadStakeLedger for display) and paid from the ledger,
// never from the base-rate wallet math.
//
// The signature keeps its (stakeLuna, staked, lockedDays) parameters so the
// intentionally unused by the v2 resolver.
func (s Settings) CashbackBpsForBuyerEffective(stakeLuna int64, staked bool, lockedDays int) (bps int, tier StakerTier, loyaltyBps int, boosted bool) {
	return s.EffectiveCashbackBps(), StakerTier{}, 0, false
}
