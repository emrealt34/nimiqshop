package db

import (
	"errors"
	"hash/fnv"
	"time"

	"github.com/dgraph-io/badger/v4"

	"nimiqshop/internal/cashback"
	"nimiqshop/internal/money"
)

/*
cashback_codes.go — durable redemption accounting for promo/cashback codes.

WHY THIS EXISTS

A cashback code is the one input on the whole purchase path that a BUYER
supplies and that directly increases what the shop pays out. Everything else
monetary is derived server-side: the price comes from the supplier's own
dry-run response, the cashback base is the shop's persisted NIM snapshot of
that price, and the rate is clamped to MaxCashbackBps.

Before this file a code had no lifecycle at all. Once configured it was:

	- usable by anyone,
	- an unlimited number of times,
	- by the same person, forever,
	- with no expiry,
	- and no ceiling on the total it could pay out.

And because a Nimiq identity is free — Hub login is one signature over a fresh
keypair, and the shop creates a user row for any valid signature — the
per-account daily/monthly spend ceilings were not a real bound on total promo
payout. An attacker could mint wallets in a loop and drain a 20% code
indefinitely. That is the cashback-abuse question, and the honest answer was
"yes, if the operator configures a generous code".

WHAT IT DOES NOW

Each code carries four optional limits (zero/empty = unlimited, so every
existing deployment keeps behaving exactly as it does today):

	MaxUsesTotal        total redemptions ever
	MaxUsesPerUser      redemptions by one account
	MaxTotalCashbackUSD total USD this code may pay out
	ExpiresAt           wall-clock end of the promotion

Accounting is reservation-based, because counting a redemption at QUOTE time
would let an abandoned cart consume the cap and counting it only at FULFILLMENT
would let a burst of concurrent quotes all pass the check and overshoot it:

	reserve (quote created)      reserved++, perUser++
	commit  (quote fulfilled)    reserved--, committed++, paidUSD += cashback
	release (expired/failed/refunded) reserved--, perUser--

The invariant checked at reserve time is committed+reserved < MaxUsesTotal, so
an in-flight cart occupies its slot and a burst cannot overshoot.

CONTENTION

A hot promo code means many concurrent checkouts writing one counter, and
Badger's optimistic concurrency would turn that into a storm of ErrConflict
retries — i.e. a promotion would slow down honest shoppers, which is exactly
what this must never do. So the total counter is SHARDED across
codeCounterShards independent keys and summed on read; a write touches one
shard chosen by the quote id. Per-user counts are their own keys and are only
ever read for the calling user.

Every mutation happens inside the caller's open transaction, so the accounting
and the quote can never disagree — a crash either committed both or neither.
*/

// codeCounterShards is the fan-out of the total-redemption counter. 32 turns a
// single hot key into 32 warm ones, which is enough to keep a promotion from
// serialising behind Badger's conflict detection while keeping the read cost
// (32 tiny gets) negligible.
const codeCounterShards = 32

// CodeCounter is one shard of a code's redemption accounting.
type CodeCounter struct {
	Committed int          `json:"committed"`
	Reserved  int          `json:"reserved"`
	PaidUSD   money.Micros `json:"paid_usd"`
	// ReservedUSD is the payout committed to checkouts that have not settled
	// yet. It is what makes MaxTotalCashbackUSD a real ceiling.
	//
	// Counting only PaidUSD would leave the budget wide open during exactly
	// the window an attacker cares about: nothing is "paid" until an order
	// fulfills, which takes minutes, so a burst of a thousand concurrent
	// reservations would every one of them see PaidUSD=0 and pass. The shop
	// would then be on the hook for all thousand. Reserving the estimate up
	// front closes that, and CommitCodeRedemption swaps the estimate for the
	// real figure once the cashback engine has computed it.
	ReservedUSD money.Micros `json:"reserved_usd"`
	UpdatedAt   time.Time    `json:"updated_at"`
}

