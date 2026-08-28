// Package cryptorefills is the supplier integration for cryptorefills.com:
// stablecoin checkout (USDT/USDC/... on Solana, ETH, Polygon, Lightning,
// ...) for gift cards, mobile top-ups and eSIMs.
//
// The partner never holds customer funds: CryptoRefills generates a unique
// wallet address per order, the customer pays it directly from their own
// wallet, and Cryptorefills delivers the product (Merchant of Record).
// Order fulfillment is observed by polling GET /v5/orders/{id} (plus an
// optional webhook when configured) — there is no customer balance, no
// refund signer and no treasury on this server.
package cryptorefills

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Client talks to the CryptoRefills API through the shared fair queue.
//
// On top of the queue, every idempotent GET is micro-cached and
// single-flighted INSIDE the client: concurrent identical requests (tracker
// tick + webhook re-fetch + user refresh on the same order, or five tabs
// opening the same brand page) collapse into ONE upstream call. TTLs are
// deliberately short so state stays fresh; the long-lived catalog caching
// lives in the handlers layer on top of this.
type Client struct {
	baseURL   string
	partnerID string
	appVer    string
	userAgent string
	http      *http.Client
	queue     *Scheduler
	gets      getCache
}

// getCache is a tiny TTL cache + singleflight for successful GET responses
// only. Errors are never cached (a transient 429/timeout must not poison
// the key); a follower whose leader failed re-fetches with its own context.
type getCache struct {
	mu      sync.Mutex
	entries map[string]getCacheEntry
	flights map[string]*getFlight
}

type getCacheEntry struct {
	value   interface{}
	expires time.Time
}

type getFlight struct {
	wg  sync.WaitGroup
	val interface{}
	err error
}

const getMaxEntries = 1024

func (g *getCache) do(ttl time.Duration, key string, fetch func() (interface{}, error)) (interface{}, error) {
	return g.doNeg(ttl, 0, nil, key, fetch)
}

// doNeg is do with optional NEGATIVE-result semantics: when the fetch
// succeeds but isEmpty(value) is true, the entry is cached for negTTL
// instead of ttl. This keeps a transient empty supplier payload (unknown
// family, glitched brand list) from pinning an empty catalog for the full
// fresh TTL while still shielding the supplier from hammering on a key
// that genuinely does not exist.
func (g *getCache) doNeg(ttl, negTTL time.Duration, isEmpty func(interface{}) bool, key string, fetch func() (interface{}, error)) (interface{}, error) {
	g.mu.Lock()
	if g.entries == nil {
		g.entries = make(map[string]getCacheEntry)
		g.flights = make(map[string]*getFlight)
	}
	now := time.Now()
	if e, ok := g.entries[key]; ok && now.Before(e.expires) {
		g.mu.Unlock()
		return e.value, nil
	}
	if f, ok := g.flights[key]; ok {
		g.mu.Unlock()
		f.wg.Wait()
		if f.err == nil {
			return f.val, nil
		}
		// Leader failed (context canceled, transient upstream error...):
		// retry as a fresh leader so one bad context cannot fail callers
		// that are still alive. Recursion depth is bounded by the number
		// of concurrent followers; each level collapses the herd further.
		return g.doNeg(ttl, negTTL, isEmpty, key, fetch)
	}
	f := &getFlight{}
	f.wg.Add(1)
	g.flights[key] = f
	g.mu.Unlock()

	val, err := fetch()

	g.mu.Lock()
	delete(g.flights, key)
	if err == nil {
		eTTL := ttl
		if isEmpty != nil && isEmpty(val) {
			eTTL = negTTL
		}
		// Cheap size guard: drop expired entries, then the soonest-to-
		// expire one, before inserting. Catalog/order keys are bounded in
		// practice; this keeps a pathological key space from growing.
		if len(g.entries) >= getMaxEntries {
			for k, e := range g.entries {
				if now.After(e.expires) {
					delete(g.entries, k)
				}
			}
		}
		if len(g.entries) >= getMaxEntries {
			var oldestKey string
			var oldest time.Time
			for k, e := range g.entries {
				if oldest.IsZero() || e.expires.Before(oldest) {
					oldest, oldestKey = e.expires, k
				}
			}
			delete(g.entries, oldestKey)
		}
		g.entries[key] = getCacheEntry{value: val, expires: time.Now().Add(eTTL)}
	}
	g.mu.Unlock()

	f.val, f.err = val, err
	f.wg.Done()
	return val, err
}

