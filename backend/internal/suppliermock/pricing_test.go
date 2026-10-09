package suppliermock

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type pricingTransport func(*http.Request) (*http.Response, error)

func (fn pricingTransport) RoundTrip(r *http.Request) (*http.Response, error) { return fn(r) }
func pricingClient(status int, body string, fail bool) *http.Client {
	return &http.Client{Transport: pricingTransport(func(*http.Request) (*http.Response, error) {
		if fail {
			return nil, errors.New("offline test transport")
		}
		return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
}
func TestMockOracleParsersRejectBadDataWithoutNetwork(t *testing.T) {
	sources := []struct {
		name        string
		build       func(*http.Client) func() (float64, error)
		good, small string
	}{
		{"coingecko", btcFromCoingecko, `{"bitcoin":{"usd":50000}}`, `{"bitcoin":{"usd":1}}`},
		{"coinpaprika", btcFromCoinpaprika, `{"quotes":{"USD":{"price":50000}}}`, `{"quotes":{"USD":{"price":1}}}`},
		{"kucoin", btcFromKucoin, `{"code":"200000","data":{"price":"50000"}}`, `{"code":"200000","data":{"price":"1"}}`},
	}
	for _, source := range sources {
		t.Run(source.name, func(t *testing.T) {
			price, err := source.build(pricingClient(200, source.good, false))()
			if err != nil || price != 50000 {
				t.Fatalf("valid price: %v %v", price, err)
			}
			for _, bad := range []struct {
				status int
				body   string
				fail   bool
			}{{503, source.good, false}, {200, "not json", false}, {200, source.small, false}, {200, source.good, true}} {
				if _, err := source.build(pricingClient(bad.status, bad.body, bad.fail))(); err == nil {
					t.Fatalf("bad price accepted: %+v", bad)
				}
			}
		})
	}
	for _, body := range []string{`{"code":"500000","data":{"price":"50000"}}`, `{"code":"200000","data":{"price":"not-a-number"}}`} {
		if _, err := btcFromKucoin(pricingClient(200, body, false))(); err == nil {
			t.Fatalf("invalid exchange data accepted: %s", body)
		}
	}
}
func TestMockPinnedPricingAndHonestLastKnownFallback(t *testing.T) {
	t.Setenv("MOCK_BTC_USD", "50000")
	if price, err := btcUSD(); err != nil || price != 50000 {
		t.Fatalf("pinned price: %v %v", price, err)
	}
	if amount, err := usdToCoin(50, "BTC"); err != nil || amount != "0.00100000" {
		t.Fatalf("BTC conversion: %s %v", amount, err)
	}
	if amount, err := usdToCoin(50, "USDT"); err != nil || amount != "50.00" {
		t.Fatalf("stablecoin conversion: %s %v", amount, err)
	}
	t.Setenv("MOCK_BTC_USD", "invalid")
	if mockBTCOverride() != 0 {
		t.Fatal("invalid override invented a price")
	}
	btcMu.Lock()
	saved, savedAt := btcCached, btcFetched
	btcCached = 42000
	btcFetched = time.Now()
	btcMu.Unlock()
	t.Cleanup(func() { btcMu.Lock(); btcCached = saved; btcFetched = savedAt; btcMu.Unlock() })
	if price, err := btcUSD(); err != nil || price != 42000 {
		t.Fatalf("cached price: %v %v", price, err)
	}
	cause := errors.New("market unavailable")
	if price, err := lastKnownBTC(cause); err != nil || price != 42000 {
		t.Fatalf("last known: %v %v", price, err)
	}
	btcMu.Lock()
	btcCached = 0
	btcMu.Unlock()
	if _, err := lastKnownBTC(cause); !errors.Is(err, cause) {
		t.Fatalf("no price must be an error: %v", err)
	}
}
func TestMockCountryCurrencyLabelsPreserveRealDenominations(t *testing.T) {
	cases := []struct {
		country, code, want string
		multiplier          float64
	}{
		{"ID", "IDR", "12.345 IDR", 1000}, {"VN", "VND", "12.345 VND", 1000}, {"IQ", "IQD", "12.345 IQD", 1000}, {"CO", "COP", "12.345 COP", 1000},
		{"PK", "PKR", "12,345 PKR", 1000}, {"AE", "AED", "12 345 AED", 1000}, {"TR", "TRY", "TRY12345", 1}, {"GB", "GBP", "£12345", 1},
		{"DE", "EUR", "12345,00 EUR", 1}, {"KR", "KRW", "₩12,345", 1}, {"IN", "INR", "₹12345", 1}, {"JP", "JPY", "¥12,345", 1}, {"BR", "BRL", "R$ 12345", 1}, {"US", "USD", "$12345", 1}, {"XX", "USD", "12345 USD", 1},
	}
	for _, tc := range cases {
		multiplier, format, code := countryCurrency(strings.ToLower(tc.country))
		if code != tc.code || multiplier != tc.multiplier || format(12345) != tc.want {
			t.Fatalf("%s: code=%s scale=%v label=%s", tc.country, code, multiplier, format(12345))
		}
	}
	if groupDots(123) != "123" || groupDots(1234567) != "1.234.567" {
		t.Fatal("large denomination grouping corrupted")
	}
}
func TestMockPaymentStatesAndFiatValues(t *testing.T) {
	for status, want := range map[string]string{stWaiting: "PaymentRequested", stStarted: "PaymentReceived", stReceived: "PaymentReceived", stDeliver: "PaymentReceived", stDone: "PaymentCompleted", stRefunded: "Refunded", stFailed: "PaymentFailed", stCreated: "WalletCreated", stExpired: "WalletCreated"} {
		if got := paymentStateFor(status); got != want {
			t.Fatalf("%s=%s want=%s", status, got, want)
		}
	}
	if paymentMethodFor("BTC", "Lightning") != "BTC-LIGHTNING" || paymentMethodFor("USDT", "POLYGON") != "USDT-POLYGON" {
		t.Fatal("payment rail mislabeled")
	}
	value := 17.5
	state := newState()
	if got := state.paymentValue(&order{Deliveries: []delivery{{ProductValue: &value}, {Family: "test-steam"}, {Family: "test-topup"}, {Family: "unknown"}}}); got != 72.5 {
		t.Fatalf("fiat value=%v", got)
	}
	if findProduct("test-ghost") != nil || findProduct("unknown") != nil || findProduct("TEST-STEAM") == nil {
		t.Fatal("ghost/case-sensitive catalog mismatch")
	}
	state.faults = map[string]interface{}{"integer": 7, "float": float64(8), "flag": true, "text": "hello", "bad": []int{1}}
	if state.faultInt("integer") != 7 || state.faultInt("float") != 8 || state.faultInt("bad") != 0 || state.faultBool("flag") != true || state.faultBool("bad") || state.faultStr("text") != "hello" || state.faultStr("bad") != "" {
		t.Fatal("fault configuration coercion corrupted")
	}
}
