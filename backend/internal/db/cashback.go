package db

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/dgraph-io/badger/v4"
	"github.com/google/uuid"

	adminmodel "nimiqshop/internal/admin"
	"nimiqshop/internal/cashback"
	"nimiqshop/internal/nimiq"
	"nimiqshop/internal/stakeledger"
)

// ledgerBoostBpsInTx reads the wallet's current ledger boost rate (bps)
// without mutating anything. Runs inside the caller's open transaction so
// the rate and the later debit see one consistent snapshot. 0 when there is
// no ledger, no price, or the book is empty — the base rate then stands.
func ledgerBoostBpsInTx(tx *badger.Txn, address string, nimUsd float64, p stakeledger.Params) int {
	addr := canonicalStakeAddr(address)
	if addr == "" || nimUsd <= 0 {
		return 0
	}
	var l stakeledger.Ledger
	if err := getJSON(tx, []byte(prefixStakeLedger+addr), &l); err != nil {
		return 0
	}
	return stakeledger.BoostBps(&l, nimUsd, p)
}

const (
	CashbackQueued    = "queued"
	CashbackSending   = "sending"
	CashbackBroadcast = "broadcast"
	CashbackPaid      = "paid"
	CashbackSkipped   = "skipped"
)

// StakerStake is the pool-stake fact resolved BEFORE the fulfillment
// transaction opened. The zero value means "not staked, or the pool could
// not be asked" — both resolve to the base rate, never to a free boost.
// LockedDays is how long the stake has been observed positive in the pool,
// used to grant locked-time loyalty bonuses.
type StakerStake struct {
	StakeLuna  int64
	Staked     bool
	LockedDays int
	// BaseBps is the flat base rate the POOL grants this buyer right now
	// (its staker base while staked, 0 otherwise). It is the pool's number,
	// applied verbatim: the shop never derives a base from the amount.
	BaseBps int
}

// EffectiveBaseBps is THE base-rate rule for a buyer: the pool's staker base
// when the buyer is staked (any amount), otherwise the operator's universal
// base (0 by default — there is no base for non-stakers unless the operator
// runs a promotion). The higher of the two wins so a promo never pays a
// staker less than a non-staker.
func (st StakerStake) EffectiveBaseBps(operatorBase int) int {
	if st.Staked && st.BaseBps > operatorBase {
		return st.BaseBps
	}
	if operatorBase < 0 {
		return 0
	}
	return operatorBase
}

// enqueueCashbackOnFulfill writes at most one cashback row for this quote
// inside the same transaction that marked it fulfilled. A later retry of
// the fulfilled transition is a no-op because of the quote-id index.
//
// v2 rate model (single ledger / Tek Defter):
//
//	base   — the pool's staker base while the buyer is staked (any amount),
//	         else the operator's universal base (0 by default), paid from the
//	         shop wallet off the product's NIM price (v1 mechanism, unchanged)
//	boost  — the wallet's ledger rate min(CB_MAX, A*P/basis), paid from the
//	         realized-fee ledger (A), debited ATOMICALLY in this same
//	         transaction (store.ApplyLedgerPurchaseInTx). Capped by the
//	         $50/day + $500/month eligible-spend limits.
//	code   — a valid promo code is
//	         EXCLUSIVE: its rate replaces base and no ledger boost stacks on
//	         top. Nothing else applies to that order — the code is the only
//	         rate.
//
// stake and ledgerParams are passed in rather than looked up here on
// purpose: this runs inside an open Badger write transaction, and a pool
// API call or a settings read in here would hold it for a round trip.
// CashbackEnrichment carries runtime config (burn wallet address) into the
// enqueue path. Both payment rails pay the same cashback rate — there is
// no stablecoin reduction.
type CashbackEnrichment struct {
	BurnAddr string
}

// positiveCoinUnits parses a supplier decimal amount ("0.00150000") into a
// float, returning 0 for empty/malformed/non-positive values.
func positiveCoinUnits(v string) float64 {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) || f <= 0 {
		return 0
	}
	return f
}

// cashbackBaseNIM uses only the shop's persisted, displayed order-price
// snapshot. Nimiq Pay's NIM-to-BTC swap costs, wallet/network fees and any
// overpayment never enlarge this base. Supplier receipt fields remain audit
// evidence, not a second cashback price source. The legacy JSON source value
// "priced" is retained; historical "paid" rows are not rewritten.
func cashbackBaseNIM(productNIM float64) (float64, string) {
	if productNIM <= 0 || math.IsNaN(productNIM) || math.IsInf(productNIM, 0) {
		return 0, "priced"
	}
	return productNIM, "priced"
}