// NewClient builds a queued client. partnerID is the X-Cr-Application
// value from the account page; appVer/userAgent are required headers.
func NewClient(baseURL, partnerID, appVer, userAgent string, cfg QueueConfig) *Client {
	if appVer == "" {
		appVer = "1.0"
	}
	if userAgent == "" {
		userAgent = "nimshop/" + appVer
	}
	// TRANSPORT TUNING: http.DefaultTransport keeps only TWO idle conns
	// per host — every concurrent supplier call beyond that paid a fresh
	// TCP+TLS handshake (~100-300ms). A shop-shaped pool with warm
	// keep-alive connections makes catalog/quote/validation calls reuse
	// one handshake for the process lifetime.
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   64,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   5 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		ForceAttemptHTTP2:     true,
	}
	return &Client{
		baseURL:   strings.TrimRight(baseURL, "/"),
		partnerID: partnerID,
		appVer:    appVer,
		userAgent: userAgent,
		http:      &http.Client{Timeout: 35 * time.Second, Transport: transport},
		queue:     NewScheduler(cfg),
	}
}

func (c *Client) QueueStats() QueueStats {
	if c.queue == nil {
		return QueueStats{}
	}
	return c.queue.Stats()
}

// OrderPollWait reports how long the NEXT GET /v5/orders/{id} would wait
// for an admission slot right now (0 = immediately). The fulfillment
// tracker uses it to skip entire passes while the endpoint is cooling down
// (supplier 429 / remote budget) instead of burning one fast rejection per
// order per tick — the exact pattern that used to flood the log with
// "budget exhausted" lines every 4 seconds.
func (c *Client) OrderPollWait() time.Duration {
	if c.queue == nil {
		return 0
	}
	return c.queue.endpointWait(policyFor(http.MethodGet, "/v5/orders/example"))
}

func (c *Client) Close() {
	if c.queue != nil {
		c.queue.Close()
	}
}

// RateLimitError is returned after the request has been admitted by the
// queue but the supplier still answered 429. The scheduler is throttled
// before this is returned.
type RateLimitError struct {
	ResetAt time.Time
	Message string
}

func (e *RateLimitError) Error() string {
	if e == nil {
		return "cryptorefills rate limited"
	}
	return fmt.Sprintf("cryptorefills rate limited (429), retry after %s: %s", e.ResetAt.UTC().Format(time.RFC3339), e.Message)
}

// SupplierError is a non-429 upstream HTTP failure. Detail carries the
// supplier's human-readable reason; Code a stable machine code when
// present.
type SupplierError struct {
	Status int
	Code   string
	Detail string
	Method string
	Path   string
}

func (e *SupplierError) Error() string {
	if e == nil {
		return "cryptorefills supplier error"
	}
	code := e.Code
	if code == "" {
		code = "-"
	}
	detail := e.Detail
	if detail == "" {
		detail = "no detail"
	}
	return fmt.Sprintf("cryptorefills error (%d %s %s): %s [%s]", e.Status, e.Method, e.Path, detail, code)
}

// Problem is one entry of the supplier's validations problem list.
type Problem struct {
	Code    string `json:"problem"`
	Details any    `json:"moreDetails,omitempty"`
}

// ProblemError is returned by ValidateOrder: the order could not be created
// and the supplier told us exactly why (limits, KYC, stock, beneficiary...).
type ProblemError struct {
	Problems []Problem
}

func (e *ProblemError) Error() string {
	if e == nil {
		return "cryptorefills validation failed"
	}
	if len(e.Problems) == 0 {
		return "cryptorefills validation failed"
	}
	codes := make([]string, 0, len(e.Problems))
	for _, p := range e.Problems {
		codes = append(codes, p.Code)
	}
	return "cryptorefills validation failed: " + strings.Join(codes, ", ")
}

