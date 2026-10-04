// Package poolstake asks the operator's own Nimiq staking pool how much NIM
// an address currently has delegated there. It exists so shop.nimiqbase.com can pay a
// higher cashback rate to customers who secure the operator's validator.
//
// The pool exposes GET /api/stakers/{address} without authentication:
//
//	200 {"address": "...", "stake_luna": 123456, ...}  delegated
//	404 {"error": "no staker with this address in this pool"}  not delegated
//
// Two rules matter for the cashback path and are enforced here:
//
//  1. The lookup is a plain HTTP call, so it must happen OUTSIDE any Badger
//     transaction. Callers prefetch it before opening the fulfillment txn.
//  2. A pool outage must never turn into free money or into a lost
//     entitlement: errors are returned as errors and the caller decides
//     (the cashback path falls back to the base rate, never guesses).
package poolstake

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"nimiqshop/internal/nimiq"
)

// DefaultTimeout bounds a single pool request. The pool answers from its own
// database, so this is generous; it exists to keep a hung pool from stalling
// fulfillment.
const DefaultTimeout = 3 * time.Second

// DefaultTTL is how long a lookup result is trusted. Fulfillment bursts
// (webhook + poller racing on the same quote) otherwise re-ask the pool
// for the same address within milliseconds.
const DefaultTTL = 60 * time.Second

// ErrNotConfigured is returned by a client with no base URL so callers can
// tell "feature off" from "pool unreachable".
var ErrNotConfigured = errors.New("poolstake: no pool API URL configured")

// DefaultStakerBaseBps mirrors the staker base NimiqBase publishes
// (config.DefaultCashbackBaseBps = 50, i.e. 0.5% for any positively-staked
// address — see GET /api/cashback/terms on the pool). It is used ONLY when
// the pool cannot be asked at all (no URL configured, or unreachable) AND
// the operator has not set an admin override: the storefront then advertises
// the same rate the pool would grant, never a more generous one. Whenever
// the pool answers, the pool's own number applies verbatim — including an
// explicit 0 (base switched off) — unless the operator's admin
// StakerCashbackBps override is present.
const DefaultStakerBaseBps = 50

// Status is one answer about one address.
type Status struct {
	Address   string `json:"address"`
	StakeLuna int64  `json:"stake_luna"`
	Staked    bool   `json:"staked"`
	// BaseBps is the flat cashback base the POOL grants this address right
	// now: its staker base while staked, 0 otherwise. The shop applies it
	// verbatim — the pool, not the shop, owns this number.
	BaseBps   int       `json:"cashback_base_bps"`
	CheckedAt time.Time `json:"checked_at"`
	FromCache bool      `json:"-"`
}

// stakeResponse mirrors the pool's staker payload. Only the fields the shop
// needs are decoded; the rest (payslips, transactions) is ignored on purpose.
// cashback_base_bps is the pool's staker base for this address (0.5% while
// staked, 0 otherwise) — the pool is the authority, the shop applies the
// number verbatim, including an explicit 0.
type stakeResponse struct {
	Address         string `json:"address"`
	StakeLuna       int64  `json:"stake_luna"`
	CashbackBaseBps int    `json:"cashback_base_bps"`
}

// Terms is the pool's public cashback contract (GET /api/cashback/terms):
// the staker base every positively-staked address earns.
type Terms struct {
	StakerBaseBps     int       `json:"staker_base_bps"`
	StakerBasePercent float64   `json:"staker_base_percent"`
	Rule              string    `json:"rule"`
	PoolFeePercentage float64   `json:"pool_fee_percentage"`
	FetchedAt         time.Time `json:"-"`
}

type cacheEntry struct {
	status Status
	expiry time.Time
}

// Client is a small caching HTTP client for the pool's public staker API.
// A nil or unconfigured Client is safe to use: Ready() is false and every
// lookup returns ErrNotConfigured.
type Client struct {
	baseURL string
	hc      *http.Client
	ttl     time.Duration

	mu      sync.Mutex
	cache   map[string]cacheEntry
	feedKey string // shared secret for the pool's /api/cashback/profit ("" = off)

	terms       Terms
	termsExpiry time.Time
}

