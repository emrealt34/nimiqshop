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
//   - sent by the buyer (see PaidBy),
//   - paid to the shop wallet (shop, normalised),
//   - exactly PriceLuna,
//   - memo bytes equal to the expected memo,
//   - executed successfully and included in a block.
//
// It returns ErrPending while the transaction is still unconfirmed, so the
// caller can ask the buyer's client to try again instead of rejecting it.
func VerifyTx(d nimiq.TxDetail, buyer, shop, memo string) error {
	if !PaidBy(d, buyer) ||
		d.To != nimiq.NormalizeAddress(shop) ||
		d.Value != PriceLuna ||
		string(d.Data) != memo {
		return ErrNotThisRating
	}
	if !d.Mined {
		return ErrPending
	}
	// Mempool responses have no execution result yet; only mined execution
	// can establish success or failure. Neither case bypasses memo/owner checks.
	if !d.Success {
		return ErrNotThisRating
	}
	return nil
}

// PaidBy reports whether the buyer's address paid the transaction.
//
// A wallet that holds NIM in a basic account (Nimiq Hub, Nimiq Wallet) sends
// from the buyer's own address, so `from` is the buyer. Nimiq Pay keeps the
// user's NIM in a per-user HTLC and pays from that contract: `from` is the
// contract's address (fromType 2) and the user's key — the address they signed
// in with — is one of the two signatures in the proof (user + Nimiq Pay
// co-signer). Matching the buyer against the proof's signers therefore proves
// the same thing the `from` comparison proves for a basic account: the buyer's
// own key authorised this exact payment.
func PaidBy(d nimiq.TxDetail, buyer string) bool {
	b := nimiq.NormalizeAddress(buyer)
	if b == "" {
		return false
	}
	if d.From == b {
		return true
	}
	if d.FromType != nimiq.AccountTypeBasic {
		for _, s := range d.Signers {
			if s == b {
				return true
			}
		}
	}
	return false
}

// PayerCandidates lists every address that may legitimately claim to have
// paid the transaction, the debited account first, then the proof's signers.
// A watcher that only knows the transaction uses it to look up which buyer's
// pending rating the payment settles.
func PayerCandidates(d nimiq.TxDetail) []string {
	out := make([]string, 0, 1+len(d.Signers))
	if d.From != "" {
		out = append(out, d.From)
	}
	if d.FromType != nimiq.AccountTypeBasic {
		for _, s := range d.Signers {
			if s != "" && s != d.From {
				out = append(out, s)
			}
		}
	}
	return out
}