func (c *Client) do(ctx context.Context, method, path string, body, out interface{}) error {
	var payload []byte
	var err error
	if body != nil {
		payload, err = json.Marshal(body)
		if err != nil {
			return err
		}
	}
	policy := policyFor(method, path)
	call := func() error {
		var reader io.Reader
		if payload != nil {
			reader = bytes.NewReader(payload)
		}
		req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
		if err != nil {
			return err
		}
		// Required headers for every request (partner auth + attribution).
		req.Header.Set("X-Cr-Application", c.partnerID)
		req.Header.Set("X-Cr-Version", c.appVer)
		req.Header.Set("User-Agent", c.userAgent)
		req.Header.Set("Content-Type", "application/json")
		if ip := endUserIPFromContext(ctx); ip != "" {
			req.Header.Set("X-Forwarded-For", ip)
		}

		resp, err := c.http.Do(req)
		if err != nil {
			return fmt.Errorf("cryptorefills %s %s: %w", method, path, err)
		}
		defer resp.Body.Close()
		if c.queue != nil {
			c.queue.Observe(policy, resp.Header)
		}

		raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		if err != nil {
			return err
		}

		if resp.StatusCode == http.StatusTooManyRequests {
			reset := parseReset(resp.Header)
			if reset.IsZero() || !reset.After(time.Now()) {
				reset = time.Now().Add(policy.window)
			}
			if c.queue != nil {
				c.queue.Throttle(policy, reset)
			}
			return &RateLimitError{ResetAt: reset, Message: strings.TrimSpace(string(raw))}
		}
		if resp.StatusCode >= 400 {
			// Observed shapes: {"status","detail","moreDetails"} for
			// suspensions and a generic {"message"} body otherwise.
			var e struct {
				Status      string `json:"status"`
				Detail      string `json:"detail"`
				Message     string `json:"message"`
				ErrorCode   string `json:"error_code"`
				MoreDetails any    `json:"moreDetails"`
			}
			_ = json.Unmarshal(raw, &e)
			detail := e.Detail
			if detail == "" {
				detail = e.Message
			}
			code := e.Status
			if code == "" {
				code = e.ErrorCode
			}
			if len(raw) == 0 {
				detail = ""
			} else if detail == "" {
				detail = strings.TrimSpace(string(raw))
				if len(detail) > 200 {
					detail = detail[:200]
				}
			}
			return &SupplierError{Status: resp.StatusCode, Code: code, Detail: detail, Method: method, Path: path}
		}

		if out != nil && len(raw) > 0 {
			if err := json.Unmarshal(raw, out); err != nil {
				return fmt.Errorf("cryptorefills %s %s: decode: %w", method, path, err)
			}
		}
		return nil
	}
	if c.queue == nil {
		return call()
	}
	return c.queue.Submit(ctx, call, policy)
}

/* ------------------------------ payment vias ------------------------------ */

// Network is one chain option for a coin (e.g. "Solana", "Polygon (Matic)").
type Network struct {
	Name      string `json:"name"`
	ChainID   int    `json:"chain_id,omitempty"`
	BaseToken string `json:"base_token,omitempty"`
	Threshold string `json:"threshold,omitempty"`
	LogoURL   string `json:"logo_url,omitempty"`
	Suspended bool   `json:"is_suspended,omitempty"`
}

// CurrencyVia is one coin (e.g. USDT) with its available networks.
type CurrencyVia struct {
	Name      string    `json:"name"`
	Threshold string    `json:"threshold,omitempty"`
	Networks  []Network `json:"networks"`
	Suspended bool      `json:"is_suspended,omitempty"`
	LogoURL   string    `json:"logo_url,omitempty"`
}

// PaymentVia is a payment channel kind: USER_WALLET (customer pays from
// their own wallet — the only kind this shop uses) or supplier-hosted.
type PaymentVia struct {
	Name       string        `json:"name"`
	Currencies []CurrencyVia `json:"currencies"`
	Available  bool          `json:"available"`
	LogoURL    string        `json:"logo_url,omitempty"`
}

// PaymentVias lists supported coins and networks. Cached client-side for
// 10 minutes (single-flighted): payment rails change at most daily.
func (c *Client) PaymentVias(ctx context.Context) ([]PaymentVia, error) {
	v, err := c.gets.do(10*time.Minute, "GET /v3/payment_vias", func() (interface{}, error) {
		var out []PaymentVia
		if err := c.do(ctx, http.MethodGet, "/v3/payment_vias", nil, &out); err != nil {
			return nil, err
		}
		return out, nil
	})
	if err != nil {
		return nil, err
	}
	return v.([]PaymentVia), nil
}

/* --------------------------------- brands --------------------------------- */

// Brand is one purchasable family (Airbnb, Steam, t-mobile, eSIM...).
type Brand struct {
	Family      string `json:"family"`
	BrandID     string `json:"brand_id,omitempty"`
	LogoURL     string `json:"logo_url,omitempty"`
	LogoBaseURL string `json:"logo_base_url,omitempty"`
	BgColor     string `json:"bg_color,omitempty"`
	Min         string `json:"min,omitempty"`
	Max         string `json:"max,omitempty"`
	Category    string `json:"category,omitempty"`
	Kind        string `json:"kind,omitempty"`
	OutOfStock  bool   `json:"is_out_of_stock"`
	CountryCode string `json:"country_code,omitempty"`
	ProductType string `json:"product_type,omitempty"`
}