func enqueueCashbackOnFulfill(tx *badger.Txn, q *Quote, stake StakerStake, ledgerParams stakeledger.Params, enrich CashbackEnrichment) error {
	if q == nil || q.ID == "" {
		return nil
	}
	// ADMIN TEST CENTER: a test quote DOES enqueue — the whole point is
	// that everything but the payment works. The row carries TestMode so
	// the worker pays it SIMULATED (TESTTX-… hash, no signing, no RPC) and
	// the admin views can badge it.
	if _, err := tx.Get(cashbackQuoteIndexKey(q.ID)); err == nil {
		return nil
	} else if !errors.Is(err, badger.ErrKeyNotFound) {
		return err
	}

	settings := adminmodel.Settings{}
	_ = getJSON(tx, []byte(adminSettingsKey), &settings)
	// Staker base is the admin-panel rate (default 1%), not the pool's.
	if stake.Staked {
		stake.BaseBps = settings.EffectiveStakerCashbackBps()
	}
	productNIM := q.EstimatedNIM
	productUSD := float64(q.ProductUSD) / 1_000_000
	if productNIM <= 0 && q.NimUsdRate > 0 && q.ProductUSD > 0 {
		productNIM = productUSD / q.NimUsdRate
	}
	// Use exactly the locked shop snapshot; no paid/invoice ratio or wallet debit.
	baseNIM, baseSource := cashbackBaseNIM(productNIM)
	// When a promo code has a partial max-order cap (e.g. $50 cart with $20
	// cap), the promo BPS applies ONLY to the capped portion. The over-cap
	// remainder is paid out at the buyer's base rate so we don't silently
	// refuse the cart. promoCapUSD <= 0 means no cap (full cart at promo).
	promoCapUSD := q.CashbackCodeMaxUSD
	if promoCapUSD <= 0 || promoCapUSD > productUSD {
		promoCapUSD = productUSD
	}
	promoCapNIM := productNIM
	if q.NimUsdRate > 0 && promoCapUSD < productUSD {
		promoCapNIM = promoCapUSD / q.NimUsdRate
	}
	if promoCapNIM > baseNIM {
		promoCapNIM = baseNIM
	}
	overCapNIM := baseNIM - promoCapNIM
	if overCapNIM < 0 {
		overCapNIM = 0
	}
	now := time.Now().UTC()

	// Buyer address (needed for the ledger; also the default payout recipient).
	var user User
	addr := ""
	if e := getJSON(tx, userKey(q.UserID), &user); e == nil {
		addr = user.NimiqAddress
	}

	// The base is the operator's call: staked (any amount) → the staker
	// base; not staked → the operator's universal base (0 by default). The
	// higher of the two wins so a promo never pays a staker less than a
	// non-staker.
	base := stake.EffectiveBaseBps(settings.EffectiveCashbackBps())
	stakerBase := stake.Staked && stake.BaseBps > 0 && base == stake.BaseBps

	// Resolve cashback destination: buyer wallet (default) or burn wallet.
	dest := strings.ToLower(strings.TrimSpace(q.CashbackDestination))
	if dest != CashbackDestBurn {
		dest = CashbackDestWallet
	}
	recipientAddr := addr
	if dest == CashbackDestBurn {
		if strings.TrimSpace(enrich.BurnAddr) != "" {
			recipientAddr = strings.TrimSpace(enrich.BurnAddr)
		} else {
			recipientAddr = BurnNIMAddress
		}
	}

	// The ledger boost rate for this wallet at the quote's price. 0 when
	// there is no price, no ledger, or an empty book — the base rate then
	// stands on its own. Never guesses: a missing P means no boost.
	nimUsd := q.NimUsdRate
	boostBps := 0
	if addr != "" && nimUsd > 0 {
		boostBps = ledgerBoostBpsInTx(tx, addr, nimUsd, ledgerParams)
	}

	// Resolve the payout. walletBps is the rate applied to the product's NIM
	// price (paid from the shop wallet); the boost part is the exact ledger
	// debit boostLuna (eligible-spend capped) and is NOT part of walletBps —
	// adding it twice would over-pay capped orders. displayBps is what the
	// row/UI report as the buyer's total rate.
	walletBps := base
	displayBps := base
	source := "base"
	boostLuna := int64(0)
	debitLedger := func() (int, error) {
		var e error
		boostLuna, boostBps, e = ApplyLedgerPurchaseInTx(tx, addr, float64(q.ProductUSD)/1_000_000, nimUsd, ledgerParams, now)
		return boostBps, e
	}
	switch {
	case q.CashbackCodeBps > 0:
		// Exclusive promo code: replaces base on the (possibly capped)
		// promo portion of the cart. No ledger boost stacks on the promo
		// portion. If there is an over-cap remainder (cart > cap), the
		// remainder still earns the buyer's base rate.
		walletBps = q.CashbackCodeBps
		displayBps = q.CashbackCodeBps
		source = "code"
	default:
		if stakerBase {
			source = "staker"
		}
		if boostBps > 0 {
			bps, e := debitLedger()
			if e != nil {
				return e
			}
			displayBps = base + bps
			source = "ledger"
		}
	}

	cb := Cashback{
		ID:                  uuid.NewString(),
		TestMode:            q.TestMode,
		QuoteID:             q.ID,
		UserID:              q.UserID,
		ProductID:           q.ProductID,
		Bps:                 displayBps,
		ProductNIM:          productNIM,
		PaidBaseNIM:         baseNIM,
		BaseSource:          baseSource,
		CashbackSource:      source,
		CashbackCode:        q.CashbackCode,
		CashbackDestination: dest,
		CreatedAt:           now,
		UpdatedAt:           now,
	}
	if boostLuna > 0 || stakerBase {
		cb.Boosted = true
		cb.StakeLuna = stake.StakeLuna
		cb.BoostLuna = boostLuna
	}

	skip := ""
	switch {
	case displayBps <= 0 && boostLuna == 0:
		skip = "cashback percent is 0"
	case productNIM <= 0:
		skip = "product NIM price unknown — refused to guess"
	default:
		if recipientAddr == "" {
			skip = "buyer address unavailable"
		} else if err := nimiq.ValidateAddress(recipientAddr); err != nil {
			skip = "recipient address invalid"
		} else {
			cb.Recipient = recipientAddr
		}
	}
	if skip == "" {
		// Promo code cashback: apply the promo BPS only to the (possibly
		// max-order-capped) portion of the cart. Any over-cap remainder
		// still earns the buyer's base rate (or staker base). When there
		// is no cap, promoCapNIM == productNIM and overCapNIM == 0 so the
		// math collapses to the previous flat-rate behaviour.
		var promoLuna int64
		var overCapBaseLuna int64
		usingCode := q.CashbackCodeBps > 0
		switch {
		case usingCode:
			promoLuna = cashback.AmountLuna(promoCapNIM, walletBps)
			if overCapNIM > 0 {
				overCapBaseLuna = cashback.AmountLuna(overCapNIM, base)
			}
		default:
			promoLuna = cashback.AmountLuna(baseNIM, walletBps)
		}
		// Boost (ledger) is computed only on the full product USD via
		// debitLedger() above — it stays additive and is never
		// double-counted with the promo rate.
		cb.AmountLuna = promoLuna + overCapBaseLuna + boostLuna
		if cb.AmountLuna < 1 {
			skip = "cashback rounds to 0 Luna"
		} else {
			if dest == CashbackDestBurn {
				cb.Memo = cashback.BurnMemo(cashback.NIMFromLuna(cb.AmountLuna), q.ProductID)
			} else {
				cb.Memo = cashback.Memo(cashback.NIMFromLuna(cb.AmountLuna), q.ProductID)
			}
		}
	}
	if skip != "" {
		cb.Status = CashbackSkipped
		cb.SkipReason = skip
	} else {
		cb.Status = CashbackQueued
	}

	raw, err := marshal(cb)
	if err != nil {
		return err
	}
	if err := tx.Set(cashbackKey(cb.ID), raw); err != nil {
		return err
	}
	if err := tx.Set(cashbackQuoteIndexKey(q.ID), []byte(cb.ID)); err != nil {
		return err
	}
	if q.UserID != "" {
		if err := tx.Set(cashbackUserIndexKey(q.UserID, cb.ID), []byte(cb.ID)); err != nil {
			return err
		}
	}
	return tx.Set(cashbackStatusIndexKey(cb.Status, cb.ID), []byte(cb.ID))
}

