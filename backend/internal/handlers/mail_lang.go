package handlers

import (
	"github.com/valyala/fasthttp"

	"nimiqshop/internal/i18n"
)

// buyerMailLang is the language of mail to this checkout's buyer, decided by
// i18n.MailLang from the product's market country: explicit ?lang=, then the
// product's country, then the site language. productCountry is "" when the
// checkout mixes markets.
func (h *Handlers) buyerMailLang(ctx *fasthttp.RequestCtx, productCountry string) i18n.Lang {
	return i18n.MailLang(ctx, productCountry)
}

// commonItemCountry returns the one country shared by every line of a batch,
// or "" when the lines come from different markets (or there are none).
func commonItemCountry(countries ...string) string {
	common := ""
	for _, c := range countries {
		if c == "" || (common != "" && c != common) {
			return ""
		}
		common = c
	}
	return common
}

func batchItemCountries(items []batchQuoteItem) []string {
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, it.Country)
	}
	return out
}
