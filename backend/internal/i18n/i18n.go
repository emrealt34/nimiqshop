// Package i18n is the backend-side translation layer. Emails (gift notes,
// order confirmations) leave the server in the BUYER's language. The frontend
// sends its active language as `?lang=` AND as a `nimshop-lang` SameSite
// cookie on every API request; Parse*() reads both (plus Accept-Language as
// a last resort) and hands a Translator / language code to mail/notification
// code.
//
// Dictionary keys match 1:1 with the frontend TypeScript dictionary
// (src/i18n/locales/en.ts); scripts/check-i18n.mjs verifies parity at build
// time so a key added in one place and forgotten in the other fails CI.
//
// Adding a language:
//  1. Drop a new <code>.go in locales/ (copy en.go and translate).
//  2. Register it in `bundles` and `All` below.
//  3. Add the matching frontend file src/i18n/locales/<code>.ts.
//  4. Run `node scripts/check-i18n.mjs` to verify.
package i18n

import (
	"net/http"
	"strings"

	"github.com/valyala/fasthttp"
	"nimiqshop/internal/i18n/locales"
)

// Lang is a supported language code.
type Lang = string

const (
	EN Lang = "en"
	ES Lang = "es"
	DE Lang = "de"
	FR Lang = "fr"
	PT Lang = "pt"
	TR Lang = "tr"
)

// CookieName is the cookie the frontend writes and the backend reads on every
// request. Keep in sync with src/i18n/index.tsx (COOKIE_NAME).
const CookieName = "nimshop-lang"

// All lists every supported language in switcher order (mirrors frontend).
var All = []Lang{EN, ES, DE, FR, PT, TR}

var bundles = map[Lang]map[string]string{
	EN: locales.En,
	ES: locales.Es,
	DE: locales.De,
	FR: locales.Fr,
	PT: locales.Pt,
	TR: locales.Tr,
}

// Valid reports whether code is a supported language.
func Valid(code string) bool {
	_, ok := bundles[code]
	return ok
}

// Clean normalises a raw string to a supported code or returns EN.
func Clean(raw string) Lang {
	code := strings.ToLower(strings.TrimSpace(raw))
	if len(code) >= 2 {
		code = code[:2]
	}
	if Valid(code) {
		return code
	}
	return EN
}

// pickFromAcceptLanguage scans an Accept-Language header for the first
// supported language code. Returns EN when none matches.
func pickFromAcceptLanguage(al string) Lang {
	if al == "" {
		return EN
	}
	for _, part := range strings.Split(al, ",") {
		seg := strings.TrimSpace(part)
		if semi := strings.Index(seg, ";"); semi >= 0 {
			seg = seg[:semi]
		}
		if len(seg) >= 2 {
			if code := Clean(seg[:2]); Valid(code) {
				return code
			}
		}
	}
	return EN
}

// T is a translator bound to a single language. Call T(key, vars) to get a
// string. A missing key returns the English value, then the key itself, so
// an untranslated string never produces an empty email.
type T func(key string, vars ...map[string]string) string

// For builds a translator for the given language.
func For(lang Lang) T {
	if !Valid(lang) {
		lang = EN
	}
	dict := bundles[lang]
	enDict := bundles[EN]
	return func(key string, vars ...map[string]string) string {
		s, ok := dict[key]
		if !ok {
			s = enDict[key]
		}
		if s == "" {
			return key
		}
		if len(vars) > 0 && vars[0] != nil {
			s = interpolate(s, vars[0])
		}
		return s
	}
}

// interpolate replaces {{name}} markers with values from vars.
func interpolate(s string, vars map[string]string) string {
	var b strings.Builder
	b.Grow(len(s))
	for {
		i := strings.Index(s, "{{")
		if i < 0 {
			b.WriteString(s)
			break
		}
		b.WriteString(s[:i])
		s = s[i+2:]
		j := strings.Index(s, "}}")
		if j < 0 {
			b.WriteString("{{")
			b.WriteString(s)
			break
		}
		name := strings.TrimSpace(s[:j])
		if v, ok := vars[name]; ok {
			b.WriteString(v)
		}
		s = s[j+2:]
	}
	return b.String()
}