// CodeUsage is the summed view of all shards for one code.
type CodeUsage struct {
	Code      string
	Committed int
	Reserved  int
	// InFlight is Committed+Reserved: the number of slots currently occupied.
	InFlight int
	PaidUSD  money.Micros
	// ReservedUSD is the payout held for unsettled checkouts. CommittedUSD is
	// the ceiling-relevant total: what has been paid plus what is already
	// spoken for.
	ReservedUSD money.Micros
	// CommittedUSD is PaidUSD + ReservedUSD.
	CommittedUSD money.Micros
}

// ErrCodeLimit is returned when reserving a redemption would break one of the
// code's configured limits. It is distinct from ErrConflict so the handler can
// answer with a promotional message rather than a generic 409.
var ErrCodeLimit = errors.New("cashback code limit reached")

// CodeLimitDetail carries which limit fired, so the buyer sees a real reason.
type CodeLimitDetail struct {
	Code   string
	Reason string
	Limit  string
}

func (e *CodeLimitDetail) Error() string {
	return "cashback code " + e.Code + ": " + e.Reason
}

// Unwrap makes errors.Is(err, ErrCodeLimit) work on a detailed refusal.
//
// Without it the typed error is only reachable through errors.As, so a caller
// that tests the sentinel — the natural thing to write, and what the rest of
// this package does for ErrLimit / ErrMonthlyLimit / ErrConflict — silently
// falls through to its default branch and reports an internal error instead of
// a promotion message. The buyer sees "something went wrong" for a fully
// explained, fully handled condition.
func (e *CodeLimitDetail) Unwrap() error { return ErrCodeLimit }

func codeCounterKey(code string, shard int) []byte {
	return []byte("ix:cb:code:ct:" + code + ":" + string(rune('a'+shard%26)) + itoaShard(shard))
}

func itoaShard(n int) string {
	if n < 10 {
		return string(rune('0' + n))
	}
	return string(rune('0'+n/10)) + string(rune('0'+n%10))
}

func codeUserKey(code, userID string) []byte {
	return []byte("ix:cb:code:user:" + code + ":" + userID)
}

// codeReserveKey records the redemption lifecycle of ONE quote against ONE
// code.
//
// It carries two facts that the shard counters cannot:
//
//  1. How much payout this quote reserved, so commit and release can give
//     back exactly that much. The estimate charged at reserve time and the
//     figure the cashback engine computes at fulfillment are deliberately
//     different numbers (the estimate is rate x order USD, before the promo
//     cap and before the stablecoin multiplier), so without a stored amount a
//     commit could only guess — and guessing wrong either strands budget
//     forever or hands it back twice.
//
//  2. Whether this quote has ALREADY been settled. This is what makes commit
//     and release idempotent, and idempotence here is not a nicety. A
//     fulfillment is driven by two independent mechanisms — the supplier
//     webhook and the polling tracker — precisely so that neither one failing
//     can lose an order. The price of that redundancy is that the same
//     transition can legitimately arrive twice. Without a settled marker the
//     second arrival increments Committed and adds the payout to PaidUSD
//     again, so one real $8 cashback is counted as $16 against the
//     promotion's ceiling. That silently halves how much the code can pay
//     out, and it does so unevenly, depending on which orders happened to be
//     webhook-driven — an operator would see a promotion "run out" early with
//     no way to explain it.
type codeReservation struct {
	// Amount is the payout held back at reserve time.
	Amount money.Micros `json:"amount"`
	// Settled is true once the reservation has been committed or released.
	Settled bool `json:"settled"`
	// Committed distinguishes "paid" from "gave the slot back", for audit.
	Committed bool `json:"committed"`
}

// settledReservationTTL bounds how long a finished redemption record is kept.
//
// Without a TTL these accumulate forever: one small key per redemption, per
// code, for the life of the database. 180 days is far longer than any quote
// can still change state (invoices expire in ~25 minutes and the settlement
// tracker resolves stragglers within days), so a record can only be discarded
// after it is provably impossible for anything to consult it again.
const settledReservationTTL = 180 * 24 * time.Hour

