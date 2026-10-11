package handlers

import (
	"context"
	"strings"
	"sync"
	"time"

	"nimiqshop/internal/nimiq"
)

type adminCashbackWalletView struct {
	Address     string     `json:"address"`
	Configured  bool       `json:"configured"`
	Enabled     bool       `json:"enabled"`
	Available   bool       `json:"available"`
	Network     string     `json:"network"`
	BalanceLuna *int64     `json:"balance_luna"`
	BalanceNIM  *float64   `json:"balance_nim"`
	ObservedAt  *time.Time `json:"observed_at"`
	Reason      string     `json:"reason,omitempty"`
}

// A cache belongs to this handler/configuration, never to the customer wallet
// cache. Concurrent Overview cards share one bounded read, including failures.
type adminCashbackWalletCache struct {
	mu      sync.Mutex
	view    adminCashbackWalletView
	expires time.Time
}

func (h *Handlers) adminCashbackWallet() adminCashbackWalletView {
	c := &h.cashbackWallet
	c.mu.Lock()
	defer c.mu.Unlock()
	if time.Now().Before(c.expires) {
		return c.view
	}
	view := adminCashbackWalletView{Enabled: h.Cfg.CashbackEnabled, Network: h.walletNetwork()}
	// Server boot resolves mnemonic + account index to this canonical signing
	// key BEFORE constructing both Handlers and the payout worker. Derive only
	// the public address, using exactly the same function as the worker.
	if strings.TrimSpace(h.Cfg.CashbackWalletSeed) == "" {
		view.Reason = "not_configured"
	} else if address, err := nimiq.AddressFromKeyHex(h.Cfg.CashbackWalletSeed); err != nil {
		view.Reason = "invalid_configuration"
	} else {
		view.Address, view.Configured = address, true
		if h.NIMRPC == nil || len(h.NIMRPC.Endpoints()) == 0 {
			view.Reason = "rpc_unavailable"
		} else {
			// Balance failure must not take down the operational dashboard. This
			// is the liquid balance available for payouts, not delegated stake.
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			luna, err := h.NIMRPC.GetAccountBalance(ctx, address)
			cancel()
			if err != nil {
				view.Reason = "balance_unavailable"
			} else {
				nim, at := lunaToNIM(luna), time.Now().UTC()
				view.Available, view.BalanceLuna, view.BalanceNIM, view.ObservedAt = true, &luna, &nim, &at
			}
		}
	}
	c.view, c.expires = view, time.Now().Add(15*time.Second)
	return view
}