// ReconcileStakerCashback repairs rows that were fulfilled while the pool API
// was unavailable. It is intentionally monotonic: it only increases an
// unpaid base-rate row, never rewrites a promo, a paid row, or a signed
// transaction. Re-running it is safe because the resulting amount is
// calculated from the existing row and the same locked product NIM.
//
// This is the recovery path for the important case "the buyer staked, then
// bought during an outage": once the stake is observed later, the missing
// staker portion becomes a normal queued cashback amount and goes through the
// existing exactly-once payout worker.
func (s *Store) ReconcileStakerCashback(address string, stake StakerStake, now time.Time) (int, error) {
	if canonicalStakeAddr(address) == "" || !stake.Staked || stake.BaseBps <= 0 {
		return 0, nil
	}
	u, err := s.GetUserByAddress(address)
	if err != nil {
		return 0, err
	}
	rows, err := s.ListCashbacksByUser(u.ID, 0)
	if err != nil {
		return 0, err
	}
	settings := adminmodel.Settings{}
	_ = s.View(func(tx *badger.Txn) error { return getJSON(tx, []byte(adminSettingsKey), &settings) })
	desired := stake.EffectiveBaseBps(settings.EffectiveCashbackBps())
	changed := 0
	for _, row := range rows {
		if row.StakeReconciled || row.CashbackCode != "" || row.CashbackSource == "code" || row.ProductNIM <= 0 {
			continue
		}
		baseAmount := cashback.AmountLuna(row.ProductNIM, desired)
		currentBase := row.AmountLuna - row.BoostLuna
		if currentBase >= baseAmount {
			continue
		}
		delta := baseAmount - currentBase
		if delta <= 0 {
			continue
		}
		// Both rails pay the same rate, so upgrades reconcile at 1×.
		mult := 1.0
		// A zero-rate skip is the "not staked at delivery" signature (the
		// operator's universal base is 0 and the buyer had no stake the
		// shop could verify). Now that the pool says staked, revive the row
		// as a normal queued cashback at the staker base. Other skips
		// (missing recipient, tree address unavailable…) are not the
		// stake's business and stay skipped.
		if row.Status == CashbackSkipped && row.AmountLuna == 0 && row.Bps == 0 && isZeroRateSkip(row.SkipReason) {
			amount := int64(float64(baseAmount) * mult)
			if amount < 1 {
				continue
			}
			// The zero-rate skip returned before the recipient was ever
			// recorded, so reviving the row to "queued" without a recipient
			// would create a cashback the payout worker can never send
			// (sendOne refuses Recipient==""). That is a silent miss — the
			// exact case this reconcile exists to prevent. Pay the buyer's own
			// (now-verified staked) wallet, or the tree address for a tree row.
			reviveRecipient := u.NimiqAddress
			if row.CashbackDestination == CashbackDestBurn {
				reviveRecipient = strings.TrimSpace(s.cashbackEnrich().BurnAddr)
			}
			if reviveRecipient == "" || nimiq.ValidateAddress(reviveRecipient) != nil {
				// No valid recipient to pay: leave the row skipped rather than
				// queue an unpayable one. A later pass can still revive it.
				continue
			}
			err := s.Update(func(tx *badger.Txn) error {
				var cb Cashback
				if err := getJSON(tx, cashbackKey(row.ID), &cb); err != nil {
					return err
				}
				if cb.StakeReconciled || cb.Status != CashbackSkipped || cb.AmountLuna != 0 || cb.Bps != 0 || !isZeroRateSkip(cb.SkipReason) {
					return nil
				}
				cb.Status = CashbackQueued
				cb.Recipient = reviveRecipient // CRITICAL: was missing → missed cashback
				cb.AmountLuna = amount
				cb.Bps = desired
				cb.Boosted = true
				cb.StakeLuna = stake.StakeLuna
				cb.StakeReconciled = true
				cb.SkipReason = ""
				cb.LastError = ""
				if cb.CashbackDestination == CashbackDestBurn {
					cb.Memo = cashback.BurnMemo(cashback.NIMFromLuna(cb.AmountLuna), cb.ProductID)
				} else {
					cb.Memo = cashback.Memo(cashback.NIMFromLuna(cb.AmountLuna), cb.ProductID)
				}
				cb.UpdatedAt = now.UTC()
				raw, e := marshal(cb)
				if e != nil {
					return e
				}
				if e = tx.Set(cashbackKey(cb.ID), raw); e != nil {
					return e
				}
				if e = tx.Delete(cashbackStatusIndexKey(CashbackSkipped, cb.ID)); e != nil {
					return e
				}
				return tx.Set(cashbackStatusIndexKey(CashbackQueued, cb.ID), []byte(cb.ID))
			})
			if err != nil {
				return changed, err
			}
			changed++
			continue
		}
		if row.Status == CashbackQueued && row.SignedTxHex == "" {
			err := s.patchCashback(row.ID, func(cb *Cashback, _ *badger.Txn) error {
				if cb.Status != CashbackQueued || cb.SignedTxHex != "" || cb.StakeReconciled {
					return nil
				}
				cb.AmountLuna += int64(float64(delta) * mult)
				if cb.Bps < desired {
					cb.Bps = desired
				}
				cb.Boosted = true
				cb.StakeLuna = stake.StakeLuna
				cb.StakeReconciled = true
				cb.LastError = ""
				return nil
			})
			if err != nil {
				return changed, err
			}
			changed++
			continue
		}
		if row.Status == CashbackPaid || row.Status == CashbackBroadcast || row.Status == CashbackSending {
			err := s.Update(func(tx *badger.Txn) error {
				var original Cashback
				if err := getJSON(tx, cashbackKey(row.ID), &original); err != nil {
					return err
				}
				if original.StakeReconciled {
					return nil
				}
				adjustAmount := int64(float64(delta) * mult)
				if adjustAmount < 1 {
					return nil
				}
				adjust := Cashback{ID: uuid.NewString(), QuoteID: row.QuoteID + "/stake-reconcile/" + uuid.NewString(), UserID: row.UserID, Recipient: row.Recipient, ProductID: row.ProductID, Bps: desired, AmountLuna: adjustAmount, Memo: cashback.Memo(cashback.NIMFromLuna(adjustAmount), row.ProductID), Status: CashbackQueued, CashbackSource: "staker_reconcile", CashbackDestination: row.CashbackDestination, StakeLuna: stake.StakeLuna, Boosted: true, CreatedAt: now.UTC(), UpdatedAt: now.UTC()}
				raw, e := marshal(adjust)
				if e != nil {
					return e
				}
				if e = tx.Set(cashbackKey(adjust.ID), raw); e != nil {
					return e
				}
				if e = tx.Set(cashbackUserIndexKey(adjust.UserID, adjust.ID), []byte(adjust.ID)); e != nil {
					return e
				}
				if e = tx.Set(cashbackStatusIndexKey(adjust.Status, adjust.ID), []byte(adjust.ID)); e != nil {
					return e
				}
				original.StakeReconciled = true
				originalRaw, e := marshal(original)
				if e != nil {
					return e
				}
				return tx.Set(cashbackKey(original.ID), originalRaw)
			})
			if err != nil {
				return changed, err
			}
			changed++
		}
	}
	return changed, nil
}