// BrandCategory groups brands by kind/category for a country.
type BrandCategory struct {
	Kind     string  `json:"kind"`
	Category string  `json:"category"`
	Brands   []Brand `json:"brands"`
}

// BrandsResponse is the /v2/brands listing for a country.
type BrandsResponse struct {
	CountryCode string          `json:"country_code"`
	Categories  []BrandCategory `json:"categories"`
}

// Brands lists all brands available for a country. Cached client-side for
// 3 minutes (single-flighted) on top of the handlers' long catalog cache;
// an EMPTY category list is cached only 60s so a transient empty glitch
// recovers quickly instead of pinning a blank storefront.
// countryCode is optional per docs, but some partner accounts return 400 for empty/global.
// When empty, we default to TR (user's main market) to avoid 400, with fallback to US if TR fails.
func (c *Client) Brands(ctx context.Context, countryCode string) (*BrandsResponse, error) {
	cc := strings.TrimSpace(countryCode)
	if cc == "" {
		cc = "TR"
	}
	v, err := c.gets.doNeg(3*time.Minute, 60*time.Second,
		func(v interface{}) bool {
			r, ok := v.(*BrandsResponse)
			return ok && (r == nil || len(r.Categories) == 0)
		},
		"GET /v2/brands?"+cc, func() (interface{}, error) {
			var out BrandsResponse
			path := "/v2/brands?country_code=" + urlQueryEscape(cc)
			ferr := c.do(ctx, http.MethodGet, path, nil, &out)
			if ferr != nil && strings.TrimSpace(countryCode) == "" {
				// Global requested but TR failed (maybe 400), try US as fallback for global catalog
				var out2 BrandsResponse
				if ferr2 := c.do(ctx, http.MethodGet, "/v2/brands?country_code=US", nil, &out2); ferr2 == nil {
					return &out2, nil
				}
			}
			if ferr != nil {
				return nil, ferr
			}
			return &out, nil
		})
	if err != nil {
		return nil, err
	}
	return v.(*BrandsResponse), nil
}

/* -------------------------------- products -------------------------------- */

// ValueRange describes a dynamic (open-value) product range.
type ValueRange struct {
	Min      float64 `json:"min"`
	Max      float64 `json:"max"`
	Currency string  `json:"currency"`
	StepSize float64 `json:"step_size"`
	Default  string  `json:"default,omitempty"`
}

// Product is one purchasable SKU under a family (fixed or range).
// FIXED: includes all real fields from CryptoRefills API (denomination, coin_amount, face_value etc.)
type Product struct {
	ProductID             string          `json:"product_id"`
	IsDynamic             bool            `json:"is_dynamic"`
	Range                 *ValueRange     `json:"range,omitempty"`
	Coin                  string          `json:"coin,omitempty"`
	CoinAmount            string          `json:"coin_amount,omitempty"`
	OriginalCoinAmount    string          `json:"original_coin_amount,omitempty"`
	PaymentMethod         string          `json:"payment_method,omitempty"`
	DeliveryType          string          `json:"delivery_type,omitempty"`
	Points                string          `json:"points,omitempty"`
	ProductType           string          `json:"product_type,omitempty"`
	Denomination          string          `json:"denomination,omitempty"`
	LocalizedDenomination string          `json:"localized_denomination,omitempty"`
	Amount                float64         `json:"amount,omitempty"`
	CurrencyCode          string          `json:"currency_code,omitempty"`
	FaceValue             json.RawMessage `json:"face_value,omitempty"`
}

// RichDescription is the supplier's per-brand rich content (v5 API,
// lang-dependent): HTML blocks for description / how-to-redeem / terms,
// plus region and branding metadata. All fields optional — many brands
// carry only a subset (or none).
type RichDescription struct {
	Markup            string `json:"markup,omitempty"`
	Description       string `json:"description,omitempty"`
	HowToRedeem       string `json:"how_to_redeem,omitempty"`
	TermAndConditions string `json:"term_and_conditions,omitempty"`
	CountryCode       string `json:"country_code,omitempty"`
	Locale            string `json:"locale,omitempty"`
	Note              string `json:"note,omitempty"`
	RedeemGeo         string `json:"redeem_geo,omitempty"`
	BrandTagline      string `json:"brand_tagline,omitempty"`
	BrandUseCase      string `json:"brand_use_case,omitempty"`
	BrandURL          string `json:"brand_url,omitempty"`
	BrandAltNames     string `json:"brand_alt_names,omitempty"`
	SizeInBytes       int    `json:"sizeInBytes,omitempty"`
}

