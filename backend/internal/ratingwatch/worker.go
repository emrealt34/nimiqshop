// Package ratingwatch indexes incoming rating payments independently of visits.
// It never signs or sends funds. The durable inbox, scan cursor and atomic
// rating proof ledger allow safe replay after RPC outages and process restarts.
package ratingwatch

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"nimiqshop/internal/activityvisibility"
	"nimiqshop/internal/db"
	"nimiqshop/internal/nimiq"
	"nimiqshop/internal/ratings"
)

type Chain interface {
	GetTransactionsByAddress(context.Context, string, int, string) ([]nimiq.Transaction, error)
	GetTransactionDetail(context.Context, string) (nimiq.TxDetail, bool, error)
}
type Worker struct {
	Store     *db.Store
	RPC       Chain
	Recipient string
}

// Run performs at most one 100-entry history page and ten new proof lookups
// per tick. Idle polling is one address-history request every thirty seconds.
func (w *Worker) Run(ctx context.Context) <-chan struct{} {
	done := make(chan struct{})
	log.Printf("ratings watcher: cashback wallet monitoring enabled (30s polling, durable checkpoints)")
	go func() {
		defer close(done)
		delay := time.Duration(0)
		for {
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
			tick, cancel := context.WithTimeout(ctx, 45*time.Second)
			catchingUp, err := w.Tick(tick, time.Now().UTC())
			cancel()
			delay = 30 * time.Second
			if err != nil {
				log.Printf("ratings watcher: synchronization deferred: %v", err)
			} else if catchingUp {
				delay = time.Second
			}
		}
	}()
	return done
}

