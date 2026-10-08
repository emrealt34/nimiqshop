package handlers

/* ratings_handlers.go — the buyer-signed on-chain star rating.

The buyer's own wallet signs and pays for every rating: 1 Luna plus the network
fee, sent to the shop wallet with the memo "<stars>[ <comment>]". The shop never
spends anything on a rating. The rating is saved only after the chain shows
that exact transaction: sent by the buyer's address, paid to the shop, the
right amount, the right memo, executed and included in a block. Each
transaction proves one rating, once.

Flow:
  1. POST …/rating/intent  → memo, recipient, amount and fee to show the buyer
  2. the buyer's wallet (Nimiq Pay or Hub) signs and broadcasts the transaction
  3. POST …/rate          → the shop finds that transaction on chain and saves
                            the rating; 202 while the chain has not confirmed it yet
*/

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/valyala/fasthttp"

	"nimiqshop/internal/db"
	"nimiqshop/internal/middleware"
	"nimiqshop/internal/nimiq"
	"nimiqshop/internal/ratings"
)

// ratingRequest is the body of the intent and rate endpoints. TxHash is set
// when the wallet reports a hash (Hub does); otherwise the shop looks for the
// buyer's transaction in the buyer's recent history (Nimiq Pay returns the
// serialized transaction only).
type ratingRequest struct {
	Stars   int    `json:"stars"`
	Comment string `json:"comment"`
	TxHash  string `json:"tx_hash,omitempty"`
}

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
		out = append(out, map[string]interface{}{
			"stars":   c.Stars,
			"comment": c.Comment,
			"at":      c.At,
		})
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
	var req ratingRequest
	if err := readJSON(ctx, &req); err != nil {
		writeError(ctx, fasthttp.StatusBadRequest, "rating must be an integer from 1 to 5")
		return
	}
	memo, _, _, ok := h.prepareRating(ctx, kind, id, userID, req)
	if !ok {
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
	var req ratingRequest
	if err := readJSON(ctx, &req); err != nil {
		writeError(ctx, fasthttp.StatusBadRequest, "rating must be an integer from 1 to 5")
		return
	}
	memo, st, buyer, ok := h.prepareRating(ctx, kind, id, userID, req)
	if !ok {
		return
	}
	comment, _ := ratings.NormalizeComment(req.Comment)

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

	agg, err := h.Store.ApplyRating(db.RatingWrite{
		Kind:       kind,
		ID:         id,
		UserID:     userID,
		Stars:      req.Stars,
		Comment:    comment,
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

	writeJSON(ctx, fasthttp.StatusOK, map[string]interface{}{
		"order_id":   id,
		"rating":     req.Stars,
		"comment":    comment,
		"rated_at":   time.Now().UTC(),
		"rating_tx":  proof.Hash,
		"edits_left": ratings.MaxRatingsPerOrder - (st.Edits + 1),
		"summary":    ratingSummaryShape(agg),
	})
}

// findRatingTx looks through the buyer's most recent transactions for the one
// that pays the shop with this exact memo. Transactions already used for a
// rating are skipped. The scan is bounded (20 history entries, 5 candidate
// lookups per request) because every request costs RPC calls.
func (h *Handlers) findRatingTx(ctx context.Context, buyer, memo string) (nimiq.TxDetail, bool, error) {
	txs, err := h.RatingRPC.GetTransactionsByAddress(ctx, buyer, 20, "")
	if err != nil {
		return nimiq.TxDetail{}, false, err
	}
	shop := nimiq.NormalizeAddress(h.RatingRecipient)
	checked := 0
	for _, t := range txs {
		if nimiq.NormalizeAddress(t.To) != shop || t.Value != ratings.PriceLuna {
			continue
		}
		if h.Store.RatingTxUsed(t.Hash) {
			continue
		}
		if checked >= 5 {
			break
		}
		checked++
		det, found, err := h.RatingRPC.GetTransactionDetail(ctx, t.Hash)
		if err != nil {
			return nimiq.TxDetail{}, false, err
		}
		if !found {
			continue
		}
		if ratings.VerifyTx(det, buyer, h.RatingRecipient, memo) == nil {
			return det, true, nil
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
		})
	}
	agg, _ := h.Store.GetRatingAggregate()
	writeJSON(ctx, fasthttp.StatusOK, map[string]interface{}{
		"comments_enabled": h.Store.RatingCommentsEnabled(),
		"items":            out,
		"summary":          ratingSummaryShape(agg),
	})
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
