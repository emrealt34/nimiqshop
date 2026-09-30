package handlers

// Canonical opaque cart selections, line merging and display totals.
// Supplier price increases/decreases are NOT restricted by ratio/percentage.

import (
	"sort"
	"strconv"
	"strings"
)

// normalizeQuoteSelection keeps fixed denominations opaque. product_value is
// part of the supplier contract ONLY for range products. Legacy clients may
// send a displayed fixed-package price; discard it rather than compare it to
// the SKU's name or let it influence the invoice, fingerprints or line merging.
func normalizeQuoteSelection(denomination *string, value *float64) {
	*denomination = strings.TrimSpace(*denomination)
	if strings.EqualFold(*denomination, "range") {
		*denomination = "range"
	} else {
		*value = 0
	}
}

// batchLineKey identifies an economically identical line.
func batchLineKey(it batchQuoteItem) string {
	return strings.ToLower(strings.TrimSpace(it.ProductID)) + "|" +
		strings.ToUpper(strings.TrimSpace(it.Country)) + "|" +
		strings.ToLower(strings.TrimSpace(it.Denomination)) + "|" +
		strconv.FormatFloat(it.ProductValue, 'f', -1, 64) + "|" +
		strings.TrimSpace(it.PhoneNumber)
}

// mergeBatchItems collapses identical lines, summing their quantities. The
// order total is unchanged (both lines were counted anyway) but the supplier
// order can no longer contain two contradictory rows for one product.
func mergeBatchItems(items []batchQuoteItem) []batchQuoteItem {
	out := make([]batchQuoteItem, 0, len(items))
	idx := make(map[string]int, len(items))
	for _, it := range items {
		k := batchLineKey(it)
		if i, ok := idx[k]; ok {
			out[i].Quantity += it.Quantity
			continue
		}
		idx[k] = len(out)
		out = append(out, it)
	}
	return out
}

// batchFaceTotals renders the per-currency face totals as display strings
// ("106.95 USD", "250 TRY"), largest first so the dominant currency leads.
func batchFaceTotals(faceByCurrency map[string]float64) []string {
	type pair struct {
		ccy string
		val float64
	}
	pairs := make([]pair, 0, len(faceByCurrency))
	for c, v := range faceByCurrency {
		if v > 0 {
			pairs = append(pairs, pair{c, v})
		}
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].val != pairs[j].val {
			return pairs[i].val > pairs[j].val
		}
		return pairs[i].ccy < pairs[j].ccy
	})
	out := make([]string, 0, len(pairs))
	for _, p := range pairs {
		out = append(out, strings.TrimSuffix(strings.TrimRight(strconv.FormatFloat(p.val, 'f', 2, 64), "0"), ".")+" "+p.ccy)
	}
	return out
}
