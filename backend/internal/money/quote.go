package money

import (
	"fmt"
	"math"
)

// NIMQuote is intentionally integer Luna based. The value is rounded upward,
// never downward, so a floating-point representation cannot undercharge an
// accepted quote. Price is USD per NIM, and margin is basis points.
type NIMQuote struct {
	ProductUSD      Micros
	OracleUSDPerNIM float64
	MarginBps       int
	RequiredLunas   int64
}

func QuoteNIM(productUSD Micros, usdPerNIM float64, marginBps int) (NIMQuote, error) {
	if productUSD <= 0 || usdPerNIM <= 0 || math.IsNaN(usdPerNIM) || math.IsInf(usdPerNIM, 0) {
		return NIMQuote{}, fmt.Errorf("invalid quote inputs")
	}
	if marginBps < 0 || marginBps > 5000 {
		return NIMQuote{}, fmt.Errorf("invalid margin")
	}
	// productUSD is micro dollars; 100,000 Lunas are one NIM.
	lunas := math.Ceil((float64(productUSD) / 1_000_000) * (1 + float64(marginBps)/10_000) / usdPerNIM * 100_000)
	if lunas > float64(math.MaxInt64) {
		return NIMQuote{}, fmt.Errorf("quote overflow")
	}
	return NIMQuote{ProductUSD: productUSD, OracleUSDPerNIM: usdPerNIM, MarginBps: marginBps, RequiredLunas: int64(lunas)}, nil
}
