package nimiq

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// PriceOracle fetches and caches the NIM/USD rate. We cache for a short
// window so a burst of checkout requests doesn't hammer the upstream feed,
// but refresh often enough that quoted NIM amounts stay close to market.
// The rate is informational for the UI — the supplier's BTC invoice is the
// payment authority.
type PriceOracle struct {
	url   string
	http  *http.Client
	cache struct {
		rate      float64
		fetchedAt time.Time
	}
	ttl time.Duration
}

func NewPriceOracle(url string) *PriceOracle {
	return &PriceOracle{
		url:  url,
		http: &http.Client{Timeout: 10 * time.Second},
		ttl:  60 * time.Second,
	}
}

func (p *PriceOracle) NimUsdRate(ctx context.Context) (float64, error) {
	if time.Since(p.cache.fetchedAt) < p.ttl && p.cache.rate > 0 {
		return p.cache.rate, nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.url, nil)
	if err != nil {
		return 0, err
	}
	resp, err := p.http.Do(req)
	if err != nil {
		return 0, fmt.Errorf("fetch price: %w", err)
	}
	defer resp.Body.Close()

	// CoinGecko simple/price shape: {"nimiq-2": {"usd": 0.012}}
	var parsed map[string]map[string]float64
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return 0, fmt.Errorf("decode price: %w", err)
	}
	for _, v := range parsed {
		if usd, ok := v["usd"]; ok && usd > 0 {
			p.cache.rate = usd
			p.cache.fetchedAt = time.Now()
			return usd, nil
		}
	}
	return 0, fmt.Errorf("no usd rate in price feed response")
}
