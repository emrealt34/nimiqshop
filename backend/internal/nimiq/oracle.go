package nimiq

// MultiSourceOracle is deliberately separate from the former single CoinGecko
// client. A quote is unsafe unless independent sources agree. Callers must
// fail closed when ValidSources < MinSources or SpreadBps is too large.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"nimiqshop/internal/safe"
)

type OracleQuote struct {
	MedianUSD    float64            `json:"median_usd"`
	ValidSources int                `json:"valid_sources"`
	SpreadBps    int64              `json:"spread_bps"`
	ObservedAt   time.Time          `json:"observed_at"`
	Sources      map[string]float64 `json:"sources"`
}
type MultiSourceOracle struct {
	http         *http.Client
	ttl          time.Duration
	minSources   int
	maxSpreadBps int64
	mu           sync.Mutex
	cached       OracleQuote
}

func NewMultiSourceOracle(minSources int, maxSpreadBps int64) *MultiSourceOracle {
	return &MultiSourceOracle{http: &http.Client{Timeout: 8 * time.Second}, ttl: 30 * time.Second, minSources: minSources, maxSpreadBps: maxSpreadBps}
}
func (o *MultiSourceOracle) NIMUSD(ctx context.Context) (OracleQuote, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if time.Since(o.cached.ObservedAt) < o.ttl {
		return o.cached, nil
	}
	type result struct {
		name  string
		value float64
		err   error
	}
	ch := make(chan result, 5)
	// Each fetcher is panic-guarded AND guarantees exactly one send on ch:
	// an unguarded panic would kill the whole process, and a fetcher that
	// dies without sending would hang the collector below forever.
	sendFetch := func(name string, fetch func(context.Context) (float64, error)) {
		safe.Go("oracle:"+name, func() {
			var v float64
			var e error
			func() {
				defer func() {
					if r := recover(); r != nil {
						v = 0
						e = fmt.Errorf("oracle %s panicked: %v", name, r)
					}
				}()
				v, e = fetch(ctx)
			}()
			ch <- result{name, v, e}
		})
	}
	sendFetch("coingecko", o.coinGecko)
	sendFetch("coinpaprika", o.coinPaprika)
	sendFetch("cryptocompare", o.cryptoCompare)
	sendFetch("kucoin", o.kuCoin)
	sendFetch("mexc", o.mexC)
	values := map[string]float64{}
	list := []float64{}
	for i := 0; i < 5; i++ {
		r := <-ch
		if r.err == nil && r.value > 0 && !math.IsInf(r.value, 0) && !math.IsNaN(r.value) {
			values[r.name] = r.value
			list = append(list, r.value)
		}
	}
	if len(list) < o.minSources {
		return OracleQuote{}, fmt.Errorf("oracle unavailable: only %d valid sources", len(list))
	}
	sort.Float64s(list)
	median := list[len(list)/2]
	if len(list)%2 == 0 {
		median = (list[len(list)/2-1] + list[len(list)/2]) / 2
	}
	spread := int64(math.Round((list[len(list)-1] - list[0]) / median * 10000))
	if spread > o.maxSpreadBps {
		return OracleQuote{}, fmt.Errorf("oracle disagreement: %d bps", spread)
	}
	q := OracleQuote{MedianUSD: median, ValidSources: len(list), SpreadBps: spread, ObservedAt: time.Now().UTC(), Sources: values}
	o.cached = q
	return q, nil
}
func (o *MultiSourceOracle) get(ctx context.Context, url string, out any) error {
	req, e := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if e != nil {
		return e
	}
	res, e := o.http.Do(req)
	if e != nil {
		return e
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("upstream status %d", res.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(out)
}
func (o *MultiSourceOracle) coinGecko(ctx context.Context) (float64, error) {
	var v struct {
		N map[string]float64 `json:"nimiq-2"`
	}
	e := o.get(ctx, "https://api.coingecko.com/api/v3/simple/price?ids=nimiq-2&vs_currencies=usd", &v)
	return v.N["usd"], e
}
func (o *MultiSourceOracle) coinPaprika(ctx context.Context) (float64, error) {
	var v struct {
		Quotes map[string]struct {
			Price float64 `json:"price"`
		} `json:"quotes"`
	}
	e := o.get(ctx, "https://api.coinpaprika.com/v1/tickers/nim-nimiq", &v)
	return v.Quotes["USD"].Price, e
}

// cryptoCompare is key-gated: Cryptorefills-era keyless calls now answer
// 401, so an unconfigured deployment skips the guaranteed-dead request
// instead of burning it every refresh. With CRYPTOCOMPARE_API_KEY set it
// becomes a live fifth opinion.
func (o *MultiSourceOracle) cryptoCompare(ctx context.Context) (float64, error) {
	key := strings.TrimSpace(os.Getenv("CRYPTOCOMPARE_API_KEY"))
	if key == "" {
		return 0, errors.New("cryptocompare: no API key configured")
	}
	var v struct {
		USD float64 `json:"USD"`
	}
	e := o.get(ctx, "https://min-api.cryptocompare.com/data/price?fsym=NIM&tsyms=USD&api_key="+url.QueryEscape(key), &v)
	return v.USD, e
}

// mexC reads NIM/USDT off MEXC's keyless public ticker — an exchange price
// that keeps the median honest when the aggregator APIs (coingecko,
// coinpaprika) rate-limit a shared deployment IP.
func (o *MultiSourceOracle) mexC(ctx context.Context) (float64, error) {
	var v struct {
		Price string `json:"price"`
	}
	e := o.get(ctx, "https://api.mexc.com/api/v3/ticker/price?symbol=NIMUSDT", &v)
	var p float64
	if _, x := fmt.Sscan(v.Price, &p); e == nil {
		e = x
	}
	return p, e
}
func (o *MultiSourceOracle) kuCoin(ctx context.Context) (float64, error) {
	var v struct {
		Data struct {
			Price string `json:"price"`
		} `json:"data"`
	}
	e := o.get(ctx, "https://api.kucoin.com/api/v1/market/orderbook/level1?symbol=NIM-USDT", &v)
	var p float64
	if _, x := fmt.Sscan(v.Data.Price, &p); e == nil {
		e = x
	}
	return p, e
}

/* --------------------------- BTC/USD oracle ------------------------------ */

// btcCached mirrors the NIM cache for the BTC leg. Independent so a BTC
// outage never poisons (or blocks) the NIM estimate.
type btcState struct {
	mu     sync.Mutex
	cached OracleQuote
}

var btcOracle btcState

// BTCUSD returns the median BTC/USD price across at least minSources
// independent feeds. It powers the informational "≈ N NIM" estimate for a
// BTC Lightning invoice; the exact NIM amount is always computed by Nimiq
// Pay at swap time.
func (o *MultiSourceOracle) BTCUSD(ctx context.Context) (OracleQuote, error) {
	btcOracle.mu.Lock()
	defer btcOracle.mu.Unlock()
	if time.Since(btcOracle.cached.ObservedAt) < o.ttl {
		return btcOracle.cached, nil
	}
	type result struct {
		name  string
		value float64
		err   error
	}
	ch := make(chan result, 5)
	sendFetch := func(name string, fetch func(context.Context) (float64, error)) {
		safe.Go("oracle:btc:"+name, func() {
			var v float64
			var e error
			func() {
				defer func() {
					if r := recover(); r != nil {
						v = 0
						e = fmt.Errorf("btc oracle %s panicked: %v", name, r)
					}
				}()
				v, e = fetch(ctx)
			}()
			ch <- result{name, v, e}
		})
	}
	sendFetch("coingecko", o.btcCoinGecko)
	sendFetch("coinpaprika", o.btcCoinPaprika)
	sendFetch("cryptocompare", o.btcCryptoCompare)
	sendFetch("kucoin", o.btcKuCoin)
	sendFetch("mexc", o.btcMexc)
	values := map[string]float64{}
	list := []float64{}
	for i := 0; i < 5; i++ {
		r := <-ch
		if r.err == nil && r.value > 0 && !math.IsInf(r.value, 0) && !math.IsNaN(r.value) {
			values[r.name] = r.value
			list = append(list, r.value)
		}
	}
	if len(list) < o.minSources {
		return OracleQuote{}, fmt.Errorf("btc oracle unavailable: only %d valid sources", len(list))
	}
	sort.Float64s(list)
	median := list[len(list)/2]
	if len(list)%2 == 0 {
		median = (list[len(list)/2-1] + list[len(list)/2]) / 2
	}
	spread := int64(math.Round((list[len(list)-1] - list[0]) / median * 10000))
	if spread > o.maxSpreadBps {
		return OracleQuote{}, fmt.Errorf("btc oracle disagreement: %d bps", spread)
	}
	q := OracleQuote{MedianUSD: median, ValidSources: len(list), SpreadBps: spread, ObservedAt: time.Now().UTC(), Sources: values}
	btcOracle.cached = q
	return q, nil
}

func (o *MultiSourceOracle) btcCoinGecko(ctx context.Context) (float64, error) {
	var v struct {
		B map[string]float64 `json:"bitcoin"`
	}
	e := o.get(ctx, "https://api.coingecko.com/api/v3/simple/price?ids=bitcoin&vs_currencies=usd", &v)
	return v.B["usd"], e
}
func (o *MultiSourceOracle) btcCoinPaprika(ctx context.Context) (float64, error) {
	var v struct {
		Quotes map[string]struct {
			Price float64 `json:"price"`
		} `json:"quotes"`
	}
	e := o.get(ctx, "https://api.coinpaprika.com/v1/tickers/btc-bitcoin", &v)
	return v.Quotes["USD"].Price, e
}
func (o *MultiSourceOracle) btcCryptoCompare(ctx context.Context) (float64, error) {
	key := strings.TrimSpace(os.Getenv("CRYPTOCOMPARE_API_KEY"))
	if key == "" {
		return 0, errors.New("cryptocompare: no API key configured")
	}
	var v struct {
		USD float64 `json:"USD"`
	}
	e := o.get(ctx, "https://min-api.cryptocompare.com/data/price?fsym=BTC&tsyms=USD&api_key="+url.QueryEscape(key), &v)
	return v.USD, e
}

// btcMexc reads BTC/USDT off MEXC's keyless public ticker.
func (o *MultiSourceOracle) btcMexc(ctx context.Context) (float64, error) {
	var v struct {
		Price string `json:"price"`
	}
	e := o.get(ctx, "https://api.mexc.com/api/v3/ticker/price?symbol=BTCUSDT", &v)
	var p float64
	if _, x := fmt.Sscan(v.Price, &p); e == nil {
		e = x
	}
	return p, e
}
func (o *MultiSourceOracle) btcKuCoin(ctx context.Context) (float64, error) {
	var v struct {
		Data struct {
			Price string `json:"price"`
		} `json:"data"`
	}
	e := o.get(ctx, "https://api.kucoin.com/api/v1/market/orderbook/level1?symbol=BTC-USDT", &v)
	var p float64
	if _, x := fmt.Sscan(v.Data.Price, &p); e == nil {
		e = x
	}
	return p, e
}
