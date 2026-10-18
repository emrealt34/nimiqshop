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

// poolContext deliberately does NOT derive from the caller's context: even
// WithoutCancel keeps the parent's Value lookups, and for a *fasthttp.RequestCtx
// those read the pooled, recycled request. The pool and chain clients read no
// context values, so a detached, bounded context is all they need.
func poolContext(_ context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), poolCallTimeout)
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
			from, _ := time.Parse(time.RFC3339, p.From)
			month := from.Format("2006-01")
			_, _ = h.Store.ApplyProfitSnapshot(addr, month, p.PoolFeeLuna, nimUsd, p.LoyaltyDays, p.LoyaltyMultiplier, p.StakeLuna, now)
		}
	}
	if p, err := h.Pool.Profit(ctx, addr, "this_month"); err == nil {
		from, _ := time.Parse(time.RFC3339, p.From)
		_, _ = h.Store.ApplyProfitSnapshot(addr, from.Format("2006-01"), p.PoolFeeLuna, nimUsd, p.LoyaltyDays, p.LoyaltyMultiplier, p.StakeLuna, now)
	}
}

// Refresh at checkout creation, not on every order-detail polling read.
func (h *Handlers) syncBuyerProfitOnDemand(userID string) {
	if user, err := h.Store.GetUser(userID); err == nil {
		// Profit accrues to the wallets that stake: every linked staking
		// wallet plus the sign-in address. Syncing only the primary would
		// leave the others' ledger books stale, and their boosts are part of
		// the same rate.
		for _, address := range user.EarningStakeAddresses() {
			h.syncProfitOnDemand(context.Background(), address)
		}
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
	// The ledger is keyed by EARNING address — that is where the fulfillment
	// path writes it — so this sums the books of every wallet that earns for
	// the account (ledgerBoostBpsAllInTx / ApplyLedgerPurchaseAllInTx use the
	// same rule when they pay). Reading only the primary wallet would show a
	// buyer with three staking wallets a third of the boost they actually get.
	settings, _ := h.Store.GetAdminSettings(0)
	params := settings.EffectiveStakeCashback()
	total := 0
	for _, address := range user.EarningStakeAddresses() {
		l, ok, err := h.Store.ReadStakeLedger(address)
		if err != nil || !ok {
			continue
		}
		total += stakeledger.BoostBps(&l, nimUsd, params)
	}
	return total
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
	h.poolStakeMeFor(ctx, user)
}

// poolStakeMeFor is the shared body of PoolStakeMe and PoolStakeRefresh.
func (h *Handlers) poolStakeMeFor(ctx *fasthttp.RequestCtx, user db.User) {
	resp, ok := h.buildStakeMeView(ctx, user)
	if !ok {
		adminStoreError(ctx)
		return
	}
	writeJSON(ctx, fasthttp.StatusOK, resp)
}

// stakeWalletView is ONE earning wallet's resolved standing: what the pool,
// the chain and the durable local observation each said about it, plus the
// ledger book that wallet has earned on its own.
type stakeWalletView struct {
	Address         string
	Staked          bool
	StakeLuna       int64
	Source          string // pool | chain | local_watch | pool_error | ""
	OtherValidator  string
	OtherStakeNIM   float64
	StakedSince     time.Time
	LockedDays      int
	CheckedAt       time.Time
	FromCache       bool
	CheckError      string
	ChainCheckError string
	BoostBps        int
	Ledger          map[string]any
	Linked          bool
	LinkedAt        time.Time
}

// resolveStakeWallet asks about a single wallet with the ladder the shop has
// always used — pool, then the durable local observation, then the chain — and
// advances that wallet's own loyalty and ledger clocks.
//
// It deliberately knows nothing about rates. The staker base is an account-level
// number (counted once, however many wallets earn), so it is applied by
// buildStakeMeView after the whole set has been resolved.
func (h *Handlers) resolveStakeWallet(ctx *fasthttp.RequestCtx, address string) stakeWalletView {
	view := stakeWalletView{Address: address}
	if address != "" && h.Cfg.PoolAPIURL != "" && h.Pool != nil {
		st, err := h.poolStake(ctx, address)
		switch err {
		case nil:
			view.Staked, view.StakeLuna = st.Staked, st.StakeLuna
			view.Source = "pool"
			// A cached "not staked" answer can race a fresh delegation.
			// Prefer our durable local pending/positive observation until
			// the pool has indexed the new stake.
			if !view.Staked {
				if cached := h.Store.CachedStakerStake(address, time.Now().UTC()); cached.Staked {
					view.Staked, view.StakeLuna = cached.Staked, cached.StakeLuna
					view.Source = "local_watch"
				}
			}
			// THE chain fallback. The pool's index is a database on the
			// pool's own machine; the delegation itself is a public fact on
			// chain, seconds old. When the pool says "no staker" but the
			// chain shows this address delegating to OUR validator, the
			// buyer IS staked and must not be shown (or paid) a zero rate.
			if !view.Staked {
				if cs, cerr := h.chainStake(ctx, address); cerr == nil && cs.Staked {
					view.Staked, view.StakeLuna = true, cs.StakeLuna
					view.Source = "chain"
					log.Printf("poolstake: chain confirms a %s NIM delegation to %s that the pool did not report for %s; using the chain",
						fmtNIM(cs.StakeLuna), h.Cfg.PoolValidatorAddress, poolstake.CanonicalAddress(address))
				} else if cerr != nil {
					view.ChainCheckError = "rpc unavailable"
				} else if cs.Elsewhere {
					// Staked with ANOTHER validator: the page offers to move it.
					view.OtherValidator = cs.Delegation
					view.OtherStakeNIM = float64(cs.ElsewhereLuna) / lunaPerNIM
				}
			}
			view.CheckedAt = st.CheckedAt
			view.FromCache = st.FromCache
			// Advance both clocks for THIS wallet: the v1 watch
			// (staked_since display) and the ledger's stake-weighted age
			// (dilute on increase, pro-rata shrink on decrease). A pool
			// outage leaves them as-is.
			now := time.Now().UTC()
			watch, werr := h.Store.ObserveStake(address, view.StakeLuna, now)
			if werr == nil {
				_ = h.Store.ObserveStakeLedger(address, view.StakeLuna, now)
			}
			if view.Staked {
				view.StakedSince = watch.FirstSeenAt
				view.LockedDays = watch.LockedDays(now)
			}
		default:
			// Never downgrade a known/freshly announced staker during an
			// outage. The durable local snapshot is only a fallback; a
			// later authoritative pool response can still replace it.
			view.CheckError = "pool unavailable"
			view.Source = "pool_error"
			// The chain is a second, independent source: while the pool is
			// unreachable it still answers whether the delegation exists.
			if cs, cerr := h.chainStake(ctx, address); cerr == nil && cs.Staked {
				view.Staked, view.StakeLuna = true, cs.StakeLuna
				view.Source = "chain"
				view.CheckError = ""
				log.Printf("poolstake: pool unreachable; chain confirms a %s NIM delegation to %s for %s",
					fmtNIM(cs.StakeLuna), h.Cfg.PoolValidatorAddress, poolstake.CanonicalAddress(address))
			} else if cached := h.Store.CachedStakerStake(address, time.Now().UTC()); cached.Staked {
				view.Staked, view.StakeLuna = true, cached.StakeLuna
				view.FromCache = true
				view.Source = "local_watch"
			}
		}
	}

	// The page needs fresh numbers now, so refresh this address only. No
	// background scan and no requests for unrelated stakers.
	h.syncProfitOnDemand(ctx, address)
	view.Ledger = h.ledgerView(address)
	if bps, ok := view.Ledger["boost_bps"].(int); ok {
		view.BoostBps = bps
	}
	return view
}

// buildStakeMeView renders a buyer's TOTAL standing across every wallet that
// earns their cashback: the address they signed in with plus each staking
// wallet they linked after proving ownership of it.
//
// That distinction is the whole point of the linked-stake-wallet feature. A
// shop account IS an address (FindOrCreateUserByAddress creates one account per
// wallet), so a buyer who stakes from their regular wallet but checks out
// through Nimiq Pay signs in as a *different* account that has never staked and
// earns the base rate — while the stake they are proud of sits one wallet over.
// Buyers also split a stake over several wallets, so the link is a list and the
// stakes behind it are summed.
//
// The aggregation rules, which the whole feature stands or falls on:
//
//   - stake is SUMMED across earning wallets, so 100 + 150 + 50 NIM in three
//     wallets earns exactly what 300 NIM in one wallet earns;
//   - the staker BASE is applied ONCE per account. It is a flat rate, not a
//     per-wallet entitlement: three wallets must not triple it;
//   - the ledger boost is summed, because each wallet's book was earned by that
//     wallet's own stake and is capped by it (ApplyLedgerPurchaseAllInTx debits
//     them the same way at payout, so the preview and the money agree);
//   - loyalty is the LONGEST streak and staked_since the EARLIEST, so linking a
//     new wallet never resets the age of an old one;
//   - payouts are unaffected: cashback goes to the sign-in wallet, always.
//
// ok=false means the admin settings could not be read; callers turn that into
// the standard store error response.
func (h *Handlers) buildStakeMeView(ctx *fasthttp.RequestCtx, user db.User) (map[string]any, bool) {
	settings, err := h.Store.GetAdminSettings(0)
	if err != nil {
		return nil, false
	}
	operatorBase := settings.EffectiveCashbackBps()
	stakerBase := settings.EffectiveStakerCashbackBps()

	primary := user.EffectiveStakeAddress()
	earning := user.EarningStakeAddresses()
	linkedAt := make(map[string]time.Time, len(user.StakeAddresses))
	for _, link := range user.LinkedStakeWallets() {
		linkedAt[poolstake.CanonicalAddress(link.Address)] = link.LinkedAt
	}

	views := make([]stakeWalletView, 0, len(earning))
	var stakeLuna int64
	var boostBps int
	var lockedDays int
	var stakedSince time.Time
	var otherValidator string
	var otherStakeNIM float64
	staked := false
	failed := 0
	chainFailed := 0
	primaryView := stakeWalletView{}

	for _, address := range earning {
		view := h.resolveStakeWallet(ctx, address)
		canonical := poolstake.CanonicalAddress(address)
		if at, ok := linkedAt[canonical]; ok {
			view.Linked = true
			view.LinkedAt = at
		}
		views = append(views, view)
		if address == primary {
			primaryView = view
		}
		if view.CheckError != "" {
			failed++
		}
		if view.ChainCheckError != "" {
			chainFailed++
		}
		stakeLuna += view.StakeLuna
		boostBps += view.BoostBps
		if view.Staked {
			staked = true
			if view.LockedDays > lockedDays {
				lockedDays = view.LockedDays
			}
			if stakedSince.IsZero() || (!view.StakedSince.IsZero() && view.StakedSince.Before(stakedSince)) {
				stakedSince = view.StakedSince
			}
		}
		// The first wallet staking elsewhere is the one the page offers to
		// move; the per-wallet list below names all of them.
		if otherValidator == "" && view.OtherValidator != "" {
			otherValidator = view.OtherValidator
			otherStakeNIM = view.OtherStakeNIM
		}
	}

	resp := map[string]any{
		// The wallet the rate card names. With several linked wallets this is
		// the one linked first; the total below covers all of them and
		// stake_addresses lists every wallet individually.
		"address": primary,
		// The wallet this account signed in with — and the wallet cashback is
		// always PAID to, however many wallets earn it.
		"account_address":         user.NimiqAddress,
		"stake_address_linked":    user.HasLinkedStakeAddress(),
		"stake_address_linked_at": user.StakeAddressLinkedAt(primary),
		"stake_addresses":         stakeAddressList(views),
		"earning_addresses":       earning,
		"linked_wallet_limit":     db.MaxLinkedStakeAddresses,
		"staked":                  false,
		"stake_luna":              int64(0),
		"stake_nim":               0.0,
		"staker_program_enabled":  h.stakeProgramEnabled(),
		"pool_validator_address":  h.Cfg.PoolValidatorAddress,
		"checked_at":              primaryView.CheckedAt,
		"from_cache":              primaryView.FromCache,
	}
	if primaryView.Source != "" {
		resp["stake_source"] = aggregateSource(views, primaryView.Source)
	}
	if failed == len(views) && failed > 0 {
		resp["stake_check_error"] = "pool unavailable"
	}
	if chainFailed == len(views) && chainFailed > 0 {
		resp["chain_check_error"] = "rpc unavailable"
	}
	if otherValidator != "" {
		resp["other_validator"] = otherValidator
		resp["other_stake_nim"] = otherStakeNIM
	}

	// THE base rule, applied once for the account: staked anywhere (any
	// amount, any wallet) → the staker base; not staked → the operator's
	// universal base (0 by default). The higher of the two wins so a promo
	// never pays a staker less than a non-staker.
	poolBase := 0
	if staked {
		poolBase = stakerBase
	}
	base := db.StakerStake{Staked: staked, StakeLuna: stakeLuna, BaseBps: poolBase}.EffectiveBaseBps(operatorBase)
	totalBps := base + boostBps
	// stakerBaseApplied: the pool's staker base is what this buyer earns
	// (it beat the operator's universal base). Drives base_source/boosted.
	stakerBaseApplied := staked && poolBase > 0 && poolBase > operatorBase

	resp["staked"] = staked
	resp["stake_luna"] = stakeLuna
	resp["stake_nim"] = float64(stakeLuna) / lunaPerNIM
	resp["base_bps"] = base
	resp["base_percent"] = float64(base) / 100.0
	resp["base_source"] = map[bool]string{true: "pool_staker", false: "operator"}[stakerBaseApplied]
	resp["staker_base_bps"] = h.poolStakerBaseBps(ctx)
	resp["boost_bps"] = boostBps
	resp["boost_percent"] = float64(boostBps) / 100.0
	resp["cashback_bps"] = totalBps
	resp["cashback_percent"] = float64(totalBps) / 100.0
	resp["boosted"] = boostBps > 0 || stakerBaseApplied
	// The ledger card describes the PRIMARY wallet; the per-wallet boosts that
	// make up boost_bps are in stake_addresses. One merged book would be a
	// different (and less honest) artefact — see ApplyLedgerPurchaseAllInTx.
	resp["ledger"] = primaryView.Ledger
	if staked {
		resp["loyalty_days"] = lockedDays
		if !stakedSince.IsZero() {
			resp["staked_since"] = stakedSince
		}
	}
	return resp, true
}

// stakeAddressList is the per-wallet breakdown the cashback page renders: which
// wallets earn, how much each holds, what each one's own book adds, and which
// (if any) still stakes with somebody else.
func stakeAddressList(views []stakeWalletView) []map[string]any {
	out := make([]map[string]any, 0, len(views))
	for _, view := range views {
		row := map[string]any{
			"address":      view.Address,
			"linked":       view.Linked,
			"staked":       view.Staked,
			"stake_nim":    float64(view.StakeLuna) / lunaPerNIM,
			"boost_bps":    view.BoostBps,
			"loyalty_days": view.LockedDays,
		}
		if !view.LinkedAt.IsZero() {
			row["linked_at"] = view.LinkedAt
		}
		if view.Source != "" {
			row["source"] = view.Source
		}
		if view.OtherValidator != "" {
			row["other_validator"] = view.OtherValidator
			row["other_stake_nim"] = view.OtherStakeNIM
		}
		out = append(out, row)
	}
	return out
}

// aggregateSource names where the total came from. "mixed" is the honest answer
// when wallets disagree (one from the pool, another confirmed on chain): the
// per-wallet rows carry the detail, and inventing a single source would claim a
// confidence the shop does not have.
func aggregateSource(views []stakeWalletView, primary string) string {
	for _, view := range views {
		if view.Source != "" && view.Source != primary {
			return "mixed"
		}
	}
	return primary
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

	// Refresh EVERY wallet that earns this account's cashback, not just the
	// primary: the buyer may have just staked (or moved a delegation) in any of
	// them, and the total on the page is the sum of all of them.
	addresses := user.EarningStakeAddresses()
	for _, address := range addresses {
		h.Pool.Forget(address)
	}
	now := time.Now().UTC()
	asked := 0
	for _, address := range addresses {
		st, err := h.poolStake(ctx, address)
		if err != nil {
			// One unreachable wallet must not fail the whole refresh — the
			// view below still renders what the others answered, plus the
			// honest stake_check_error when none of them could be asked.
			continue
		}
		asked++
		watch, _ := h.Store.ObserveStake(address, st.StakeLuna, now)
		_ = h.Store.ObserveStakeLedger(address, st.StakeLuna, now)
		_ = watch
	}
	if asked == 0 {
		writeError(ctx, fasthttp.StatusBadGateway, "could not reach the staking pool")
		return
	}
	h.poolStakeMeFor(ctx, user)
}