// Family is a brand's catalog entry for a country + coin: metadata plus
// the purchasable products (fixed denominations or ranges).
type Family struct {
	CountryCode    string    `json:"country_code"`
	Category       string    `json:"category"`
	AdditionalCats []string  `json:"additional_categories"`
	Kind           string    `json:"kind"`
	DefaultDenom   string    `json:"default_denomination"`
	Products       []Product `json:"products"`
	Family         string    `json:"family"`
	BrandID        string    `json:"brand_id"`
	Brand          string    `json:"brand"`
	OutOfStock     bool      `json:"is_out_of_stock"`
	LogoURL        string    `json:"logo_url,omitempty"`
	LogoBaseURL    string    `json:"logo_base_url,omitempty"`
	// BgColor is the brand's OWN background color — the product hero and
	// order thumbs render the logo on it (the brand's original background,
	// never a hardcoded white).
	BgColor string `json:"bg_color,omitempty"`
	ProductTc      string    `json:"product_tc,omitempty"`
	// RichDescription: supplier's own "How to redeem" / "Terms and
	// conditions" / region content (HTML) — shown verbatim (sanitized) on
	// the product page, same as CryptoRefills' own storefront.
	RichDescription *RichDescription `json:"rich_description,omitempty"`
}

// ProductsByCountry lists a family's products for a country. familyName
// is required by the API ("" returns nothing). Cached client-side for
// 3 minutes (single-flighted); EMPTY results are cached briefly (60s) so a
// nonexistent family cannot be hammered, while real catalog data rides the
// handlers' longer cache.
func (c *Client) ProductsByCountry(ctx context.Context, countryCode, familyName, coin string) ([]Family, error) {
	q := "/v5/products/country/" + urlQueryEscape(countryCode) + "?family_name=" + urlQueryEscape(familyName) + "&lang=en"
	if coin != "" {
		q += "&coin=" + urlQueryEscape(coin)
	}
	v, err := c.gets.doNeg(3*time.Minute, 60*time.Second,
		func(v interface{}) bool {
			fs, ok := v.([]Family)
			return ok && len(fs) == 0
		},
		"GET "+q, func() (interface{}, error) {
			var out []Family
			if ferr := c.do(ctx, http.MethodGet, q, nil, &out); ferr != nil {
				return nil, ferr
			}
			return out, nil
		})
	if err != nil {
		return nil, err
	}
	return v.([]Family), nil
}

// PriceQuote is the /v4/products/price conversion for one denomination.
type PriceQuote struct {
	ProductID          string      `json:"product_id"`
	IsDynamic          bool        `json:"is_dynamic"`
	Range              *ValueRange `json:"range,omitempty"`
	CoinAmount         string      `json:"coin_amount"`
	Coin               string      `json:"coin"`
	OriginalCoinAmount string      `json:"original_coin_amount,omitempty"`
	PaymentMethod      string      `json:"payment_method,omitempty"`
	DeliveryType       string      `json:"delivery_type,omitempty"`
	Points             string      `json:"points,omitempty"`
}

// Price converts a face value to the exact coin amount (supplier-priced,
// margin included). Cached client-side for 45 seconds (single-flighted):
// the same face value typed twice in a row costs one upstream call. A
// degenerate quote (empty coin_amount) is cached only 5s.
func (c *Client) Price(ctx context.Context, brandName, countryCode string, faceValue float64, coin string) (*PriceQuote, error) {
	q := fmt.Sprintf("/v4/products/price?brand_name=%s&country_code=%s&face_value=%s&coin=%s",
		urlQueryEscape(brandName), urlQueryEscape(countryCode), fmt.Sprintf("%.2f", faceValue), urlQueryEscape(coin))
	v, err := c.gets.doNeg(45*time.Second, 5*time.Second,
		func(v interface{}) bool {
			p, ok := v.(*PriceQuote)
			return ok && (p == nil || strings.TrimSpace(p.CoinAmount) == "")
		},
		"GET "+q, func() (interface{}, error) {
			var out PriceQuote
			if ferr := c.do(ctx, http.MethodGet, q, nil, &out); ferr != nil {
				return nil, ferr
			}
			return &out, nil
		})
	if err != nil {
		return nil, err
	}
	return v.(*PriceQuote), nil
}

/* --------------------------------- orders --------------------------------- */

