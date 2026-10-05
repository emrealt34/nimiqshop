package handlers

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/valyala/fasthttp"

	"nimiqshop/internal/db"
	"nimiqshop/internal/middleware"
)

/* wallet_handlers.go — the buyer's OWN NIM, read-only from the chain.
 *
 * WHY THIS EXISTS (owner, 2026-10-05): while shopping, the first question a
 * buyer asks is "how much can I afford with the NIM I hold?". That number used
 * to appear only after choosing an amount inside a product page.
 *
 * WHY IT REPORTS FOUR FIGURES (owner, 2026-10-05: "your wallet'deki NIM
 * miktarım yanlış"): reporting one number was the bug. `getAccountByAddress`
 * returns the LIQUID balance only, so a buyer whose NIM is staked — the shop's
 * own cashback programme asks them to stake — saw a fraction of what their
 * wallet shows. Live proof: a real feed address holds 200,000 luna liquid and
 * 10,000,000 luna actively staked; the old response said "2 NIM" and the
 * wallet says "102 NIM". Both are true, and only one of them is spendable, so
 * the response now carries every part and the UI states which is which:
 *
 *   available  = the address balance (what a payment can actually spend)
 *   staked     = active stake delegated to a validator (NOT spendable)
 *   inactive   = stake that is no longer active (not spendable)
 *   retired    = stake retired back to the address (not spendable by itself)
 *   total      = available + staked + inactive + retired (what a wallet shows)
 *
 * The affordability maths in the UI uses `available` ONLY — a buyer cannot pay
 * with staked NIM — while the headline figure can be reconciled with the number
 * the buyer sees in Nimiq Pay. Contract balances (HTLC, vesting) are still not
 * included: they are not necessarily spendable even when they are "yours".
 *
 * Inside Nimiq Pay the client prefers the host bridge (`getBalance(address)`,
 * Mini App SDK 0.2.1+) and cross-checks it against this endpoint. The address
 * always comes from the SESSION, never from a query parameter, so the shop can
 * never be used as an open "look up any address" proxy.
 */

// balanceCacheTTL keeps a page-view storm off the public RPC. The figure is
// display-only; nothing in the pay path trusts it.
const balanceCacheTTL = 20 * time.Second

type walletBalanceEntry struct {
	luna     int64
	staked   int64
	inactive int64
	retired  int64
	at       time.Time
}

var (
	walletCacheMu sync.Mutex
	walletCache   = map[string]walletBalanceEntry{}
)

// WalletBalance answers with the signed-in wallet's NIM: what is spendable,
// what is staked, and the total a wallet would show. A zero balance is a
// success, not an error.
func (h *Handlers) WalletBalance(ctx *fasthttp.RequestCtx) {
	userID := middleware.UserID(ctx)

	user, err := h.Store.GetUser(userID)
	if errors.Is(err, db.ErrNotFound) {
		writeError(ctx, fasthttp.StatusUnauthorized, "unknown session")
		return
	}
	if err != nil {
		writeError(ctx, fasthttp.StatusInternalServerError, "could not load the account")
		return
	}

	address := strings.TrimSpace(user.NimiqAddress)
	if address == "" {
		// A session without a wallet (an operator account, a legacy record):
		// there is nothing to look up, and that is not a server failure.
		writeJSON(ctx, fasthttp.StatusOK, map[string]interface{}{
			"address":     "",
			"available":   false,
			"balance_nim": 0,
			"staked_nim":  0,
			"total_nim":   0,
			"network":     h.walletNetwork(),
			"reason":      "no_wallet",
		})
		return
	}
	if h.NIMRPC == nil {
		// NIMRPC is optional in some deployments (tests, offline boxes). Say so
		// instead of pretending the wallet is empty.
		writeJSON(ctx, fasthttp.StatusOK, map[string]interface{}{
			"address":     address,
			"available":   false,
			"balance_nim": 0,
			"staked_nim":  0,
			"total_nim":   0,
			"network":     h.walletNetwork(),
			"reason":      "rpc_unavailable",
		})
		return
	}

	fresh := string(ctx.QueryArgs().Peek("fresh")) == "1"
	entry, cached, ok := h.readWalletBalance(address, fresh)
	if !ok {
		// A chain read that failed is a 502 with a retry hint, never a silent
		// zero: the UI must be able to tell "0 NIM" from "we could not look".
		ctx.Response.Header.Set("Retry-After", "3")
		writeError(ctx, fasthttp.StatusBadGateway, "balance lookup failed — retry in a few seconds")
		return
	}

	staked := lunaToNIM(entry.staked)
	inactive := lunaToNIM(entry.inactive)
	retired := lunaToNIM(entry.retired)
	available := lunaToNIM(entry.luna)

	writeJSON(ctx, fasthttp.StatusOK, map[string]interface{}{
		"address":      address,
		"available":    true,
		"balance_luna": entry.luna,
		"balance_nim":  available,
		"staked_nim":   staked,
		"inactive_nim": inactive,
		"retired_nim":  retired,
		"total_nim":    available + staked + inactive + retired,
		"network":      h.walletNetwork(),
		"cached":       cached,
		"observed_at":  entry.at,
	})
}

// readWalletBalance returns the cached entry for the address, or reads the
// chain. `fresh` bypasses the cache (the buyer tapped refresh).
func (h *Handlers) readWalletBalance(address string, fresh bool) (walletBalanceEntry, bool, bool) {
	key := strings.ToUpper(strings.ReplaceAll(address, " ", ""))
	now := time.Now()

	if !fresh {
		walletCacheMu.Lock()
		hit, found := walletCache[key]
		walletCacheMu.Unlock()
		if found && now.Sub(hit.at) < balanceCacheTTL {
			return hit, true, true
		}
	}

	ctxN, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()

	luna, err := h.NIMRPC.GetAccountBalance(ctxN, address)
	if err != nil {
		return walletBalanceEntry{}, false, false
	}

	// Staking is a SECOND public read on the same provider. An address with no
	// staking record makes the RPC answer an error; that means "not staked",
	// never "the lookup failed".
	entry := walletBalanceEntry{luna: luna, at: now}
	if staker, sErr := h.NIMRPC.GetStaker(ctxN, address); sErr == nil {
		entry.staked = staker.Balance
		entry.inactive = staker.InactiveBalance
		entry.retired = staker.RetiredBalance
	}

	walletCacheMu.Lock()
	walletCache[key] = entry
	// Bound the map: this is a display cache for wallets seen recently, not a
	// store. Stale entries are dropped on write rather than swept.
	if len(walletCache) > 512 {
		for k, v := range walletCache {
			if now.Sub(v.at) > balanceCacheTTL {
				delete(walletCache, k)
			}
		}
	}
	walletCacheMu.Unlock()

	return entry, false, true
}

// walletNetwork is the chain the shop reads, so the UI can say which one the
// figure came from (and never present a testnet number as a real balance).
func (h *Handlers) walletNetwork() string {
	network := strings.ToLower(strings.TrimSpace(h.Cfg.CashbackNetwork))
	if network != "testnet" {
		return "mainnet"
	}
	return network
}

// lunaToNIM converts luna to NIM for display. 1 NIM = 100,000 luna, the same
// constant the Mini App `getBalance` result is documented with, so the two
// sources can never disagree about what the buyer holds.
func lunaToNIM(luna int64) float64 {
	return float64(luna) / 100000
}
