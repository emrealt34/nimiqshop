package i18n

import (
	"testing"

	"github.com/valyala/fasthttp"
)

func reqWith(query, cookie, acceptLang string) *fasthttp.RequestCtx {
	ctx := &fasthttp.RequestCtx{}
	ctx.Request.SetRequestURI("/api/quotes?lang=" + query)
	if cookie != "" {
		ctx.Request.Header.SetCookie(CookieName, cookie)
	}
	if acceptLang != "" {
		ctx.Request.Header.Set("Accept-Language", acceptLang)
	}
	return ctx
}

func TestMailLangPrecedence(t *testing.T) {
	cases := []struct {
		name                       string
		query, cookie, al, country string
		want                       Lang
	}{
		{"country beats browser and cookie", "", "en", "en-US,en;q=0.9", "TR", TR},
		{"german country", "", "", "", "DE", DE},
		{"swiss country takes German", "", "", "", "ch", DE},
		{"explicit query beats country", "es", "", "", "TR", ES},
		{"unmapped country falls back to cookie", "", "pt", "de", "US", PT},
		{"unmapped country falls back to browser", "", "", "fr-FR", "", FR},
		{"nothing known is English", "", "", "", "", EN},
		{"bad query is ignored", "xx", "", "", "TR", TR},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ctx := reqWith(c.query, c.cookie, c.al)
			if got := MailLang(ctx, c.country); got != c.want {
				t.Fatalf("MailLang = %q, want %q", got, c.want)
			}
		})
	}
}

func TestLangForCountry(t *testing.T) {
	if l, ok := LangForCountry("TR"); !ok || l != TR {
		t.Fatalf("TR: %v %v", l, ok)
	}
	if _, ok := LangForCountry("US"); ok {
		t.Fatal("US must not map to a language")
	}
}

func TestSupplierLang(t *testing.T) {
	if SupplierLang("tr") != TR || SupplierLang("") != EN || SupplierLang("zz") != EN || SupplierLang(" DE-de") != DE {
		t.Fatal("SupplierLang normalisation")
	}
}
