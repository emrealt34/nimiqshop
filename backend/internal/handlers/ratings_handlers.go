package handlers

/* ratings_handlers.go — the buyer-signed on-chain star rating.

The buyer's own wallet signs and pays for every rating: 1 Luna plus the network
fee, sent to the shop wallet with the memo "<stars>[ <comment>]". The shop never
spends anything on a rating. The rating is saved only after the chain shows
that exact transaction: paid by the buyer, paid to the shop, the right amount,
the right memo, executed and included in a block. Each transaction proves one
rating, once.

"Paid by the buyer" (ratings.PaidBy) means the transaction's sender is the
buyer's signed-in address, or — for Nimiq Pay, which keeps the user's NIM in a
per-user HTLC and pays from that contract — the buyer's key is one of the
signatures in the transaction proof. The chain's `from` alone would reject
every Nimiq Pay rating with "transaction does not match this rating".

Flow:
  1. POST …/rating/intent  → memo, recipient, amount and fee to show the buyer
  2. the buyer's wallet (Nimiq Pay or Hub) signs and broadcasts the transaction
  3. POST …/rate          → the shop finds that transaction on chain and saves
                            the rating; 202 while the chain has not confirmed it yet
*/

import (
	"context"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/valyala/fasthttp"

	"nimiqshop/internal/db"
	"nimiqshop/internal/middleware"
	"nimiqshop/internal/nimiq"
	"nimiqshop/internal/ratings"
)

const ratingChainTimeout = 12 * time.Second

func (h *Handlers) ratingWired() bool {
	return h.RatingRPC != nil && h.RatingRecipient != ""
}

// RatingConfig tells the storefront whether ratings are on and what a rating
// costs. Public and read-only.
func (h *Handlers) RatingConfig(ctx *fasthttp.RequestCtx) {
	writeJSON(ctx, fasthttp.StatusOK, map[string]interface{}{
		"enabled":          h.ratingWired(),
		"recipient":        h.RatingRecipient,
		"value_luna":       ratings.PriceLuna,
		"fee_luna":         h.RatingFeeLuna,
		"max_comment":      ratings.MaxComment,
		"max_ratings":      ratings.MaxRatingsPerOrder,
		"comments_enabled": h.Store.RatingCommentsEnabled(),
	})
}

// RatingComments lists the newest visible buyer comments. No purchase ids and
// no addresses are returned: a comment is words and stars only.
func (h *Handlers) RatingComments(ctx *fasthttp.RequestCtx) {
	items, err := h.Store.ListRatingComments(30, false)
	if err != nil {
		writeError(ctx, fasthttp.StatusInternalServerError, "could not load comments")
		return
	}
	out := make([]map[string]interface{}, 0, len(items))
	for _, c := range items {
		row := map[string]interface{}{
			"stars":   c.Stars,
			"comment": c.Comment,
			"at":      c.At,
		}
		// Who left it, as the feed is allowed to say it. The identity was
		// captured when the rating was WRITTEN (db.commentIdentity): a
		// public row carries no purchase id, so nothing about the buyer
		// can be resolved from this list. Comments written before that
		// simply arrive without a face.
		if c.Address != "" {
			row["address"] = c.Address
		}
		if label := cleanPublicProductLabel(c.Product); label != "" {
			row["product"] = label
		}
		if c.TestMode {
			row["test_mode"] = true
		}
		out = append(out, row)
	}
	writeJSON(ctx, fasthttp.StatusOK, map[string]interface{}{
		"comments_enabled": h.Store.RatingCommentsEnabled(),
		"items":            out,
	})
}

