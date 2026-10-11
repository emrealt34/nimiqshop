package handlers

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/valyala/fasthttp"
	"nimiqshop/internal/catalog"
	"nimiqshop/internal/config"
	"nimiqshop/internal/cryptorefills"
)

func TestQuoteMetadataCannotLoseEMoneyFromAnotherVariant(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("metadata lookup must not create or validate an order: %s", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"family":"Mixed Card","brand":"Retail Card","kind":"giftcard","category":"retail","products":[{"product_id":"retail","denomination":"10 USD"}]},{"family":"Mixed Card","brand":"Financial Card","kind":"giftcard","category":"e-money","products":[{"product_id":"financial","denomination":"10 USD"}]}]`))
	}))
	defer server.Close()
	client := cryptorefills.NewClient(server.URL, "test", "test", "test", cryptorefills.QueueConfig{})
	defer client.Close()
	h := New(nil, config.Config{}, client)
	ctx := &fasthttp.RequestCtx{}
	ctx.Request.SetRequestURI("/api/quotes?lang=en")
	meta := h.lookupFamilyMeta(ctx, "Mixed Card", "TR")
	if !meta.Known {
		t.Fatal("local supplier fixture was not loaded")
	}
	rules := catalog.Rules{}
	if err := catalog.GateQuote(&rules, "Mixed Card", meta.Category, meta.Kind, "TR", meta.AdditionalCats, 5, 10); err == nil {
		t.Fatal("the first retail variant must not conceal the second e-money variant at checkout or in admin test purchases")
	}
}
