package handlers

import (
	"context"
	"github.com/valyala/fasthttp"
	"net/http"
	"nimiqshop/internal/middleware"
	"nimiqshop/internal/poolstake"
	"time"
)

// StakePosition reads only the authenticated buyer's wallets, independent of
// pool indexing. Validator names/logos are supplied separately by the cached
// validator worker API.
//
// It reports on EVERY wallet that earns this account's cashback — the one they
// signed in with plus each linked staking wallet — and sums the positions,
// because that total is what the cashback rate is computed from. Reporting one
// wallet of several would show a stake the shop itself does not use.
func (h *Handlers) StakePosition(ctx *fasthttp.RequestCtx) {
	user, err := h.Store.GetUser(middleware.UserID(ctx))
	if err != nil || user.NimiqAddress == "" {
		writeError(ctx, http.StatusUnauthorized, "session expired")
		return
	}
	if h.Chain == nil || !h.Chain.Ready() {
		writeError(ctx, http.StatusServiceUnavailable, "staking lookup is unavailable")
		return
	}
	addresses := user.EarningStakeAddresses()
	primary := user.EffectiveStakeAddress()
	if string(ctx.Method()) == http.MethodPost {
		// A refresh has to drop the cache for every earning wallet, not just
		// the primary: the buyer may have moved a delegation in any of them.
		for _, address := range addresses {
			h.Chain.Forget(address)
		}
	}

	// One budget for the whole set. Six chain reads (the cap plus the sign-in
	// wallet) against a warm cache are milliseconds; against a cold RPC they
	// must not add up to a minute on a page the buyer is waiting on.
	cctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	var (
		rows                                  []map[string]any
		activeLuna, inactiveLuna, retiredLuna int64
		exists, partial                       bool
		delegation                            string
		inactiveFrom                          *uint32
		checkedAt                             time.Time
		fromCache                             = true
	)
	for _, address := range addresses {
		st, verr := h.Chain.Verify(cctx, address)
		if verr != nil {
			if address == primary {
				// The wallet the card names could not be checked: there is no
				// honest position to show. A secondary wallet that fails is
				// reported as partial instead of failing the whole card.
				writeError(ctx, http.StatusBadGateway, "could not verify staking position")
				return
			}
			partial = true
			rows = append(rows, map[string]any{
				"address": poolstake.CanonicalAddress(address),
				"linked":  address != user.NimiqAddress,
				"error":   true,
			})
			continue
		}
		exists = exists || st.Exists
		activeLuna += st.ActiveLuna
		inactiveLuna += st.InactiveLuna
		retiredLuna += st.RetiredLuna
		if delegation == "" && st.Delegation != "" {
			delegation = st.Delegation
		}
		// The earliest "inactive since" marker among the wallets: a block
		// height, so the smallest number is the oldest.
		if st.InactiveLuna > 0 && st.InactiveFrom != nil && (inactiveFrom == nil || *st.InactiveFrom < *inactiveFrom) {
			inactiveFrom = st.InactiveFrom
		}
		if st.CheckedAt.After(checkedAt) {
			checkedAt = st.CheckedAt
		}
		fromCache = fromCache && st.FromCache
		row := map[string]any{
			"address":      poolstake.CanonicalAddress(address),
			"linked":       address != user.NimiqAddress,
			"exists":       st.Exists,
			"delegation":   st.Delegation,
			"active_nim":   float64(st.ActiveLuna) / lunaPerNIM,
			"inactive_nim": float64(st.InactiveLuna) / lunaPerNIM,
			"retired_nim":  float64(st.RetiredLuna) / lunaPerNIM,
			"from_cache":   st.FromCache,
		}
		if st.InactiveFrom != nil {
			row["inactive_from"] = st.InactiveFrom
		}
		rows = append(rows, row)
	}

	writeJSON(ctx, http.StatusOK, map[string]any{
		// The wallet this card names. With several linked wallets it is the one
		// linked first; the totals below cover all of them and `positions`
		// lists every wallet individually.
		"address": poolstake.CanonicalAddress(primary), "exists": exists,
		"account_address":         poolstake.CanonicalAddress(user.NimiqAddress),
		"stake_address_linked":    user.HasLinkedStakeAddress(),
		"stake_address_linked_at": user.StakeAddressLinkedAt(primary),
		"positions":               rows,
		"earning_addresses":       addresses,
		"partial":                 partial,
		"delegation":              delegation, "active_nim": float64(activeLuna) / lunaPerNIM,
		"inactive_nim": float64(inactiveLuna) / lunaPerNIM, "retired_nim": float64(retiredLuna) / lunaPerNIM,
		"inactive_from": inactiveFrom, "checked_at": checkedAt, "from_cache": fromCache,
	})
}
