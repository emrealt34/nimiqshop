package handlers

import (
	"errors"
	"log"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/valyala/fasthttp"

	"nimiqshop/internal/catalog"
	"nimiqshop/internal/cryptorefills"
	phonepkg "nimiqshop/internal/phone"
)

// errEmptyCatalog signals a supplier SUCCESS that carried no data. It is
// never cached as fresh content: an empty brand/family payload (a glitch,
// a half-migrated account, a filtered response) must fall back to the
// stale/disk layers instead of blanking the storefront for the whole TTL.
// THIS was the root cause of "products stopped showing": the old code
// cached empty results exactly like real ones.
var errEmptyCatalog = errors.New("supplier returned an empty catalog payload")

/*
The catalog handlers are thin, deeply-cached proxies over the CryptoRefills
API. Cache layout for every public catalog endpoint:

	L1  in-memory fresh   brands 6h · family 1h · price 10m · vias 12h
	L2  in-memory stale   served on supplier error (up to 30 days old)
	L3  Badger snapshot   served on L1/L2 miss + supplier error, survives restarts
	L4  supplier          reached at most once per key per fresh TTL, and every
	                      concurrent miss collapses through ONE singleflight

On top of the server layers, public responses carry Cache-Control so the
browser and Cloudflare absorb repeat traffic before it reaches us, and the
CryptoRefills client itself micro-caches every GET (3s-10min).

ADMIN CATALOG RULES: responses are filtered AFTER the cache with the live
operator rules (price cap, hidden families, banned categories/kinds,
country visibility), so a rule change applies to the very next request
without any cache flush. The purchase path re-checks the rules at quote
time (catalog.GateQuote) — a stale page can never buy.

A buyer hammering F5, a bot, or fifty tabs of the same user therefore
produce ZERO supplier calls until the fresh TTL expires — and even then,
exactly ONE upstream call (singleflight) that all concurrent requests share.
*/

// Catalog fresh-TTL constants. Deliberately long: catalogs change at most
// daily, the purchase path re-validates everything against the supplier
// anyway, and every minute of TTL is a minute the partner budget is not
// spent on re-fetching static data.
const (
	ttlBrands         = 6 * time.Hour
	ttlFamily         = 1 * time.Hour
	ttlPrice          = 10 * time.Minute
	ttlVias           = 12 * time.Hour
	ttlFamilyMeta     = 1 * time.Hour
	ttlFamilyMetaMiss = 30 * time.Second // negative/empty lookups only
)

// MISSING-FAMILY TOMBSTONES — the self-healing list. The /v2/brands
// listing and the /v5/products listing are DIFFERENT supplier endpoints:
// a brand can appear in the directory while its product listing is empty
// (delisted, region-locked, supplier data quirk). That mismatch used to
// mean "card on the home page -> Product not found on the product page",
// forever. Now: when a family proves empty, a tombstone is recorded and
// PUBLIC listings drop that card for the tombstone TTL; the moment the
// family serves products again, the tombstone clears and the card returns.
var (
	missingMu       sync.Mutex
	missingFamilies = map[string]time.Time{} // "family|country" -> hidden until
)

const missingFamilyTTL = 45 * time.Minute

func missingKey(family, country string) string {
	return strings.ToLower(strings.TrimSpace(family)) + "|" + strings.ToUpper(strings.TrimSpace(country))
}
func markFamilyMissing(family, country string) {
	missingMu.Lock()
	defer missingMu.Unlock()
	missingFamilies[missingKey(family, country)] = time.Now().Add(missingFamilyTTL)
}
func clearFamilyMissing(family, country string) {
	missingMu.Lock()
	defer missingMu.Unlock()
	delete(missingFamilies, missingKey(family, country))
}
func familyIsMissing(family, country string) bool {
	missingMu.Lock()
	defer missingMu.Unlock()
	k := missingKey(family, country)
	until, ok := missingFamilies[k]
	if !ok {
		return false
	}
	if time.Now().After(until) {
		delete(missingFamilies, k)
		return false
	}
	return true
}

