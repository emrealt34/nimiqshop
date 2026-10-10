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
		name                         string
		query, cookie, al, productCC string
		want                         Lang
	}{
		{"turkish product beats English browser", "", "en", "en-US,en;q=0.9", "TR", TR},
		{"english product beats Turkish browser", "", "tr", "tr-TR", "US", EN},
		{"british product is English", "", "de", "de", "GB", EN},
		{"german product", "", "", "", "DE", DE},
		{"swiss product takes German", "", "", "", "ch", DE},
		{"explicit query beats product", "es", "", "", "TR", ES},
		{"unmapped product falls back to cookie", "", "pt", "de", "JP", PT},
		{"unmapped product falls back to browser", "", "", "fr-FR", "", FR},
		{"nothing known is English", "", "", "", "", EN},
		{"bad query is ignored", "xx", "", "", "TR", TR},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ctx := reqWith(c.query, c.cookie, c.al)
			if got := MailLang(ctx, c.productCC); got != c.want {
				t.Fatalf("MailLang = %q, want %q", got, c.want)
			}
		})
	}
}

func TestLangForCountry(t *testing.T) {
	if l, ok := LangForCountry("TR"); !ok || l != TR {
		t.Fatalf("TR: %v %v", l, ok)
	}
	if l, ok := LangForCountry("US"); !ok || l != EN {
		t.Fatal("US is an English market")
	}
	if _, ok := LangForCountry("JP"); ok {
		t.Fatal("JP has no mapped language and must fall through")
	}
}

func TestSupplierLang(t *testing.T) {
	if SupplierLang("tr") != TR || SupplierLang("") != EN || SupplierLang("zz") != EN || SupplierLang(" DE-de") != DE {
		t.Fatal("SupplierLang normalisation")
	}
}
