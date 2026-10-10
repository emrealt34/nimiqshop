package handlers

import (
	"github.com/valyala/fasthttp"

	"nimiqshop/internal/clientip"
	"nimiqshop/internal/i18n"
)

// buyerMailLang is the language of mail to this checkout's buyer, decided by
// i18n.MailLang: explicit ?lang=, then the buyer's purchase country, then the
// site language. The trusted edge country is preferred; the browser's edge hint
// is only a fallback, and it decides the mail language only (never access).
func (h *Handlers) buyerMailLang(ctx *fasthttp.RequestCtx) i18n.Lang {
	country := clientip.Resolve(ctx, h.Cfg.TrustProxy, h.Cfg.ClientIPPolicy()).Country
	if country == "" {
		country = clientip.CountryHint(ctx)
	}
	return i18n.MailLang(ctx, country)
}
