// Package notification delivers the 1-Luna + memo notification channel.
//
// When an order is fulfilled (or a support ticket is answered) we send the
// buyer a 1-Luna Nimiq transaction whose memo carries the message ("Your
// shop.nimiqbase.com order is ready"). 1 Luna is ~0.00001 NIM — effectively free — but
// the memo turns it into a push notification that lands in the buyer's own
// wallet, no email or third party required.
//
// The sender is DISABLED by default (NOTIFICATION_ENABLED=false) because the
// signing path (see internal/nimiq/sign.go) must be validated against a real
// funded Nimiq node/key first, via cmd/notif-test. When disabled, Notify is a
// no-op, so enabling it never blocks or breaks order flow.
package notification

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"nimiqshop/internal/db"
	"nimiqshop/internal/nimiq"
)

const (
	// One notification is exactly 1 Luna (the buyer receives ~0.00001 NIM).
	NotificationLunas = int64(1)
	maxMemoLen        = 64 // Nimiq recipient-data ceiling; keep memos short.
)

// ShopName is the public shop hostname used in wallet-memo copy. Set once at
// process start from SITE_HOST (default shop.nimiqbase.com). Email lives in
// the mailtrap package — the SMTP/SMS gift client is gone for good.
var ShopName = "shop.nimiqbase.com"

type Notifier struct {
	rpc       *nimiq.Client
	store     *db.Store
	seedHex   string
	networkID byte
	feeLunas  int64
	enabled   bool
}

// New builds a Notifier. When enabled is false or the key is empty, Notify is a
// no-op, so this is always safe to construct.
func New(rpc *nimiq.Client, store *db.Store, seedHex string, networkID byte, feeLunas int64, enabled bool) *Notifier {
	return &Notifier{rpc: rpc, store: store, seedHex: seedHex, networkID: networkID, feeLunas: feeLunas, enabled: enabled}
}

// Enabled reports whether the sender is configured on.
func (n *Notifier) Enabled() bool { return n != nil && n.enabled && n.seedHex != "" }

// NotifyReason is the ONLY entry point callers should use. It applies the
// send policy (opt-out, per-reason cooldown, rolling monthly budget) before
// spending a line of the recipient's permanent wallet history, then records
// the send so the next policy check sees it.
//
// userID is the recipient's own NQ address (that is what the shop's auth
// uses as the user id), so it doubles as the delivery address.
//
// A refused send is NOT an error: it returns (false, nil). Only real
// failures (RPC, signing, broadcast) return an error.
func (n *Notifier) NotifyReason(ctx context.Context, reason Reason, refID, userID, detail string) (bool, error) {
	return n.notifyReason(ctx, reason, refID, userID, detail, false)
}

// NotifyReasonSimulated is the admin test-center path: the SAME policy
// pipeline (opt-out, cooldown, budget, send-ledger recording, idempotency)
// runs, but nothing is ever signed or broadcast — there is no RPC call at
// all. Everything works; only the payment is simulated.
func (n *Notifier) NotifyReasonSimulated(ctx context.Context, reason Reason, refID, userID, detail string) (bool, error) {
	return n.notifyReason(ctx, reason, refID, userID, detail, true)
}

func (n *Notifier) notifyReason(ctx context.Context, reason Reason, refID, userID, detail string, simulate bool) (bool, error) {
	if !n.Enabled() || strings.TrimSpace(userID) == "" {
		return false, nil
	}
	if !ValidReason(reason) {
		// Refusing an unknown reason is deliberate: it is the guard that
		// stops someone bolting a marketing blast onto this channel.
		log.Printf("notif: refusing unknown reason %q (ref %s)", reason, refID)
		return false, nil
	}

	optedOut := false
	if enabled, err := n.store.WalletNotifyEnabled(userID); err == nil {
		optedOut = !enabled
	}
	lastSame, _ := n.store.LastNotifySend(userID, string(reason))
	budgetUsed := 0
	if CountsToBudget(reason) {
		budgeted := make([]string, 0, 2)
		for _, r := range Reasons() {
			if CountsToBudget(r) {
				budgeted = append(budgeted, string(r))
			}
		}
		budgetUsed, _ = n.store.NotifyBudgetUsed(userID, budgeted, time.Now().UTC().Add(-30*24*time.Hour))
	}

	if d := Allow(reason, optedOut, lastSame, budgetUsed, time.Now().UTC()); !d.Allowed {
		log.Printf("notif: suppressed %s for %s (ref %s): %s", reason, userID, refID, d.Blocked)
		return false, nil
	}

	if simulate {
		if err := n.notifySimulated(refID, userID, Memo(reason, detail)); err != nil {
			return false, err
		}
	} else if err := n.Notify(ctx, refID, userID, Memo(reason, detail)); err != nil {
		return false, err
	}
	if err := n.store.RecordNotifySend(userID, string(reason), time.Now().UTC()); err != nil {
		// The message is already on-chain; failing to record it can only
		// make the NEXT send more conservative, never less. Log and move on.
		log.Printf("notif: sent %s to %s but failed to record the send ledger: %v", reason, userID, err)
	}
	return true, nil
}

