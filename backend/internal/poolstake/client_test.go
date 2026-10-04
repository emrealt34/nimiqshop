package poolstake

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type responseTransport func(*http.Request) (*http.Response, error)

func (f responseTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func fixtureClient(t *testing.T, status int, body string, inspect func(*http.Request)) *Client {
	t.Helper()
	c := New(" https://pool.invalid/// ", time.Second, time.Minute)
	c.hc.Transport = responseTransport(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodGet || r.Header.Get("Accept") != "application/json" {
			t.Errorf("unexpected request: %s %v", r.Method, r.Header)
		}
		if inspect != nil {
			inspect(r)
		}
		return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})
	return c
}

func TestConfigurationAndDisabledCalls(t *testing.T) {
	ctx := context.Background()
	var absent *Client
	absent.SetFeedKey("ignored")
	absent.Forget("ignored")
	for _, c := range []*Client{absent, New(" / ", 0, 0)} {
		if c.Ready() {
			t.Fatal("unconfigured client is ready")
		}
		if _, err := c.Stake(ctx, "NQ00"); !errors.Is(err, ErrNotConfigured) {
			t.Fatalf("Stake: %v", err)
		}
		if _, err := c.Profit(ctx, "NQ00", ""); !errors.Is(err, ErrNotConfigured) {
			t.Fatalf("Profit: %v", err)
		}
		if _, err := c.Terms(ctx); !errors.Is(err, ErrNotConfigured) {
			t.Fatalf("Terms: %v", err)
		}
	}
	c := New("https://pool.invalid", 0, 0)
	if c.hc.Timeout != DefaultTimeout || c.ttl != DefaultTTL {
		t.Fatal("incorrect defaults")
	}
	if CanonicalAddress(" nq12 abcd ") != "NQ12ABCD" {
		t.Fatal("address normalization")
	}
	if _, err := c.Stake(ctx, " "); err == nil {
		t.Fatal("empty stake address accepted")
	}
	if _, err := c.Profit(ctx, " ", ""); err == nil {
		t.Fatal("empty profit address accepted")
	}
	if _, err := c.Profit(ctx, "NQ00", ""); err == nil {
		t.Fatal("missing profit key accepted")
	}
}

// fixtureAddr is checksum-valid on purpose: Stake now answers an address that
// cannot be a staker locally, without a request, so a made-up string could no
// longer exercise the HTTP path at all.
const fixtureAddr = "NQ73SE1XYRRFQ8NCDQCPHLJMNR858P7V2HPD"

func TestStakeValuesAndCache(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		code       int
		stake      int64
		base       int
	}{
		{"staked", `{"stake_luna":123,"cashback_base_bps":50}`, 200, 123, 50},
		{"explicit disabled base", `{"stake_luna":123,"cashback_base_bps":0}`, 200, 123, 0},
		{"negative", `{"stake_luna":-1,"cashback_base_bps":-1}`, 200, 0, 0},
		{"not found", `{}`, 404, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			c := fixtureClient(t, tc.code, tc.body, func(r *http.Request) {
				calls++
				if r.URL.Path != "/api/stakers/"+fixtureAddr || r.Header.Get("X-Feed-Key") != "" {
					t.Errorf("public stake request leaked key or wrong path: %v", r.URL)
				}
			})
			c.SetFeedKey("fixture-key")
			c.cache = nil
			first, err := c.Stake(context.Background(), " nq73 se1x yrrf q8nc dqcp hljm nr85 8p7v 2hpd ")
			if err != nil || first.StakeLuna != tc.stake || first.BaseBps != tc.base || first.Staked != (tc.stake > 0) || first.FromCache || first.CheckedAt.IsZero() {
				t.Fatalf("first: %+v %v", first, err)
			}
			second, err := c.Stake(context.Background(), fixtureAddr)
			if err != nil || !second.FromCache || calls != 1 || second.CheckedAt != first.CheckedAt {
				t.Fatalf("cache: %+v %v calls=%d", second, err, calls)
			}
			c.Forget("nq73 se1x yrrf q8nc dqcp hljm nr85 8p7v 2hpd")
			if _, err = c.Stake(context.Background(), fixtureAddr); err != nil || calls != 2 {
				t.Fatalf("forget: calls=%d err=%v", calls, err)
			}
			c.cache[fixtureAddr] = cacheEntry{expiry: time.Now().Add(-time.Hour)}
			if _, err = c.Stake(context.Background(), fixtureAddr); err != nil || calls != 3 {
				t.Fatalf("expired cache: %d %v", calls, err)
			}
		})
	}
	c := fixtureClient(t, 200, `{}`, nil)
	for i := 0; i < 4096; i++ {
		c.cache[fmt.Sprint(i)] = cacheEntry{expiry: time.Now().Add(-time.Hour)}
	}
	c.cache["fresh"] = cacheEntry{expiry: time.Now().Add(time.Hour)}
	if _, err := c.Stake(context.Background(), fixtureAddr); err != nil {
		t.Fatal(err)
	}
	if len(c.cache) != 2 {
		t.Fatalf("expired cache not pruned: %d", len(c.cache))
	}
}