// dropMissingBrands removes tombstoned brands from a public listing copy.
func dropMissingBrands(resp *cryptorefills.BrandsResponse, country string) *cryptorefills.BrandsResponse {
	if resp == nil {
		return resp
	}
	out := *resp
	out.Categories = make([]cryptorefills.BrandCategory, 0, len(resp.Categories))
	for _, cat := range resp.Categories {
		brands := make([]cryptorefills.Brand, 0, len(cat.Brands))
		for _, b := range cat.Brands {
			if !familyIsMissing(b.Family, country) {
				brands = append(brands, b)
			}
		}
		if len(brands) > 0 {
			cat.Brands = brands
			out.Categories = append(out.Categories, cat)
		}
	}
	return &out
}

// rulesForRequest loads the live catalog rules (cheap: single Badger key)
// with a 10-second in-memory cache so every product-card render on the
// homepage does not hit Badger 20+ times at once.
func (h *Handlers) rulesForRequest() catalog.Rules {
	if cached, ok := h.cache.get("catalog:rules"); ok {
		if rules, ok := cached.(catalog.Rules); ok {
			return rules
		}
	}
	rules, err := h.Store.GetCatalogRules()
	if err != nil {
		// Fail OPEN on the browse path (an operator DB hiccup must not
		// blank the storefront); the quote gate fails CLOSED separately.
		return catalog.Rules{}
	}
	h.cache.setTTL("catalog:rules", rules, 10*time.Second)
	return rules
}

/* --------------------------------- brands -------------------------------- */

func (h *Handlers) listBrandsCore(ctx *fasthttp.RequestCtx) (*cryptorefills.BrandsResponse, bool) {
	country := strings.ToUpper(strings.TrimSpace(string(ctx.QueryArgs().Peek("country"))))
	if len(country) > 2 {
		writeError(ctx, fasthttp.StatusBadRequest, "country must be a 2-letter code")
		return nil, false
	}
	if country == "" {
		country = "TR"
	}
	const prefix = "cr:brands:"
	cacheKey := prefix + country

	// L1: fresh in-memory (6h). The overwhelming majority of requests end
	// here — zero supplier calls, zero allocations beyond the response.
	if cached, ok := h.cache.get(cacheKey); ok {
		if resp, ok := cached.(*cryptorefills.BrandsResponse); ok {
			return resp, true
		}
	}

	// L4 via singleflight; L2/L3 inside the fallback path below.
	snapKey := "snap:" + cacheKey
	v, err := h.flights.Do("brands:"+country, func() (interface{}, error) {
		// Another goroutine may have populated the cache while we queued.
		if cached, ok := h.cache.get(cacheKey); ok {
			if resp, ok := cached.(*cryptorefills.BrandsResponse); ok {
				return resp, nil
			}
		}
		resp, ferr := h.CR.Brands(h.supplierContext(ctx), country)
		if ferr != nil {
			return nil, ferr
		}
		if resp == nil || len(resp.Categories) == 0 {
			return nil, errEmptyCatalog
		}
		h.cache.setTTL(cacheKey, resp, ttlBrands)
		h.saveCatalogSnapshot(snapKey, resp)
		return resp, nil
	})
	if err == nil {
		return v.(*cryptorefills.BrandsResponse), true
	}
	if !errors.Is(err, errEmptyCatalog) {
		log.Printf("cr brands(%s): %v", country, err)
	}

	// L2: stale in-memory — a 429 storm or outage serves slightly-old data.
	if stale, age, ok := h.cache.peekStale(cacheKey); ok && age < catalogStaleCap {
		if resp, ok := stale.(*cryptorefills.BrandsResponse); ok {
			log.Printf("cr brands(%s): serving stale in-memory catalog (age %s) after supplier error", country, age.Round(time.Second))
			return resp, true
		}
	}
	// L3: disk snapshot — covers cold starts and long outages.
	if snap, ok := h.loadCatalogSnapshot(snapKey, decodeBrands); ok {
		if resp, ok := snap.(*cryptorefills.BrandsResponse); ok && len(resp.Categories) > 0 {
			log.Printf("cr brands(%s): serving disk snapshot after supplier error", country)
			// Short TTL so the next pass retries the supplier soon.
			h.cache.setTTL(cacheKey, resp, 5*time.Minute)
			return resp, true
		}
	}
	if errors.Is(err, errEmptyCatalog) {
		log.Printf("cr brands(%s): supplier returned empty catalog and no fallback exists", country)
		writeError(ctx, fasthttp.StatusServiceUnavailable, "brand catalog is temporarily unavailable; please retry")
		return nil, false
	}
	h.supplierError(ctx, err, "could not load brand catalog")
	return nil, false
}