// Notify sends 1 Luna + memo to recipientFriendly, idempotent on refID. It is
// best-effort: callers fire it in a goroutine and log the error rather than
// failing the surrounding flow — a missed notification must never break an order.
//
// Prefer NotifyReason: this raw form bypasses the frequency policy and
// exists for the fulfilled/cashback paths that are already one-per-object.
func (n *Notifier) Notify(ctx context.Context, refID, recipientFriendly, memo string) error {
	_, err := n.NotifyTx(ctx, refID, recipientFriendly, memo)
	return err
}

// NotifyTx is Notify that also reports the broadcast transaction hash. Callers
// that publish the memo as a public PROOF (star ratings do: the memo is the
// proof) need the hash to link; everyone else keeps calling Notify. An empty
// hash with a nil error means the idempotency ledger had already sent this ref,
// so nothing was broadcast this time.
func (n *Notifier) NotifyTx(ctx context.Context, refID, recipientFriendly, memo string) (string, error) {
	if !n.Enabled() || recipientFriendly == "" {
		return "", nil
	}

	sent, err := n.store.IsNotificationSent(refID)
	if err != nil {
		return "", fmt.Errorf("notif: check sent: %w", err)
	}
	if sent {
		return "", nil // already delivered — never double-send
	}

	memo = TrimMemo(memo)

	height, err := n.rpc.GetBlockNumber(ctx)
	if err != nil {
		return "", fmt.Errorf("notif: block height: %w", err)
	}

	txHex, _, err := nimiq.BuildBasicTransaction(n.seedHex, recipientFriendly, NotificationLunas, n.feeLunas, uint32(height), n.networkID, []byte(memo))
	if err != nil {
		return "", fmt.Errorf("notif: build/sign: %w", err)
	}

	txHash, err := n.rpc.BroadcastRawTransaction(ctx, txHex)
	if err != nil {
		return "", fmt.Errorf("notif: broadcast: %w", err)
	}

	if err := n.store.MarkNotificationSent(refID); err != nil {
		log.Printf("notif: sent tx %s to %s but failed to record idempotency for %s: %v", txHash, recipientFriendly, refID, err)
	}
	log.Printf("notif: sent 1 Luna + memo to %s (ref %s) tx=%s", recipientFriendly, refID, txHash)
	return txHash, nil
}

// RatingMemo is the PUBLIC proof line for a star rating. The memo is what a
// block explorer shows, so it names the shop, the stars and the order the
// rating belongs to: anyone can read it off the chain, forever, without
// trusting our database. Order ids are UUIDs — the first block is enough to
// recognise one, and it keeps the line inside the 64-byte memo ceiling.
func RatingMemo(orderID string, stars int) string {
	if stars < 1 {
		stars = 1
	}
	if stars > 5 {
		stars = 5
	}
	id := strings.TrimSpace(orderID)
	if len(id) > 8 {
		id = id[:8]
	}
	return TrimMemo(fmt.Sprintf("%s rating %d/5 order %s", shopHost(), stars, id))
}

// notifySimulated is Notify with the chain steps replaced by a marker: the
// idempotency ledger is written exactly like a real send (so repeat runs are
// still at-most-once and the admin views show the notification), but no
// transaction is built, signed or broadcast.
func (n *Notifier) notifySimulated(refID, recipientFriendly, memo string) error {
	sent, err := n.store.IsNotificationSent(refID)
	if err != nil {
		return fmt.Errorf("notif: check sent: %w", err)
	}
	if sent {
		return nil // already delivered — never double-send
	}
	if err := n.store.MarkNotificationSent(refID); err != nil {
		log.Printf("notif: simulated memo to %s (ref %s) but failed to record idempotency: %v", recipientFriendly, refID, err)
	}
	log.Printf("notif: SIMULATED 1 Luna + memo to %s (ref %s) memo=%q — test center, nothing broadcast", recipientFriendly, refID, memo)
	return nil
}