// Delivery is one line of an order: the product Cryptorefills will deliver
// to the beneficiary (email for gift cards/eSIMs, E.164 phone for topups).
// FIXED: flat fields at delivery level for request (as per official docs), plus nested deliverable for response parsing
type Delivery struct {
	ID                 string   `json:"id,omitempty"`
	DeliveryState      string   `json:"delivery_state,omitempty"`
	Kind               string   `json:"kind,omitempty"`
	ProductID          string   `json:"product_id,omitempty"`
	FamilyName         string   `json:"family_name,omitempty"`
	BrandName          string   `json:"brand_name,omitempty"`
	CountryCode        string   `json:"country_code,omitempty"`
	Denomination       string   `json:"denomination,omitempty"`
	ProductValue       *float64 `json:"product_value,omitempty"`
	BeneficiaryAccount string   `json:"beneficiary_account,omitempty"`
	ForcedProvider     string   `json:"forcedProvider,omitempty"`
	Deliverable        `json:"deliverable,omitempty"`
}

// Deliverable carries the delivery target and, once Done, the redemption
// payload (code, PIN, barcode, QR, instructions).
type Deliverable struct {
	Denomination       string   `json:"denomination,omitempty"`
	LocalizedDenom     string   `json:"localized_denomination,omitempty"`
	Family             string   `json:"family,omitempty"`
	BrandName          string   `json:"brand_name,omitempty"`
	CountryCode        string   `json:"country_code,omitempty"`
	BeneficiaryAccount string   `json:"beneficiary_account,omitempty"`
	DeliveryType       string   `json:"delivery_type,omitempty"`
	ProductValue       *float64 `json:"product_value,omitempty"`
	CurrencyCode       string   `json:"currency_code,omitempty"`
	CoinAmount         string   `json:"coin_amount,omitempty"`
	OriginalCoinAmount string   `json:"original_coin_amount,omitempty"`
	OriginalPrice      string   `json:"original_price,omitempty"`
	PinCode            string   `json:"pin_code,omitempty"`
	PinSerial          string   `json:"pin_serial,omitempty"`
	SecurityCode       string   `json:"security_code,omitempty"`
	OperatorRef        string   `json:"operator_reference,omitempty"`
	BarcodeImageURL    string   `json:"barcode_image_url,omitempty"`
	QRImageURL         string   `json:"qr_image_url,omitempty"`
	RedeemInstructions string   `json:"redeem_instructions,omitempty"`
	HowToRedeem        string   `json:"how_to_redeem,omitempty"`
	ErrorDescription   string   `json:"error_description,omitempty"`
	FailureReason      string   `json:"failure_reason,omitempty"`
}

// OrderPayment selects coin + network + wallet kind for an order.
type OrderPayment struct {
	Type       string `json:"type"`        // "via"
	PaymentVia string `json:"payment_via"` // "USER_WALLET"
	Coin       string `json:"coin"`
	Network    string `json:"network,omitempty"`
}

// CreateOrderRequest is the POST /v5/orders (and /v5/orders/validations)
// body.
type CreateOrderRequest struct {
	Deliveries []Delivery   `json:"deliveries"`
	Payment    OrderPayment `json:"payment"`
	// Email mirrors the docs' top-level "email" field (the end user the
	// supplier delivers to / notifies). The per-delivery
	// beneficiary_account is the delivery target itself.
	Email       string       `json:"email,omitempty"`
	User        *OrderUser   `json:"user,omitempty"`
	Lang        string       `json:"lang"`
	Acquisition *Acquisition `json:"acquisition,omitempty"`
}

// OrderUser is the end customer record. Email is mandatory: Cryptorefills
// delivers the product to it.
type OrderUser struct {
	Email                 string `json:"email"`
	HasAcceptedNewsletter bool   `json:"has_accepted_newsletter,omitempty"`
}

// Acquisition is optional marketing attribution.
type Acquisition struct {
	UTMSource string `json:"utm_source,omitempty"`
}

// UnmarshalJSON accepts both the real API names (order_id/order_state)
// and legacy aliases (id/status) so a supplier-side rename degrades to a
// loud parse check in tests instead of empty ids in production.
func (o *Order) UnmarshalJSON(b []byte) error {
	type orderAlias Order
	var raw orderAlias
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	*o = Order(raw)
	if o.ID == "" || o.Status == "" {
		var legacy struct {
			LegacyID     string `json:"id"`
			LegacyStatus string `json:"status"`
		}
		if err := json.Unmarshal(b, &legacy); err == nil {
			if o.ID == "" {
				o.ID = legacy.LegacyID
			}
			if o.Status == "" {
				o.Status = legacy.LegacyStatus
			}
		}
	}
	return nil
}

