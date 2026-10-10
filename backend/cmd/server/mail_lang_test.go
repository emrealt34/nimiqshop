package main

import (
	"net/http"
	"testing"
)

// The buyer's mail language follows the purchase country (edge hint), then the
// site language. Each case uses a fresh wallet so checkouts never collide.
func TestBuyerMailLanguageFollowsCountry(t *testing.T) {
	s := bootStack(t, stackOptions{})

	quoteLang := func(headers map[string]string) string {
		t.Helper()
		auth, _ := s.signIn()
		for k, v := range headers {
			auth[k] = v
		}
		res := s.do(http.MethodPost, "/api/quotes", map[string]any{
			"product_id": "test-steam", "country": "US", "quantity": 1,
			"denomination": "50 USD", "product_value": 50, "email": "buyer@example.com",
		}, auth)
		if res.status >= 300 {
			t.Fatalf("quote: %d %s", res.status, truncate(res.body))
		}
		get := s.do(http.MethodGet, "/api/quotes/"+quoteIDFrom(t, res), nil, auth)
		q, _ := get.json(t)["quote"].(map[string]any)
		lang, _ := q["lang"].(string)
		return lang
	}

	cases := []struct {
		name    string
		headers map[string]string
		want    string
	}{
		{"Turkish buyer, English browser", map[string]string{"X-Nimshop-Country-Hint": "TR", "Accept-Language": "en-US,en;q=0.9"}, "tr"},
		{"German buyer", map[string]string{"X-Nimshop-Country-Hint": "DE", "Accept-Language": "tr"}, "de"},
		{"unmapped country uses browser language", map[string]string{"X-Nimshop-Country-Hint": "US", "Accept-Language": "fr-FR"}, "fr"},
		{"no country, no language: English", map[string]string{}, "en"},
	}
	for _, c := range cases {
		if got := quoteLang(c.headers); got != c.want {
			t.Errorf("%s: mail language %q, want %q", c.name, got, c.want)
		}
	}
}
