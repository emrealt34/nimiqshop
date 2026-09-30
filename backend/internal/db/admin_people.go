package db

import (
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/dgraph-io/badger/v4"
)

/*
 * admin_people.go — the operator console's "who are my customers and what did
 * they just do?" views.
 *
 * Badger has no query language, so every view here is one bounded prefix walk
 * plus an in-memory sort. The same trade-off the other admin views make: fine
 * for an operator console, and the HTTP layer caps every response. If the shop
 * ever outgrows it, these are the functions to put cursors behind — nothing
 * else reads them.
 *
 * Aggregates are computed with ONE walk per record type, grouped by user, so
 * 300 index lookups.
 */

// UserAggregate is everything the console shows next to one customer.
type UserAggregate struct {
	// QuoteCount is every purchase attempt; OrderCount only the ones that
	// were actually paid for and moved towards delivery.
	QuoteCount int `json:"quote_count"`
	OrderCount int `json:"order_count"`
	// SpendMicroUSD sums ProductUSD over paid-and-beyond quotes: money the
	// customer really committed, not abandoned carts.
	SpendMicroUSD int64 `json:"spend_micro_usd"`
	// CashbackPaidLuna is what actually left the shop wallet for this buyer;
	// CashbackPendingLuna is queued/sending/broadcast and not yet confirmed.
	CashbackPaidLuna    int64 `json:"cashback_paid_luna"`
	CashbackPendingLuna int64 `json:"cashback_pending_luna"`
	CashbackCount       int   `json:"cashback_count"`
	CashbackSkipped     int   `json:"cashback_skipped"`
	// CashbackBoostedCount is how many of those payouts were paid at a
	// pool-staker rate — the programme's own conversion number.
	CashbackBoostedCount int `json:"cashback_boosted_count"`
	// LastOrderAt / LastActivityAt drive "active today / this week".
	LastOrderAt    time.Time `json:"last_order_at,omitempty"`
	LastActivityAt time.Time `json:"last_activity_at,omitempty"`
	// LastCashback* is the most recent payout's locked rate so the console
	// can show "this buyer last paid at 3.2% (promo NIM10)" without a second scan.
	LastCashbackAt     time.Time `json:"last_cashback_at,omitempty"`
	LastCashbackBps    int       `json:"last_cashback_bps,omitempty"`
	LastCashbackSource string    `json:"last_cashback_source,omitempty"`
	LastCashbackCode   string    `json:"last_cashback_code,omitempty"`
}

// touch records an activity timestamp if it is newer than what we have.
func (a *UserAggregate) touch(at time.Time) {
	if at.IsZero() {
		return
	}
	if at.After(a.LastActivityAt) {
		a.LastActivityAt = at
	}
}

// CashbackEarnedLuna is everything this buyer has been granted: confirmed
// payouts plus the ones still travelling to the chain.
func (a UserAggregate) CashbackEarnedLuna() int64 { return a.CashbackPaidLuna + a.CashbackPendingLuna }

func noteLastCashback(a *UserAggregate, cb Cashback) {
	at := cb.CreatedAt
	if cb.PaidAt != nil && cb.PaidAt.After(at) {
		at = *cb.PaidAt
	}
	if at.IsZero() || (!a.LastCashbackAt.IsZero() && !at.After(a.LastCashbackAt)) {
		return
	}
	a.LastCashbackAt = at
	a.LastCashbackBps = cb.Bps
	a.LastCashbackSource = cb.CashbackSource
	a.LastCashbackCode = cb.CashbackCode
}

// isPaidStatus reports whether a quote got far enough to count as spend.
func isPaidStatus(status string) bool {
	switch status {
	case "payment_received", "delivering", "fulfilled", "refunded":
		return true
	}
	return false
}

