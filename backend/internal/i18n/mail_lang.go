package i18n

import (
	"strings"

	"github.com/valyala/fasthttp"
)

// Buyer-mail language.
//
// Mail to a buyer (the gift/receipt note and the supplier's code mail) is
// written in ONE language, chosen at checkout in this order:
//
//  1. an explicit ?lang= on the checkout request,
//  2. the buyer's purchase country (the edge's country for the visit), when it
//     maps to a supported language,
//  3. the site language the buyer already has (cookie, then Accept-Language),
//  4. English.
//
// The site-language cookie is NOT written from step 2, so a buyer in Turkey
// who reads the shop in English does not get the shop switched to Turkish.

// countryLang maps an ISO 3166-1 alpha-2 country to the language of its
// market. Countries with several official languages take the dominant one
// (CH → de, BE → fr, CA is absent and falls through to the site language).
var countryLang = map[string]Lang{
	"TR": TR,
	"DE": DE, "AT": DE, "CH": DE, "LI": DE,
	"ES": ES, "MX": ES, "AR": ES, "CO": ES, "CL": ES, "PE": ES, "VE": ES, "EC": ES,
	"UY": ES, "PY": ES, "BO": ES, "CR": ES, "GT": ES, "HN": ES, "NI": ES, "PA": ES,
	"SV": ES, "DO": ES, "PR": ES, "CU": ES,
	"FR": FR, "BE": FR, "LU": FR, "MC": FR, "SN": FR, "CI": FR,
	"PT": PT, "BR": PT, "AO": PT, "MZ": PT,
}

// LangForCountry returns the language for a country code, or ok=false when the
// country has no mapped language.
func LangForCountry(cc string) (Lang, bool) {
	l, ok := countryLang[strings.ToUpper(strings.TrimSpace(cc))]
	return l, ok
}

// MailLang picks the language of mail to the buyer of this request. country is
// the buyer's purchase country ("" when unknown).
func MailLang(ctx *fasthttp.RequestCtx, country string) Lang {
	if ctx != nil {
		if q := strings.ToLower(strings.TrimSpace(string(ctx.QueryArgs().Peek("lang")))); len(q) >= 2 && Valid(q[:2]) {
			return Lang(q[:2])
		}
	}
	if l, ok := LangForCountry(country); ok {
		return l
	}
	return ParseLangCtx(ctx)
}

// SupplierLang returns a supported language code for a stored quote, or EN.
func SupplierLang(stored string) Lang {
	if s := strings.ToLower(strings.TrimSpace(stored)); len(s) >= 2 && Valid(s[:2]) {
		return Lang(s[:2])
	}
	return EN
}