// prepareRating runs every check that needs no chain access. It writes the
// error response itself and returns ok=false when the rating must not go on.
// On success it returns the memo the buyer must sign, the purchase state and
// the buyer's own address.
func (h *Handlers) prepareRating(ctx *fasthttp.RequestCtx, kind, id, userID string, req ratingRequest) (memo string, st db.RatingState, buyer string, ok bool) {
	if !h.ratingWired() {
		writeError(ctx, fasthttp.StatusServiceUnavailable, "star ratings are not available right now")
		return "", st, "", false
	}
	comment, err := ratings.NormalizeComment(req.Comment)
	if err != nil {
		writeError(ctx, fasthttp.StatusBadRequest, err.Error())
		return "", st, "", false
	}
	if comment != "" && !h.Store.RatingCommentsEnabled() {
		writeError(ctx, fasthttp.StatusForbidden, db.ErrCommentsOff.Error())
		return "", st, "", false
	}
	memo, err = ratings.Memo(req.Stars, comment)
	if err != nil {
		writeError(ctx, fasthttp.StatusBadRequest, err.Error())
		return "", st, "", false
	}
	st, err = h.Store.GetRatingState(kind, id, userID)
	if errors.Is(err, db.ErrNotFound) {
		writeError(ctx, fasthttp.StatusNotFound, "order not found")
		return "", st, "", false
	}
	if err != nil {
		writeError(ctx, fasthttp.StatusInternalServerError, "could not load the purchase")
		return "", st, "", false
	}
	if !st.Delivered {
		writeError(ctx, fasthttp.StatusConflict, "this order cannot be rated yet (delivery must complete)")
		return "", st, "", false
	}
	if st.Rated && st.Stars == req.Stars && st.Comment == comment {
		writeError(ctx, fasthttp.StatusConflict, "this rating is already saved")
		return "", st, "", false
	}
	if st.Edits >= ratings.MaxRatingsPerOrder {
		writeError(ctx, fasthttp.StatusConflict, "this purchase already has the maximum number of ratings")
		return "", st, "", false
	}
	addrs, _ := h.Store.UserAddresses([]string{userID})
	buyer = addrs[userID]
	if buyer == "" {
		writeError(ctx, fasthttp.StatusConflict, "sign in with your wallet to rate")
		return "", st, "", false
	}
	return memo, st, buyer, true
}

// RatingIntentOrder / RatingIntentQuote return what the buyer's wallet must send.
func (h *Handlers) RatingIntentOrder(ctx *fasthttp.RequestCtx) {
	h.ratingIntent(ctx, db.RatingKindOrder)
}

func (h *Handlers) RatingIntentQuote(ctx *fasthttp.RequestCtx) {
	h.ratingIntent(ctx, db.RatingKindQuote)
}

func (h *Handlers) ratingIntent(ctx *fasthttp.RequestCtx, kind string) {
	id, _ := ctx.UserValue("id").(string)
	userID := middleware.UserID(ctx)
	req, err := decodeRatingRequest(ctx.PostBody())
	if err != nil {
		writeError(ctx, fasthttp.StatusBadRequest, err.Error())
		return
	}
	memo, _, buyer, ok := h.prepareRating(ctx, kind, id, userID, req)
	if !ok {
		return
	}
	if !h.rememberRatingIntent(ctx, kind, id, userID, buyer, memo) {
		return
	}
	writeJSON(ctx, fasthttp.StatusOK, map[string]interface{}{
		"memo":       memo,
		"recipient":  h.RatingRecipient,
		"value_luna": ratings.PriceLuna,
		"fee_luna":   h.RatingFeeLuna,
	})
}

// RateOrder / RateQuote save a rating once the chain shows the buyer's transaction.
func (h *Handlers) RateOrder(ctx *fasthttp.RequestCtx) {
	h.submitRating(ctx, db.RatingKindOrder)
}

func (h *Handlers) RateQuote(ctx *fasthttp.RequestCtx) {
	h.submitRating(ctx, db.RatingKindQuote)
}