// New builds a client for a pool whose API is served at baseURL, for example
// "https://pool.example.com". A trailing slash is fine.
func New(baseURL string, timeout, ttl time.Duration) *Client {
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	return &Client{
		baseURL: strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		hc:      &http.Client{Timeout: timeout},
		ttl:     ttl,
		cache:   make(map[string]cacheEntry),
	}
}

// Ready reports whether lookups can happen at all.
func (c *Client) Ready() bool { return c != nil && c.baseURL != "" }

// CanonicalAddress normalizes a Nimiq address for lookup and cache keying:
// the pool accepts the spaced form, but "NQ07 0000..." and "NQ070000..."
// must not become two cache entries for one buyer.
func CanonicalAddress(addr string) string {
	return strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(addr), " ", ""))
}

// Stake returns the address' active stake in the operator's pool.
//
// A 404 is not an error: it means "this address never delegated here", which
// is the common case for a first-time buyer.
func (c *Client) Stake(ctx context.Context, address string) (Status, error) {
	if !c.Ready() {
		return Status{}, ErrNotConfigured
	}
	addr := CanonicalAddress(address)
	if addr == "" {
		return Status{}, errors.New("poolstake: empty address")
	}
	// An address that fails its own checksum can never be a staker of this
	// (or any) pool. Answering here, without a request, is deliberate: the
	// pool rejects such a string with 400, and because a 400 used to be an
	// error the post-fulfillment recheck loop treated it as an outage,
	// extended its deadline forever and re-asked the pool several times a
	// second, for one account, indefinitely (observed live in the logs).
	if err := nimiq.ValidateAddress(addr); err != nil {
		return Status{Address: addr, Staked: false, CheckedAt: time.Now().UTC()}, nil
	}

	now := time.Now().UTC()
	c.mu.Lock()
	if e, ok := c.cache[addr]; ok && now.Before(e.expiry) {
		c.mu.Unlock()
		e.status.FromCache = true
		return e.status, nil
	}
	c.mu.Unlock()

	st, err := c.fetch(ctx, addr, now)
	if err != nil {
		return Status{}, err
	}

	c.mu.Lock()
	if c.cache == nil {
		c.cache = make(map[string]cacheEntry)
	}
	c.cache[addr] = cacheEntry{status: st, expiry: now.Add(c.ttl)}
	// Keep the cache from growing without bound across a long-lived process:
	// drop expired entries when the map gets large. Cheap and infrequent.
	if len(c.cache) > 4096 {
		for k, e := range c.cache {
			if !now.Before(e.expiry) {
				delete(c.cache, k)
			}
		}
	}
	c.mu.Unlock()
	return st, nil
}

// SetFeedKey installs the shared secret the pool's /api/cashback/profit
// endpoint requires (X-Feed-Key, matched against GPOOL_FEED_API_KEY on the
// pool side). Without it the profit boost is unavailable (public stake and
// terms lookups keep working).
func (c *Client) SetFeedKey(key string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.feedKey = key
	c.mu.Unlock()
}

// Profit is the pool's cumulative realized fee from one staker in a UTC
// calendar period. It is intentionally NOT periodically polled: callers use
// it only when the buyer opens/refreshes cashback data or an order is
// fulfilled. The store consumes the cumulative value as an idempotent delta.
type Profit struct {
	Address           string  `json:"address"`
	Period            string  `json:"period"`
	From              string  `json:"from"`
	To                string  `json:"to"`
	PoolFeeLuna       int64   `json:"pool_fee_luna"`
	StakeLuna         int64   `json:"stake_luna"`
	LoyaltyDays       float64 `json:"loyalty_days"`
	LoyaltyMaxDays    int     `json:"loyalty_max_days"`
	LoyaltyMultiplier float64 `json:"loyalty_multiplier"`
}

func (c *Client) Profit(ctx context.Context, address, period string) (Profit, error) {
	if !c.Ready() {
		return Profit{}, ErrNotConfigured
	}
	addr := CanonicalAddress(address)
	if addr == "" {
		return Profit{}, errors.New("poolstake: empty address")
	}
	if period == "" {
		period = "this_month"
	}
	c.mu.Lock()
	key := c.feedKey
	c.mu.Unlock()
	if key == "" {
		return Profit{}, errors.New("poolstake: profit API key not configured")
	}
	endpoint := fmt.Sprintf("%s/api/cashback/profit?address=%s&period=%s", c.baseURL,
		url.QueryEscape(addr), url.QueryEscape(period))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return Profit{}, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Feed-Key", key)
	resp, err := c.hc.Do(req)
	if err != nil {
		return Profit{}, fmt.Errorf("poolstake: pool unreachable: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Profit{}, fmt.Errorf("poolstake: profit returned %d", resp.StatusCode)
	}
	var out Profit
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return Profit{}, fmt.Errorf("poolstake: bad profit response: %w", err)
	}
	if out.PoolFeeLuna < 0 {
		return Profit{}, errors.New("poolstake: negative pool profit")
	}
	return out, nil
}

