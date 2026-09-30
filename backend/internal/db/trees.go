package db

import (
	"errors"
	"fmt"
	"time"

	"github.com/dgraph-io/badger/v4"
	"github.com/google/uuid"

	"nimiqshop/internal/money"
)

// TreeDestination is where a buyer wants their cashback to go.
const (
	TreeDestCashback = "cashback" // default: pay NIM to the buyer
	TreeDestTrees    = "trees"    // send NIM to the tree-planting wallet; count trees
)

// TreeUserPrefs stores per-buyer cashback destination preference.
type TreeUserPrefs struct {
	UserID      string    `json:"user_id"`
	Destination string    `json:"destination"` // cashback | trees
	UpdatedAt   time.Time `json:"updated_at"`
}

// TreeContribution is one cashback event that went to tree planting.
// USD value is locked at fulfillment time using the product's USD value so
// the tree count is deterministic regardless of NIM/stablecoin volatility.
type TreeContribution struct {
	ID          string       `json:"id"`
	UserID      string       `json:"user_id"`
	QuoteID     string       `json:"quote_id"`
	ProductID   string       `json:"product_id"`
	AmountUSD   money.Micros `json:"amount_usd"` // portion of product usd that became trees = cashback usd value
	CashbackNIM float64      `json:"cashback_nim"`
	Trees       float64      `json:"trees"`             // fractional trees "funded"/earmarked at fulfilment time
	TxHash      string       `json:"tx_hash,omitempty"` // NIM cashback tx hash
	CreatedAt   time.Time    `json:"created_at"`
	WeekBucket  string       `json:"week_bucket"`  // ISO week like "2026W37"
	MonthBucket string       `json:"month_bucket"` // "2026-09"
	// Settled records that this contribution's NIM was included in a recorded
	// TreeSettlement (swapped to USDT on Polygon and paid to OneTreePlanted).
	// Trees are physically put in the ground by OneTreePlanted on THEIR planting
	// schedule (normally the month(s) after payment), so "SettledTrees" is the
	// share of a real payout — NOT an instantaneous claim that a tree stands.
	Settled      bool    `json:"settled"`
	SettledTrees float64 `json:"settled_trees,omitempty"` // reconciled actual trees after swap/payout
	SettlementID string  `json:"settlement_id,omitempty"`
}

// TreeSettlement is the manual monthly payout the admin records once the NIM
// has been swapped to USDT on Polygon and sent to OneTreePlanted. This makes
// the whole chain public/verifiable.
type TreeProofImage struct {
	Data    string `json:"data"`
	Caption string `json:"caption,omitempty"`
}

type TreeSettlement struct {
	ID             string           `json:"id"`
	MonthBucket    string           `json:"month_bucket"`              // "2026-09"
	AmountUSDT     float64          `json:"amount_usdt"`               // numeric payout amount in the settlement coin (USDT)
	AmountLabel    string           `json:"amount_label,omitempty"`    // manually entered display label, e.g. USDT, EUR, NIM
	AmountValue    string           `json:"amount_value,omitempty"`    // manually entered display amount
	TreesPlanted   float64          `json:"trees_planted,omitempty"`   // manually confirmed tree count
	TxHash         string           `json:"tx_hash"`                   // Polygon tx hash
	TransactionURL string           `json:"transaction_url,omitempty"` // optional direct explorer link
	FromAddress    string           `json:"from_address"`
	ToAddress      string           `json:"to_address"`
	Note           string           `json:"note,omitempty"`
	Status         string           `json:"status"`               // paid | skipped
	AmountNIM      float64          `json:"amount_nim,omitempty"` // pooled donated NIM for this month
	WalletNIM      float64          `json:"wallet_nim,omitempty"` // wallet balance shown when skipped
	ProofImages    []TreeProofImage `json:"proof_images,omitempty"`
	CreatedAt      time.Time        `json:"created_at"`
}

