package handlers

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/valyala/fasthttp"

	"nimiqshop/internal/db"
	"nimiqshop/internal/middleware"
)

/* wallet_handlers.go — the buyer's OWN NIM balance, read-only from the chain.
 *
 * WHY THIS EXISTS (owner, 2026-10-05): while shopping, the first question a
 * buyer asks is "how much can I afford with the NIM I hold?" — that number used
 * to appear only after choosing an amount inside a product page. It is now a
 * first-class figure on the home, product, cart, checkout and orders surfaces.
 *
 * Inside Nimiq Pay the client reads the balance through the host bridge
 * (`getBalance(address)`, Mini App SDK 0.2.1+) — no RPC, no round trip, and it
 * follows the host's active network. This endpoint is the fallback for every
 * other context (a plain browser, or an older host whose provider has no
 * getBalance) and never becomes an open balance proxy: the address comes from
 * the SESSION, never from a query parameter, so a caller can only ever read the
 * wallet they signed in with. Balances are public chain data — the restriction
 * exists to keep the shop out of the "query any address through our server"
 * business, not because the number is secret.
 */

// WalletBalance answers with the signed-in wallet's spendable NIM balance.
//
// The figure is the address balance, not a wallet total: contract balances
// (HTLC, vesting, staked) are reported separately by the chain and are not
// necessarily spendable, so the UI labels this as available NIM and never as
// "your total". A zero balance is a success, not an error.
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
			"reason":      "rpc_unavailable",
		})
		return
	}

	ctxN, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()

	luna, err := h.NIMRPC.GetAccountBalance(ctxN, address)
	if err != nil {
		// A chain read that failed is a 502 with a retry hint, never a silent
		// zero: the UI must be able to tell "0 NIM" from "we could not look".
		ctx.Response.Header.Set("Retry-After", "3")
		writeError(ctx, fasthttp.StatusBadGateway, "balance lookup failed — retry in a few seconds")
		return
	}

	network := strings.ToLower(strings.TrimSpace(h.Cfg.CashbackNetwork))
	if network != "testnet" {
		network = "mainnet"
	}

	writeJSON(ctx, fasthttp.StatusOK, map[string]interface{}{
		"address":      address,
		"available":    true,
		"balance_luna": luna,
		"balance_nim":  lunaToNIM(luna),
		"network":      network,
		"observed_at":  time.Now().UTC(),
	})
}

// lunaToNIM converts luna to NIM for display. 1 NIM = 100,000 luna, the same
// constant the Mini App `getBalance` result is documented with, so the two
// sources can never disagree about what the buyer holds.
func lunaToNIM(luna int64) float64 {
	return float64(luna) / 100000
}
