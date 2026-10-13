package worker

import (
	"context"
	"log"
	"sync"
	"time"

	"nimiqshop/internal/db"
	"nimiqshop/internal/nimiq"
	"nimiqshop/internal/safe"
)

// Worker pays queued cashbacks from the shop wallet. Disabled when the
// seed is empty — queued rows wait until the operator funds/configures it.
//
// Durability / exactly-once:
//  1. Fulfill writes the cashback row in the same Badger txn (RPC is never
//     involved). A row is never "lost" because an RPC was down.
//  2. The signed tx hex is persisted BEFORE the first broadcast. A crash
//     after sign, before or after send, always resumes the SAME bytes.
//  3. Paid is set only when every configured RPC reports the tx confirmed.
//  4. One obligation per quote/winner. Replacement signatures require a
//     persisted finalized expiry proof; ambiguous outcomes remain pending.
type PayoutStore interface {
	ListCashbacksByStatus(string, int) ([]db.Cashback, error)
	ClaimCashbackSend(string) (db.Cashback, error)
	AttachCashbackSignedTx(string, string, string, uint32) error
	MarkCashbackBroadcast(string, string) error
	MarkCashbackPaid(string, string) error
	FailCashbackSend(string, string) error
	RetireExpiredPayout(string, string, nimiq.PayoutExpiryProof) error
}

type Worker struct {
	tickMu    sync.Mutex
	Store     PayoutStore
	RPC       *nimiq.Client
	SeedHex   string
	NetworkID byte
	FeeLuna   int64
	Enabled   bool
	Interval  time.Duration
}

func (w *Worker) Ready() bool {
	return w != nil && w.Enabled && w.SeedHex != "" && w.RPC != nil && w.Store != nil
}

func (w *Worker) Run(ctx context.Context) {
	if w == nil || w.Store == nil {
		return
	}
	if w.Interval <= 0 {
		w.Interval = 12 * time.Second
	}
	if w.FeeLuna < 1 {
		w.FeeLuna = 1
	}
	if !w.Ready() {
		// REAL payouts wait for seed/RPC — but the admin test center's
		// simulated rows (TestMode) flow through the same queue with zero
		// dependencies, so the pipeline is testable without a wallet.
		//
		// Even while idle, a configured seed is shown: WHICH wallet it derives
		// to and how it is funded. The operator should see the payout address
		// from the very first boot — before flipping CASHBACK_ENABLED on.
		if w.SeedHex != "" {
			addr, err := nimiq.AddressFromKeyHex(w.SeedHex)
			if err != nil {
				log.Printf("cashback: invalid CASHBACK_WALLET_SEED: %v", err)
			} else {
				log.Printf("cashback: worker idle for REAL payouts (enabled=%v) — shop wallet %s; admin TEST-center rows are still processed (simulated)", w.Enabled, addr)
				w.logBalance(addr)
			}
		} else {
			log.Printf("cashback: worker idle for REAL payouts (enabled=%v seed_set=false) — set CASHBACK_WALLET_SEED (+ CASHBACK_ENABLED=true) to activate; admin TEST-center rows are still processed (simulated)", w.Enabled)
		}
	} else {
		addr, err := nimiq.AddressFromKeyHex(w.SeedHex)
		if err != nil {
			log.Printf("cashback: invalid CASHBACK_WALLET_SEED: %v", err)
			return
		}
		log.Printf("cashback: worker on, shop wallet %s rpc=%v network_id=%d", addr, w.RPC.Endpoints(), w.NetworkID)
		// Balance banner on startup (and every hour): the operator sees the
		// funded state of the payout wallet without opening an explorer.
		w.logBalance(addr)
		safe.Go("cashback:balance-monitor", func() {
			t := time.NewTicker(time.Hour)
			defer t.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-t.C:
					func() {
						defer safe.Guard("cashback:balance", nil)
						w.logBalance(addr)
					}()
				}
			}
		})
	}

	safe.Go("cashback:worker", func() {
		t := time.NewTicker(w.Interval)
		defer t.Stop()
		w.tick(ctx)
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				func() {
					defer safe.Guard("cashback:tick", nil)
					w.tick(ctx)
				}()
			}
		}
	})
}

// logBalance prints the shop wallet's current on-chain balance in NIM.
// Never fatal: an unreachable RPC logs a warning and the next hourly tick
// (or restart) retries.
func (w *Worker) logBalance(addr string) {
	if w.RPC == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	luna, err := w.RPC.GetAccountBalance(ctx, addr)
	if err != nil {
		log.Printf("cashback: shop wallet %s balance unavailable: %v", addr, err)
		return
	}
	log.Printf("cashback: shop wallet %s balance: %.5f NIM (%d luna)", addr, float64(luna)/100_000, luna)
}