// SetTreeUserPrefs saves (or updates) a buyer's cashback destination.
func (s *Store) SetTreeUserPrefs(userID, destination string) (TreeUserPrefs, error) {
	if userID == "" {
		return TreeUserPrefs{}, ErrNotFound
	}
	dest := destination
	if dest != TreeDestTrees {
		dest = TreeDestCashback
	}
	prefs := TreeUserPrefs{UserID: userID, Destination: dest, UpdatedAt: time.Now().UTC()}
	err := s.Update(func(tx *badger.Txn) error {
		raw, err := marshal(prefs)
		if err != nil {
			return err
		}
		return tx.Set(treeUserPrefsKey(userID), raw)
	})
	return prefs, err
}

// GetTreeUserPrefs returns a buyer's prefs (defaults to cashback).
func (s *Store) GetTreeUserPrefs(userID string) (TreeUserPrefs, error) {
	var out TreeUserPrefs
	err := s.View(func(tx *badger.Txn) error {
		return getJSON(tx, treeUserPrefsKey(userID), &out)
	})
	if errors.Is(err, ErrNotFound) {
		return TreeUserPrefs{UserID: userID, Destination: TreeDestCashback}, nil
	}
	return out, err
}

// weekBucket returns an ISO-week bucket like "2026W37" (Monday-start weeks).
func weekBucket(t time.Time) string {
	y, w := t.ISOWeek()
	return fmt.Sprintf("%04dW%02d", y, w)
}

// RecordTreeContribution writes one tree-planting contribution atomically.
// Called from the cashback worker once the NIM send actually broadcasts.
func (s *Store) RecordTreeContribution(userID, quoteID, productID, txHash string, amountUSD money.Micros, cashbackNIM float64, treesPerUSD float64) (TreeContribution, error) {
	if userID == "" || quoteID == "" {
		return TreeContribution{}, ErrConflict
	}
	if treesPerUSD <= 0 {
		treesPerUSD = 1.0
	}
	now := time.Now().UTC()
	usdVal := float64(amountUSD) / 1_000_000.0
	con := TreeContribution{
		ID:          uuid.NewString(),
		UserID:      userID,
		QuoteID:     quoteID,
		ProductID:   productID,
		AmountUSD:   amountUSD,
		CashbackNIM: cashbackNIM,
		Trees:       usdVal * treesPerUSD,
		TxHash:      txHash,
		CreatedAt:   now,
		WeekBucket:  weekBucket(now),
		MonthBucket: now.Format("2006-01"),
	}
	err := s.Update(func(tx *badger.Txn) error {
		// Dedup by quote id: one cashback per quote → one tree contribution.
		// We don't have an index keyed on quote_id, so scan user contribs
		// (cheap — few rows per user) for that quote and refuse duplicates.
		if e := scanIndex(tx, treeUserIndexPrefix(userID), 0, func(id string) error {
			var c TreeContribution
			if er := getJSON(tx, treeContribKey(id), &c); er != nil {
				if errors.Is(er, ErrNotFound) {
					return nil
				}
				return er
			}
			if c.QuoteID == quoteID {
				return ErrConflict
			}
			return nil
		}); e != nil {
			return e
		}
		raw, er := marshal(con)
		if er != nil {
			return er
		}
		if er := tx.Set(treeContribKey(con.ID), raw); er != nil {
			return er
		}
		ts := con.CreatedAt.UnixNano()
		if er := tx.Set(treeUserIndexKey(userID, ts, con.ID), []byte(con.ID)); er != nil {
			return er
		}
		if er := tx.Set(treeBucketIndexKey("week:"+con.WeekBucket, ts, con.ID), []byte(con.ID)); er != nil {
			return er
		}
		if er := tx.Set(treeBucketIndexKey("month:"+con.MonthBucket, ts, con.ID), []byte(con.ID)); er != nil {
			return er
		}
		if er := tx.Set(treeBucketIndexKey("all", ts, con.ID), []byte(con.ID)); er != nil {
			return er
		}
		return nil
	})
	return con, err
}

// TreeUserTotal is a leaderboard row: user + trees.
type TreeUserTotal struct {
	UserID  string  `json:"user_id"`
	Trees   float64 `json:"trees"`   // nominal trees funded (earmarked) from cashback
	Planted float64 `json:"planted"` // reconciled trees actually paid out (settled)
	Count   int     `json:"count"`
}