// epochOrString decodes a supplier timestamp that the real API normally
// sends as a JSON string ("1787679708") but SOME responses emit as a bare
// JSON number (1787679708). A plain `string` field dies on the numeric
// variant with "cannot unmarshal number into ... of type string" and kills
// order creation, so both shapes must decode. The value is kept as the
// string form; ParseEpochOrRFC3339 interprets it later.
type epochOrString string

func (e *epochOrString) UnmarshalJSON(b []byte) error {
	if len(b) == 0 || string(b) == "null" {
		return nil
	}
	if b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		*e = epochOrString(s)
		return nil
	}
	if b[0] == '{' || b[0] == '[' {
		return fmt.Errorf("epochOrString: unexpected object/array in timestamp field: %.40s", string(b))
	}
	// JSON number: keep integer seconds; drop any sub-second fraction or
	// exponent form so ParseEpochOrRFC3339 sees a plain epoch.
	s := string(b)
	if strings.ContainsAny(s, ".eE") {
		if f, err := strconv.ParseFloat(s, 64); err == nil {
			s = strconv.FormatInt(int64(f), 10)
		}
	}
	*e = epochOrString(s)
	return nil
}

// flexFloat decodes a JSON field that the real API sends as a number
// (1234.56) but occasionally as a string ("1234.56"). A plain float64
// dies on the string variant with "cannot unmarshal string into Go
// struct field of type float64", so both shapes must decode.
type flexFloat float64

func (f *flexFloat) UnmarshalJSON(b []byte) error {
	if len(b) == 0 || string(b) == "null" {
		return nil
	}
	if b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
		if err != nil {
			return fmt.Errorf("flexFloat: cannot parse %q: %w", s, err)
		}
		*f = flexFloat(v)
		return nil
	}
	var v float64
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	*f = flexFloat(v)
	return nil
}

// Order is a supplier order in any state. WalletAddress/CoinAmount are
// present from creation; deliveries carry redemption data once Done.
//
// Field names match the REAL api.cryptorefills.com responses (verified
// against production 2026-08): `order_id`/`order_state` (NOT id/status),
// unix-epoch created_at, no payment_expires_at, plus qr_text/qr_url.
// For BTC + Lightning the wallet_address IS a BOLT11 invoice (lnbc…).
type Order struct {
	ID                 string    `json:"order_id"`
	ReferenceID        string    `json:"reference_id,omitempty"`
	Status             string    `json:"order_state"`
	PaymentState       string    `json:"payment_state,omitempty"`
	Coin               string    `json:"coin,omitempty"`
	CoinAmount         string    `json:"coin_amount,omitempty"`
	OriginalCoinAmount string    `json:"original_coin_amount,omitempty"`
	SentCoinAmount     string    `json:"sent_coin_amount,omitempty"`
	ReceivedAmount     string    `json:"received_amount,omitempty"`
	WalletAddress      string    `json:"wallet_address,omitempty"`
	Network            string    `json:"network,omitempty"`
	PaymentMethod      string    `json:"payment_method,omitempty"`
	PaymentMethodProto string    `json:"payment_method_protocol,omitempty"`
	PaymentValue       flexFloat `json:"payment_value,omitempty"`
	PaymentID          string    `json:"payment_id,omitempty"`
	// QRText is the supplier's ready-to-render payment URI
	// ("lightning:lnbc…") and QRURL a scannable QR image.
	QRText string `json:"qr_text,omitempty"`
	QRURL  string `json:"qr_url,omitempty"`
	// CreatedAt/UpdatedAt are unix-epoch seconds on the real API — usually
	// strings, occasionally bare JSON numbers (see epochOrString).
	CreatedAt        epochOrString `json:"created_at,omitempty"`
	UpdatedAt        epochOrString `json:"updated_at,omitempty"`
	Deliveries       []Delivery    `json:"deliveries,omitempty"`
	Refund           *OrderRefund  `json:"refund,omitempty"`
	RefundWalletAddr string        `json:"refund_wallet_address,omitempty"`
}

// CreatedTime parses the epoch-or-RFC3339 created_at field.
func (o *Order) CreatedTime() time.Time { return ParseEpochOrRFC3339(string(o.CreatedAt)) }

// UpdatedTime parses the epoch-or-RFC3339 updated_at field.
func (o *Order) UpdatedTime() time.Time { return ParseEpochOrRFC3339(string(o.UpdatedAt)) }

// IsLightning reports whether this order's payment rail is BTC Lightning
// (the only rail this shop exposes to customers).
func (o *Order) IsLightning() bool {
	return IsBOLT11(o.WalletAddress) ||
		strings.EqualFold(o.Network, "Lightning") ||
		strings.EqualFold(o.PaymentMethod, "BTC-LIGHTNING") ||
		strings.EqualFold(o.PaymentMethodProto, "LIGHTNING_LIKE")
}

