package ratings

import (
	"errors"

	"nimiqshop/internal/nimiq"
)

// PriceLuna is what the buyer pays for one rating (1 Luna). The fee is set by
// configuration and paid on top, by the buyer's wallet.
const PriceLuna = 1

var (
	// ErrPending means the chain has not confirmed the transaction yet.
	ErrPending = errors.New("transaction not confirmed yet")
	// ErrNotThisRating means the transaction does not match the rating: wrong
	// sender, wrong recipient, wrong amount, a memo that differs, or it failed.
	ErrNotThisRating = errors.New("transaction does not match this rating")
)

// VerifyTx checks one on-chain transaction against the rating it must prove:
//   - sent by the buyer's own address (buyer, normalised),
//   - paid to the shop wallet (shop, normalised),
//   - exactly PriceLuna,
//   - memo bytes equal to the expected memo,
//   - executed successfully and included in a block.
//
// It returns ErrPending while the transaction is still unconfirmed, so the
// caller can ask the buyer's client to try again instead of rejecting it.
func VerifyTx(d nimiq.TxDetail, buyer, shop, memo string) error {
	if d.From != nimiq.NormalizeAddress(buyer) ||
		d.To != nimiq.NormalizeAddress(shop) ||
		d.Value != PriceLuna ||
		string(d.Data) != memo ||
		!d.Success {
		return ErrNotThisRating
	}
	if !d.Mined {
		return ErrPending
	}
	return nil
}