// UserCashbackTotals sums a buyer's cashback: paid (on-chain) vs still in flight.
func (s *Store) UserCashbackTotals(userID string) (paidLuna, pendingLuna int64, paidCount, pendingCount int, err error) {
	sum, err := s.GetUserCashbackSummary(userID)
	return sum.PaidLuna, sum.PendingLuna, sum.PaidCount, sum.PendingCount, err
}

// UserCashbackSummary holds a single user's lifetime cashback & burn breakdown.
type UserCashbackSummary struct {
	PaidLuna     int64
	PendingLuna  int64
	BurnedLuna   int64
	WalletLuna   int64
	PaidCount    int
	PendingCount int
	BurnedCount  int
	WalletCount  int
	LastDest     string
}

func (s *Store) GetUserCashbackSummary(userID string) (UserCashbackSummary, error) {
	var out UserCashbackSummary
	if userID == "" {
		return out, nil
	}
	var latestTime time.Time
	err := s.View(func(tx *badger.Txn) error {
		return scanIndex(tx, cashbackUserIndexPrefix(userID), 0, func(id string) error {
			var cb Cashback
			if e := getJSON(tx, cashbackKey(id), &cb); e != nil {
				if errors.Is(e, ErrNotFound) {
					return nil
				}
				return e
			}
			if cb.CreatedAt.After(latestTime) && (cb.CashbackDestination == CashbackDestBurn || cb.CashbackDestination == CashbackDestWallet) {
				latestTime = cb.CreatedAt
				out.LastDest = cb.CashbackDestination
			}
			switch cb.Status {
			case CashbackPaid:
				out.PaidLuna += cb.AmountLuna
				out.PaidCount++
			case CashbackQueued, CashbackSending, CashbackBroadcast:
				out.PendingLuna += cb.AmountLuna
				out.PendingCount++
			default:
				return nil
			}
			if cb.CashbackDestination == CashbackDestBurn {
				out.BurnedLuna += cb.AmountLuna
				out.BurnedCount++
			} else {
				out.WalletLuna += cb.AmountLuna
				out.WalletCount++
			}
			return nil
		})
	})
	if out.LastDest == "" {
		out.LastDest = CashbackDestWallet
	}
	return out, err
}