func codeReserveKey(code, quoteID string) []byte {
	return []byte("ix:cb:code:res:" + code + ":" + quoteID)
}

func shardFor(quoteID string) int {
	h := fnv.New32a()
	_, _ = h.Write([]byte(quoteID))
	return int(h.Sum32() % codeCounterShards)
}

// CodeUsageFor sums every shard for one code. Called outside transactions
// (display, admin console) and inside them (the reserve check).
func (s *Store) CodeUsageFor(code string) (CodeUsage, error) {
	var u CodeUsage
	u.Code = code
	err := s.View(func(tx *badger.Txn) error {
		return codeUsageInTx(tx, code, &u)
	})
	return u, err
}

func codeUsageInTx(tx *badger.Txn, code string, u *CodeUsage) error {
	for i := 0; i < codeCounterShards; i++ {
		var c CodeCounter
		if err := getJSON(tx, codeCounterKey(code, i), &c); err != nil {
			if errors.Is(err, ErrNotFound) {
				continue
			}
			return err
		}
		u.Committed += c.Committed
		u.Reserved += c.Reserved
		u.PaidUSD += c.PaidUSD
		u.ReservedUSD += c.ReservedUSD
	}
	u.InFlight = u.Committed + u.Reserved
	// The ceiling-relevant figure: already paid plus already spoken for.
	u.CommittedUSD = u.PaidUSD + u.ReservedUSD
	return nil
}

// CodeUserUsesFor reports how many redemptions one account has reserved or
// committed against one code.
func (s *Store) CodeUserUsesFor(code, userID string) (int, error) {
	var n int
	err := s.View(func(tx *badger.Txn) error {
		v, err := codeUserUsesInTx(tx, code, userID)
		n = v
		return err
	})
	return n, err
}

func codeUserUsesInTx(tx *badger.Txn, code, userID string) (int, error) {
	if code == "" || userID == "" {
		return 0, nil
	}
	var c CodeCounter
	if err := getJSON(tx, codeUserKey(code, userID), &c); err != nil {
		if errors.Is(err, ErrNotFound) {
			return 0, nil
		}
		return 0, err
	}
	return c.Committed + c.Reserved, nil
}

// CodeLimits is the operator's policy for one code. Zero values mean
// "unlimited", which preserves the behaviour of every pre-existing config.
type CodeLimits struct {
	MaxUsesTotal        int
	MaxUsesPerUser      int
	MaxTotalCashbackUSD money.Micros
	ExpiresAt           time.Time
}