// publicBrands = listBrandsCore + tombstone filtering for PUBLIC paths.
// (AdminListBrands keeps the raw view so the operator can see everything.)
func (h *Handlers) publicBrands(ctx *fasthttp.RequestCtx) (*cryptorefills.BrandsResponse, string, bool) {
	full, ok := h.listBrandsCore(ctx)
	if !ok {
		return nil, "", false
	}
	country := strings.ToUpper(strings.TrimSpace(string(ctx.QueryArgs().Peek("country"))))
	if len(country) != 2 {
		country = "TR"
	}
	return dropMissingBrands(full, country), country, true
}

// ListBrands returns the full brand directory for a country (grouped by
// kind/category), filtered by the live admin rules. This is the primary
// browse endpoint.
func (h *Handlers) ListBrands(ctx *fasthttp.RequestCtx) {
	full, _, ok := h.publicBrands(ctx)
	if !ok {
		return
	}
	ctx.Response.Header.Set("Cache-Control", "public, max-age=300")
	rules := h.rulesForRequest()
	writeJSON(ctx, fasthttp.StatusOK, catalog.FilterBrands(&rules, full))
}

// AdminListBrands is the same directory WITHOUT rule filtering and
// WITHOUT tombstones, so the operator can see (and then hide) everything.
func (h *Handlers) AdminListBrands(ctx *fasthttp.RequestCtx) {
	full, ok := h.listBrandsCore(ctx)
	if !ok {
		return
	}
	ctx.Response.Header.Set("Cache-Control", "no-store")
	writeJSON(ctx, fasthttp.StatusOK, full)
}

// ListGiftCardProducts is the legacy alias: brands with kind=giftcard.
func (h *Handlers) ListGiftCardProducts(ctx *fasthttp.RequestCtx) {
	h.listBrandsFiltered(ctx, "giftcard")
}

// ListTopupProducts is the legacy alias: brands with kind=mobile_recharge
// (top-ups; eSIMs live in their own category and are excluded here).
func (h *Handlers) ListTopupProducts(ctx *fasthttp.RequestCtx) {
	h.listBrandsFiltered(ctx, "mobile_recharge")
}

// ListEsimProducts is the legacy alias: brands in the e-sim category.
func (h *Handlers) ListEsimProducts(ctx *fasthttp.RequestCtx) {
	h.listBrandsFiltered(ctx, "esim")
}

func (h *Handlers) listBrandsFiltered(ctx *fasthttp.RequestCtx, kind string) {
	full, country, ok := h.publicBrands(ctx)
	if !ok {
		return
	}
	_ = country
	ctx.Response.Header.Set("Cache-Control", "public, max-age=300")
	rules := h.rulesForRequest()
	filtered := catalog.FilterBrands(&rules, full)
	out := filterBrandCategories(filtered, kind)
	writeJSON(ctx, fasthttp.StatusOK, out)
}

// filterBrandCategories keeps only the categories matching the requested
// kind ("giftcard", "mobile_recharge") or the e-sim category.
func filterBrandCategories(full *cryptorefills.BrandsResponse, kind string) interface{} {
	if full == nil {
		return map[string]interface{}{"categories": []interface{}{}}
	}
	out := []cryptorefills.BrandCategory{}
	for _, c := range full.Categories {
		match := c.Kind == kind || c.Category == kind || (c.Category == "e-sim" && kind == "esim")
		if kind == "mobile_recharge" && c.Category == "e-sim" {
			match = false // eSIMs are their own tab, not top-ups
		}
		if match {
			out = append(out, c)
		}
	}
	if out == nil {
		out = []cryptorefills.BrandCategory{}
	}
	return map[string]interface{}{"categories": out}
}

/* -------------------------------- products ------------------------------- */

// GetFamily returns one family's product options (denominations/ranges) for
// a country, filtered by the live rules (hidden families 404, ranges
// clamped to the price cap). The path param is the family name;
// ?country= is required.
func (h *Handlers) GetFamily(ctx *fasthttp.RequestCtx) {
	h.getFamily(ctx, false)
}

