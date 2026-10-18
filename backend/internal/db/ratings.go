package db

import (
	"errors"
	"strings"
	"time"

	"github.com/dgraph-io/badger/v4"
)

/* ---------------- On-chain ratings: writes, proofs, comments ----------------

A rating is accepted only together with the buyer's own transaction (see the
handlers). This file records it: the star value, the optional comment, the
transaction hash that proves it, and the public aggregate, all inside one
badger transaction so none of them can disagree. */

const (
	RatingKindOrder = "order"
	RatingKindQuote = "quote"

	prefixRatingTx        = "ratingtx:"
	metaRatingCommentsKey = "meta:ratings_comments"
	metaRatingCommentsOff = "meta:ratings_comments_off"

	// recentCommentsMax bounds the public comment list. Older comments fall off
	// the list but keep their order record and their on-chain memo.
	recentCommentsMax = 200
)

var (
	// ErrRatingRepeat means the submitted stars and comment equal what is already
	// saved. Nothing is written and the buyer should not have been asked to pay.
	ErrRatingRepeat = errors.New("same rating already saved")
	// ErrRatingLimit means the record already carries the maximum number of ratings.
	ErrRatingLimit = errors.New("rating limit reached for this purchase")
	// ErrRatingTxUsed means this transaction already proved a rating.
	ErrRatingTxUsed = errors.New("transaction already used for a rating")
	// ErrCommentsOff means the admin has turned rating comments off.
	ErrCommentsOff = errors.New("rating comments are switched off")
)

// RatingComment is one entry of the recent-comments list. The public view
// leaves out Kind and ID so a comment cannot be tied to a purchase row.
type RatingComment struct {
	Kind    string    `json:"kind"`
	ID      string    `json:"id"`
	Stars   int       `json:"stars"`
	Comment string    `json:"comment"`
	At      time.Time `json:"at"`
	Hidden  bool      `json:"hidden,omitempty"`
	// TestMode marks a comment left on an admin test-center purchase. Public
	// callers only see it while the operator shows test orders publicly.
	TestMode bool `json:"test_mode,omitempty"`
}

// RatingState is the rating view of one purchase for its owner.
type RatingState struct {
	Rated     bool
	Stars     int
	Comment   string
	Edits     int
	Delivered bool
	RatedAt   *time.Time
	RatingTx  string
	// Owner is false when the record exists but belongs to someone else.
	Owner bool
	// TestMode is true for an admin test-center purchase (Quote.TestMode).
	TestMode bool
}

// RatingWrite is one accepted on-chain rating.
type RatingWrite struct {
	IntentKey  string // optional durable reservation consumed in the rating commit
	Kind       string
	ID         string
	UserID     string
	Stars      int
	Comment    string // already normalised by internal/ratings
	TxHash     string
	MaxRatings int
	Now        time.Time
}

func ratingTxKey(hash string) []byte { return []byte(prefixRatingTx + strings.ToLower(hash)) }

// rateable reports whether a record is in a status that may carry a rating.
func rateable(kind string, status string) bool {
	if kind == RatingKindQuote {
		return status == "fulfilled"
	}
	return isDeliveredStatus(status)
}

// loadRatingRecord reads an order or quote and returns the rating view of it.
func loadRatingRecord(txn *badger.Txn, kind, id string) (RatingState, string, error) {
	var st RatingState
	switch kind {
	case RatingKindOrder:
		var o Order
		if err := getJSON(txn, orderKey(id), &o); err != nil {
			return st, "", err
		}
		st = RatingState{Rated: o.Rating > 0, Stars: o.Rating, Comment: o.RatingComment,
			Edits: o.RatingEdits, Delivered: rateable(kind, o.Status), RatedAt: o.RatedAt,
			RatingTx: o.RatingTx, Owner: true}
		return st, o.UserID, nil
	case RatingKindQuote:
		var q Quote
		if err := getJSON(txn, quoteKey(id), &q); err != nil {
			return st, "", err
		}
		st = RatingState{Rated: q.Rating > 0, Stars: q.Rating, Comment: q.RatingComment,
			Edits: q.RatingEdits, Delivered: rateable(kind, q.Status), RatedAt: q.RatedAt,
			RatingTx: q.RatingTx, Owner: true, TestMode: q.TestMode}
		return st, q.UserID, nil
	}
	return st, "", ErrNotFound
}

// GetRatingState returns the rating view of a purchase for userID. A purchase
// that belongs to somebody else reports Owner=false and no details.
func (s *Store) GetRatingState(kind, id, userID string) (RatingState, error) {
	var st RatingState
	err := s.View(func(txn *badger.Txn) error {
		v, owner, err := loadRatingRecord(txn, kind, id)
		if err != nil {
			return err
		}
		if owner != userID {
			return ErrNotFound
		}
		st = v
		return nil
	})
	return st, err
}

