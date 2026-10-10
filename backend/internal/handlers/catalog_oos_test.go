package handlers

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/valyala/fasthttp"

	"nimiqshop/internal/cryptorefills"
)

func resetMissing(t *testing.T) {
	t.Helper()
	missingMu.Lock()
	missingFamilies = map[string]time.Time{}
	missingMu.Unlock()
	t.Cleanup(func() {
		missingMu.Lock()
		missingFamilies = map[string]time.Time{}
		missingMu.Unlock()
	})
}

func problemErr(codes ...string) error {
	pe := &cryptorefills.ProblemError{}
	for _, c := range codes {
		pe.Problems = append(pe.Problems, cryptorefills.Problem{Code: c})
	}
	return pe
}

func TestOutOfStockProblemOnlyForSoldOutCodes(t *testing.T) {
	cases := []struct {
		err  error
		want bool
	}{
		{problemErr("OUT_OF_STOCK"), true},
		{problemErr("NOT_AVAILABLE_PRODUCT"), true},
		{problemErr("AMOUNT_TOO_LOW"), false},
		{errors.New("timeout"), false},
		{nil, false},
	}
	for _, c := range cases {
		if got := outOfStockProblem(c.err); got != c.want {
			t.Errorf("outOfStockProblem(%v) = %v, want %v", c.err, got, c.want)
		}
	}
}

func TestNoteSupplierOutOfStockMarksFamilyForTheTombstoneTTL(t *testing.T) {
	resetMissing(t)
	noteSupplierOutOfStock(problemErr("AMOUNT_TOO_LOW"), "Asos", "GB")
	if familyIsMissing("Asos", "GB") {
		t.Fatal("a non-stock problem must not mark the family")
	}
	noteSupplierOutOfStock(problemErr("OUT_OF_STOCK"), "Asos", "GB")
	if !familyIsMissing("Asos", "GB") {
		t.Fatal("OUT_OF_STOCK must mark the family")
	}
	if familyIsMissing("Asos", "TR") {
		t.Fatal("the mark is per country")
	}
}

func TestFlaggedBrandsStayListedAsOutOfStock(t *testing.T) {
	resetMissing(t)
	markFamilyMissing("Asos", "GB")
	resp := &cryptorefills.BrandsResponse{Categories: []cryptorefills.BrandCategory{{
		Brands: []cryptorefills.Brand{{Family: "Asos"}, {Family: "Steam"}},
	}}}
	out := dropMissingBrands(resp, "GB")
	brands := out.Categories[0].Brands
	if len(brands) != 2 {
		t.Fatalf("flagged brand must stay listed, got %d brands", len(brands))
	}
	if !brands[0].OutOfStock || brands[1].OutOfStock {
		t.Fatalf("only the flagged brand is out of stock: %+v", brands)
	}
}

func TestUnavailableFamiliesEndpointListsOneCountry(t *testing.T) {
	resetMissing(t)
	markFamilyMissing("Steam", "GB")
	markFamilyMissing("Asos", "GB")
	markFamilyMissing("Netflix", "TR")

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.SetRequestURI("/api/catalog/unavailable?country=gb")
	(&Handlers{}).UnavailableFamilies(ctx)
	if ctx.Response.StatusCode() != fasthttp.StatusOK {
		t.Fatalf("status %d", ctx.Response.StatusCode())
	}
	var got struct {
		Country  string   `json:"country"`
		Families []string `json:"families"`
	}
	if err := json.Unmarshal(ctx.Response.Body(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Country != "GB" || len(got.Families) != 2 || got.Families[0] != "asos" || got.Families[1] != "steam" {
		t.Fatalf("unexpected payload: %+v", got)
	}
}