func (w *Worker) Tick(ctx context.Context, now time.Time) (bool, error) {
	catchingUp, scanErr := w.scan(ctx)
	// Finish paging before applying revisions, including after a restart.
	if catchingUp || scanErr != nil {
		return catchingUp, scanErr
	}
	entries, err := w.Store.PendingRatingChainInbox(now, 10)
	if err != nil {
		return catchingUp, err
	}
	// Older payments for the same purchase are applied before newer revisions.
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].ChainAt.Before(entries[j].ChainAt) })
	for _, entry := range entries {
		if err := w.reconcile(ctx, entry, now); err != nil {
			return catchingUp, err
		}
	}
	return catchingUp, scanErr
}
func (w *Worker) scan(ctx context.Context) (bool, error) {
	state, err := w.Store.GetRatingChainScan(w.Recipient)
	if err != nil {
		return false, err
	}
	txs, err := w.RPC.GetTransactionsByAddress(ctx, w.Recipient, 100, state.Before)
	if err != nil {
		return false, err
	}
	if len(txs) == 0 {
		if state.Target == "" && state.Before == "" {
			return false, nil
		}
		if state.Target != "" {
			state.Head = state.Target
		}
		state.Target = ""
		state.Before = ""
		return false, w.Store.SaveRatingChainPage(w.Recipient, state, nil)
	}
	if state.Before == "" && state.Head != "" && txs[0].Hash == state.Head {
		return false, nil
	}
	if state.Target == "" {
		state.Target = txs[0].Hash
	}
	entries := make([]db.RatingChainInbox, 0)
	reached := false
	for _, tx := range txs {
		if tx.Hash == state.Head {
			reached = true
			break
		}
		if !nimiq.IsTxHash(tx.Hash) {
			return true, errors.New("invalid history transaction hash")
		}
		if nimiq.NormalizeAddress(tx.To) != nimiq.NormalizeAddress(w.Recipient) || tx.Value != ratings.PriceLuna {
			continue
		}
		// This only filters RPC metadata. A review is never accepted from history:
		// getTransactionByHash must independently verify every candidate below.
		if tx.RecipientData != nil {
			data, err := hex.DecodeString(*tx.RecipientData)
			if err != nil {
				return true, errors.New("invalid history memo encoding")
			}
			if _, _, ok := ratings.ParseMemo(string(data)); !ok {
				continue
			}
		}
		entries = append(entries, db.RatingChainInbox{Hash: strings.ToLower(tx.Hash), Status: "pending", ChainAt: time.UnixMilli(tx.Timestamp).UTC()})
	}
	if reached || len(txs) < 100 {
		state.Head = state.Target
		state.Target = ""
		state.Before = ""
	} else {
		next := txs[len(txs)-1].Hash
		if next == state.Before {
			return true, errors.New("history cursor did not advance")
		}
		state.Before = next
	}
	return state.Before != "", w.Store.SaveRatingChainPage(w.Recipient, state, entries)
}
func (w *Worker) reconcile(ctx context.Context, entry db.RatingChainInbox, now time.Time) error {
	if w.Store.RatingTxUsed(entry.Hash) {
		entry.Status = "applied"
		return w.Store.SaveRatingChainInbox(entry)
	}
	if !entry.Verified {
		proof, found, err := w.RPC.GetTransactionDetail(ctx, entry.Hash)
		if err != nil {
			return err
		}
		if !found || !proof.Mined {
			entry.NextAttempt = now.Add(30 * time.Second)
			return w.Store.SaveRatingChainInbox(entry)
		}
		stars, comment, ok := ratings.ParseMemo(string(proof.Data))
		if !ok || !strings.EqualFold(proof.Hash, entry.Hash) || ratings.VerifyTx(proof, proof.From, w.Recipient, string(proof.Data)) != nil {
			entry.Status = "ignored"
			return w.Store.SaveRatingChainInbox(entry)
		}
		// Who paid: the debited account for a basic wallet; for a Nimiq Pay
		// payment the debited account is the user's HTLC and the user is one
		// of the proof's signers. The buyer is whichever candidate reserved
		// this memo (or, for pre-intent releases, is a known user).
		buyer, err := w.resolveBuyer(proof)
		if err != nil {
			return err
		}
		entry.Verified = true
		entry.Buyer = buyer
		entry.Recipient = proof.To
		entry.Stars = stars
		entry.Comment = comment
		entry.Fingerprint = db.RatingIntentKey(buyer, proof.To, string(proof.Data))
	}
	intent, err := w.Store.GetRatingIntent(entry.Fingerprint)
	if errors.Is(err, db.ErrNotFound) {
		var found bool
		intent, found, err = w.legacyIntent(entry)
		if err != nil {
			return err
		}
		if !found {
			entry.Status = "unmatched"
			return w.Store.SaveRatingChainInbox(entry)
		}
	}
	if err != nil {
		return err
	}
	// Never attach an unrelated historical payment to a newly opened rating.
	if entry.ChainAt.Before(intent.CreatedAt.Add(-2 * time.Minute)) {
		entry.Status = "ignored"
		return w.Store.SaveRatingChainInbox(entry)
	}
	// Contents come exclusively from the verified chain proof; the durable
	// reservation supplies the authenticated purchase and owner association.
	_, err = w.Store.ApplyRating(db.RatingWrite{IntentKey: intent.Key, Kind: intent.Kind, ID: intent.ID, UserID: intent.UserID, Stars: entry.Stars, Comment: entry.Comment, TxHash: entry.Hash, MaxRatings: ratings.MaxRatingsPerOrder, Now: now})
	if err != nil {
		if errors.Is(err, db.ErrRatingRepeat) || errors.Is(err, db.ErrRatingLimit) {
			return w.Store.IgnoreRatingChainInbox(entry, intent)
		}
		if errors.Is(err, db.ErrRatingTxUsed) {
			entry.Status = "applied"
		} else {
			// A moderation switch or concurrent purchase update can be temporary.
			// Keep the proof; retry from disk without repeatedly fetching the chain.
			entry.NextAttempt = now.Add(5 * time.Minute)
			if saveErr := w.Store.SaveRatingChainInbox(entry); saveErr != nil {
				return saveErr
			}
			return fmt.Errorf("verified review could not be recorded: %w", err)
		}
	} else {
		entry.Status = "applied"
		log.Printf("ratings watcher: verified chain review recorded (kind=%s)", intent.Kind)
	}
	return w.Store.SaveRatingChainInbox(entry)
}