// TreeLeaderboard returns the top N planters for a bucket key.
// bucket is "week:YYYYWww", "month:YYYY-MM", or "all".
func (s *Store) TreeLeaderboard(bucket string, limit int) ([]TreeUserTotal, error) {
	if limit <= 0 {
		limit = 50
	}
	totals := map[string]*TreeUserTotal{}
	err := s.View(func(tx *badger.Txn) error {
		prefix := []byte("ix:tp:" + bucket + ":")
		if bucket == "all" {
			prefix = treeBucketIndexPrefix("all")
		}
		return scanIndex(tx, prefix, limit*50, func(id string) error {
			var c TreeContribution
			if e := getJSON(tx, treeContribKey(id), &c); e != nil {
				if errors.Is(e, ErrNotFound) {
					return nil
				}
				return e
			}
			u := totals[c.UserID]
			if u == nil {
				u = &TreeUserTotal{UserID: c.UserID}
				totals[c.UserID] = u
			}
			u.Trees += c.Trees
			if c.Settled {
				u.Planted += c.SettledTrees
			}
			u.Count++
			return nil
		})
	})
	if err != nil {
		return nil, err
	}
	out := make([]TreeUserTotal, 0, len(totals))
	for _, v := range totals {
		out = append(out, *v)
	}
	// sort desc by trees
	for i := 0; i < len(out); i++ {
		for j := i + 1; j < len(out); j++ {
			if out[j].Trees > out[i].Trees {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// TreeTotals returns aggregate stats across all contributions.
type TreeTotals struct {
	TotalTrees    float64 `json:"total_trees"`   // nominal trees funded (back-compat = funded)
	FundedTrees   float64 `json:"funded_trees"`  // trees earmarked from cashback (not yet necessarily planted)
	PlantedTrees  float64 `json:"planted_trees"` // trees actually paid out to OneTreePlanted (settled)
	PendingTrees  float64 `json:"pending_trees"` // funded but not yet settled (next payout will cover them)
	TotalUSD      float64 `json:"total_usd"`
	PlantedUSD    float64 `json:"planted_usd"` // actual stablecoin already paid out via recorded settlements
	PendingUSD    float64 `json:"pending_usd"` // cashback usd not yet settled
	TotalContribs int     `json:"total_contribs"`
	TotalPlanters int     `json:"total_planters"`
}

func (s *Store) TreeTotals() (TreeTotals, error) {
	var out TreeTotals
	seen := map[string]bool{}
	err := s.View(func(tx *badger.Txn) error {
		return scanIndex(tx, treeBucketIndexPrefix("all"), 0, func(id string) error {
			var c TreeContribution
			if e := getJSON(tx, treeContribKey(id), &c); e != nil {
				if errors.Is(e, ErrNotFound) {
					return nil
				}
				return e
			}
			out.TotalTrees += c.Trees
			out.FundedTrees += c.Trees
			usd := float64(c.AmountUSD) / 1_000_000.0
			out.TotalUSD += usd
			if c.Settled {
				out.PlantedTrees += c.SettledTrees
				// Reflect the actual (post-swap) payout: scale each settled
				// contribution's usd by its reconciled tree ratio, which recovers
				// the real stablecoin a settlement delivered instead of overstating it.
				if c.Trees > 0 {
					out.PlantedUSD += usd * (c.SettledTrees / c.Trees)
				}
			} else {
				out.PendingTrees += c.Trees
				out.PendingUSD += usd
			}
			out.TotalContribs++
			if !seen[c.UserID] {
				seen[c.UserID] = true
				out.TotalPlanters++
			}
			return nil
		})
	})
	return out, err
}

// UserTreeTotals returns a single user's funded, planted (settled) and pending
// tree counts plus their donating-order count.
type UserTreeTotals struct {
	Funded  float64 `json:"user_trees"`
	Planted float64 `json:"user_trees_planted"`
	Pending float64 `json:"user_trees_pending"`
	Orders  int     `json:"user_orders"`
}

// UserTreeTotal returns how many trees a single user has funded/planted.
func (s *Store) UserTreeTotal(userID string) (UserTreeTotals, error) {
	var out UserTreeTotals
	if userID == "" {
		return out, nil
	}
	err := s.View(func(tx *badger.Txn) error {
		return scanIndex(tx, treeUserIndexPrefix(userID), 0, func(id string) error {
			var c TreeContribution
			if e := getJSON(tx, treeContribKey(id), &c); e != nil {
				if errors.Is(e, ErrNotFound) {
					return nil
				}
				return e
			}
			out.Funded += c.Trees
			if c.Settled {
				out.Planted += c.SettledTrees
			} else {
				out.Pending += c.Trees
			}
			out.Orders++
			return nil
		})
	})
	return out, err
}

// RecentTreeContributions returns the N most recent contributions (all users).
func (s *Store) RecentTreeContributions(limit int) ([]TreeContribution, error) {
	if limit <= 0 {
		limit = 20
	}
	var out []TreeContribution
	err := s.View(func(tx *badger.Txn) error {
		return scanIndex(tx, treeBucketIndexPrefix("all"), limit, func(id string) error {
			var c TreeContribution
			if e := getJSON(tx, treeContribKey(id), &c); e != nil {
				if errors.Is(e, ErrNotFound) {
					return nil
				}
				return e
			}
			out = append(out, c)
			return nil
		})
	})
	return out, err
}

// ListTreeSettlements returns every recorded monthly settlement newest-first.
func (s *Store) ListTreeSettlements(limit int) ([]TreeSettlement, error) {
	if limit <= 0 {
		limit = 50
	}
	var out []TreeSettlement
	err := s.View(func(tx *badger.Txn) error {
		return scanIndex(tx, treeSettIndexPrefix(), limit, func(id string) error {
			var st TreeSettlement
			if e := getJSON(tx, treeSettKey(id), &st); e != nil {
				if errors.Is(e, ErrNotFound) {
					return nil
				}
				return e
			}
			out = append(out, st)
			return nil
		})
	})
	// Newest first — reverseTS means forward scan is already newest-first.
	return out, err
}

// RecordTreeSettlement is the admin's manual month-end disclosure. Recording a
// settlement also RECONCILES every currently-pending contribution against the
// real amount of stablecoin that was actually delivered after the NIM→USDT swap.
// Swap fees/slippage mean the delivered amount is normally a little below the sum of
// the earmarked cashback, so each pending contribution's "planted" tree count is
// scaled pro-rata down (or up) to exactly match what OneTreePlanted really got.
func (s *Store) RecordTreeSettlement(monthBucket, txHash, fromAddr, toAddr string, amountUSDT float64, note string) (TreeSettlement, error) {
	return s.RecordTreeSettlementWithMeta(monthBucket, txHash, "", fromAddr, toAddr, amountUSDT, "USDT", fmt.Sprintf("%.2f", amountUSDT), 0, 0, 0, nil, "paid", note)
}

// UpdateTreeSettlement edits an existing manual payout and keeps user planted
// totals consistent when the manually confirmed tree count changes.
func (s *Store) UpdateTreeSettlement(id string, st TreeSettlement) error {
	return s.Update(func(tx *badger.Txn) error {
		var old TreeSettlement
		if err := getJSON(tx, treeSettKey(id), &old); err != nil {
			return err
		}
		st.ID = id
		st.CreatedAt = old.CreatedAt
		if st.Status == "" {
			st.Status = old.Status
		}
		if st.Status == "paid" && old.Status == "paid" && old.TreesPlanted > 0 && st.TreesPlanted >= 0 {
			var total float64
			if err := scanIndex(tx, treeBucketIndexPrefix("all"), 0, func(cid string) error {
				var c TreeContribution
				if err := getJSON(tx, treeContribKey(cid), &c); err != nil {
					if errors.Is(err, ErrNotFound) {
						return nil
					}
					return err
				}
				if c.SettlementID == id {
					total += c.Trees
				}
				return nil
			}); err != nil {
				return err
			}
			if total > 0 {
				return scanIndex(tx, treeBucketIndexPrefix("all"), 0, func(cid string) error {
					var c TreeContribution
					if err := getJSON(tx, treeContribKey(cid), &c); err != nil {
						if errors.Is(err, ErrNotFound) {
							return nil
						}
						return err
					}
					if c.SettlementID == id {
						c.SettledTrees = c.Trees * st.TreesPlanted / total
						raw, err := marshal(c)
						if err != nil {
							return err
						}
						return tx.Set(treeContribKey(cid), raw)
					}
					return nil
				})
			}
		}
		raw, err := marshal(st)
		if err != nil {
			return err
		}
		return tx.Set(treeSettKey(id), raw)
	})
}

// RecordTreeSettlementWithMeta records either a paid month or an intentionally
// skipped month. Skipped months remain visible so a small balance is never
// mistaken for a missing payout.
func (s *Store) RecordTreeSettlementWithMeta(monthBucket, txHash, transactionURL, fromAddr, toAddr string, amountUSDT float64, amountLabel, amountValue string, treesPlanted, amountNIM, walletNIM float64, proofImages []TreeProofImage, status, note string) (TreeSettlement, error) {
	now := time.Now().UTC()
	if status != "skipped" {
		status = "paid"
	}
	if monthBucket == "" {
		monthBucket = now.AddDate(0, -1, 0).Format("2006-01") // last month default
	}
	st := TreeSettlement{
		ID:             uuid.NewString(),
		MonthBucket:    monthBucket,
		AmountUSDT:     amountUSDT,
		TxHash:         txHash,
		TransactionURL: transactionURL,
		FromAddress:    fromAddr,
		ToAddress:      toAddr,
		Note:           note,
		Status:         status,
		AmountLabel:    amountLabel,
		AmountValue:    amountValue,
		TreesPlanted:   treesPlanted,
		AmountNIM:      amountNIM,
		WalletNIM:      walletNIM,
		ProofImages:    proofImages,
		CreatedAt:      now,
	}
	err := s.Update(func(tx *badger.Txn) error {
		raw, e := marshal(st)
		if e != nil {
			return e
		}
		if e := tx.Set(treeSettKey(st.ID), raw); e != nil {
			return e
		}
		if e := tx.Set(treeSettIndexKey(st.CreatedAt.UnixNano(), st.ID), []byte(st.ID)); e != nil {
			return e
		}

		// ---- reconcile pending contributions to the actual payout ----
		// In the current manual flow the admin's tree count is authoritative;
		// this also works when the payout is not USDT. The legacy amount-based
		// fallback remains for old callers/tests.
		if status != "paid" || (treesPlanted <= 0 && amountUSDT <= 0) {
			return nil
		}
		var pend []*TreeContribution
		if e := scanIndex(tx, treeBucketIndexPrefix("all"), 0, func(id string) error {
			var c TreeContribution
			if er := getJSON(tx, treeContribKey(id), &c); er != nil {
				if errors.Is(er, ErrNotFound) {
					return nil
				}
				return er
			}
			if !c.Settled {
				cp := c
				pend = append(pend, &cp)
			}
			return nil
		}); e != nil {
			return e
		}
		var totalUSD, totalTrees float64
		for _, c := range pend {
			totalUSD += float64(c.AmountUSD) / 1_000_000.0
			totalTrees += c.Trees
		}
		if totalTrees <= 0 {
			return nil
		}
		ratio := treesPlanted / totalTrees
		if treesPlanted <= 0 {
			if totalUSD <= 0 {
				return nil
			}
			ratio = amountUSDT / totalUSD
		}
		for _, c := range pend {
			c.Settled = true
			c.SettlementID = st.ID
			c.SettledTrees = c.Trees * ratio
			cr, er := marshal(c)
			if er != nil {
				return er
			}
			if er := tx.Set(treeContribKey(c.ID), cr); er != nil {
				return er
			}
		}
		return nil
	})
	return st, err
}