// ReserveCodeRedemption records an in-flight redemption for a newly created
// quote. It MUST run inside the same transaction that writes the quote, so a
// crash can never leave a counted redemption without an order (or the
// reverse).
//
// cashbackEstimateUSD is the cashback this order is expected to pay; it is
// charged against MaxTotalCashbackUSD at reserve time so a burst of
// concurrent checkouts cannot collectively blow through a payout ceiling.
func ReserveCodeRedemption(tx *badger.Txn, code, userID, quoteID string, limits CodeLimits, cashbackEstimateUSD money.Micros, now time.Time) error {
	if code == "" {
		return nil
	}
	if !limits.ExpiresAt.IsZero() && now.After(limits.ExpiresAt) {
		return &CodeLimitDetail{Code: code, Reason: "this promotion has ended", Limit: "expired"}
	}

	var u CodeUsage
	u.Code = code
	if err := codeUsageInTx(tx, code, &u); err != nil {
		return err
	}
	if limits.MaxUsesTotal > 0 && u.InFlight >= limits.MaxUsesTotal {
		return &CodeLimitDetail{Code: code, Reason: "this promotion has reached its total redemption limit", Limit: "total"}
	}
	// Measured against PAID + RESERVED, not paid alone. A promotion's budget
	// is consumed the moment an order is placed, not the moment it settles —
	// otherwise every concurrent checkout in a burst would see the same
	// untouched balance and all of them would be admitted, which is exactly
	// how a 20% code gets drained in one second rather than one week.
	if limits.MaxTotalCashbackUSD > 0 && u.CommittedUSD+cashbackEstimateUSD > limits.MaxTotalCashbackUSD {
		return &CodeLimitDetail{Code: code, Reason: "this promotion has reached its total cashback budget", Limit: "budget"}
	}
	if limits.MaxUsesPerUser > 0 && userID != "" {
		n, err := codeUserUsesInTx(tx, code, userID)
		if err != nil {
			return err
		}
		if n >= limits.MaxUsesPerUser {
			return &CodeLimitDetail{Code: code, Reason: "you have already used this promotion code", Limit: "per_user"}
		}
	}

	shard := shardFor(quoteID)
	key := codeCounterKey(code, shard)
	var c CodeCounter
	if err := getJSON(tx, key, &c); err != nil && !errors.Is(err, ErrNotFound) {
		return err
	}
	c.Reserved++
	// A negative estimate would ENLARGE the promotion's remaining budget, so
	// it is clamped rather than trusted: the estimate comes from arithmetic
	// on the supplier's own figures, but the accounting must not depend on
	// that arithmetic never producing something odd.
	if cashbackEstimateUSD > 0 {
		c.ReservedUSD += cashbackEstimateUSD
	}
	c.UpdatedAt = now
	raw, err := marshal(c)
	if err != nil {
		return err
	}
	if err := tx.Set(key, raw); err != nil {
		return err
	}
	// Remember what THIS quote reserved, so commit/release can give back
	// exactly this much. Keyed per quote, so two orders on the same code
	// never interfere with each other's release.
	//
	// Written even when the estimate is zero: the record's other job is the
	// settled marker, and a code with no payout ceiling still needs its
	// redemption COUNT to be idempotent.
	resKey := codeReserveKey(code, quoteID)
	reserved := cashbackEstimateUSD
	if reserved < 0 {
		reserved = 0
	}
	resRaw, err := marshal(codeReservation{Amount: reserved})
	if err != nil {
		return err
	}
	if err := tx.Set(resKey, resRaw); err != nil {
		return err
	}
	if userID != "" && limits.MaxUsesPerUser > 0 {
		uk := codeUserKey(code, userID)
		var uc CodeCounter
		if err := getJSON(tx, uk, &uc); err != nil && !errors.Is(err, ErrNotFound) {
			return err
		}
		uc.Reserved++
		uc.UpdatedAt = now
		uraw, err := marshal(uc)
		if err != nil {
			return err
		}
		if err := tx.Set(uk, uraw); err != nil {
			return err
		}
	}
	return nil
}

// CommitCodeRedemption turns a reservation into a paid redemption. Called from
// the fulfillment transition, in the same transaction as the delivery, with
// the ACTUAL cashback amount the engine just computed.
func CommitCodeRedemption(tx *badger.Txn, code, userID, quoteID string, cashbackUSD money.Micros, now time.Time) error {
	if code == "" {
		return nil
	}
	return moveCodeReservation(tx, code, userID, quoteID, true, cashbackUSD, now)
}

// ReleaseCodeRedemption gives the slot back when a quote ends without paying:
// expired, failed or refunded. Without it, abandoned carts would permanently
// consume a promotion's budget.
func ReleaseCodeRedemption(tx *badger.Txn, code, userID, quoteID string, now time.Time) error {
	if code == "" {
		return nil
	}
	return moveCodeReservation(tx, code, userID, quoteID, false, 0, now)
}