func (h *Handlers) submitRating(ctx *fasthttp.RequestCtx, kind string) {
	id, _ := ctx.UserValue("id").(string)
	userID := middleware.UserID(ctx)
	req, err := decodeRatingRequest(ctx.PostBody())
	if err != nil {
		writeError(ctx, fasthttp.StatusBadRequest, err.Error())
		return
	}
	// A lost HTTP acknowledgement must not require another rating payment.
	// Only acknowledge the same owner's already verified, identical rating.
	comment, commentError := ratings.NormalizeComment(req.Comment)
	if saved, err := h.Store.GetRatingState(kind, id, userID); err == nil && commentError == nil && saved.Rated && saved.RatingTx != "" && saved.Stars == req.Stars && saved.Comment == comment && (req.TxHash == "" || req.TxHash == saved.RatingTx) {
		agg, err := h.Store.PublicRatingAggregate()
		if err != nil {
			writeError(ctx, 500, "could not load rating summary")
			return
		}
		writeJSON(ctx, 200, map[string]any{"order_id": id, "rating": saved.Stars, "comment": saved.Comment, "rated_at": saved.RatedAt, "rating_tx": saved.RatingTx, "edits_left": ratings.MaxRatingsPerOrder - saved.Edits, "summary": ratingSummaryShape(agg)})
		return
	}
	memo, st, buyer, ok := h.prepareRating(ctx, kind, id, userID, req)
	if !ok {
		return
	}

	if !h.rememberRatingIntent(ctx, kind, id, userID, buyer, memo) {
		return
	}

	cctx, cancel := context.WithTimeout(context.Background(), ratingChainTimeout)
	defer cancel()

	var proof nimiq.TxDetail
	if req.TxHash != "" {
		if !nimiq.IsTxHash(req.TxHash) {
			writeError(ctx, fasthttp.StatusBadRequest, "tx_hash must be a 64-character transaction hash")
			return
		}
		det, found, err := h.RatingRPC.GetTransactionDetail(cctx, req.TxHash)
		if err != nil {
			writeError(ctx, fasthttp.StatusServiceUnavailable, "could not reach the Nimiq network, try again")
			return
		}
		if !found {
			writePendingRating(ctx)
			return
		}
		switch err := ratings.VerifyTx(det, buyer, h.RatingRecipient, memo); {
		case errors.Is(err, ratings.ErrPending):
			writePendingRating(ctx)
			return
		case err != nil:
			writeError(ctx, fasthttp.StatusBadRequest, ratings.ErrNotThisRating.Error())
			return
		}
		proof = det
	} else {
		det, found, err := h.findRatingTx(cctx, buyer, memo)
		if err != nil {
			writeError(ctx, fasthttp.StatusServiceUnavailable, "could not reach the Nimiq network, try again")
			return
		}
		if !found {
			writePendingRating(ctx)
			return
		}
		proof = det
	}

	// The verified on-chain memo is the source of the stored review.
	// Browser fields select the requested memo, never establish a rating.
	chainStars, chainComment, parsed := ratings.ParseMemo(string(proof.Data))
	if !parsed {
		writeError(ctx, 400, "invalid on-chain rating memo")
		return
	}
	agg, err := h.Store.ApplyRating(db.RatingWrite{
		IntentKey:  db.RatingIntentKey(buyer, h.RatingRecipient, memo),
		Kind:       kind,
		ID:         id,
		UserID:     userID,
		Stars:      chainStars,
		Comment:    chainComment,
		TxHash:     proof.Hash,
		MaxRatings: ratings.MaxRatingsPerOrder,
		Now:        time.Now().UTC(),
	})
	switch {
	case errors.Is(err, db.ErrNotFound):
		writeError(ctx, fasthttp.StatusNotFound, "order not found")
		return
	case errors.Is(err, db.ErrConflict):
		writeError(ctx, fasthttp.StatusConflict, "this order cannot be rated yet (delivery must complete)")
		return
	case errors.Is(err, db.ErrRatingRepeat):
		writeError(ctx, fasthttp.StatusConflict, "this rating is already saved")
		return
	case errors.Is(err, db.ErrRatingLimit):
		writeError(ctx, fasthttp.StatusConflict, "this purchase already has the maximum number of ratings")
		return
	case errors.Is(err, db.ErrRatingTxUsed):
		writeError(ctx, fasthttp.StatusConflict, "this transaction was already used for a rating")
		return
	case errors.Is(err, db.ErrCommentsOff):
		writeError(ctx, fasthttp.StatusForbidden, db.ErrCommentsOff.Error())
		return
	case err != nil:
		writeError(ctx, fasthttp.StatusInternalServerError, "could not save rating")
		return
	}
	// The buyer sees the same summary the storefront shows: ApplyRating
	// returns the global counter, which still includes hidden test orders.
	publicAgg, perr := h.Store.PublicRatingAggregate()
	if perr != nil {
		publicAgg = agg
	}

	writeJSON(ctx, fasthttp.StatusOK, map[string]interface{}{
		"order_id":   id,
		"rating":     chainStars,
		"comment":    chainComment,
		"rated_at":   time.Now().UTC(),
		"rating_tx":  proof.Hash,
		"edits_left": ratings.MaxRatingsPerOrder - (st.Edits + 1),
		"summary":    ratingSummaryShape(publicAgg),
	})
}