// RatingTxUsed reports whether a transaction hash has already proved a rating.
func (s *Store) RatingTxUsed(hash string) bool {
	used := false
	_ = s.View(func(txn *badger.Txn) error {
		_, err := txn.Get(ratingTxKey(hash))
		used = err == nil
		return nil
	})
	return used
}

// ApplyRating saves one accepted rating and returns the updated aggregate. It
// refuses a repeat, a purchase at its limit, and a transaction used before.
func (s *Store) ApplyRating(w RatingWrite) (RatingAggregate, error) {
	var agg RatingAggregate
	if w.Stars < 1 || w.Stars > 5 || strings.TrimSpace(w.TxHash) == "" {
		return agg, ErrConflict
	}
	err := s.Update(func(txn *badger.Txn) error {
		if w.IntentKey != "" {
			var intent RatingIntent
			if err := getJSON(txn, ratingIntentKey(w.IntentKey), &intent); err != nil {
				return err
			}
			memo := string(rune('0' + w.Stars))
			if w.Comment != "" {
				memo += " " + w.Comment
			}
			if intent.Kind != w.Kind || intent.ID != w.ID || intent.UserID != w.UserID || RatingIntentKey(intent.Buyer, intent.Recipient, memo) != w.IntentKey {
				return ErrConflict
			}
		}
		st, owner, err := loadRatingRecord(txn, w.Kind, w.ID)
		if err != nil {
			return err
		}
		if owner != w.UserID {
			return ErrNotFound
		}
		if !st.Delivered {
			return ErrConflict // not delivered yet, not rateable
		}
		if w.Comment != "" && !commentsOn(txn) {
			return ErrCommentsOff
		}
		if st.Rated && st.Stars == w.Stars && st.Comment == w.Comment {
			return ErrRatingRepeat
		}
		if st.Edits >= w.MaxRatings {
			return ErrRatingLimit
		}
		if _, e := txn.Get(ratingTxKey(w.TxHash)); e == nil {
			return ErrRatingTxUsed
		} else if !errors.Is(e, badger.ErrKeyNotFound) {
			return e
		}

		agg = loadAggregate(txn)
		agg.apply(st.Stars, w.Stars)
		// A rating on a test purchase is counted twice: in the global
		// aggregate and in the test-only one, so the public summary can
		// leave simulations out while they are hidden.
		if st.TestMode {
			testAgg := loadTestAggregate(txn)
			testAgg.apply(st.Stars, w.Stars)
			if err := saveTestAggregate(txn, testAgg); err != nil {
				return err
			}
		}

		if st.Rated && st.RatingTx != "" {
			oldKey := []byte("rating:history:" + strings.ToLower(st.RatingTx))
			if _, err := txn.Get(oldKey); errors.Is(err, badger.ErrKeyNotFound) {
				previous, err := marshal(map[string]any{"Kind": w.Kind, "ID": w.ID, "UserID": w.UserID, "Stars": st.Stars, "Comment": st.Comment, "TxHash": st.RatingTx, "RatedAt": st.RatedAt})
				if err != nil {
					return err
				}
				if err := txn.Set(oldKey, previous); err != nil {
					return err
				}
			} else if err != nil {
				return err
			}
		}
		now := w.Now.UTC()
		if err := storeRatingFields(txn, w, now); err != nil {
			return err
		}
		if err := txn.Set(ratingTxKey(w.TxHash), []byte(w.Kind+":"+w.ID)); err != nil {
			return err
		}
		// Preserve every accepted comment revision in the same atomic commit.
		history, err := marshal(w)
		if err != nil {
			return err
		}
		if err := txn.Set([]byte("rating:history:"+strings.ToLower(w.TxHash)), history); err != nil {
			return err
		}
		if err := saveAggregate(txn, agg); err != nil {
			return err
		}
		if w.IntentKey != "" {
			if err := txn.Delete(ratingIntentKey(w.IntentKey)); err != nil {
				return err
			}
		}
		return upsertRecentComment(txn, RatingComment{Kind: w.Kind, ID: w.ID,
			Stars: w.Stars, Comment: w.Comment, At: now, TestMode: st.TestMode})
	})
	return agg, err
}

