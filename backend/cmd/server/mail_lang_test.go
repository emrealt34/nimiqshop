package main

import (
	"net/http"
	"testing"
)

// The buyer's mail language follows the PRODUCT's market (its country), then
// the site language. Each case uses a fresh wallet so checkouts never collide.
func TestBuyerMailLanguageFollowsProductCountry(t *testing.T) {
	s := bootStack(t, stackOptions{})

	quoteLang := func(productCountry string, headers map[string]string) string {
		t.Helper()
		auth, _ := s.signIn()
		for k, v := range headers {
			auth[k] = v
		}
		res := s.do(http.MethodPost, "/api/quotes", map[string]any{
			"product_id": "test-steam", "country": productCountry, "quantity": 1,
			"denomination": "50 USD", "product_value": 50, "email": "buyer@example.com",
		}, auth)
		if res.status >= 300 {
			t.Fatalf("quote (%s): %d %s", productCountry, res.status, truncate(res.body))
		}
		get := s.do(http.MethodGet, "/api/quotes/"+quoteIDFrom(t, res), nil, auth)
		q, _ := get.json(t)["quote"].(map[string]any)
		lang, _ := q["lang"].(string)
		return lang
	}

	// The test catalog only carries US products, so the end-to-end check is
	// that an English product stays English whatever the buyer's language
	// signals say. The other markets are covered by the unit tests.
	cases := []struct {
		name    string
		headers map[string]string
		want    string
	}{
		{"US product, Turkish browser: English", map[string]string{"Accept-Language": "tr-TR,tr;q=0.9"}, "en"},
		{"US product, Turkish site cookie: English", map[string]string{"Cookie": "nimshop-lang=tr"}, "en"},
		{"US product, no signals: English", map[string]string{}, "en"},
	}
	for _, c := range cases {
		if got := quoteLang("US", c.headers); got != c.want {
			t.Errorf("%s: mail language %q, want %q", c.name, got, c.want)
		}
	}
}