// AdminGetFamily is the unfiltered variant for the operator console.
func (h *Handlers) AdminGetFamily(ctx *fasthttp.RequestCtx) {
	h.getFamily(ctx, true)
}

func (h *Handlers) getFamily(ctx *fasthttp.RequestCtx, bypassRules bool) {
	family, _ := ctx.UserValue("productId").(string)
	if family == "" {
		family = strings.TrimSpace(string(ctx.QueryArgs().Peek("family")))
	}
	// PATH-DECODE FIX: fasthttp/router hands path params over STILL
	// PERCENT-ENCODED ("Google%20Play"). The supplier call escapes them a
	// second time ("Google%2520Play"), which never matches and every
	// family whose name contains a space/&/() answered a false
	// "product not found" (Google Play, App Store & iTunes, Valorant
	// Point (VP)…). Decode the path segment once; the ?family= query
	// fallback is already decoded by fasthttp and stays untouched.
	if u, uerr := url.PathUnescape(family); uerr == nil {
		family = u
	}
	if family == "" {
		writeError(ctx, fasthttp.StatusBadRequest, "family is required")
		return
	}
	country := strings.ToUpper(strings.TrimSpace(string(ctx.QueryArgs().Peek("country"))))
	if len(country) != 2 {
		writeError(ctx, fasthttp.StatusBadRequest, "country query param (2-letter code) is required")
		return
	}
	// The payment coin is decided SERVER-SIDE — clients do not (and need not)
	// send ?coin= on public calls; it is always forced to the shop's rail.
	coin := string(ctx.QueryArgs().Peek("coin"))
	if bypassRules {
		coin = strings.ToUpper(coin)
	} else {
		coin = PaymentCoin
	}
	cacheKey := "cr:family:" + family + ":" + country + ":" + coin

	// L1 fresh. A cached EMPTY slice is a short-lived NEGATIVE entry
	// (30s): "this family genuinely does not exist" — 404 without any
	// supplier call. It is the only empty result we ever cache.
	if cached, ok := h.cache.get(cacheKey); ok {
		if c, ok := cached.([]cryptorefills.Family); ok {
			h.respondFamilies(ctx, c, bypassRules)
			return
		}
	}

	snapKey := "snap:" + cacheKey
	v, err := h.flights.Do("family:"+family+":"+country+":"+coin, func() (interface{}, error) {
		if cached, ok := h.cache.get(cacheKey); ok {
			if c, ok := cached.([]cryptorefills.Family); ok {
				return c, nil
			}
		}
		fams, ferr := h.CR.ProductsByCountry(h.supplierContext(ctx), country, family, coin)
		if ferr != nil {
			return nil, ferr
		}
		if len(fams) == 0 {
			// Empty supplier answer: a transient glitch or a truly unknown
			// family. BEFORE conceding a 404, try the stale in-memory layer
			// and the disk snapshot — for any family that ever existed,
			// "product not found" must be effectively impossible. The
			// negative cache is armed LAST (30s) so dead lookups cannot
			// hammer the supplier — writing it first would overwrite the
			// very stale layer we are falling back to.
			// Tombstone: PUBLIC listings drop this card so nobody else
			// clicks into a 404 (self-healing list).
			markFamilyMissing(family, country)
			if stale, age, ok := h.cache.peekStale(cacheKey); ok && age < catalogStaleCap {
				if c, ok := stale.([]cryptorefills.Family); ok && len(c) > 0 {
					log.Printf("cr products(%s/%s): supplier returned empty; serving stale in-memory family (age %s)", country, family, age.Round(time.Second))
					h.cache.setTTL(cacheKey, c, ttlFamilyMetaMiss)
					return c, nil
				}
			}
			if snap, ok := h.loadCatalogSnapshot(snapKey, decodeFamilies); ok {
				if c, ok := snap.([]cryptorefills.Family); ok && len(c) > 0 {
					log.Printf("cr products(%s/%s): supplier returned empty; serving disk snapshot family", country, family)
					h.cache.setTTL(cacheKey, c, ttlFamilyMetaMiss)
					return c, nil
				}
			}
			h.cache.setTTL(cacheKey, fams, ttlFamilyMetaMiss)
			return fams, nil
		}
		clearFamilyMissing(family, country) // family is back: unhide the card
		h.cache.setTTL(cacheKey, fams, ttlFamily)
		h.saveCatalogSnapshot(snapKey, fams)
		return fams, nil
	})
	if err == nil {
		fams, _ := v.([]cryptorefills.Family)
		h.respondFamilies(ctx, fams, bypassRules)
		return
	}
	if !errors.Is(err, errEmptyCatalog) {
		log.Printf("cr products(%s/%s): %v", country, family, err)
	}
	// L2 stale, then L3 disk.
	if stale, age, ok := h.cache.peekStale(cacheKey); ok && age < catalogStaleCap {
		if c, ok := stale.([]cryptorefills.Family); ok && len(c) > 0 {
			log.Printf("cr products(%s/%s): serving stale in-memory family (age %s) after supplier error", country, family, age.Round(time.Second))
			h.respondFamilies(ctx, c, bypassRules)
			return
		}
	}
	if snap, ok := h.loadCatalogSnapshot(snapKey, decodeFamilies); ok {
		if c, ok := snap.([]cryptorefills.Family); ok && len(c) > 0 {
			log.Printf("cr products(%s/%s): serving disk snapshot after supplier error", country, family)
			h.cache.setTTL(cacheKey, c, 5*time.Minute)
			h.respondFamilies(ctx, c, bypassRules)
			return
		}
	}
	h.supplierError(ctx, err, "product not available in this country")
}