// from picks a language from explicit parts (query, cookie) and an
// Accept-Language header. Used by both the net/http and fasthttp variants.
func from(query, cookie, acceptLang string) Lang {
	if query != "" {
		if c := Clean(query); Valid(c) {
			return c
		}
	}
	if cookie != "" {
		if c := Clean(cookie); Valid(c) {
			return c
		}
	}
	if c := pickFromAcceptLanguage(acceptLang); Valid(c) {
		return c
	}
	return EN
}

// Parse reads the language from a net/http request and returns a translator.
func Parse(r *http.Request) T {
	if r == nil {
		return For(EN)
	}
	q := r.URL.Query().Get("lang")
	c := ""
	if ck, err := r.Cookie(CookieName); err == nil && ck != nil {
		c = ck.Value
	}
	al := r.Header.Get("Accept-Language")
	return For(from(q, c, al))
}

// ParseLang returns just the language code for a net/http request (for
// storing on a Quote/User record).
func ParseLang(r *http.Request) Lang {
	if r == nil {
		return EN
	}
	q := r.URL.Query().Get("lang")
	c := ""
	if ck, err := r.Cookie(CookieName); err == nil && ck != nil {
		c = ck.Value
	}
	al := r.Header.Get("Accept-Language")
	return from(q, c, al)
}

// ParseLangCtx returns the language code for a fasthttp request (used by
// fasthttp handlers so gift emails ship in the buyer's language).
func ParseLangCtx(ctx *fasthttp.RequestCtx) Lang {
	if ctx == nil {
		return EN
	}
	q := string(ctx.QueryArgs().Peek("lang"))
	c := string(ctx.Request.Header.Cookie(CookieName))
	al := string(ctx.Request.Header.Peek("Accept-Language"))
	return from(q, c, al)
}

// cookieOpts is the single place the cookie shape lives. One year,
// SameSite=Lax, HttpOnly=false (JS needs to read it for first paint).
const cookieMaxAge = 60 * 60 * 24 * 365

// crossSiteCookies, set once at boot (i18n.SetCrossSiteCookies) when the
// frontend and the API live on different sites (SESSION_COOKIE_SAME_SITE=none),
// makes the language cookie usable across those sites: SameSite=Lax cookies
// are never sent on cross-site fetches, so the preference would silently reset
// to the browser default on every visit.
var crossSiteCookies bool

// SetCrossSiteCookies switches the language cookie to SameSite=None; Secure
// for cross-domain deployments (frontend and API on different hostnames).
func SetCrossSiteCookies(v bool) { crossSiteCookies = v }

// SetCookieCtx writes the language cookie on a fasthttp response. It is not
// a session cookie: it only carries the UI language, and the frontend reads
// it with document.cookie (src/i18n/index.tsx) to pick the locale before
// hydration, so it is deliberately not HttpOnly.
func SetCookieCtx(ctx *fasthttp.RequestCtx, lang Lang) {
	if !Valid(lang) {
		lang = EN
	}
	cookie := fasthttp.AcquireCookie()
	defer fasthttp.ReleaseCookie(cookie)
	cookie.SetKey(CookieName)
	cookie.SetValue(lang)
	cookie.SetPath("/")
	cookie.SetMaxAge(cookieMaxAge)
	if crossSiteCookies {
		cookie.SetSameSite(fasthttp.CookieSameSiteNoneMode)
		cookie.SetSecure(true)
	} else {
		cookie.SetSameSite(fasthttp.CookieSameSiteLaxMode)
	}
	cookie.SetHTTPOnly(false)
	ctx.Response.Header.SetCookie(cookie)
}