// findRatingTx looks for the transaction that pays the shop with this exact
// memo, first in the buyer's most recent transactions, then in the shop
// wallet's. The second pass exists because a Nimiq Pay payment is sent from
// the user's HTLC, so it is indexed under the contract's address and the
// shop's, not under the buyer's own address. Transactions already used for a
// rating are skipped. The scan is bounded (100 history entries per address, 5
// candidate lookups per request) because every request costs RPC calls.
func (h *Handlers) findRatingTx(ctx context.Context, buyer, memo string) (nimiq.TxDetail, bool, error) {
	shop := nimiq.NormalizeAddress(h.RatingRecipient)
	expectedData := hex.EncodeToString([]byte(memo))
	checked := 0
	seen := map[string]struct{}{}
	for _, addr := range []string{buyer, h.RatingRecipient} {
		txs, err := h.RatingRPC.GetTransactionsByAddress(ctx, addr, 100, "")
		if err != nil {
			return nimiq.TxDetail{}, false, err
		}
		for _, t := range txs {
			if nimiq.NormalizeAddress(t.To) != shop || t.Value != ratings.PriceLuna {
				continue
			}
			// History includes memo bytes on current RPCs. Skip unrelated 1-Luna
			// payments before spending the limited detail-lookup budget.
			if t.RecipientData != nil && !strings.EqualFold(*t.RecipientData, expectedData) {
				continue
			}
			if _, dup := seen[strings.ToLower(t.Hash)]; dup || h.Store.RatingTxUsed(t.Hash) {
				continue
			}
			seen[strings.ToLower(t.Hash)] = struct{}{}
			if checked >= 5 {
				return nimiq.TxDetail{}, false, nil
			}
			checked++
			det, found, err := h.RatingRPC.GetTransactionDetail(ctx, t.Hash)
			if err != nil {
				return nimiq.TxDetail{}, false, err
			}
			if !found {
				continue
			}
			// VerifyTx decides who paid (basic sender or the HTLC's signing
			// buyer); history metadata never establishes a rating by itself.
			if ratings.VerifyTx(det, buyer, h.RatingRecipient, memo) == nil {
				return det, true, nil
			}
		}
	}
	return nimiq.TxDetail{}, false, nil
}

func writePendingRating(ctx *fasthttp.RequestCtx) {
	writeJSON(ctx, fasthttp.StatusAccepted, map[string]interface{}{
		"status":  "pending",
		"message": "waiting for the transaction to reach the chain",
	})
}

/* ---------------- Admin moderation ---------------- */

// AdminListRatings shows every buyer comment, hidden ones included, with the
// purchase they belong to, plus the switch state. Admin only.
func (h *Handlers) AdminListRatings(ctx *fasthttp.RequestCtx) {
	items, err := h.Store.ListRatingComments(300, true)
	if err != nil {
		writeError(ctx, fasthttp.StatusInternalServerError, "could not load comments")
		return
	}
	out := make([]map[string]interface{}, 0, len(items))
	for _, c := range items {
		out = append(out, map[string]interface{}{
			"kind":    c.Kind,
			"id":      c.ID,
			"stars":   c.Stars,
			"comment": c.Comment,
			"at":      c.At,
			"hidden":  c.Hidden,
			// A comment on an admin test-center purchase: public only while
			// the Test Center switch shows test orders.
			"test_mode": c.TestMode,
			// Who left it. The comment records the purchase, not the
			// person; the operator console is the one place that may put a
			// face to it (owner, 2026-10-19). Best effort — an old or
			// deleted purchase simply carries no address.
			"buyer_address": h.addressOfPurchase(c.Kind, c.ID),
		})
	}
	agg, _ := h.Store.GetRatingAggregate()
	writeJSON(ctx, fasthttp.StatusOK, map[string]interface{}{
		"comments_enabled": h.Store.RatingCommentsEnabled(),
		"items":            out,
		"summary":          ratingSummaryShape(agg),
	})
}

// addressOfPurchase resolves the buyer's NIM wallet for one rated purchase.
//
// A rating is keyed by the purchase it belongs to (a quote, or an order from
// the older table), and the comment itself stores no identity at all — which
// is right for a PUBLIC list and unhelpful for a moderator who has to tell
// one buyer's comments from another's. This is the admin-only bridge back to
// the account, and it is best effort throughout: any missing link (a purged
// quote, an orphaned order, a user who never logged in again) simply means
// "no address", never an error and never a guess.
func (h *Handlers) addressOfPurchase(kind, id string) string {
	userID := ""
	if q, err := h.Store.GetQuote(id); err == nil {
		userID = q.UserID
	}
	if userID == "" {
		if o, err := h.Store.GetOrder(id); err == nil {
			userID = o.UserID
		}
	}
	if userID == "" {
		return ""
	}
	u, err := h.Store.GetUser(userID)
	if err != nil {
		return ""
	}
	return u.NimiqAddress
}