// respondFamilies writes the family payload handling the empty/negative
// case (404) and the rule filter in one place.
func (h *Handlers) respondFamilies(ctx *fasthttp.RequestCtx, fams []cryptorefills.Family, bypassRules bool) {
	if len(fams) == 0 {
		writeError(ctx, fasthttp.StatusNotFound, "product not found")
		return
	}
	if bypassRules {
		ctx.Response.Header.Set("Cache-Control", "no-store")
		writeJSON(ctx, fasthttp.StatusOK, fams)
		return
	}
	ctx.Response.Header.Set("Cache-Control", "public, max-age=120")
	rules := h.rulesForRequest()
	filtered := catalog.FilterFamilies(&rules, fams)
	if len(filtered) == 0 {
		writeError(ctx, fasthttp.StatusNotFound, "product not found")
		return
	}
	writeJSON(ctx, fasthttp.StatusOK, filtered)
}

// GetProduct keeps the legacy route shape: /api/catalog/products/{id} with
// ?country= mapping to the family lookup.
func (h *Handlers) GetProduct(ctx *fasthttp.RequestCtx) {
	h.GetFamily(ctx)
}

/* --------------------------------- price --------------------------------- */

// ProductPrice converts a face value to the exact BTC amount for the
// frontend's "you will pay X BTC (≈ N NIM)" display. The coin parameter is
// accepted but forced to BTC — the shop's only rail is Lightning.
// Fresh 10 min · stale/disk fallback on supplier error · singleflight per
// (brand,country,value) so a typo-retry storm costs one upstream call.
func (h *Handlers) ProductPrice(ctx *fasthttp.RequestCtx) {
	brand := string(ctx.QueryArgs().Peek("brand"))
	country := strings.ToUpper(string(ctx.QueryArgs().Peek("country")))
	fv := string(ctx.QueryArgs().Peek("face_value"))
	if brand == "" || len(country) != 2 || fv == "" {
		writeError(ctx, fasthttp.StatusBadRequest, "brand, country and face_value are required")
		return
	}
	rules := h.rulesForRequest()
	if rules.IsFamilyHidden(brand) {
		writeError(ctx, fasthttp.StatusNotFound, "product not found")
		return
	}
	q, err := strconv.ParseFloat(strings.TrimSpace(fv), 64)
	if err != nil || q <= 0 {
		writeError(ctx, fasthttp.StatusBadRequest, "invalid face_value")
		return
	}
	// The admin cap is USD; the requested face value is in the family's
	// LOCAL currency. Convert before checking so "150.000 IDR" (~$10)
	// passes a $20 cap and only genuinely-over-cap prices are refused.
	meta := h.lookupFamilyMeta(ctx, brand, country)
	if !rules.DenominationVisible(catalog.ToUSD(q, meta.Currency)) {
		writeError(ctx, fasthttp.StatusBadRequest, "this denomination exceeds the current order limit")
		return
	}
	cacheKey := "cr:price:" + brand + ":" + country + ":" + fv + ":" + PaymentCoin
	if cached, ok := h.cache.get(cacheKey); ok {
		ctx.Response.Header.Set("Cache-Control", "public, max-age=60")
		writeJSON(ctx, fasthttp.StatusOK, cached)
		return
	}
	snapKey := "snap:" + cacheKey
	v, err := h.flights.Do("price:"+brand+":"+country+":"+fv, func() (interface{}, error) {
		if cached, ok := h.cache.get(cacheKey); ok {
			return cached, nil
		}
		out, ferr := h.CR.Price(h.supplierContext(ctx), brand, country, q, PaymentCoin)
		if ferr != nil {
			return nil, ferr
		}
		if out == nil || strings.TrimSpace(out.CoinAmount) == "" {
			return nil, errEmptyCatalog // never cache a useless quote
		}
		h.cache.setTTL(cacheKey, out, ttlPrice)
		h.saveCatalogSnapshot(snapKey, out)
		return out, nil
	})
	if err == nil {
		ctx.Response.Header.Set("Cache-Control", "public, max-age=60")
		writeJSON(ctx, fasthttp.StatusOK, v)
		return
	}
	if !errors.Is(err, errEmptyCatalog) {
		log.Printf("cr price(%s/%s/%s): %v", brand, country, fv, err)
	}
	if stale, age, ok := h.cache.peekStale(cacheKey); ok && age < 48*time.Hour {
		log.Printf("cr price(%s/%s/%s): serving stale price (age %s) after supplier error", brand, country, fv, age.Round(time.Second))
		ctx.Response.Header.Set("Cache-Control", "no-store")
		writeJSON(ctx, fasthttp.StatusOK, stale)
		return
	}
	if snap, ok := h.loadCatalogSnapshot(snapKey, decodePrice); ok {
		log.Printf("cr price(%s/%s/%s): serving disk snapshot after supplier error", brand, country, fv)
		h.cache.setTTL(cacheKey, snap, time.Minute)
		ctx.Response.Header.Set("Cache-Control", "no-store")
		writeJSON(ctx, fasthttp.StatusOK, snap)
		return
	}
	h.supplierError(ctx, err, "could not price this product")
}

