package handlers

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/valyala/fasthttp"
	"nimiqshop/internal/nimiq"
)

type walletStakeEntry struct {
	active, inactive, retired int64
	at                        time.Time
}

var walletStakeCache = struct {
	sync.Mutex
	entries map[string]walletStakeEntry
}{entries: make(map[string]walletStakeEntry)}

// PublicWalletStake exposes public chain amounts only, with a validated address,
// fixed RPC method, rate limiting and a bounded cache. No shop session is needed.
func (h *Handlers) PublicWalletStake(ctx *fasthttp.RequestCtx) {
	address := nimiq.NormalizeAddress(string(ctx.QueryArgs().Peek("address")))
	if nimiq.ValidateAddress(address) != nil {
		writeError(ctx, fasthttp.StatusBadRequest, "invalid Nimiq address")
		return
	}
	entry, ok := h.readWalletStake(address, string(ctx.QueryArgs().Peek("fresh")) == "1")
	if !ok {
		writeError(ctx, fasthttp.StatusBadGateway, "staking lookup unavailable")
		return
	}
	writeJSON(ctx, fasthttp.StatusOK, map[string]any{
		"address": address, "staked_nim": lunaToNIM(entry.active),
		"inactive_nim": lunaToNIM(entry.inactive), "retired_nim": lunaToNIM(entry.retired),
		"network": h.walletNetwork(), "observed_at": entry.at,
	})
}

func (h *Handlers) readWalletStake(address string, fresh bool) (walletStakeEntry, bool) {
	if h.NIMRPC == nil {
		return walletStakeEntry{}, false
	}
	// Scope the cache to the reader/network as well as the address (test stacks
	// and a changed RPC configuration must not share an old staking result).
	key := strings.Join(h.NIMRPC.Endpoints(), ",") + ":" + h.walletNetwork() + ":" + nimiq.NormalizeAddress(address)
	now := time.Now()
	walletStakeCache.Lock()
	hit, found := walletStakeCache.entries[key]
	walletStakeCache.Unlock()
	if !fresh && found && now.Sub(hit.at) < balanceCacheTTL {
		return hit, true
	}
	cctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	st, err := h.NIMRPC.GetStaker(cctx, address)
	if err != nil {
		var rpcErr *nimiq.RPCError
		if !errors.As(err, &rpcErr) {
			return walletStakeEntry{}, false
		}
		// Detail: the live node puts "No staker with address: …" in the error's
		// data field and leaves the message generic.
		message := strings.ToLower(rpcErr.Detail())
		if !strings.Contains(message, "no staker") && !strings.Contains(message, "not a staker") {
			return walletStakeEntry{}, false
		}
	}
	entry := walletStakeEntry{active: st.Balance, inactive: st.InactiveBalance, retired: st.RetiredBalance, at: now}
	walletStakeCache.Lock()
	if len(walletStakeCache.entries) >= 512 {
		for k, v := range walletStakeCache.entries {
			if now.Sub(v.at) >= balanceCacheTTL {
				delete(walletStakeCache.entries, k)
			}
		}
	}
	if len(walletStakeCache.entries) < 512 || found {
		walletStakeCache.entries[key] = entry
	}
	walletStakeCache.Unlock()
	return entry, true
}