// groups the result by user id. Missing users are simply absent.
func (s *Store) UserAggregates() (map[string]UserAggregate, error) {
	agg := map[string]UserAggregate{}
	get := func(id string) *UserAggregate {
		a := agg[id]
		return &a
	}
	put := func(id string, a *UserAggregate) { agg[id] = *a }

	err := s.View(func(txn *badger.Txn) error {
		if err := scanJSONPrefix(txn, []byte(prefixQuote), func(item *badger.Item) error {
			var q Quote
			if err := item.Value(func(v []byte) error { return unmarshal(v, &q) }); err != nil {
				return err
			}
			if q.UserID == "" {
				return nil
			}
			a := get(q.UserID)
			a.QuoteCount++
			a.touch(q.CreatedAt)
			if isPaidStatus(q.Status) {
				a.OrderCount++
				a.SpendMicroUSD += int64(q.ProductUSD)
				if q.CreatedAt.After(a.LastOrderAt) {
					a.LastOrderAt = q.CreatedAt
				}
			}
			put(q.UserID, a)
			return nil
		}); err != nil {
			return err
		}

		if err := scanJSONPrefix(txn, []byte("cb:"), func(item *badger.Item) error {
			var cb Cashback
			if err := item.Value(func(v []byte) error { return unmarshal(v, &cb) }); err != nil {
				return err
			}
			if cb.UserID == "" {
				return nil
			}
			a := get(cb.UserID)
			a.touch(cb.CreatedAt)
			switch cb.Status {
			case CashbackPaid:
				a.CashbackPaidLuna += cb.AmountLuna
				a.CashbackCount++
				if cb.Boosted {
					a.CashbackBoostedCount++
				}
				if cb.PaidAt != nil {
					a.touch(*cb.PaidAt)
				}
				noteLastCashback(a, cb)
			case CashbackQueued, CashbackSending, CashbackBroadcast:
				a.CashbackPendingLuna += cb.AmountLuna
				a.CashbackCount++
				if cb.Boosted {
					a.CashbackBoostedCount++
				}
				noteLastCashback(a, cb)
			case CashbackSkipped:
				a.CashbackSkipped++
			}
			put(cb.UserID, a)
			return nil
		}); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	return agg, nil
}

// UserWithStats is one row of the admin customer list.
type UserWithStats struct {
	User
	Agg UserAggregate `json:"agg"`
}

// SortUsersWithStats orders rows by one of the console's column headers.
// Ties break on registration time, then id, so paging is stable.
func SortUsersWithStats(rows []UserWithStats, sortBy string, desc bool) {
	less := func(i, j int) bool {
		a, b := rows[i], rows[j]
		switch sortBy {
		case "orders":
			if a.Agg.OrderCount != b.Agg.OrderCount {
				return a.Agg.OrderCount < b.Agg.OrderCount
			}
		case "spend":
			if a.Agg.SpendMicroUSD != b.Agg.SpendMicroUSD {
				return a.Agg.SpendMicroUSD < b.Agg.SpendMicroUSD
			}
		case "cashback":
			// Earned = paid + still in flight. Ranking on paid alone would
			// leave a young shop unable to sort its customers at all.
			ae, be := a.Agg.CashbackEarnedLuna(), b.Agg.CashbackEarnedLuna()
			if ae != be {
				return ae < be
			}
		case "last_seen":
			ai, bi := a.effectiveLastSeen(), b.effectiveLastSeen()
			if !ai.Equal(bi) {
				return ai.Before(bi)
			}
		case "address":
			if a.NimiqAddress != b.NimiqAddress {
				return a.NimiqAddress < b.NimiqAddress
			}
		default: // "registered"
			if !a.CreatedAt.Equal(b.CreatedAt) {
				return a.CreatedAt.Before(b.CreatedAt)
			}
		}
		return a.ID < b.ID
	}
	sort.SliceStable(rows, less)
	if desc {
		for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
			rows[i], rows[j] = rows[j], rows[i]
		}
	}
}

// effectiveLastSeen is the newest thing we know about a customer: presence, or
// failing that their last recorded activity.
func (u UserWithStats) effectiveLastSeen() time.Time {
	if u.LastSeenAt.After(u.Agg.LastActivityAt) {
		return u.LastSeenAt
	}
	return u.Agg.LastActivityAt
}

// ListUsersWithStats is the admin customer list: every registered buyer with
// their aggregates, sorted, then capped. limit<=0 means "no cap".
func (s *Store) ListUsersWithStats(sortBy string, desc bool, limit int) ([]UserWithStats, error) {
	agg, err := s.UserAggregates()
	if err != nil {
		return nil, err
	}
	var rows []UserWithStats
	err = s.View(func(txn *badger.Txn) error {
		return scanJSONPrefix(txn, []byte(prefixUser), func(item *badger.Item) error {
			var u User
			if err := item.Value(func(v []byte) error { return unmarshal(v, &u) }); err != nil {
				return err
			}
			a := agg[u.ID]
			// Fold presence into the activity clock so "active today" counts
			// a login that produced no order.
			a.touch(u.LastSeenAt)
			agg[u.ID] = a
			rows = append(rows, UserWithStats{User: u, Agg: a})
			return nil
		})
	})
	if err != nil {
		return nil, err
	}
	SortUsersWithStats(rows, sortBy, desc)
	if limit > 0 && len(rows) > limit {
		rows = rows[:limit]
	}
	return rows, nil
}

// PlayerStats are the console's headline numbers. "Today" is since 00:00 UTC;
// "this week" is the rolling 7 days ending now, which is what an operator
// actually means when they glance at the number on a Wednesday.
type PlayerStats struct {
	TotalRegistered int       `json:"total_registered"`
	NewToday        int       `json:"new_today"`
	NewThisWeek     int       `json:"new_this_week"`
	ActiveToday     int       `json:"active_today"`
	ActiveThisWeek  int       `json:"active_this_week"`
	WithOrders      int       `json:"with_orders"`
	Stakers         int       `json:"stakers"`
	Now             time.Time `json:"now"`
	TodayStart      time.Time `json:"today_start"`
	WeekStart       time.Time `json:"week_start"`
}

// PlayerStats counts registered buyers and the ones who did something in each
// window. A customer is "active" when any of presence, a purchase attempt, a
func (s *Store) PlayerStats(now time.Time) (PlayerStats, error) {
	now = now.UTC()
	todayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	weekStart := now.AddDate(0, 0, -7)

	stats := PlayerStats{Now: now, TodayStart: todayStart, WeekStart: weekStart}

	rows, err := s.ListUsersWithStats("registered", false, 0)
	if err != nil {
		return stats, err
	}
	stats.TotalRegistered = len(rows)
	for _, r := range rows {
		if !r.CreatedAt.IsZero() {
			if !r.CreatedAt.Before(todayStart) {
				stats.NewToday++
			}
			if !r.CreatedAt.Before(weekStart) {
				stats.NewThisWeek++
			}
		}
		last := r.effectiveLastSeen()
		if !last.IsZero() {
			if !last.Before(todayStart) {
				stats.ActiveToday++
			}
			if !last.Before(weekStart) {
				stats.ActiveThisWeek++
			}
		}
		if r.Agg.OrderCount > 0 {
			stats.WithOrders++
		}
	}

	stakers, err := s.countPrefix([]byte(prefixStakeWatch))
	if err != nil {
		return stats, err
	}
	stats.Stakers = stakers
	return stats, nil
}

// CashbackQueueStat is one status bucket of the payout queue.
type CashbackQueueStat struct {
	Status     string     `json:"status"`
	Count      int        `json:"count"`
	AmountLuna int64      `json:"amount_luna"`
	OldestAt   *time.Time `json:"oldest_at,omitempty"`
	NewestAt   *time.Time `json:"newest_at,omitempty"`
	// Boosted counts rows paid at a pool-staker rate rather than the base
	// rate — the pool programme's own conversion number.
	Boosted int `json:"boosted"`
	// Retrying is a sending row that already failed at least once: the worker
	// keeps rebroadcasting the same signed bytes.
	Retrying int `json:"retrying"`
	// LastErrorCount counts rows carrying an error message right now.
	LastErrorCount int `json:"last_error_count"`
}

// CashbackQueueStats is the operator's "is the payout pipeline healthy?" view:
// one bucket per status, with the NIM sitting in each and the age of the
// oldest row — a payout that has been queued for hours is the thing to look at.
func (s *Store) CashbackQueueStats() (map[string]CashbackQueueStat, error) {
	out := map[string]CashbackQueueStat{}
	for _, st := range []string{CashbackQueued, CashbackSending, CashbackBroadcast, CashbackPaid, CashbackSkipped} {
		out[st] = CashbackQueueStat{Status: st}
	}
	err := s.View(func(txn *badger.Txn) error {
		return scanJSONPrefix(txn, []byte("cb:"), func(item *badger.Item) error {
			var cb Cashback
			if err := item.Value(func(v []byte) error { return unmarshal(v, &cb) }); err != nil {
				return err
			}
			bucket, ok := out[cb.Status]
			if !ok {
				bucket = CashbackQueueStat{Status: cb.Status}
			}
			bucket.Count++
			bucket.AmountLuna += cb.AmountLuna
			if cb.LastError != "" {
				bucket.LastErrorCount++
			}
			if cb.Boosted {
				bucket.Boosted++
			}
			if cb.Status == CashbackSending && (cb.LastError != "" || cb.SignedTxHex != "") {
				bucket.Retrying++
			}
			at := cb.CreatedAt
			if bucket.OldestAt == nil || at.Before(*bucket.OldestAt) {
				t := at
				bucket.OldestAt = &t
			}
			if bucket.NewestAt == nil || at.After(*bucket.NewestAt) {
				t := at
				bucket.NewestAt = &t
			}
			out[cb.Status] = bucket
			return nil
		})
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

/* ---------------- Per-customer activity timeline ---------------- */

// ActivityEvent is one line of a customer's history, newest first in the
// response. Kind is stable machine text; Label/Detail are ready to render.
type ActivityEvent struct {
	At        time.Time `json:"at"`
	Kind      string    `json:"kind"`
	Label     string    `json:"label"`
	Detail    string    `json:"detail,omitempty"`
	Ref       string    `json:"ref,omitempty"`
	AmountNIM float64   `json:"amount_nim,omitempty"`
	Status    string    `json:"status,omitempty"`
	Bps       int       `json:"bps,omitempty"`
	Source    string    `json:"source,omitempty"`
	Code      string    `json:"code,omitempty"`
}

// UserActivity rebuilds one customer's timeline from the records that already
// lifecycle, cashback payouts and support tickets. Nothing is inferred — if a
// timestamp was never written, the event simply does not appear.
func (s *Store) UserActivity(userID string, limit int) ([]ActivityEvent, error) {
	if userID == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = 100
	}
	var events []ActivityEvent

	user, err := s.GetUser(userID)
	if err != nil {
		return nil, err
	}
	events = append(events, ActivityEvent{
		At:     user.CreatedAt,
		Kind:   "registered",
		Label:  "Account created",
		Detail: shortAddress(user.NimiqAddress),
	})
	if !user.LastSeenAt.IsZero() {
		detail := "Last seen"
		if user.LastCountry != "" {
			detail += " from " + user.LastCountry
		}
		if user.LastIP != "" {
			detail += " · " + user.LastIP
		}
		events = append(events, ActivityEvent{At: user.LastSeenAt, Kind: "seen", Label: "Session activity", Detail: detail})
	}

	quotes, err := s.ListQuotesForUser(userID, 200)
	if err != nil {
		return nil, err
	}
	for _, q := range quotes {
		product := q.ProductID
		if product == "" {
			product = "item"
		}
		if q.ProductCountry != "" {
			product += " (" + q.ProductCountry + ")"
		}
		face := q.Denomination
		events = append(events, ActivityEvent{
			At: q.CreatedAt, Kind: "order_created", Label: "Started a purchase",
			Detail: joinNonEmpty(product, face), Ref: q.ID, Status: q.Status,
			AmountNIM: q.EstimatedNIM,
		})
		if !q.SupplierRequestAt.IsZero() {
			events = append(events, ActivityEvent{
				At: q.SupplierRequestAt, Kind: "supplier_order", Label: "Supplier order requested",
				Detail: joinNonEmpty(product, q.SupplierOrderID), Ref: q.ID, Status: q.Status,
			})
		}
		if q.Status == "expired" {
			// An unpaid window that ran out. PaymentExpiry is the accurate
			// moment when an invoice was issued; a quote that expired before
			// one still belongs in the timeline, so fall back to UpdatedAt.
			at := q.PaymentExpiry
			if at.IsZero() {
				at = q.UpdatedAt
			}
			events = append(events, ActivityEvent{
				At: at, Kind: "expired", Label: "Payment window expired unpaid",
				Detail: joinNonEmpty(product, face), Ref: q.ID, Status: q.Status,
				AmountNIM: q.EstimatedNIM,
			})
		}
		switch q.Status {
		case "payment_received", "delivering":
			events = append(events, ActivityEvent{
				At: q.UpdatedAt, Kind: "paid", Label: "Payment received",
				Detail: joinNonEmpty(product, q.SupplierStatus), Ref: q.ID, Status: q.Status, AmountNIM: q.EstimatedNIM,
			})
		case "fulfilled":
			events = append(events, ActivityEvent{
				At: q.UpdatedAt, Kind: "delivered", Label: "Delivered",
				Detail: product, Ref: q.ID, Status: q.Status, AmountNIM: q.EstimatedNIM,
			})
		case "refunded":
			events = append(events, ActivityEvent{
				At: q.UpdatedAt, Kind: "refunded", Label: "Refunded by supplier",
				Detail: joinNonEmpty(product, q.RefundReason), Ref: q.ID, Status: q.Status,
			})
		case "manual_review":
			events = append(events, ActivityEvent{
				At: q.UpdatedAt, Kind: "manual_review", Label: "Held for manual review",
				Detail: product, Ref: q.ID, Status: q.Status,
			})
		}
		if q.Rating > 0 && q.RatedAt != nil {
			events = append(events, ActivityEvent{
				At: *q.RatedAt, Kind: "rated", Label: "Rated the delivery",
				Detail: joinNonEmpty(product, itoa(q.Rating)+" star(s)"), Ref: q.ID,
			})
		}
	}

	cashbacks, err := s.ListCashbacksByUser(userID, 200)
	if err != nil {
		return nil, err
	}
	for _, cb := range cashbacks {
		nim := float64(cb.AmountLuna) / 100_000
		rate := ""
		if cb.Bps > 0 {
			rate = bpsLabel(cb.Bps) + " cashback"
		}
		src := cb.CashbackSource
		if src == "" && cb.Boosted {
			src = "ledger"
		}
		if src == "" && cb.CashbackCode != "" {
			src = "code"
		}
		if src == "" && cb.Bps > 0 {
			src = "base"
		}
		switch cb.Status {
		case CashbackPaid:
			at := cb.CreatedAt
			if cb.PaidAt != nil {
				at = *cb.PaidAt
			}
			events = append(events, ActivityEvent{
				At: at, Kind: "cashback_paid", Label: "Cashback paid on-chain",
				Detail: joinNonEmpty(cb.ProductID, rate, cashbackSourceLabel(src, cb.CashbackCode), cb.TxHash), Ref: cb.QuoteID,
				AmountNIM: nim, Status: cb.Status, Bps: cb.Bps, Source: src, Code: cb.CashbackCode,
			})
		case CashbackQueued, CashbackSending, CashbackBroadcast:
			events = append(events, ActivityEvent{
				At: cb.CreatedAt, Kind: "cashback_queued", Label: "Cashback queued",
				Detail: joinNonEmpty(cb.ProductID, rate, cashbackSourceLabel(src, cb.CashbackCode), cashbackStatusLabelShort(cb.Status)), Ref: cb.QuoteID,
				AmountNIM: nim, Status: cb.Status, Bps: cb.Bps, Source: src, Code: cb.CashbackCode,
			})
		case CashbackSkipped:
			events = append(events, ActivityEvent{
				At: cb.CreatedAt, Kind: "cashback_skipped", Label: "No cashback on this order",
				Detail: joinNonEmpty(cb.ProductID, cb.SkipReason), Ref: cb.QuoteID, Status: cb.Status,
				Bps: cb.Bps, Source: src, Code: cb.CashbackCode,
			})
		}
	}

	tickets, _ := s.ListSupportTicketsForUser(userID, 50)
	for _, tk := range tickets {
		events = append(events, ActivityEvent{
			At: tk.CreatedAt, Kind: "support_opened", Label: "Opened a support ticket",
			Detail: joinNonEmpty(tk.Subject, tk.ProductID), Ref: tk.ID, Status: tk.Status,
		})
		if !tk.UpdatedAt.IsZero() && tk.Status != "open" {
			events = append(events, ActivityEvent{
				At: tk.UpdatedAt, Kind: "support_update", Label: "Ticket now " + tk.Status,
				Detail: tk.Subject, Ref: tk.ID, Status: tk.Status,
			})
		}
	}

	sort.SliceStable(events, func(i, j int) bool {
		if events[i].At.Equal(events[j].At) {
			return events[i].Kind < events[j].Kind
		}
		return events[i].At.After(events[j].At)
	})
	if len(events) > limit {
		events = events[:limit]
	}
	return events, nil
}

func cashbackSourceLabel(src, code string) string {
	switch src {
	case "ledger":
		return "staker boost"
	case "code":
		if code != "" {
			return "promo " + code
		}
		return "promo code"
	case "base":
		return "base rate"
	default:
		if code != "" {
			return "promo " + code
		}
		return src
	}
}

func cashbackStatusLabelShort(status string) string {
	switch status {
	case CashbackQueued:
		return "queued"
	case CashbackSending:
		return "sending"
	case CashbackBroadcast:
		return "confirming"
	default:
		return status
	}
}

// bpsLabel renders basis points the way the console does: 150 → "1.5%".
func bpsLabel(bps int) string {
	return strconv.FormatFloat(float64(bps)/100, 'f', -1, 64) + "%"
}

// groupSpace inserts a space every 4 characters (left to right) so Nimiq
// addresses always read as 4-char groups: "NQ77AYF1" -> "NQ77 AYF1".
func groupSpace(s string) string {
	r := []rune(s)
	var b []rune
	for i, c := range r {
		if i > 0 && i%4 == 0 {
			b = append(b, ' ')
		}
		b = append(b, c)
	}
	return string(b)
}

// groupSpaceTail groups from the RIGHT so the spaces land exactly where they
// sit in the fully-grouped address: "MNCP31" -> "MN CP31".
func groupSpaceTail(s string) string {
	r := []rune(s)
	n := len(r)
	var b []rune
	for i, c := range r {
		fromRight := n - i
		if i > 0 && fromRight%4 == 0 {
			b = append(b, ' ')
		}
		b = append(b, c)
	}
	return string(b)
}

// shortAddress shortens "NQ86AAAABBBB…" to "NQ86 AYF1 … MN CP31" for list
// rows — 4-char groups with spaces, exactly like the full display form.
func shortAddress(addr string) string {
	r := []rune(strings.ReplaceAll(addr, " ", ""))
	if len(r) <= 16 {
		return groupSpace(addr)
	}
	return groupSpace(string(r[:8])) + " … " + groupSpaceTail(string(r[len(r)-6:]))
}

func joinNonEmpty(parts ...string) string {
	out := ""
	for _, p := range parts {
		if p == "" {
			continue
		}
		if out == "" {
			out = p
			continue
		}
		out += " · " + p
	}
	return out
}

func itoa(v int) string { return strconv.Itoa(v) }
