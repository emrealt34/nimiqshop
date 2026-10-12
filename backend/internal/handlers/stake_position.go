package handlers

import (
	"context"
	"github.com/valyala/fasthttp"
	"net/http"
	"nimiqshop/internal/middleware"
	"nimiqshop/internal/poolstake"
	"time"
)

// StakePosition reads only the authenticated wallet, independent of pool indexing.
// Validator names/logos are supplied separately by the cached validator worker API.
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
	if string(ctx.Method()) == http.MethodPost {
		h.Chain.Forget(user.NimiqAddress)
	}
	cctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	st, err := h.Chain.Verify(cctx, user.NimiqAddress)
	if err != nil {
		writeError(ctx, http.StatusBadGateway, "could not verify staking position")
		return
	}
	writeJSON(ctx, http.StatusOK, map[string]any{
		"address": poolstake.CanonicalAddress(user.NimiqAddress), "exists": st.Exists,
		"delegation": st.Delegation, "active_nim": float64(st.ActiveLuna) / lunaPerNIM,
		"inactive_nim": float64(st.InactiveLuna) / lunaPerNIM, "retired_nim": float64(st.RetiredLuna) / lunaPerNIM,
		"inactive_from": st.InactiveFrom, "checked_at": st.CheckedAt, "from_cache": st.FromCache,
	})
}