// CashbackPublicTotals holds global aggregate cashback & burn stats across all orders.
type CashbackPublicTotals struct {
	TotalNIM      float64 `json:"total_nim"`
	PaidNIM       float64 `json:"paid_nim"`
	PendingNIM    float64 `json:"pending_nim"`
	BurnedNIM     float64 `json:"burned_nim"`
	BurnedPaidNIM float64 `json:"burned_paid_nim"`
	WalletNIM     float64 `json:"wallet_nim"`
	TotalUSD      float64 `json:"total_usd"`
	BurnedUSD     float64 `json:"burned_usd"`
	Orders        int     `json:"orders"`
	BurnedOrders  int     `json:"burned_orders"`
	Earners       int     `json:"earners"`
}

// CashbackLeaderRow is one entry on the public cashback & burn leaderboard.
type CashbackLeaderRow struct {
	Rank      int     `json:"rank"`
	User      string  `json:"user"`
	TotalNIM  float64 `json:"total_nim"`
	BurnedNIM float64 `json:"burned_nim"`
	WalletNIM float64 `json:"wallet_nim"`
	Orders    int     `json:"orders"`
}

func isoWeekBucket(t time.Time) string {
	y, w := t.UTC().ISOWeek()
	return fmt.Sprintf("%04dW%02d", y, w)
}