// LightningInvoice returns the BOLT11 payment request when the order is on
// the Lightning rail (the wallet address itself), else "".
func (o *Order) LightningInvoice() string {
	if IsBOLT11(o.WalletAddress) {
		return o.WalletAddress
	}
	return ""
}

// ParseEpochOrRFC3339 accepts unix-epoch seconds (the real API) or an
// RFC3339 timestamp and returns the zero Time when nothing parseable.
func ParseEpochOrRFC3339(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		return time.Unix(n, 0).UTC()
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05", time.RFC3339Nano} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}

// IsBOLT11 reports whether s looks like a BOLT11 Lightning invoice
// (mainnet lnbc…, testnet lntb…, regtest lnbcrt…).
func IsBOLT11(s string) bool {
	s = strings.TrimSpace(strings.ToLower(s))
	if !strings.HasPrefix(s, "lnbc") && !strings.HasPrefix(s, "lntb") && !strings.HasPrefix(s, "lnbcrt") {
		return false
	}
	if len(s) < 30 {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}

// LightningURI turns a BOLT11 invoice into the lightning: URI that Nimiq
// Pay (and every Lightning wallet) opens directly. It returns "" for
// non-invoice addresses.
func LightningURI(invoice string) string {
	if !IsBOLT11(invoice) {
		return ""
	}
	return "lightning:" + strings.TrimSpace(invoice)
}

// OrderRefund is populated for Refunded orders.
type OrderRefund struct {
	Amount   string `json:"amount,omitempty"`
	Currency string `json:"currency,omitempty"`
	Reason   string `json:"reason,omitempty"`
}

// CreateOrder places an order and returns the wallet address + coin amount
// the customer must pay. The payment window is 30 minutes.
func (c *Client) CreateOrder(ctx context.Context, req *CreateOrderRequest) (*Order, error) {
	var out Order
	if err := c.do(ctx, http.MethodPost, "/v5/orders", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetOrder fetches an order's current state (polling fulfillment path).
// Cached client-side for 3 seconds and single-flighted: the tracker tick,
// a supplier webhook and a user "refresh" clicking on the same order all
// collapse into one upstream call instead of three. 3s cannot delay a
// state transition meaningfully (the payment window is 30 minutes) but it
// removes the duplicate-fetch burst that used to burn the polling budget.
func (c *Client) GetOrder(ctx context.Context, orderID string) (*Order, error) {
	v, err := c.gets.do(3*time.Second, "GET /v5/orders/"+orderID, func() (interface{}, error) {
		var out Order
		if ferr := c.do(ctx, http.MethodGet, "/v5/orders/"+urlQueryEscape(orderID), nil, &out); ferr != nil {
			return nil, ferr
		}
		return &out, nil
	})
	if err != nil {
		return nil, err
	}
	return v.(*Order), nil
}

// ValidateOrder is a dry run: same product/limit/compliance checks as
// order creation, without generating an order. The docs explicitly say to
// use it for testing and pre-flight checks.
func (c *Client) ValidateOrder(ctx context.Context, req *CreateOrderRequest) (*ValidationResult, error) {
	var out ValidationResult
	err := c.do(ctx, http.MethodPost, "/v5/orders/validations", req, &out)
	if err != nil {
		return nil, err
	}
	if len(out.ProblemList) > 0 {
		return &out, &ProblemError{Problems: out.ProblemList}
	}
	return &out, nil
}

// ValidationResult is the /v5/orders/validations response.
type ValidationResult struct {
	Coin               string     `json:"coin"`
	CoinAmount         string     `json:"coin_amount"`
	OriginalCoinAmount string     `json:"original_coin_amount,omitempty"`
	CouponCode         string     `json:"coupon_code,omitempty"`
	ProblemList        []Problem  `json:"problems"`
	Deliveries         []Delivery `json:"deliveries"`
	Error              string     `json:"error,omitempty"`
}

// Problems implements the problem list accessor.
func (v *ValidationResult) Problems() []Problem { return v.ProblemList }

var _ error = (*ProblemError)(nil)

// urlQueryEscape is a thin wrapper over the standard library so query
// building stays in one place.
func urlQueryEscape(s string) string {
	return url.QueryEscape(s)
}

var (
	// ErrNoWallet is returned when an order response lacks a wallet
	// address (should never happen for a successful creation).
	ErrNoWallet = errors.New("order response missing wallet address")
)