// storeRatingFields writes the rating onto the order or quote record.
func storeRatingFields(txn *badger.Txn, w RatingWrite, now time.Time) error {
	switch w.Kind {
	case RatingKindOrder:
		var o Order
		if err := getJSON(txn, orderKey(w.ID), &o); err != nil {
			return err
		}
		o.Rating = w.Stars
		o.RatedAt = &now
		o.RatingComment = w.Comment
		o.RatingCommentHidden = false // a new comment starts visible
		o.RatingEdits++
		o.RatingTx = w.TxHash
		o.UpdatedAt = now
		blob, err := marshalPreservingUnknown(txn, orderKey(o.ID), o)
		if err != nil {
			return err
		}
		return txn.Set(orderKey(o.ID), blob)
	case RatingKindQuote:
		var q Quote
		if err := getJSON(txn, quoteKey(w.ID), &q); err != nil {
			return err
		}
		q.Rating = w.Stars
		q.RatedAt = &now
		q.RatingComment = w.Comment
		q.RatingCommentHidden = false
		q.RatingEdits++
		q.RatingTx = w.TxHash
		q.UpdatedAt = now
		return putQuoteTx(txn, &q)
	}
	return ErrNotFound
}

// SetRatingCommentHidden hides or shows one buyer comment. The stars still
// count in the aggregate; only the words disappear from the public surfaces.
func (s *Store) SetRatingCommentHidden(kind, id string, hidden bool) error {
	return s.Update(func(txn *badger.Txn) error {
		switch kind {
		case RatingKindOrder:
			var o Order
			if err := getJSON(txn, orderKey(id), &o); err != nil {
				return err
			}
			o.RatingCommentHidden = hidden
			blob, err := marshalPreservingUnknown(txn, orderKey(o.ID), o)
			if err != nil {
				return err
			}
			if err := txn.Set(orderKey(o.ID), blob); err != nil {
				return err
			}
		case RatingKindQuote:
			var q Quote
			if err := getJSON(txn, quoteKey(id), &q); err != nil {
				return err
			}
			q.RatingCommentHidden = hidden
			if err := putQuoteTx(txn, &q); err != nil {
				return err
			}
		default:
			return ErrNotFound
		}
		return setRecentCommentHidden(txn, kind, id, hidden)
	})
}

// RatingCommentsEnabled reports whether buyers may attach comments. The switch
// is stored as an "off" flag, so the default (no key) is enabled.
func (s *Store) RatingCommentsEnabled() bool {
	on := true
	_ = s.View(func(txn *badger.Txn) error {
		on = commentsOn(txn)
		return nil
	})
	return on
}

// SetRatingCommentsEnabled turns buyer comments on or off for everybody.
func (s *Store) SetRatingCommentsEnabled(on bool) error {
	return s.Update(func(txn *badger.Txn) error {
		if on {
			return txn.Delete([]byte(metaRatingCommentsOff))
		}
		return txn.Set([]byte(metaRatingCommentsOff), []byte("1"))
	})
}

func commentsOn(txn *badger.Txn) bool {
	_, err := txn.Get([]byte(metaRatingCommentsOff))
	return errors.Is(err, badger.ErrKeyNotFound)
}

// ListRatingComments returns the newest comments. Public callers get visible
// ones only and an empty list while comments are switched off. Admin callers
// see everything, including hidden entries, with their record ids.
func (s *Store) ListRatingComments(limit int, admin bool) ([]RatingComment, error) {
	var out []RatingComment
	err := s.View(func(txn *badger.Txn) error {
		if !admin && !commentsOn(txn) {
			return nil
		}
		showTests := admin
		if !admin {
			var err error
			if showTests, err = testActivityVisible(txn); err != nil {
				return err
			}
		}
		list := loadRecentComments(txn)
		for _, c := range list {
			if !admin && (c.Hidden || c.Comment == "") {
				continue
			}
			if !c.TestMode && c.Kind == RatingKindQuote {
				// Entries written before the flag existed: ask the record.
				c.TestMode = isTestPurchase(txn, c.Kind, c.ID)
			}
			if c.TestMode && !showTests {
				continue
			}
			if !admin {
				c.Kind, c.ID = "", ""
			}
			out = append(out, c)
			if limit > 0 && len(out) >= limit {
				break
			}
		}
		return nil
	})
	return out, err
}

// isTestPurchase reports whether a rated record is an admin test-center
// purchase. Legacy orders never are; an unreadable record is not either.
func isTestPurchase(txn *badger.Txn, kind, id string) bool {
	if kind != RatingKindQuote {
		return false
	}
	var q Quote
	if err := getJSON(txn, quoteKey(id), &q); err != nil {
		return false
	}
	return q.TestMode
}

// apply moves one purchase's rating from old stars (0 = unrated) to new stars.
func (a *RatingAggregate) apply(old, stars int) {
	if old == 0 {
		a.Count++
		a.Sum += stars
		a.Dist[stars]++
		return
	}
	a.Sum += stars - old
	a.Dist[old]--
	a.Dist[stars]++
}