// CashbackLeaderboardAndTotals scans active/paid cashback records to compute
// global totals (all-time) and the top N earners for the requested time bucket
// ("all", "week:YYYYWww", or "month:YYYY-MM").
func (s *Store) CashbackLeaderboardAndTotals(bucket string, limit int) (CashbackPublicTotals, []CashbackLeaderRow, error) {
	if limit <= 0 {
		limit = 50
	}
	var totals CashbackPublicTotals
	earnersSeen := map[string]bool{}
	type agg struct {
		user      string
		totalLuna int64
		burnLuna  int64
		walLuna   int64
		orders    int
	}
	byUser := map[string]*agg{}

	err := s.View(func(tx *badger.Txn) error {
		for _, st := range []string{CashbackPaid, CashbackQueued, CashbackSending, CashbackBroadcast} {
			if err := scanIndex(tx, cashbackStatusIndexPrefix(st), 0, func(id string) error {
				var cb Cashback
				if e := getJSON(tx, cashbackKey(id), &cb); e != nil {
					if errors.Is(e, ErrNotFound) {
						return nil
					}
					return e
				}
				if cb.AmountLuna <= 0 {
					return nil
				}
				nim := float64(cb.AmountLuna) / cashback.LunaPerNIM
				isBurn := cb.CashbackDestination == CashbackDestBurn

				totals.TotalNIM += nim
				totals.Orders++
				if st == CashbackPaid {
					totals.PaidNIM += nim
				} else {
					totals.PendingNIM += nim
				}
				if isBurn {
					totals.BurnedNIM += nim
					totals.BurnedOrders++
					if st == CashbackPaid {
						totals.BurnedPaidNIM += nim
					}
				} else {
					totals.WalletNIM += nim
				}
				uidKey := cb.UserID
				if uidKey == "" {
					uidKey = cb.Recipient
				}
				if uidKey != "" && !earnersSeen[uidKey] {
					earnersSeen[uidKey] = true
					totals.Earners++
				}

				// Check time bucket filter for the leaderboard
				ts := cb.CreatedAt.UTC()
				if strings.HasPrefix(bucket, "week:") {
					if "week:"+isoWeekBucket(ts) != bucket {
						return nil
					}
				} else if strings.HasPrefix(bucket, "month:") {
					if "month:"+ts.Format("2006-01") != bucket {
						return nil
					}
				}

				if uidKey == "" {
					return nil
				}
				// Respect quote anonymity on the public leaderboard
				anon := false
				if cb.QuoteID != "" {
					var q Quote
					if err := getJSON(tx, quoteKey(cb.QuoteID), &q); err == nil && q.Anonymous {
						anon = true
					}
				}
				displayUser := ""
				if !anon {
					if cb.UserID != "" {
						var u User
						if err := getJSON(tx, userKey(cb.UserID), &u); err == nil && u.NimiqAddress != "" {
							displayUser = u.NimiqAddress
						}
					}
					if displayUser == "" && !isBurn {
						displayUser = cb.Recipient
					}
				}
				if anon || displayUser == "" {
					displayUser = uidKey
					if len(displayUser) > 8 {
						displayUser = displayUser[:8]
					}
				}
				key := uidKey
				if anon {
					key = "anon:" + uidKey
				}
				row := byUser[key]
				if row == nil {
					row = &agg{user: displayUser}
					byUser[key] = row
				}
				row.totalLuna += cb.AmountLuna
				if isBurn {
					row.burnLuna += cb.AmountLuna
				} else {
					row.walLuna += cb.AmountLuna
				}
				row.orders++
				return nil
			}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return totals, nil, err
	}

	list := make([]*agg, 0, len(byUser))
	for _, v := range byUser {
		list = append(list, v)
	}
	for i := 0; i < len(list); i++ {
		for j := i + 1; j < len(list); j++ {
			if list[j].totalLuna > list[i].totalLuna {
				list[i], list[j] = list[j], list[i]
			}
		}
	}
	if len(list) > limit {
		list = list[:limit]
	}
	out := make([]CashbackLeaderRow, 0, len(list))
	for i, r := range list {
		out = append(out, CashbackLeaderRow{
			Rank:      i + 1,
			User:      r.user,
			TotalNIM:  float64(r.totalLuna) / cashback.LunaPerNIM,
			BurnedNIM: float64(r.burnLuna) / cashback.LunaPerNIM,
			WalletNIM: float64(r.walLuna) / cashback.LunaPerNIM,
			Orders:    r.orders,
		})
	}
	return totals, out, nil
}

func (s *Store) GetCashback(id string) (Cashback, error) {
	var cb Cashback
	err := s.View(func(tx *badger.Txn) error { return getJSON(tx, cashbackKey(id), &cb) })
	return cb, err
}

func (s *Store) GetCashbackByQuote(quoteID string) (Cashback, error) {
	var cb Cashback
	err := s.View(func(tx *badger.Txn) error {
		id, err := getString(tx, cashbackQuoteIndexKey(quoteID))
		if err != nil {
			return err
		}
		return getJSON(tx, cashbackKey(id), &cb)
	})
	return cb, err
}

func (s *Store) ListCashbacksByStatus(status string, limit int) ([]Cashback, error) {
	var out []Cashback
	err := s.View(func(tx *badger.Txn) error {
		return scanIndex(tx, cashbackStatusIndexKey(status, ""), limit, func(id string) error {
			var cb Cashback
			if err := getJSON(tx, cashbackKey(id), &cb); err != nil {
				if errors.Is(err, ErrNotFound) {
					return nil
				}
				return err
			}
			out = append(out, cb)
			return nil
		})
	})
	return out, err
}

// PurgeOrphanCashbacks deletes cashback rows whose quote no longer exists —
// the ledger orphans the owner's one-shot order wipe left behind (2026-10-05).
// They inflated the public totals and the leaderboard with fake "pending"
// NIM. A row with no QuoteID or a lookup error other than not-found is kept.
func (s *Store) PurgeOrphanCashbacks() (int, error) {
	type victim struct {
		key    []byte
		userID string
		id     string
	}
	var victims []victim
	err := s.View(func(txn *badger.Txn) error {
		return scanJSONPrefix(txn, []byte("cb:"), func(item *badger.Item) error {
			var cb Cashback
			if err := item.Value(func(value []byte) error { return unmarshal(value, &cb) }); err != nil {
				return err
			}
			if cb.QuoteID != "" {
				var q Quote
				e := getJSON(txn, quoteKey(cb.QuoteID), &q)
				if e == nil {
					return nil // quote alive — row stays
				}
				if !errors.Is(e, ErrNotFound) {
					return nil // lookup broken — never delete on uncertainty
				}
			}
			// Orphaned by the wipe, OR an adjustment/boost row with no quote
			// link at all — both are test-era ledger dust (owner: only the
			// real UniPin survives).
			victims = append(victims, victim{key: item.KeyCopy(nil), userID: cb.UserID, id: cb.ID})
			return nil
		})
	})
	if err != nil || len(victims) == 0 {
		return 0, err
	}
	err = s.Update(func(txn *badger.Txn) error {
		for _, v := range victims {
			if err := txn.Delete(v.key); err != nil {
				return err
			}
			if v.userID != "" {
				if err := txn.Delete(cashbackUserIndexKey(v.userID, v.id)); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return len(victims), nil
}

func (s *Store) ListRecentCashbacks(limit int) ([]Cashback, error) {
	if limit <= 0 {
		limit = 50
	}
	var out []Cashback
	for _, st := range []string{CashbackQueued, CashbackSending, CashbackBroadcast, CashbackPaid, CashbackSkipped} {
		part, err := s.ListCashbacksByStatus(st, limit)
		if err != nil {
			return nil, err
		}
		out = append(out, part...)
		if len(out) >= limit {
			break
		}
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// ClaimCashbackSend moves queued → sending. A sending row that already has
// SignedTxHex is returned as-is (crash resume — never a second signature).
// A sending row with no hex after 2 minutes crashed before persist; that
// window never reached the network, so re-queue is safe.
func (s *Store) ClaimCashbackSend(id string) (Cashback, error) {
	var cb Cashback
	err := s.Update(func(tx *badger.Txn) error {
		if err := getJSON(tx, cashbackKey(id), &cb); err != nil {
			return err
		}
		if cb.Status == CashbackPaid || cb.Status == CashbackBroadcast || cb.Status == CashbackSkipped {
			return ErrConflict
		}
		if cb.Status == CashbackSending && cb.SignedTxHex != "" {
			return nil // resume the same signed bytes
		}
		if cb.Status == CashbackSending && cb.SignedTxHex == "" && cb.SendingAt != nil && time.Since(*cb.SendingAt) > 2*time.Minute {
			if err := tx.Delete(cashbackStatusIndexKey(cb.Status, cb.ID)); err != nil {
				return err
			}
			cb.Status = CashbackQueued
			cb.SendingAt = nil
		}
		if cb.Status != CashbackQueued {
			return ErrConflict
		}
		now := time.Now().UTC()
		if err := tx.Delete(cashbackStatusIndexKey(cb.Status, cb.ID)); err != nil {
			return err
		}
		cb.Status = CashbackSending
		cb.SendingAt = &now
		cb.UpdatedAt = now
		cb.LastError = ""
		raw, err := marshal(cb)
		if err != nil {
			return err
		}
		if err := tx.Set(cashbackKey(cb.ID), raw); err != nil {
			return err
		}
		return tx.Set(cashbackStatusIndexKey(cb.Status, cb.ID), []byte(cb.ID))
	})
	return cb, err
}

// AttachCashbackSignedTx records the one-and-only signed payload before any
// broadcast. A second, different hex is rejected (ErrConflict) so a crash
// cannot mint a second payment.
func (s *Store) AttachCashbackSignedTx(id, txHex, txHash string, validityStart uint32) error {
	txHex = strings.TrimSpace(txHex)
	if txHex == "" {
		return ErrConflict
	}
	return s.patchCashback(id, func(cb *Cashback, tx *badger.Txn) error {
		if cb.Status != CashbackSending && cb.Status != CashbackQueued {
			return ErrConflict
		}
		if cb.SignedTxHex != "" {
			if cb.SignedTxHex != txHex {
				return ErrConflict
			}
			return nil
		}
		cb.SignedTxHex = txHex
		if txHash != "" && cb.TxHash == "" {
			cb.TxHash = txHash
		}
		cb.ValidityStart = validityStart
		return nil
	})
}

func (s *Store) MarkCashbackBroadcast(id, txHash string) error {
	return s.patchCashback(id, func(cb *Cashback, tx *badger.Txn) error {
		if cb.Status != CashbackSending && cb.Status != CashbackBroadcast {
			return ErrConflict
		}
		if cb.TxHash != "" && cb.TxHash != txHash {
			return ErrConflict
		}
		if err := tx.Delete(cashbackStatusIndexKey(cb.Status, cb.ID)); err != nil {
			return err
		}
		cb.Status = CashbackBroadcast
		cb.TxHash = txHash
		cb.LastError = ""
		return tx.Set(cashbackStatusIndexKey(cb.Status, cb.ID), []byte(cb.ID))
	})
}

func (s *Store) MarkCashbackPaid(id, txHash string) error {
	return s.patchCashback(id, func(cb *Cashback, tx *badger.Txn) error {
		if cb.Status == CashbackPaid {
			return nil
		}
		if err := tx.Delete(cashbackStatusIndexKey(cb.Status, cb.ID)); err != nil {
			return err
		}
		now := time.Now().UTC()
		cb.Status = CashbackPaid
		if txHash != "" {
			cb.TxHash = txHash
		}
		cb.PaidAt = &now
		cb.LastError = ""
		return tx.Set(cashbackStatusIndexKey(cb.Status, cb.ID), []byte(cb.ID))
	})
}

func (s *Store) FailCashbackSend(id, reason string) error {
	return s.patchCashback(id, func(cb *Cashback, tx *badger.Txn) error {
		if cb.Status != CashbackSending {
			return nil
		}
		cb.LastError = reason
		if cb.SignedTxHex != "" {
			// Already signed: stay sending so the worker rebroadcasts the
			// same bytes. Never drop back to a "fresh" queued that could
			// be mistaken for unsigned.
			return nil
		}
		if err := tx.Delete(cashbackStatusIndexKey(cb.Status, cb.ID)); err != nil {
			return err
		}
		cb.Status = CashbackQueued
		cb.SendingAt = nil
		return tx.Set(cashbackStatusIndexKey(cb.Status, cb.ID), []byte(cb.ID))
	})
}

func (s *Store) patchCashback(id string, mutate func(*Cashback, *badger.Txn) error) error {
	return s.Update(func(tx *badger.Txn) error {
		var cb Cashback
		if err := getJSON(tx, cashbackKey(id), &cb); err != nil {
			return err
		}
		if err := mutate(&cb, tx); err != nil {
			return err
		}
		cb.UpdatedAt = time.Now().UTC()
		raw, err := marshal(cb)
		if err != nil {
			return err
		}
		return tx.Set(cashbackKey(cb.ID), raw)
	})
}

// ListCashbacksByUser returns a buyer's cashback rows, in-flight first: the
// profile/cashback page shows "pending" and "paid" in one list, and a queued
// payout is the one a customer is actually waiting on.
func (s *Store) ListCashbacksByUser(userID string, limit int) ([]Cashback, error) {
	if userID == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = 50
	}
	// Pending states first, then paid, then skipped — the same order the UI
	// wants, so no client-side sorting is needed.
	order := []string{CashbackSending, CashbackBroadcast, CashbackQueued, CashbackPaid, CashbackSkipped}
	var out []Cashback
	for _, st := range order {
		err := s.View(func(tx *badger.Txn) error {
			// The user index is not partitioned by status, so scan the user's
			// rows once and filter. A buyer has a handful of rows, not a table.
			return scanIndex(tx, cashbackUserIndexPrefix(userID), 0, func(id string) error {
				var cb Cashback
				if err := getJSON(tx, cashbackKey(id), &cb); err != nil {
					if errors.Is(err, ErrNotFound) {
						return nil
					}
					return err
				}
				if cb.Status != st {
					return nil
				}
				out = append(out, cb)
				return nil
			})
		})
		if err != nil {
			return nil, err
		}
		if len(out) >= limit {
			break
		}
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// CashbackStatusCounts is the operator console's "how many payouts are stuck?"
// answer, keyed by status. It counts the status index directly, so it stays
// correct even for rows a page-sized listing would miss.
func (s *Store) CashbackStatusCounts() (map[string]int, error) {
	counts := map[string]int{}
	for _, st := range []string{CashbackQueued, CashbackSending, CashbackBroadcast, CashbackPaid, CashbackSkipped} {
		counts[st] = 0
	}
	err := s.View(func(tx *badger.Txn) error {
		for st := range counts {
			n := 0
			if err := scanIndex(tx, cashbackStatusIndexPrefix(st), 0, func(string) error {
				n++
				return nil
			}); err != nil {
				return err
			}
			counts[st] = n
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return counts, nil
}

// isZeroRateSkip reports whether a cashback row was skipped purely because
// the buyer had no stake-backed rate (the operator's universal base is 0).
// Only these rows may be revived by a later stake observation; every other
// skip reason (bad recipient, missing tree address, unknown price) is
// orthogonal to staking and must not be touched.
func isZeroRateSkip(reason string) bool {
	return reason == "cashback rounds to 0 Luna" || reason == "cashback percent is 0"
}

// QueueManualCashback inserts a REAL (TestMode=false) queued cashback row
// with its index keys. It powers cmd/cashback-test: an operator can drive a
// genuine end-to-end payout — sign → persist hex → broadcast → confirm →
// paid — through the normal exactly-once worker, without placing a real
// supplier order. The row is indistinguishable from a shop-earned cashback.
func (s *Store) QueueManualCashback(recipient, memo string, amountLuna int64, now time.Time) (Cashback, error) {
	if amountLuna < 1 {
		return Cashback{}, fmt.Errorf("amount must be at least 1 luna")
	}
	if err := nimiq.ValidateAddress(recipient); err != nil {
		return Cashback{}, fmt.Errorf("recipient: %w", err)
	}
	id := uuid.NewString()
	cb := Cashback{
		ID:                  id,
		QuoteID:             "manual/" + id,
		Recipient:           recipient,
		ProductID:           "manual-payout",
		AmountLuna:          amountLuna,
		Memo:                memo,
		Status:              CashbackQueued,
		CashbackSource:      "manual",
		CashbackDestination: CashbackDestWallet,
		CreatedAt:           now.UTC(),
		UpdatedAt:           now.UTC(),
	}
	err := s.Update(func(tx *badger.Txn) error {
		raw, err := marshal(cb)
		if err != nil {
			return err
		}
		if err := tx.Set(cashbackKey(cb.ID), raw); err != nil {
			return err
		}
		if err := tx.Set(cashbackQuoteIndexKey(cb.QuoteID), []byte(cb.ID)); err != nil {
			return err
		}
		return tx.Set(cashbackStatusIndexKey(cb.Status, cb.ID), []byte(cb.ID))
	})
	if err != nil {
		return Cashback{}, err
	}
	return cb, nil
}