func (w *Worker) tick(ctx context.Context) {
	w.tickMu.Lock()
	defer w.tickMu.Unlock()
	if ctx.Err() != nil {
		return
	}
	real := w.Ready()
	broadcast, err := w.Store.ListCashbacksByStatus(db.CashbackBroadcast, 50)
	if err == nil {
		for _, cb := range broadcast {
			if !cb.TestMode && !real {
				continue // a real payout cannot move without the wallet; a test one can
			}
			w.confirm(ctx, cb)
		}
	}
	queued, err := w.Store.ListCashbacksByStatus(db.CashbackQueued, 20)
	if err != nil {
		return
	}
	sending, _ := w.Store.ListCashbacksByStatus(db.CashbackSending, 20)
	queued = append(queued, sending...)
	for _, cb := range queued {
		if ctx.Err() != nil {
			return
		}
		if !cb.TestMode && !real {
			continue
		}
		w.sendOne(ctx, cb)
	}
}

func (w *Worker) confirm(ctx context.Context, cb db.Cashback) {
	// TEST-CENTER ROW: no chain to ask — the simulated broadcast is
	// "confirmed" immediately, through the same paid path.
	if cb.TestMode {
		if err := w.Store.MarkCashbackPaid(cb.ID, cb.TxHash); err == nil {
			log.Printf("cashback: paid quote %s tx=%s luna=%d dest=%s (SIMULATED test payout)", cb.QuoteID, cb.TxHash, cb.AmountLuna, cb.CashbackDestination)
		}
		return
	}
	if cb.TxHash == "" {
		if cb.SignedTxHex != "" {
			// Hash never landed in the record — rebroadcast the persisted
			// bytes so the RPC can tell us the hash, then wait for confirm.
			w.rebroadcast(ctx, cb)
		}
		return
	}
	cctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	found, confirmed, err := w.RPC.GetTransactionByHash(cctx, cb.TxHash)
	if err != nil {
		_ = w.Store.FailCashbackSend(cb.ID, "confirmation unavailable: "+err.Error())
		w.rebroadcast(ctx, cb) // retry only the already persisted bytes
		return
	}
	if confirmed {
		if err := w.Store.MarkCashbackPaid(cb.ID, cb.TxHash); err == nil {
			log.Printf("cashback: paid quote %s tx=%s luna=%d dest=%s", cb.QuoteID, cb.TxHash, cb.AmountLuna, cb.CashbackDestination)
		}
		return
	}
	if found {
		w.rebroadcast(ctx, cb)
		return
	}
	if !found {
		// Both RPCs agree it is not visible yet — rebroadcast the same hex.
		w.rebroadcast(ctx, cb)
	}
}

func (w *Worker) sendOne(ctx context.Context, cb db.Cashback) {
	if cb.AmountLuna < 1 || cb.Recipient == "" {
		return
	}
	if cb.Status == db.CashbackPaid || cb.Status == db.CashbackSkipped || cb.Status == db.CashbackBroadcast {
		return
	}

	// TEST-CENTER ROW: walk the same queue states (queued → sending →
	// broadcast) but never touch the wallet — no seed, no signing, no RPC.
	// The TESTTX- hash marks the payout as simulated in every view.
	if cb.TestMode {
		claimed, err := w.Store.ClaimCashbackSend(cb.ID)
		if err != nil {
			return
		}
		hash := "TESTTX-" + claimed.ID[:8]
		if err := w.Store.MarkCashbackBroadcast(claimed.ID, hash); err == nil {
			log.Printf("cashback: broadcast quote %s → %s luna=%d memo=%q (SIMULATED test payout)", claimed.QuoteID, claimed.Recipient, claimed.AmountLuna, claimed.Memo)
		}
		return
	}

	claimed := cb
	if cb.SignedTxHex == "" {
		var err error
		claimed, err = w.Store.ClaimCashbackSend(cb.ID)
		if err != nil {
			return
		}
	}

	cctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()

	if claimed.TxHash != "" && claimed.SignedTxHex != "" {
		w.rebroadcast(cctx, claimed)
		return
	}

	txHex := claimed.SignedTxHex
	if txHex == "" {
		address, err := nimiq.AddressFromKeyHex(w.SeedHex)
		if err != nil {
			_ = w.Store.FailCashbackSend(claimed.ID, "wallet: "+err.Error())
			return
		}
		balance, err := w.RPC.GetAccountBalance(cctx, address)
		if err != nil {
			_ = w.Store.FailCashbackSend(claimed.ID, "balance unavailable: "+err.Error())
			return
		}
		if balance < claimed.AmountLuna || balance-claimed.AmountLuna < w.FeeLuna {
			_ = w.Store.FailCashbackSend(claimed.ID, "insufficient wallet balance; waiting for funding")
			return
		}
		height, err := w.RPC.GetBlockNumber(cctx)
		if err != nil {
			_ = w.Store.FailCashbackSend(claimed.ID, "block height: "+err.Error())
			return
		}
		signedHex, txHash, berr := nimiq.BuildBasicTransaction(w.SeedHex, claimed.Recipient, claimed.AmountLuna, w.FeeLuna, uint32(height), w.NetworkID, []byte(claimed.Memo))
		if berr != nil {
			_ = w.Store.FailCashbackSend(claimed.ID, "sign: "+berr.Error())
			return
		}
		txHex = signedHex
		// Persist the signed bytes AND their canonical hash BEFORE any network
		// write. A crash after broadcast but before we learn the node's reply
		// can then confirm the payout by this deterministic hash instead of
		// re-sending blind — no loss, no double-send, record self-heals.
		_, start, _, parseErr := nimiq.SignedTxInfo(txHex)
		if parseErr != nil {
			_ = w.Store.FailCashbackSend(claimed.ID, "signed transaction validation failed")
			return
		}
		if err := w.Store.AttachCashbackSignedTx(claimed.ID, txHex, txHash, start); err != nil {
			log.Printf("cashback: persist signed tx failed quote %s: %v", claimed.QuoteID, err)
			return
		}
		claimed.SignedTxHex = txHex
		claimed.TxHash = txHash
		claimed.ValidityStart = start
	}

	w.rebroadcast(cctx, claimed)
}

