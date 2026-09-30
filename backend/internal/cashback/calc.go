// Package cashback pays the buyer a percentage of the product's NIM price
// from the shop wallet, only after the supplier has fulfilled the order.
package cashback

import (
	"fmt"
	"math"
	"strings"
	"unicode/utf8"
)

const (
	// LunaPerNIM is Nimiq's on-chain subunit (1 NIM = 100_000 Luna).
	LunaPerNIM = 100_000
	// MaxMemoBytes is the Nimiq basic-transaction extra-data ceiling.
	MaxMemoBytes = 64
)

// ShopName is the public shop hostname used in on-chain memos. Set once at
// process start from SITE_HOST (default shop.nimiqbase.com).
var ShopName = "shop.nimiqbase.com"

func shop() string {
	if s := strings.TrimSpace(ShopName); s != "" {
		return s
	}
	return "shop.nimiqbase.com"
}

// AmountLuna is bps/10_000 of productNIM, rounded to the nearest Luna.
// Returns 0 when the inputs are not a honest positive amount (never guess).
func AmountLuna(productNIM float64, bps int) int64 {
	if productNIM <= 0 || bps <= 0 || math.IsNaN(productNIM) || math.IsInf(productNIM, 0) {
		return 0
	}
	luna := productNIM * float64(bps) / 10_000 * LunaPerNIM
	if luna < 0.5 {
		return 0
	}
	out := int64(math.Round(luna))
	if out < 1 {
		return 0
	}
	return out
}

// NIMFromLuna is the display amount (not used for signing).
func NIMFromLuna(luna int64) float64 {
	return float64(luna) / LunaPerNIM
}

// formatNIM renders the amount written into the on-chain memo. It keeps two
// decimals below 100 NIM on purpose: the memo is the buyer's reconciliation
// receipt, and rounding 12.34 down to "12.3" would make it disagree with the
// amount actually sent. Memo() already clips the product name to stay inside
// MaxMemoBytes, so the extra character cannot overflow the extra-data field.
func formatNIM(n float64) string {
	if n >= 100 {
		return fmt.Sprintf("%.0f", n)
	}
	s := fmt.Sprintf("%.2f", n)
	s = strings.TrimRight(s, "0")
	s = strings.TrimRight(s, ".")
	if s == "" {
		s = "0"
	}
	return s
}

// Memo builds "You earn X NIM by purchasing Y from <SITE_HOST>", truncated to
// 64 bytes so the on-chain extra data is always valid.
func Memo(amountNIM float64, product string) string {
	amt := formatNIM(amountNIM)
	product = strings.TrimSpace(product)
	product = strings.ReplaceAll(product, "\n", " ")
	suffix := " from " + shop()
	prefix := "You earn " + amt + " NIM by purchasing "
	if len(prefix)+len(suffix) >= MaxMemoBytes {
		s := "You earn " + amt + " NIM from " + shop()
		return clipBytes(s, MaxMemoBytes)
	}
	budget := MaxMemoBytes - len(prefix) - len(suffix)
	p := clipBytes(product, budget)
	if p == "" {
		p = "item"
		if len(prefix)+len(p)+len(suffix) > MaxMemoBytes {
			return clipBytes("You earn "+amt+" NIM from "+shop(), MaxMemoBytes)
		}
	}
	return prefix + p + suffix
}

// TreeMemo builds the memo used when cashback is being donated to tree planting.
// "<X> NIM — cashback planted trees from <product> @ <site>"
// Makes it clear on the block explorer that this transaction's cashback was
// used to plant trees, not a regular cashback payout to the buyer.
func TreeMemo(amountNIM float64, product string) string {
	amt := formatNIM(amountNIM)
	product = strings.TrimSpace(product)
	product = strings.ReplaceAll(product, "\n", " ")
	// Pattern: "<amt> NIM cashback → trees from <product> @ <site>"
	suffix := " @ " + shop()
	prefix := amt + " NIM cashback -> trees from "
	if len(prefix)+len(suffix) >= MaxMemoBytes {
		s := amt + " NIM plants trees @ " + shop()
		return clipBytes(s, MaxMemoBytes)
	}
	budget := MaxMemoBytes - len(prefix) - len(suffix)
	p := clipBytes(product, budget)
	if p == "" {
		p = "purchase"
		if len(prefix)+len(p)+len(suffix) > MaxMemoBytes {
			return clipBytes(amt+" NIM plants trees @ "+shop(), MaxMemoBytes)
		}
	}
	return prefix + p + suffix
}

func clipBytes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.ValidString(s[:n]) {
		n--
	}
	return s[:n]
}