// Forget drops the cached answer for an address, for example right after the
// buyer sends a new stake transaction from the shop.
func (c *Client) Forget(address string) {
	if c == nil {
		return
	}
	addr := CanonicalAddress(address)
	c.mu.Lock()
	delete(c.cache, addr)
	c.mu.Unlock()
}

func (c *Client) fetch(ctx context.Context, addr string, now time.Time) (Status, error) {
	endpoint := c.baseURL + "/api/stakers/" + url.PathEscape(addr)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return Status{}, err
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.hc.Do(req)
	if err != nil {
		return Status{}, fmt.Errorf("poolstake: pool unreachable: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	switch {
	case resp.StatusCode == http.StatusNotFound:
		return Status{Address: addr, StakeLuna: 0, Staked: false, CheckedAt: now}, nil
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		var body stakeResponse
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			return Status{}, fmt.Errorf("poolstake: bad pool response: %w", err)
		}
		if body.StakeLuna < 0 {
			body.StakeLuna = 0
		}
		if body.CashbackBaseBps < 0 {
			body.CashbackBaseBps = 0
		}
		staked := body.StakeLuna > 0
		// The pool's number, verbatim: staked → its staker base (an explicit
		// 0 means the pool turned the base off), not staked → 0. The shop
		// never second-guesses the pool.
		base := 0
		if staked {
			base = body.CashbackBaseBps
		}
		return Status{Address: addr, StakeLuna: body.StakeLuna, Staked: staked, BaseBps: base, CheckedAt: now}, nil
	case resp.StatusCode == http.StatusBadRequest:
		// The pool answered "invalid address" (or another input refusal).
		// That is a definitive "not a staker", not an outage: retrying can
		// never change it. Surfaced through a log line so the operator can
		// still see that a bad address reached us.
		log.Printf("poolstake: pool refused address %s with 400; treating as not-staked", addr)
		return Status{Address: addr, Staked: false, CheckedAt: now}, nil
	default:
		return Status{}, fmt.Errorf("poolstake: pool returned %d", resp.StatusCode)
	}
}

// termsTTL bounds how often the public terms are re-fetched; they change
// only when the pool operator edits the config.
const termsTTL = 5 * time.Minute

// Terms returns the pool's public staker-base contract, cached for termsTTL.
// Every pool ships GET /api/cashback/terms: a 404 is a misconfiguration and
// comes back as an error, so the shop never invents a rate the pool did not
// publish.
func (c *Client) Terms(ctx context.Context) (Terms, error) {
	if !c.Ready() {
		return Terms{}, ErrNotConfigured
	}
	now := time.Now().UTC()
	c.mu.Lock()
	if now.Before(c.termsExpiry) {
		t := c.terms
		c.mu.Unlock()
		return t, nil
	}
	c.mu.Unlock()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/api/cashback/terms", nil)
	if err != nil {
		return Terms{}, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.hc.Do(req)
	if err != nil {
		return Terms{}, fmt.Errorf("poolstake: pool unreachable: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Terms{}, fmt.Errorf("poolstake: terms returned %d", resp.StatusCode)
	}
	var t Terms
	if err := json.NewDecoder(resp.Body).Decode(&t); err != nil {
		return Terms{}, fmt.Errorf("poolstake: bad terms response: %w", err)
	}
	if t.StakerBaseBps <= 0 && t.StakerBasePercent > 0 {
		t.StakerBaseBps = int(t.StakerBasePercent * 100)
	}
	if t.StakerBaseBps < 0 {
		t.StakerBaseBps = 0
	}
	t.FetchedAt = now
	c.mu.Lock()
	c.terms, c.termsExpiry = t, now.Add(termsTTL)
	c.mu.Unlock()
	return t, nil
}