/* ------------------------------ payment vias ----------------------------- */

// PaymentVias lists supported coins/networks. NOTE: the shop no longer
// offers a coin picker — this endpoint stays for transparency/debugging
// and always returns the supplier list unfiltered. Fresh 12h, disk-backed.
func (h *Handlers) PaymentVias(ctx *fasthttp.RequestCtx) {
	const cacheKey = "cr:payment_vias"
	if cached, ok := h.cache.get(cacheKey); ok {
		ctx.Response.Header.Set("Cache-Control", "public, max-age=600")
		writeJSON(ctx, fasthttp.StatusOK, cached)
		return
	}
	snapKey := "snap:" + cacheKey
	v, err := h.flights.Do("vias", func() (interface{}, error) {
		if cached, ok := h.cache.get(cacheKey); ok {
			return cached, nil
		}
		out, ferr := h.CR.PaymentVias(h.supplierContext(ctx))
		if ferr != nil {
			return nil, ferr
		}
		if len(out) == 0 {
			return nil, errEmptyCatalog
		}
		h.cache.setTTL(cacheKey, out, ttlVias)
		h.saveCatalogSnapshot(snapKey, out)
		return out, nil
	})
	if err == nil {
		ctx.Response.Header.Set("Cache-Control", "public, max-age=600")
		writeJSON(ctx, fasthttp.StatusOK, v)
		return
	}
	if stale, age, ok := h.cache.peekStale(cacheKey); ok && age < catalogStaleCap {
		if out, ok := stale.([]cryptorefills.PaymentVia); ok && len(out) > 0 {
			log.Printf("cr payment_vias: serving stale (age %s) after supplier error", age.Round(time.Second))
			ctx.Response.Header.Set("Cache-Control", "no-store")
			writeJSON(ctx, fasthttp.StatusOK, out)
			return
		}
	}
	if snap, ok := h.loadCatalogSnapshot(snapKey, decodeVias); ok {
		if out, ok := snap.([]cryptorefills.PaymentVia); ok && len(out) > 0 {
			log.Printf("cr payment_vias: serving disk snapshot after supplier error")
			h.cache.setTTL(cacheKey, out, time.Hour)
			ctx.Response.Header.Set("Cache-Control", "no-store")
			writeJSON(ctx, fasthttp.StatusOK, out)
			return
		}
	}
	h.supplierError(ctx, err, "could not load payment methods")
}