// AdminSetRatingComments switches buyer comments on or off for everybody.
// Stars keep working either way. Admin only.
func (h *Handlers) AdminSetRatingComments(ctx *fasthttp.RequestCtx) {
	var req struct {
		Enabled bool `json:"enabled"`
	}
	if err := readJSON(ctx, &req); err != nil {
		writeError(ctx, fasthttp.StatusBadRequest, "send {\"enabled\": true|false}")
		return
	}
	if err := h.Store.SetRatingCommentsEnabled(req.Enabled); err != nil {
		writeError(ctx, fasthttp.StatusInternalServerError, "could not save the setting")
		return
	}
	writeJSON(ctx, fasthttp.StatusOK, map[string]interface{}{"comments_enabled": req.Enabled})
}

// AdminHideRatingComment hides or shows one comment. The stars stay in the
// aggregate. Admin only.
func (h *Handlers) AdminHideRatingComment(ctx *fasthttp.RequestCtx) {
	kind, _ := ctx.UserValue("kind").(string)
	id, _ := ctx.UserValue("id").(string)
	kind = strings.ToLower(kind)
	if kind != db.RatingKindOrder && kind != db.RatingKindQuote {
		writeError(ctx, fasthttp.StatusBadRequest, "kind must be order or quote")
		return
	}
	var req struct {
		Hidden bool `json:"hidden"`
	}
	if err := readJSON(ctx, &req); err != nil {
		writeError(ctx, fasthttp.StatusBadRequest, "send {\"hidden\": true|false}")
		return
	}
	err := h.Store.SetRatingCommentHidden(kind, id, req.Hidden)
	if errors.Is(err, db.ErrNotFound) {
		writeError(ctx, fasthttp.StatusNotFound, "rating not found")
		return
	}
	if err != nil {
		writeError(ctx, fasthttp.StatusInternalServerError, "could not update the comment")
		return
	}
	writeJSON(ctx, fasthttp.StatusOK, map[string]interface{}{"kind": kind, "id": id, "hidden": req.Hidden})
}

// Persist before returning a wallet intent: a disconnected buyer is no longer
// needed after broadcasting. A memo shared by two purchases is reserved once.
func (h *Handlers) rememberRatingIntent(ctx *fasthttp.RequestCtx, kind, id, userID, buyer, memo string) bool {
	intent := db.RatingIntent{Key: db.RatingIntentKey(buyer, h.RatingRecipient, memo), Kind: kind, ID: id, UserID: userID, Buyer: buyer, Recipient: h.RatingRecipient, CreatedAt: time.Now().UTC()}
	if err := h.Store.SaveRatingIntent(intent); err != nil {
		if errors.Is(err, db.ErrRatingIntentBusy) {
			writeError(ctx, 409, err.Error())
		} else {
			writeError(ctx, 503, "could not persist rating intent")
		}
		return false
	}
	return true
}

// Cancel is used only after the wallet explicitly declined payment.
func (h *Handlers) CancelRatingIntentOrder(ctx *fasthttp.RequestCtx) {
	h.cancelRatingIntent(ctx, db.RatingKindOrder)
}
func (h *Handlers) CancelRatingIntentQuote(ctx *fasthttp.RequestCtx) {
	h.cancelRatingIntent(ctx, db.RatingKindQuote)
}
func (h *Handlers) cancelRatingIntent(ctx *fasthttp.RequestCtx, kind string) {
	id, _ := ctx.UserValue("id").(string)
	userID := middleware.UserID(ctx)
	req, err := decodeRatingRequest(ctx.PostBody())
	if err != nil {
		writeError(ctx, 400, err.Error())
		return
	}
	memo, err := ratings.Memo(req.Stars, req.Comment)
	if err != nil {
		writeError(ctx, 400, err.Error())
		return
	}
	addrs, err := h.Store.UserAddresses([]string{userID})
	if err != nil || addrs[userID] == "" {
		writeError(ctx, 503, "could not load wallet")
		return
	}
	if err := h.Store.CancelRatingIntent(db.RatingIntentKey(addrs[userID], h.RatingRecipient, memo), kind, id, userID); err != nil {
		writeError(ctx, 409, "could not cancel rating intent")
		return
	}
	writeJSON(ctx, 200, map[string]bool{"cancelled": true})
}
