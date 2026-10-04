package handlers

import (
	"context"
	"log"
	"strconv"
	"time"

	"github.com/valyala/fasthttp"

	"nimiqshop/internal/chainstake"
	"nimiqshop/internal/db"
	"nimiqshop/internal/middleware"
	"nimiqshop/internal/poolstake"
	"nimiqshop/internal/stakeledger"
)

// LunaPerNIM mirrors cashback.LunaPerNIM without importing that package
// (the value is a protocol constant, not a policy one).
const lunaPerNIM = 100_000

// stakeCashbackView is the public shape of the single-ledger programme:
// one parameter set, no tiers, no loyalty ladder. The storefront renders
// "everyone earns the base rate; staking adds up to max_boost on top" and
// nothing else — there are no levels to display.
func (h *Handlers) stakeCashbackView() map[string]any {
	settings, _ := h.Store.GetAdminSettings(0)
	p := settings.EffectiveStakeCashback()
	return map[string]any{
		"max_boost_bps":       p.MaxBoostBps,
		"max_boost_percent":   float64(p.MaxBoostBps) / 100.0,
		"ledger_max_usd":      p.AMaxUSD,
		"min_stake_nim":       p.MinStakeNIM,
		"daily_cap_usd":       p.DailyCapUSD,
		"monthly_cap_usd":     p.MonthlyCapUSD,
		"loyalty_start":       p.G0,
		"loyalty_ramp_days":   p.TDays,
		"loyalty_ramp_years":  float64(p.TDays) / 365.0,
		"profit_credit_share": p.K,
		"monthly_carry_share": p.Q,
		"display_basis_usd":   p.DisplayBasisUSD,
		"refresh_mode":        "on_demand",
	}
}

// stakeProgramEnabled is the single answer to "is the staker boost live?":
// the pool must be reachable AND the feed must be wired. Advertising a
// boost the backend can never fund would be a promise the shop cannot keep.
func (h *Handlers) stakeProgramEnabled() bool {
	return h.Cfg.PoolAPIURL != "" && h.Cfg.PoolFeedAPIKey != ""
}

// syncProfitOnDemand refreshes one wallet only when its data is actually
// needed. There is no global 30-second poll. Pool/API failures are best-effort:
// the existing ledger remains intact and fulfillment can still pay base rate.
/* Pool/chain calls never borrow the HTTP request's context.
 *
 * fasthttp recycles a RequestCtx the moment the client's connection goes away
 * (a buyer or admin closing the tab is enough), while net/http's cancellation
 * watcher — started from the context we hand it — keeps READING that recycled
 * ctx until the call returns. The race detector caught exactly that on CI
 * (TestPoolStakeMeFallsBackToTheChain: fasthttp Shutdown vs RequestCtx.Done
 * inside poolstake.Client.Profit and chainstake.Verifier.Verify). The supplier
 * side already follows this rule (see supplierContext); these helpers bring
 * the pool and chain calls in line.
 *
 * Unlike supplierContext the work still finishes inside the handler, so the
 * caller releases the timer: defer cancel().
 */

const poolCallTimeout = 8 * time.Second

func poolContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), poolCallTimeout)
}

// poolStake is the detached wrapper for every pool Status call in this file.
func (h *Handlers) poolStake(ctx context.Context, address string) (poolstake.Status, error) {
	cctx, cancel := poolContext(ctx)
	defer cancel()
	return h.Pool.Stake(cctx, address)
}

func (h *Handlers) syncProfitOnDemand(ctx context.Context, addr string) {
	if h.Pool == nil || addr == "" || !h.stakeProgramEnabled() {
		return
	}
	nimUsd := h.bestNIMUSD()
	if nimUsd <= 0 {
		return
	}
	ctx, cancel := poolContext(ctx)
	defer cancel()
	now := time.Now().UTC()
	currentMonth := now.Format("2006-01")
	l, ok, _ := h.Store.ReadStakeLedger(addr)
	// If the last snapshot belongs to an older month, close the immediately
	// preceding month first. This catches profit settled while the buyer did
	// not visit the shop; repeating it is harmless because totals are deltas.
	if ok && l.ProfitMonth != "" && l.ProfitMonth != currentMonth {
		if p, err := h.Pool.Profit(ctx, addr, "last_month"); err == nil {
			month := now.AddDate(0, -1, 0).Format("2006-01")
			_, _ = h.Store.ApplyProfitSnapshot(addr, month, p.PoolFeeLuna, nimUsd, p.LoyaltyDays, p.LoyaltyMultiplier, p.StakeLuna, now)
		}
	}
	if p, err := h.Pool.Profit(ctx, addr, "this_month"); err == nil {
		_, _ = h.Store.ApplyProfitSnapshot(addr, currentMonth, p.PoolFeeLuna, nimUsd, p.LoyaltyDays, p.LoyaltyMultiplier, p.StakeLuna, now)
	}
}