// resolveBuyer picks the buyer address a verified payment belongs to. The
// candidates are the debited account and, for contract senders, the keys that
// signed the proof (ratings.PayerCandidates). The first candidate with a
// pending reservation for this exact memo wins; without any reservation the
// first candidate that is a registered user does, so legacyIntent can still
// match a pre-intent payment; otherwise the debited account is kept.
func (w *Worker) resolveBuyer(proof nimiq.TxDetail) (string, error) {
	candidates := ratings.PayerCandidates(proof)
	if len(candidates) == 0 {
		return proof.From, nil
	}
	memo := string(proof.Data)
	for _, c := range candidates {
		_, err := w.Store.GetRatingIntent(db.RatingIntentKey(c, proof.To, memo))
		if err == nil {
			return c, nil
		}
		if !errors.Is(err, db.ErrNotFound) {
			return "", err
		}
	}
	for _, c := range candidates {
		_, err := w.Store.GetUserByAddress(c)
		if err == nil {
			return c, nil
		}
		if !errors.Is(err, db.ErrNotFound) {
			return "", err
		}
	}
	return candidates[0], nil
}

// Older releases did not persist intents. Recover only when the chain sender
// has exactly ONE real, delivered purchase at payment time. Ambiguous payments
// stay in the durable inbox; never guess a product or another buyer's order.
func (w *Worker) legacyIntent(entry db.RatingChainInbox) (db.RatingIntent, bool, error) {
	var intent db.RatingIntent
	if entry.ChainAt.IsZero() || entry.ChainAt.UnixMilli() <= 0 {
		return intent, false, nil
	}
	user, err := w.Store.GetUserByAddress(entry.Buyer)
	if errors.Is(err, db.ErrNotFound) {
		return intent, false, nil
	}
	if err != nil {
		return intent, false, err
	}
	orders, err := w.Store.ListOrders(user.ID, 1001)
	if err != nil {
		return intent, false, err
	}
	quotes, err := w.Store.ListQuotesForUser(user.ID, 1001)
	if err != nil {
		return intent, false, err
	}
	if len(orders) >= 1001 || len(quotes) >= 1001 {
		return intent, false, nil
	}
	count := 0
	accept := func(kind, id string, created, delivered time.Time) error {
		if created.After(entry.ChainAt) || delivered.IsZero() || delivered.After(entry.ChainAt) {
			return nil
		}
		state, err := w.Store.GetRatingState(kind, id, user.ID)
		if err != nil {
			return err
		}
		if !state.Delivered {
			return nil
		}
		count++
		intent = db.RatingIntent{Key: entry.Fingerprint, Kind: kind, ID: id, UserID: user.ID, Buyer: entry.Buyer, Recipient: entry.Recipient, CreatedAt: entry.ChainAt}
		return nil
	}
	for _, order := range orders {
		if !activityvisibility.Visible(order.ID, false) {
			continue
		}
		if err := accept("order", order.ID, order.CreatedAt, order.DeliveryTime()); err != nil {
			return intent, false, err
		}
	}
	for _, quote := range quotes {
		if !activityvisibility.Visible(quote.ID, quote.TestMode) {
			continue
		}
		if err := accept("quote", quote.ID, quote.CreatedAt, quote.DeliveryTime()); err != nil {
			return intent, false, err
		}
	}
	if count != 1 {
		return db.RatingIntent{}, false, nil
	}
	if err := w.Store.SaveRatingIntent(intent); errors.Is(err, db.ErrRatingIntentBusy) {
		existing, loadErr := w.Store.GetRatingIntent(entry.Fingerprint)
		return existing, loadErr == nil, loadErr
	} else if err != nil {
		return intent, false, err
	}
	return intent, true, nil
}