func (w *Worker) rebroadcast(ctx context.Context, cb db.Cashback) {
	if cb.SignedTxHex == "" {
		return
	}
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	// Reconcile the canonical hash before any retry. An accepted transaction
	// whose RPC reply was lost must be confirmed, not replaced.
	canonical, _, network, parseErr := nimiq.SignedTxInfo(cb.SignedTxHex)
	if parseErr != nil || network != w.NetworkID || (cb.TxHash != "" && cb.TxHash != canonical) {
		_ = w.Store.FailCashbackSend(cb.ID, "signed transaction identity/network requires operator review")
		return
	}
	found, confirmed, lookupErr := w.RPC.GetTransactionByHash(cctx, canonical)
	if lookupErr != nil {
		_ = w.Store.FailCashbackSend(cb.ID, "transaction lookup unavailable: "+lookupErr.Error())
		// Unknown lookup is not proof of non-payment, so never replace the
		// signature. Broadcasting these exact persisted bytes remains safe:
		// it is the same transaction identity even if the first RPC reply was lost.
	}
	if found {
		if err := w.Store.MarkCashbackBroadcast(cb.ID, canonical); err == nil && confirmed {
			_ = w.Store.MarkCashbackPaid(cb.ID, canonical)
		}
		if confirmed {
			return
		}
	}
	proof, expired, proofErr := w.RPC.ProvePayoutExpired(cctx, cb.SignedTxHex)
	if proofErr != nil {
		_ = w.Store.FailCashbackSend(cb.ID, "expiry proof unavailable: "+proofErr.Error())
		// No proof means no replacement. The same signed tx may still be sent.
	}
	if proofErr == nil && expired {
		if err := w.Store.RetireExpiredPayout(cb.ID, cb.SignedTxHex, proof); err != nil {
			log.Printf("cashback: expiry recovery deferred id=%s: %v", cb.ID, err)
		}
		return
	}
	if found {
		return
	}
	hash, err := w.RPC.BroadcastRawTransaction(cctx, cb.SignedTxHex)
	if err != nil {
		msg := err.Error()
		hint := cb.TxHash
		if hint == "" {
			hint = nimiq.SignedTxHash(cb.SignedTxHex)
		}
		// Ground truth over error strings: different RPCs word a duplicate
		// submission differently (some return a plain "internal error"), so a
		// rejected rebroadcast does NOT mean the payout is missing. If the
		// deterministic hash is already on chain, adopt it and let confirm()
		// finish the row — the coins moved exactly once.
		if hint != "" {
			if found, _, e := w.RPC.GetTransactionByHash(cctx, hint); e == nil && found {
				_ = w.Store.MarkCashbackBroadcast(cb.ID, hint)
				return
			}
		}
		// Genuinely not on chain: keep the signed bytes and retry next tick.
		_ = w.Store.FailCashbackSend(cb.ID, "broadcast: "+msg)
		log.Printf("cashback: broadcast failed quote %s: %v (will retry same tx)", cb.QuoteID, err)
		return
	}
	if hash == "" {
		return
	}
	if err := w.Store.MarkCashbackBroadcast(cb.ID, hash); err != nil {
		log.Printf("cashback: sent %s but failed to persist hash for %s: %v", hash, cb.ID, err)
	} else {
		log.Printf("cashback: broadcast quote %s → %s luna=%d tx=%s memo=%q", cb.QuoteID, cb.Recipient, cb.AmountLuna, hash, cb.Memo)
	}
}
