package handlers

/*
 * gift_items.go — the product list of a gift or order note, the way the
 * site's order list shows it: one brand tile per line (the brand's logo on the
 * brand's own background), the name, the face value and the quantity.
 *
 * The logo comes from the same supplier brand catalog the storefront uses
 * (logo_url / bg_color), prepared by brandlogo. A line with no known logo
 * still gets a tile, with the product's first letter.
 */

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"nimiqshop/internal/brandlogo"
	"nimiqshop/internal/cryptorefills"
	"nimiqshop/internal/db"
	"nimiqshop/internal/mailtrap"
)

// giftLogos prepares brand logos for the mail. Process-wide so the cache
// survives across sends.
var giftLogos = brandlogo.New()

// giftItemLogoLimit is how many lines get a logo fetched. The mail shows at
// most this many rows with their own tile; later lines are summarised.
const giftItemLogoLimit = 8

// brandRef is the catalog's logo field and background for one brand family.
type brandRef struct {
	logo string
	bg   string
}

// brandIndexFrom maps each brand family (lower case) to its logo and
// background. When a family appears more than once, the first entry that has
// both wins, so a bare duplicate cannot blank out a complete one.
func brandIndexFrom(cats []cryptorefills.BrandCategory) map[string]brandRef {
	idx := map[string]brandRef{}
	for _, c := range cats {
		for _, b := range c.Brands {
			key := strings.ToLower(strings.TrimSpace(b.Family))
			if key == "" {
				continue
			}
			logo := b.LogoURL
			if strings.TrimSpace(logo) == "" {
				logo = b.LogoBaseURL
			}
			if prev, ok := idx[key]; ok && prev.logo != "" && prev.bg != "" {
				continue
			}
			idx[key] = brandRef{logo: logo, bg: b.BgColor}
		}
	}
	return idx
}

// giftLinesFor returns the quote's own lines, or one synthetic line for a
// single-product quote (which has no lines of its own).
func giftLinesFor(q db.Quote) []db.QuoteLine {
	if len(q.Lines) > 0 {
		return q.Lines
	}
	return []db.QuoteLine{{
		ProductID:    q.ProductID,
		ProductValue: q.ProductValue,
		Denomination: q.Denomination,
		Country:      q.ProductCountry,
		Quantity:     1,
	}}
}

// giftItemsFrom builds the items and, aligned by index, the logo field each
// one should fetch. Pure: no network, so the mapping is testable on its own.
// Only the first giftItemLogoLimit lines get a logo field.
func giftItemsFrom(lines []db.QuoteLine, idx map[string]brandRef) ([]mailtrap.GiftItem, []string) {
	var items []mailtrap.GiftItem
	var fields []string
	for _, l := range lines {
		name := strings.TrimSpace(l.ProductID)
		if name == "" {
			continue
		}
		ref := idx[strings.ToLower(name)]
		field := ""
		if len(items) < giftItemLogoLimit {
			field = ref.logo
		}
		qty := l.Quantity
		if qty < 1 {
			qty = 1
		}
		items = append(items, mailtrap.GiftItem{
			Name:    name,
			Detail:  giftFaceDetail(l),
			Qty:     qty,
			BgColor: ref.bg,
		})
		fields = append(fields, field)
	}
	return items, fields
}

// giftFaceDetail is the face value shown under the name: the supplier's label
// when it is a fixed value, otherwise the denomination, otherwise the amount.
func giftFaceDetail(l db.QuoteLine) string {
	if fl := strings.TrimSpace(l.FaceLabel); fl != "" && fl != "range" {
		return fl
	}
	if d := strings.TrimSpace(l.Denomination); d != "" && d != "range" {
		return d
	}
	if l.ProductValue > 0 {
		cur := strings.TrimSpace(l.ProductCurrency)
		if cur == "" {
			cur = adminExtractCurrency(l.Denomination, l.Country)
		}
		return fmt.Sprintf("%g %s", l.ProductValue, cur)
	}
	return ""
}

// giftItemsFor is the product list for a quote's note. Network failures only
// cost the logos: names, values and quantities are always there.
func (h *Handlers) giftItemsFor(q db.Quote) []mailtrap.GiftItem {
	lines := giftLinesFor(q)
	var idx map[string]brandRef
	if h != nil && h.CR != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		country := strings.TrimSpace(q.ProductCountry)
		if country == "" && len(lines) > 0 {
			country = lines[0].Country
		}
		if resp, err := h.CR.Brands(ctx, country); err == nil && resp != nil {
			idx = brandIndexFrom(resp.Categories)
		}
		cancel()
	}
	items, fields := giftItemsFrom(lines, idx)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	for i, f := range fields {
		if f == "" {
			continue
		}
		wg.Add(1)
		go func(i int, f string) {
			defer wg.Done()
			items[i].Logo = giftLogos.PNG(ctx, f)
		}(i, f)
	}
	wg.Wait()
	return items
}

// giftLabelFor names a multi-line note for the subject line and the delivery
// wording: the brand names joined, never a single line's value.
func giftLabelFor(items []mailtrap.GiftItem) string {
	names := make([]string, 0, len(items))
	for _, it := range items {
		names = append(names, it.Name)
	}
	return strings.Join(names, " + ")
}
