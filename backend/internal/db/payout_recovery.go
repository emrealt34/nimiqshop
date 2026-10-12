package db

import (
	"github.com/dgraph-io/badger/v4"
	"nimiqshop/internal/nimiq"
	"time"
)

func retireExpiredPayout(cb *Cashback, raw string, proof nimiq.PayoutExpiryProof) error {
	if cb.Status != CashbackSending && cb.Status != CashbackBroadcast {
		return ErrConflict
	}
	if cb.TestMode || cb.SignedTxHex != raw || (cb.TxHash != "" && cb.TxHash != proof.Hash) {
		return ErrConflict
	}
	hash, start, _, err := nimiq.SignedTxInfo(raw)
	if err != nil || hash != proof.Hash || start != proof.ValidityStart || proof.Window != 7200 || uint64(proof.FinalizedHeight) <= uint64(start)+uint64(proof.Window) || !nimiq.IsTxHash(proof.FinalizedHash) {
		return ErrConflict
	}
	cb.ExpiredAttempts = append(cb.ExpiredAttempts, ExpiredPayoutAttempt{SignedTxHex: raw, Proof: proof, RetiredAt: time.Now().UTC()})
	cb.SignedTxHex = ""
	cb.TxHash = ""
	cb.ValidityStart = 0
	cb.SendingAt = nil
	cb.Status = CashbackQueued
	cb.LastError = "Previous transaction expired without payment; queued for a fresh signed attempt"
	return nil
}
func (s *Store) RetireExpiredPayout(id, raw string, proof nimiq.PayoutExpiryProof) error {
	return s.patchCashback(id, func(cb *Cashback, tx *badger.Txn) error {
		old := cb.Status
		if err := retireExpiredPayout(cb, raw, proof); err != nil {
			return err
		}
		if err := tx.Delete(cashbackStatusIndexKey(old, cb.ID)); err != nil {
			return err
		}
		return tx.Set(cashbackStatusIndexKey(cb.Status, cb.ID), []byte(cb.ID))
	})
}
func (q QuizPayoutStore) RetireExpiredPayout(id, raw string, proof nimiq.PayoutExpiryProof) error {
	return q.patch(id, func(row *QuizPayout, _ *badger.Txn) error { return retireExpiredPayout(&row.Cashback, raw, proof) })
}

// A unique obligation reference prevents two legitimate same-value payouts to
// the same wallet in the same block from producing identical transactions.
func uniqueCashbackMemo(memo, id string) string {
	suffix := " [" + id + "]"
	if len(suffix) >= nimiq.MaxMemoLen {
		return memo
	}
	text := []rune(memo)
	for len([]byte(string(text)))+len(suffix) > nimiq.MaxMemoLen {
		text = text[:len(text)-1]
	}
	return string(text) + suffix
}