// minus returns the aggregate without the ratings counted in b, clamped at
// zero so a stale counter can never produce a negative public summary.
func (a RatingAggregate) minus(b RatingAggregate) RatingAggregate {
	out := RatingAggregate{Count: a.Count - b.Count, Sum: a.Sum - b.Sum}
	for i := range out.Dist {
		out.Dist[i] = a.Dist[i] - b.Dist[i]
		if out.Dist[i] < 0 {
			out.Dist[i] = 0
		}
	}
	if out.Count < 0 || out.Sum < 0 {
		out = RatingAggregate{}
	}
	return out
}

func loadTestAggregate(txn *badger.Txn) RatingAggregate {
	var agg RatingAggregate
	item, err := txn.Get([]byte(metaRatingAggregateTestKey))
	if err != nil {
		return agg
	}
	_ = item.Value(func(v []byte) error { return unmarshal(v, &agg) })
	return agg
}

func saveTestAggregate(txn *badger.Txn, agg RatingAggregate) error {
	blob, err := marshal(agg)
	if err != nil {
		return err
	}
	return txn.Set([]byte(metaRatingAggregateTestKey), blob)
}

// PublicRatingAggregate is the star summary the storefront may show: every
// rating while the operator shows test orders publicly, otherwise every
// rating except those left on test purchases.
func (s *Store) PublicRatingAggregate() (RatingAggregate, error) {
	var agg RatingAggregate
	err := s.View(func(txn *badger.Txn) error {
		showTests, err := testActivityVisible(txn)
		if err != nil {
			return err
		}
		agg = loadAggregate(txn)
		if !showTests {
			agg = agg.minus(loadTestAggregate(txn))
		}
		return nil
	})
	return agg, err
}

// RebuildRatingAggregates recounts both aggregates from the rated records.
// It runs at boot so a database written before the test-only aggregate
// existed stops counting old test ratings in the public summary. The
// counters are derived data: the records are the truth.
func (s *Store) RebuildRatingAggregates() (all, test RatingAggregate, err error) {
	err = s.Update(func(txn *badger.Txn) error {
		it := txn.NewIterator(badger.DefaultIteratorOptions)
		defer it.Close()
		for it.Seek([]byte(prefixQuote)); it.ValidForPrefix([]byte(prefixQuote)); it.Next() {
			var q Quote
			if err := getJSON(txn, it.Item().Key(), &q); err != nil {
				return err
			}
			if q.Rating < 1 || q.Rating > 5 {
				continue
			}
			all.apply(0, q.Rating)
			if q.TestMode {
				test.apply(0, q.Rating)
			}
		}
		for it.Seek([]byte(prefixOrder)); it.ValidForPrefix([]byte(prefixOrder)); it.Next() {
			var o Order
			if err := getJSON(txn, it.Item().Key(), &o); err != nil {
				return err
			}
			if o.Rating >= 1 && o.Rating <= 5 {
				all.apply(0, o.Rating)
			}
		}
		if err := saveAggregate(txn, all); err != nil {
			return err
		}
		return saveTestAggregate(txn, test)
	})
	return all, test, err
}

func loadRecentComments(txn *badger.Txn) []RatingComment {
	var list []RatingComment
	if err := getJSON(txn, []byte(metaRatingCommentsKey), &list); err != nil {
		return nil
	}
	return list
}

func saveRecentComments(txn *badger.Txn, list []RatingComment) error {
	if len(list) > recentCommentsMax {
		list = list[:recentCommentsMax]
	}
	blob, err := marshal(list)
	if err != nil {
		return err
	}
	return txn.Set([]byte(metaRatingCommentsKey), blob)
}

// upsertRecentComment puts the newest state of one purchase at the top of the
// list, replacing its earlier entry. An empty comment drops the entry.
func upsertRecentComment(txn *badger.Txn, c RatingComment) error {
	list := loadRecentComments(txn)
	out := make([]RatingComment, 0, len(list)+1)
	if c.Comment != "" {
		out = append(out, c)
	}
	for _, e := range list {
		if e.Kind == c.Kind && e.ID == c.ID {
			continue
		}
		out = append(out, e)
	}
	return saveRecentComments(txn, out)
}

func setRecentCommentHidden(txn *badger.Txn, kind, id string, hidden bool) error {
	list := loadRecentComments(txn)
	for i := range list {
		if list[i].Kind == kind && list[i].ID == id {
			list[i].Hidden = hidden
		}
	}
	return saveRecentComments(txn, list)
}
