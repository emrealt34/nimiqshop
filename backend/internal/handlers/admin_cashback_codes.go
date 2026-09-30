package handlers

import (
	"log"
	"sort"
	"time"

	adminmodel "nimiqshop/internal/admin"
	"nimiqshop/internal/db"
	"nimiqshop/internal/money"
)

func cashbackCodeRuleView(rule adminmodel.CashbackCodeRule, source string, usageCount int, lastUsedAt time.Time, usage db.CodeUsage) map[string]any {
	row := map[string]any{
		"code":             rule.Code,
		"cashback_bps":     rule.CashbackBps,
		"cashback_percent": float64(rule.CashbackBps) / 100.0,
		"source":           source,
		"usage_count":      usageCount,
		"max_order_usd":    rule.MaxOrderUSD,

		/* --- Redemption lifecycle: policy and live state, side by side ---
		 *
		 * An operator running a capped promotion needs to see how much of it
		 * is left WITHOUT reading the quote table. The counters below come
		 * from the same sharded accounting the checkout gate enforces against
		 * (internal/db/cashback_codes.go), so what the console shows and what
		 * the gate refuses are guaranteed to be the same number — there is no
		 * second, drifting source of truth.
		 *
		 * "reserved" is worth surfacing separately from "committed" because
		 * it is the part an operator would otherwise misread: those are
		 * carts open right now that have not paid yet. A promotion can look
		 * exhausted while half its budget is sitting in abandoned invoices
		 * that will be released within the hour.
		 */
		"max_uses_total":         rule.MaxUsesTotal,
		"max_uses_per_user":      rule.MaxUsesPerUser,
		"max_total_cashback_usd": rule.MaxTotalCashbackUSD,
		"redeemed":               usage.Committed,
		"reserved":               usage.Reserved,
		"in_flight":              usage.InFlight,
		"paid_usd":               float64(usage.PaidUSD) / 1_000_000,
		"reserved_usd":           float64(usage.ReservedUSD) / 1_000_000,
		"committed_usd":          float64(usage.CommittedUSD) / 1_000_000,
	}
	if !rule.ExpiresAt.IsZero() {
		row["expires_at"] = rule.ExpiresAt
		row["expired"] = time.Now().UTC().After(rule.ExpiresAt)
	}
	// Remaining budget, precomputed so the console cannot get the arithmetic
	// wrong. Negative means the ceiling was reached by reservations that have
	// not settled yet; it is clamped for display but the raw figures above
	// stay visible so the operator can see why.
	if rule.MaxUsesTotal > 0 {
		left := rule.MaxUsesTotal - usage.InFlight
		if left < 0 {
			left = 0
		}
		row["uses_remaining"] = left
	}
	if rule.MaxTotalCashbackUSD > 0 {
		left := money.FromFloat(rule.MaxTotalCashbackUSD) - usage.CommittedUSD
		if left < 0 {
			left = 0
		}
		row["cashback_budget_remaining_usd"] = float64(left) / 1_000_000
	}
	if !lastUsedAt.IsZero() {
		row["last_used_at"] = lastUsedAt
	}
	return row
}

func (h *Handlers) adminCashbackCodeData() (rules []map[string]any, uses []map[string]any, managed bool, source string, err error) {
	settings, err := h.Store.GetAdminSettings(0)
	if err != nil {
		return nil, nil, false, "", err
	}
	managed = settings.CashbackCodeRules != nil
	activeRules, source, err := h.effectiveCashbackCodeRules()
	if err != nil {
		return nil, nil, managed, "", err
	}

	quotes, err := h.Store.ListAllQuotes(0)
	if err != nil {
		return nil, nil, managed, source, err
	}
	idSet := map[string]struct{}{}
	for _, q := range quotes {
		if q.CashbackCode != "" && q.UserID != "" {
			idSet[q.UserID] = struct{}{}
		}
	}
	ids := make([]string, 0, len(idSet))
	for id := range idSet {
		ids = append(ids, id)
	}
	addrs, _ := h.Store.UserAddresses(ids)

	usageCount := map[string]int{}
	lastUsedAt := map[string]time.Time{}
	for _, q := range quotes {
		if q.CashbackCode == "" {
			continue
		}
		usageCount[q.CashbackCode]++
		if q.CreatedAt.After(lastUsedAt[q.CashbackCode]) {
			lastUsedAt[q.CashbackCode] = q.CreatedAt
		}
		uses = append(uses, map[string]any{
			"code":             q.CashbackCode,
			"cashback_bps":     q.CashbackCodeBps,
			"cashback_percent": float64(q.CashbackCodeBps) / 100.0,
			"quote_id":         q.ID,
			"user_id":          q.UserID,
			"user_address":     addrs[q.UserID],
			"customer_email":   q.CustomerEmail,
			"status":           q.Status,
			"created_at":       q.CreatedAt,
			"updated_at":       q.UpdatedAt,
		})
	}
	sort.Slice(uses, func(i, j int) bool {
		ti, _ := uses[i]["created_at"].(time.Time)
		tj, _ := uses[j]["created_at"].(time.Time)
		return ti.After(tj)
	})
	if len(uses) > 100 {
		uses = uses[:100]
	}

	rules = make([]map[string]any, 0, len(activeRules))
	for _, rule := range activeRules {
		// 32 shard reads per code, regardless of how many orders the shop has
		// ever taken. The usage_count / last_used_at figures above are still
		// derived from the quote scan because they are a historical list the
		// operator browses; the ENFORCEMENT numbers come from the counters.
		usage, uerr := h.Store.CodeUsageFor(rule.Code)
		if uerr != nil {
			// A counter read failure must not blank the whole cashback
			// settings panel. Show the rule with zeroed live figures; the
			// gate still enforces correctly, it is only the display that
			// degrades, and it degrades visibly rather than silently.
			log.Printf("admin: cashback code %q usage counters unreadable: %v", rule.Code, uerr)
		}
		rules = append(rules, cashbackCodeRuleView(rule, source, usageCount[rule.Code], lastUsedAt[rule.Code], usage))
	}
	return rules, uses, managed, source, nil
}
