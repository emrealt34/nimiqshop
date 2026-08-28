// Package notification delivers the 1-Luna + memo notification channel.
//
// When an order is fulfilled (or a support ticket is answered) we send the
// buyer a 1-Luna Nimiq transaction whose memo carries the message ("Your
// nim.shop order is ready"). 1 Luna is ~0.00001 NIM — effectively free — but
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

	"nimiqshop/internal/db"
	"nimiqshop/internal/nimiq"
)

const (
	// One notification is exactly 1 Luna (the buyer receives ~0.00001 NIM).
	NotificationLunas = int64(1)
	maxMemoLen        = 64 // Nimiq recipient-data ceiling; keep memos short.
)

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

// Notify sends 1 Luna + memo to recipientFriendly, idempotent on refID. It is
// best-effort: callers fire it in a goroutine and log the error rather than
// failing the surrounding flow — a missed notification must never break an order.
func (n *Notifier) Notify(ctx context.Context, refID, recipientFriendly, memo string) error {
	if !n.Enabled() || recipientFriendly == "" {
		return nil
	}

	sent, err := n.store.IsNotificationSent(refID)
	if err != nil {
		return fmt.Errorf("notif: check sent: %w", err)
	}
	if sent {
		return nil // already delivered — never double-send
	}

	if len(memo) > maxMemoLen {
		memo = memo[:maxMemoLen]
	}

	height, err := n.rpc.GetBlockNumber(ctx)
	if err != nil {
		return fmt.Errorf("notif: block height: %w", err)
	}

	txHex, err := nimiq.BuildBasicTransaction(n.seedHex, recipientFriendly, NotificationLunas, n.feeLunas, uint32(height), n.networkID, []byte(memo))
	if err != nil {
		return fmt.Errorf("notif: build/sign: %w", err)
	}

	txHash, err := n.rpc.BroadcastRawTransaction(ctx, txHex)
	if err != nil {
		return fmt.Errorf("notif: broadcast: %w", err)
	}

	if err := n.store.MarkNotificationSent(refID); err != nil {
		log.Printf("notif: sent tx %s to %s but failed to record idempotency for %s: %v", txHash, recipientFriendly, refID, err)
	}
	log.Printf("notif: sent 1 Luna + memo to %s (ref %s) tx=%s", recipientFriendly, refID, txHash)
	return nil
}