func TestProfitContract(t *testing.T) {
	for _, period := range []string{"", "last_month & extra"} {
		want := period
		if want == "" {
			want = "this_month"
		}
		c := fixtureClient(t, 200, `{"address":"NQ12ABCD","pool_fee_luna":123,"loyalty_days":2.5}`, func(r *http.Request) {
			if r.URL.Path != "/api/cashback/profit" || r.URL.Query().Get("address") != "NQ12ABCD" || r.URL.Query().Get("period") != want || r.Header.Get("X-Feed-Key") != "fixture-key" {
				t.Errorf("profit contract: %v", r.URL)
			}
		})
		c.SetFeedKey("fixture-key")
		got, err := c.Profit(context.Background(), " nq12 abcd ", period)
		if err != nil || got.PoolFeeLuna != 123 || got.LoyaltyDays != 2.5 {
			t.Fatalf("profit: %+v %v", got, err)
		}
	}
	c := fixtureClient(t, 200, `{"pool_fee_luna":-1}`, nil)
	c.SetFeedKey("fixture-key")
	if _, err := c.Profit(context.Background(), "NQ00", ""); err == nil || !strings.Contains(err.Error(), "negative") {
		t.Fatalf("negative profit: %v", err)
	}
}

func TestTermsConversionAndCache(t *testing.T) {
	for _, tc := range []struct {
		body string
		want int
	}{{`{"staker_base_bps":50}`, 50}, {`{"staker_base_percent":1.25}`, 125}, {`{"staker_base_bps":-5}`, 0}, {`{}`, 0}} {
		calls := 0
		c := fixtureClient(t, 200, tc.body, func(r *http.Request) {
			calls++
			if r.URL.Path != "/api/cashback/terms" || r.Header.Get("X-Feed-Key") != "" {
				t.Errorf("public terms contract: %v", r.URL)
			}
		})
		first, err := c.Terms(context.Background())
		if err != nil || first.StakerBaseBps != tc.want || first.FetchedAt.IsZero() {
			t.Fatalf("terms: %+v %v", first, err)
		}
		second, err := c.Terms(context.Background())
		if err != nil || first != second || calls != 1 {
			t.Fatalf("terms cache: %+v %v calls=%d", second, err, calls)
		}
	}
}

func TestEveryEndpointFailure(t *testing.T) {
	operations := map[string]func(*Client) error{
		// A checksum-valid address: "NQ00" would now short-circuit locally
		// (an address that cannot be a staker is answered without a
		// request), so it could no longer exercise the transport failures.
		"stake": func(c *Client) error {
			_, err := c.Stake(context.Background(), "NQ73SE1XYRRFQ8NCDQCPHLJMNR858P7V2HPD")
			return err
		},
		"profit": func(c *Client) error { _, err := c.Profit(context.Background(), "NQ00", ""); return err },
		"terms":  func(c *Client) error { _, err := c.Terms(context.Background()); return err },
	}
	for name, operation := range operations {
		t.Run(name, func(t *testing.T) {
			for _, tc := range []struct {
				name            string
				code            int
				body            string
				network, badURL bool
			}{{"status", 503, `{}`, false, false}, {"json", 200, `{`, false, false}, {"transport", 200, `{}`, true, false}, {"request", 200, `{}`, false, true}} {
				t.Run(tc.name, func(t *testing.T) {
					c := fixtureClient(t, tc.code, tc.body, nil)
					c.SetFeedKey("fixture-key")
					sentinel := errors.New("fixture transport unavailable")
					if tc.network {
						c.hc.Transport = responseTransport(func(*http.Request) (*http.Response, error) { return nil, sentinel })
					}
					if tc.badURL {
						c.baseURL = "://invalid"
					}
					err := operation(c)
					if err == nil {
						t.Fatal("failure was accepted")
					}
					if tc.network && !errors.Is(err, sentinel) {
						t.Fatalf("transport cause lost: %v", err)
					}
					if len(c.cache) != 0 || !c.termsExpiry.IsZero() {
						t.Fatal("failure was cached")
					}
				})
			}
		})
	}
}

// The pool answers an address it cannot parse with 400 "invalid address".
// That used to be an error, which the post-fulfillment recheck loop read as
// an outage: it extended its own deadline and re-asked forever (observed
// live: the same account hit the pool several times a second, indefinitely).
// A checksum failure is knowable locally, and both shapes are now a
// definitive "not staked".
func TestInvalidAddressIsNotStakedAndNeverHitsTheNetwork(t *testing.T) {
	called := false
	c := fixtureClient(t, 400, `{"error":"invalid address"}`, func(*http.Request) { called = true })

	// Local checksum check: no request at all.
	st, err := c.Stake(context.Background(), "NQ08D44A44B90F772E228C13C3456FBDXKH9")
	if err != nil {
		t.Fatalf("invalid address must not be an error: %v", err)
	}
	if st.Staked {
		t.Fatalf("invalid address must not be staked: %+v", st)
	}
	if called {
		t.Fatal("an address that fails its own checksum must not reach the pool")
	}
}

func TestPool400IsDefinitiveNotStaked(t *testing.T) {
	// A valid address the pool still refuses (its own parser/config).
	c := fixtureClient(t, 400, `{"error":"invalid address"}`, nil)
	st, err := c.Stake(context.Background(), "NQ73SE1XYRRFQ8NCDQCPHLJMNR858P7V2HPD")
	if err != nil {
		t.Fatalf("a 400 must not be an outage: %v", err)
	}
	if st.Staked || st.StakeLuna != 0 {
		t.Fatalf("want not-staked, got %+v", st)
	}
}

func TestPool5xxStaysAnError(t *testing.T) {
	c := fixtureClient(t, 503, `{}`, nil)
	if _, err := c.Stake(context.Background(), "NQ73SE1XYRRFQ8NCDQCPHLJMNR858P7V2HPD"); err == nil {
		t.Fatal("a 5xx is a real outage and must stay an error")
	}
}
