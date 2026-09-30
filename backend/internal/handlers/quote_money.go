package handlers

import (
	"errors"
	"math"
	"strconv"
	"strings"
)

// quotedUSD uses the live validated amount, not a denomination or client price.
// A complete structured catalog total is a fallback during BTC oracle warm-up.
// Unknown totals must not silently become zero and bypass monetary limits.
func quotedUSD(amount, method string, btcUSD, catalogUSD float64, complete bool) (float64, error) {
	v, err := strconv.ParseFloat(strings.TrimSpace(amount), 64)
	if err != nil || v <= 0 || math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, errors.New("supplier returned an invalid live amount; no order was created")
	}
	if IsStablecoinMethod(method) {
		return v, nil
	}
	if btcUSD > 0 && !math.IsNaN(btcUSD) && !math.IsInf(btcUSD, 0) {
		usd := v * btcUSD
		if !math.IsInf(usd, 0) {
			return usd, nil
		}
	}
	if complete && catalogUSD > 0 && !math.IsNaN(catalogUSD) && !math.IsInf(catalogUSD, 0) {
		return catalogUSD, nil
	}
	return 0, errors.New("live currency conversion is temporarily unavailable; no order was created")
}