// ledgerView renders a wallet's ledger for the buyer-facing endpoints.
// Dollar-first on purpose: "you have $X available", NIM in parentheses.
func (h *Handlers) ledgerView(addr string) map[string]any {
	l, ok, err := h.Store.ReadStakeLedger(addr)
	if err != nil || !ok {
		return map[string]any{
			"has_ledger": false,
		}
	}
	settings, _ := h.Store.GetAdminSettings(0)
	p := settings.EffectiveStakeCashback()
	nimUsd := h.bestNIMUSD()
	availableUSD := 0.0
	if nimUsd > 0 {
		availableUSD = l.A * nimUsd
	}
	dayRem, monthRem := stakeledger.RemainingCaps(&l, time.Now().UTC(), p)
	return map[string]any{
		"has_ledger":             true,
		"boost_bps":              stakeledger.BoostBps(&l, nimUsd, p),
		"boost_percent":          float64(stakeledger.BoostBps(&l, nimUsd, p)) / 100.0,
		"available_cashback_usd": availableUSD,
		"available_cashback_nim": l.A,
		"loyalty_days":           l.D,
		"loyalty_max_days":       p.TDays,
		"loyalty_multiplier":     l.LoyaltyMultiplier,
		"daily_remaining_usd":    dayRem,
		"monthly_remaining_usd":  monthRem,
		"pool_profit_month":      l.ProfitMonth,
		"pool_profit_luna":       l.ProfitLuna,
		"pool_profit_nim":        float64(l.ProfitLuna) / lunaPerNIM,
		"refresh_mode":           "on_demand",
		"updated_at":             l.UpdatedAt,
	}
}

// ledgerBoostBpsBestEffort returns the wallet's current ledger boost (bps)
// for a user id, best-effort: 0 on any lookup failure, missing price, or
// the exact in-transaction resolver instead.
func (h *Handlers) ledgerBoostBpsBestEffort(userID string) int {
	if userID == "" {
		return 0
	}
	user, err := h.Store.GetUser(userID)
	if err != nil || user.NimiqAddress == "" {
		return 0
	}
	nimUsd := h.bestNIMUSD()
	if nimUsd <= 0 {
		return 0
	}
	l, ok, err := h.Store.ReadStakeLedger(user.NimiqAddress)
	if err != nil || !ok {
		return 0
	}
	settings, _ := h.Store.GetAdminSettings(0)
	return stakeledger.BoostBps(&l, nimUsd, settings.EffectiveStakeCashback())
}