/* --------------------------------- search -------------------------------- */

// SearchProducts does a case-insensitive brand-name search over the cached
// country directory, then applies the admin rules. The supplier has no
// free-text search endpoint. It inherits listBrandsCore's cache: a search
// storm costs zero supplier calls.
func (h *Handlers) SearchProducts(ctx *fasthttp.RequestCtx) {
	q := strings.ToLower(strings.TrimSpace(string(ctx.QueryArgs().Peek("q"))))
	country := strings.ToUpper(string(ctx.QueryArgs().Peek("country")))
	if len(q) == 0 || len(q) > 100 {
		writeError(ctx, fasthttp.StatusBadRequest, "q is required (1-100 chars)")
		return
	}
	if len(country) > 2 {
		writeError(ctx, fasthttp.StatusBadRequest, "country must be a 2-letter code")
		return
	}
	full, _, ok := h.publicBrands(ctx)
	if !ok {
		return
	}
	ctx.Response.Header.Set("Cache-Control", "public, max-age=60")
	rules := h.rulesForRequest()
	type row struct {
		Family   string `json:"family"`
		Kind     string `json:"kind"`
		Category string `json:"category"`
		Country  string `json:"country_code,omitempty"`
	}
	results := []row{}
	for _, c := range full.Categories {
		for _, b := range c.Brands {
			if strings.Contains(strings.ToLower(b.Family), q) {
				results = append(results, row{Family: b.Family, Kind: c.Kind, Category: c.Category, Country: b.CountryCode})
			}
		}
		if len(results) >= 50 {
			break
		}
	}
	// Apply rules on the flattened rows.
	filtered := make([]row, 0, len(results))
	for _, r := range results {
		if rules.IsFamilyHidden(r.Family) || rules.IsKindBanned(r.Kind) ||
			rules.IsCategoryBanned(r.Category) || !rules.CountryVisible(r.Country) {
			continue
		}
		filtered = append(filtered, r)
	}
	writeJSON(ctx, fasthttp.StatusOK, filtered)
}

/* ------------------------------- check phone ----------------------------- */

// CheckPhone is kept for frontend compatibility. CryptoRefills has no
// operator-lookup endpoint; the supplier validates the number at order
// time, so this returns the country's top-up brands (the realistic set of
// possible operators) plus the phone echo.
func (h *Handlers) CheckPhone(ctx *fasthttp.RequestCtx) {
	rawPhone := string(ctx.QueryArgs().Peek("phone_number"))
	if rawPhone == "" {
		rawPhone = string(ctx.QueryArgs().Peek("phone"))
	}
	if rawPhone == "" {
		writeError(ctx, fasthttp.StatusBadRequest, "phone_number is required (E.164, e.g. +905551234567)")
		return
	}
	country := strings.ToUpper(string(ctx.QueryArgs().Peek("country")))
	// Normalize (separators, 00 access prefix, national 0-prefix format via
	// the product's country) and enforce strict E.164. The response returns
	// the normalized value so clients always send exactly what the supplier
	// requires.
	phone, err := phonepkg.Normalize(rawPhone, country)
	if err != nil {
		writeError(ctx, fasthttp.StatusBadRequest, err.Error())
		return
	}
	operators := []map[string]interface{}{}
	full, _, ok := h.publicBrands(ctx)
	if ok && len(country) == 2 {
		rules := h.rulesForRequest()
		filtered := catalog.FilterBrands(&rules, full)
		for _, c := range filtered.Categories {
			if c.Kind != "mobile_recharge" || c.Category == "e-sim" {
				continue
			}
			for _, b := range c.Brands {
				operators = append(operators, map[string]interface{}{"id": b.Family, "name": b.Family})
			}
		}
	}
	writeJSON(ctx, fasthttp.StatusOK, map[string]interface{}{
		"phone_number": phone,
		"operators":    operators,
		"supported":    len(operators) > 0,
		"note":         "operator support is validated by the supplier at order time",
	})
}