func moveCodeReservation(tx *badger.Txn, code, userID, quoteID string, commit bool, cashbackUSD money.Micros, now time.Time) error {
	resKey := codeReserveKey(code, quoteID)
	var res codeReservation
	resFound := true
	if err := getJSON(tx, resKey, &res); err != nil {
		if !errors.Is(err, ErrNotFound) {
			return err
		}
		resFound = false
	}
	// IDEMPOTENCE GATE. A fulfillment can legitimately arrive twice — the
	// supplier webhook and the polling tracker drive the same transition
	// independently, so that neither one failing can lose an order. Without
	// this check the second arrival would increment Committed and add the
	// payout to PaidUSD a second time: one real $8 cashback counted as $16
	// against the promotion's ceiling, silently shrinking what the code can
	// pay out. The same applies to a repeated release, which would otherwise
	// hand the same budget back twice and let a promotion spend more than its
	// cap.
	if resFound && res.Settled {
		return nil
	}
	if cashbackUSD < 0 {
		cashbackUSD = 0
	}

	shard := shardFor(quoteID)
	key := codeCounterKey(code, shard)
	var c CodeCounter
	if err := getJSON(tx, key, &c); err != nil && !errors.Is(err, ErrNotFound) {
		return err
	}

	// What this specific quote held back. Zero for a quote created before
	// this accounting existed, which must keep working exactly as it did.
	held := res.Amount
	if held < 0 {
		held = 0
	}

	if c.Reserved > 0 {
		c.Reserved--
	}
	// Give back what this quote held, clamped to what the shard actually
	// holds: a bookkeeping mismatch must never drive ReservedUSD negative,
	// because negative reserved budget is budget that does not exist.
	if held > 0 {
		if held > c.ReservedUSD {
			held = c.ReservedUSD
		}
		c.ReservedUSD -= held
	}
	if commit {
		c.Committed++
		// The conservative estimate is swapped for the figure the cashback
		// engine actually computed. This is where the promotion's spent
		// budget stops being an estimate.
		if cashbackUSD > 0 {
			c.PaidUSD += cashbackUSD
		}
	}
	c.UpdatedAt = now
	raw, err := marshal(c)
	if err != nil {
		return err
	}
	if err := tx.Set(key, raw); err != nil {
		return err
	}

	// Mark this quote settled. Written with a TTL: the record's only remaining
	// job is to refuse a duplicate that arrives later, and nothing can arrive
	// later than the lifetime of the quote itself.
	settledRaw, err := marshal(codeReservation{Amount: 0, Settled: true, Committed: commit})
	if err != nil {
		return err
	}
	if err := tx.SetEntry(badger.NewEntry(resKey, settledRaw).WithTTL(settledReservationTTL)); err != nil {
		return err
	}

	if userID == "" {
		return nil
	}
	uk := codeUserKey(code, userID)
	var uc CodeCounter
	if err := getJSON(tx, uk, &uc); err != nil && !errors.Is(err, ErrNotFound) {
		return err
	}
	if uc.Reserved > 0 {
		uc.Reserved--
	}
	if commit {
		uc.Committed++
	}
	uc.UpdatedAt = now
	uraw, err := marshal(uc)
	if err != nil {
		return err
	}
	return tx.Set(uk, uraw)
}

// cashbackUSDForQuote reads the cashback row the fulfillment transaction just
// enqueued and returns its amount in USD, for charging against a promo code's
// total payout budget. It returns 0 when there is no row (a skipped cashback)
// or no rate to convert with — a code whose order earns nothing should not
// consume budget.
//
// It runs INSIDE the fulfillment transaction, after
// enqueueCashbackOnFulfill, so it sees the row that transition is writing.
func cashbackUSDForQuote(tx *badger.Txn, q *Quote) money.Micros {
	if q == nil || q.ID == "" {
		return 0
	}
	id, err := getString(tx, cashbackQuoteIndexKey(q.ID))
	if err != nil || id == "" {
		return 0
	}
	var cb Cashback
	if err := getJSON(tx, cashbackKey(id), &cb); err != nil || cb.Status == CashbackSkipped || cb.AmountLuna <= 0 {
		return 0
	}
	// NIM → USD at the rate the quote itself locked, which is the same rate
	// the cashback engine used. Never a live oracle call: this is accounting
	// for a payout already decided.
	if q.NimUsdRate <= 0 {
		return 0
	}
	usd := cashback.NIMFromLuna(cb.AmountLuna) * q.NimUsdRate
	if usd <= 0 {
		return 0
	}
	return money.FromFloat(usd)
}