// bestNIMUSD is a best-effort price for display: the warm oracle snapshot
// (cached here for 30s so the page does not pay a network round trip on
// every render), 0 when unavailable (callers then show USD-less views
// rather than guess).
func (h *Handlers) bestNIMUSD() float64 {
	if v, ok := h.cache.get("nim_usd_price"); ok {
		if p, ok := v.(float64); ok && p > 0 {
			return p
		}
	}
	if h.NIMUSDPrice == nil {
		return 0
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	p, err := h.NIMUSDPrice(ctx)
	if err != nil || p <= 0 {
		return 0
	}
	h.cache.setTTL("nim_usd_price", p, 30*time.Second)
	return p
}

// PublicCashbackRate is the storefront's rate source: the live flat base
// rate (1% by default) plus the single-ledger programme parameters the
// pre-login screens show. No tier tables, no lock lists — v2 has none.
func (h *Handlers) PublicCashbackRate(ctx *fasthttp.RequestCtx) {
	settings, err := h.Store.GetAdminSettings(0)
	if err != nil {
		adminStoreError(ctx)
		return
	}
	bps := settings.EffectiveCashbackBps()
	stakerBase := h.poolStakerBaseBps(ctx)
	writeJSON(ctx, fasthttp.StatusOK, map[string]interface{}{
		// The universal base: what a NON-staker earns (0 by default).
		"cashback_bps":     bps,
		"cashback_percent": float64(bps) / 100.0,
		// The staker base: what ANY positively-staked address earns as its
		// base — the amount does not matter. The operator's admin override
		// wins when set; otherwise the pool's published number.
		"staker_base_bps":       stakerBase,
		"staker_base_percent":   float64(stakerBase) / 100.0,
		"staker_base_rule":      "any_positive_stake",
		"cashback_code_enabled": h.cashbackCodeEnabled(),

		"staker_program_enabled": h.stakeProgramEnabled(),
		"stake_cashback":         h.stakeCashbackView(),
		"pool_validator_address": h.Cfg.PoolValidatorAddress,
		// Legacy keys kept for one release so old frontends render:
		// the v1 ladder is empty by definition in v2.
		"staker_tiers":   []any{},
		"staker_loyalty": []any{},
	})
}

// chainStake asks the CHAIN whether address currently delegates a positive
// stake to the operator's validator. Nil verifier = feature off. The verifier
// caches, so this is at most one RPC per address per TTL.
func (h *Handlers) chainStake(ctx context.Context, address string) (chainstake.Staker, error) {
	if h.Chain == nil || address == "" {
		return chainstake.Staker{}, nil
	}
	cctx, cancel := poolContext(ctx)
	defer cancel()
	return h.Chain.Verify(cctx, address)
}

// fmtNIM renders Luna as a short NIM string for log lines.
func fmtNIM(luna int64) string {
	return strconv.FormatFloat(float64(luna)/lunaPerNIM, 'f', 2, 64)
}

// PoolStakeMe is the logged-in buyer's own standing: their live stake,
// their ledger (available $, boost rate, loyalty age, remaining caps) and
// the total rate the next delivery will pay. The product/checkout screens
// call this to render the personalized answer.
func (h *Handlers) PoolStakeMe(ctx *fasthttp.RequestCtx) {
	userID := middleware.UserID(ctx)
	user, err := h.Store.GetUser(userID)
	if err != nil {
		writeError(ctx, fasthttp.StatusUnauthorized, "session expired")
		return
	}
	h.poolStakeMeFor(ctx, user.NimiqAddress)
}

// poolStakeMeFor is the shared body of PoolStakeMe and PoolStakeRefresh.
func (h *Handlers) poolStakeMeFor(ctx *fasthttp.RequestCtx, address string) {
	resp := map[string]any{
		"address":                address,
		"staked":                 false,
		"stake_luna":             int64(0),
		"stake_nim":              0.0,
		"staker_program_enabled": h.stakeProgramEnabled(),
		"pool_validator_address": h.Cfg.PoolValidatorAddress,
		"checked_at":             time.Time{},
	}

	settings, err := h.Store.GetAdminSettings(0)
	if err != nil {
		adminStoreError(ctx)
		return
	}
	operatorBase := settings.EffectiveCashbackBps()
	base := operatorBase
	poolBase := 0

	var stakeLuna int64
	staked := false
	lockedDays := 0

	if h.Cfg.PoolAPIURL != "" && h.Pool != nil && address != "" {
		st, err := h.poolStake(ctx, address)
		switch err {
		case nil:
			staked, stakeLuna = st.Staked, st.StakeLuna
			resp["stake_source"] = "pool"
			// A cached "not staked" answer can race a fresh delegation.
			// Prefer our durable local pending/positive observation until
			// the pool has indexed the new stake.
			if !staked {
				if cached := h.Store.CachedStakerStake(address, time.Now().UTC()); cached.Staked {
					staked, stakeLuna = cached.Staked, cached.StakeLuna
					st.BaseBps = cached.BaseBps
					resp["stake_source"] = "local_watch"
				}
			}
			// THE chain fallback. The pool's index is a database on the
			// pool's own machine; the delegation itself is a public fact on
			// chain, seconds old. When the pool says "no staker" but the
			// chain shows this address delegating to OUR validator, the
			// buyer IS staked and must not be shown (or paid) a zero rate.
			if !staked {
				if cs, cerr := h.chainStake(ctx, address); cerr == nil && cs.Staked {
					staked, stakeLuna = true, cs.StakeLuna
					resp["stake_source"] = "chain"
					log.Printf("poolstake: chain confirms a %s NIM delegation to %s that the pool did not report; using the chain",
						fmtNIM(cs.StakeLuna), h.Cfg.PoolValidatorAddress)
				} else if cerr != nil {
					resp["chain_check_error"] = "rpc unavailable"
				}
			}
			// Pool answers only whether they are staked. The rate is the
			// admin-panel staker base (default 1%).
			poolBase = 0
			if staked {
				poolBase = settings.EffectiveStakerCashbackBps()
			}
			base = db.StakerStake{Staked: staked, StakeLuna: stakeLuna, BaseBps: poolBase}.EffectiveBaseBps(operatorBase)
			resp["checked_at"] = st.CheckedAt
			resp["from_cache"] = st.FromCache
			// Advance both clocks: the v1 watch (staked_since display) and
			// the ledger's stake-weighted age (dilute on increase, pro-rata
			// shrink on decrease). A pool outage leaves them as-is.
			now := time.Now().UTC()
			watch, werr := h.Store.ObserveStake(address, stakeLuna, now)
			if werr == nil {
				_ = h.Store.ObserveStakeLedger(address, stakeLuna, now)
			}
			if staked {
				resp["staked_since"] = watch.FirstSeenAt
				lockedDays = watch.LockedDays(now)
			}
		default:
			// Never downgrade a known/freshly announced staker during an
			// outage. The durable local snapshot is only a fallback; a
			// later authoritative pool response can still replace it.
			resp["stake_check_error"] = "pool unavailable"
			resp["stake_source"] = "pool_error"
			// The chain is a second, independent source: while the pool is
			// unreachable it still answers whether the delegation exists.
			if cs, cerr := h.chainStake(ctx, address); cerr == nil && cs.Staked {
				staked, stakeLuna = true, cs.StakeLuna
				resp["stake_source"] = "chain"
				delete(resp, "stake_check_error")
				log.Printf("poolstake: pool unreachable; chain confirms a %s NIM delegation to %s",
					fmtNIM(cs.StakeLuna), h.Cfg.PoolValidatorAddress)
			} else if cached := h.Store.CachedStakerStake(address, time.Now().UTC()); cached.Staked {
				staked, stakeLuna = true, cached.StakeLuna
				poolBase = settings.EffectiveStakerCashbackBps()
				cached.BaseBps = poolBase
				base = cached.EffectiveBaseBps(operatorBase)
				resp["from_cache"] = true
				resp["stake_source"] = "local_watch"
			}
		}
	}

	// The page needs fresh numbers now, so refresh this address only. No
	// background scan and no requests for unrelated stakers.
	h.syncProfitOnDemand(ctx, address)
	ledger := h.ledgerView(address)
	ledgerBps, _ := ledger["boost_bps"].(int)
	totalBps := base + ledgerBps

	resp["staked"] = staked
	resp["stake_luna"] = stakeLuna
	resp["stake_nim"] = float64(stakeLuna) / lunaPerNIM
	resp["base_bps"] = base
	resp["base_percent"] = float64(base) / 100.0
	// stakerBaseApplied: the pool's staker base is what this buyer earns
	// (it beat the operator's universal base). Drives base_source/boosted.
	stakerBaseApplied := staked && poolBase > 0 && poolBase > operatorBase
	resp["base_source"] = map[bool]string{true: "pool_staker", false: "operator"}[stakerBaseApplied]
	resp["staker_base_bps"] = h.poolStakerBaseBps(ctx)
	resp["boost_bps"] = ledgerBps
	resp["boost_percent"] = float64(ledgerBps) / 100.0
	resp["cashback_bps"] = totalBps
	resp["cashback_percent"] = float64(totalBps) / 100.0
	resp["boosted"] = ledgerBps > 0 || stakerBaseApplied
	resp["ledger"] = ledger
	if staked {
		resp["loyalty_days"] = lockedDays
	}
	writeJSON(ctx, fasthttp.StatusOK, resp)
}

// PoolStakeRefresh drops the cached pool answer for the signed-in buyer and
// re-asks. The frontend calls it right after Nimiq Pay confirms a stake
// transaction so the new rate appears without waiting for the cache TTL.
func (h *Handlers) PoolStakeRefresh(ctx *fasthttp.RequestCtx) {
	if h.Pool == nil || h.Cfg.PoolAPIURL == "" {
		writeError(ctx, fasthttp.StatusServiceUnavailable, "pool staking is not configured")
		return
	}
	userID := middleware.UserID(ctx)
	user, err := h.Store.GetUser(userID)
	if err != nil || user.NimiqAddress == "" {
		writeError(ctx, fasthttp.StatusUnauthorized, "session expired")
		return
	}

	h.Pool.Forget(user.NimiqAddress)
	st, err := h.poolStake(ctx, user.NimiqAddress)
	if err != nil {
		writeError(ctx, fasthttp.StatusBadGateway, "could not reach the staking pool")
		return
	}
	now := time.Now().UTC()
	watch, _ := h.Store.ObserveStake(user.NimiqAddress, st.StakeLuna, now)
	_ = h.Store.ObserveStakeLedger(user.NimiqAddress, st.StakeLuna, now)
	_ = watch
	h.poolStakeMeFor(ctx, user.NimiqAddress)
}
