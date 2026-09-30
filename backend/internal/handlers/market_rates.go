package handlers

import (
	"context"
	"encoding/json"
	"sync"
	"time"
)

// The NIM/BTC rates are a always-warm, never-blocking service:
//
//   - a background refresher fetches NIM/USD + BTC/USD from the oracles
//     IMMEDIATELY at boot and then every 60 seconds, storing the result in
//     an in-memory snapshot;
//   - GET /api/market/nim-rate serves that snapshot INSTANTLY — it never
//     waits for an oracle round trip (the old code blocked up to 12s when
//     the cache was stale, which stalled the storefront's price displays);
//   - if an oracle refresh fails, the last good snapshot is KEPT (stale
//     beats empty); if the process just booted and nothing was fetched yet,
//     the endpoint answers 503 "warming up" while kicking a refresh —
//     within a couple of seconds every following request is instant.

type rateSnapshot struct {
	nimUSD     float64
	btcUSD     float64
	nimAt      time.Time
	btcAt      time.Time
	nimSources int
	btcSources int
	updatedAt  time.Time
	fetched    bool
}

// rateSnapshotJSON is the persisted (Store meta "rates_snapshot") shape of a
// rateSnapshot. The in-memory struct deliberately keeps its fields unexported;
// without this explicit mapping encoding/json would write "{}" and the
// warm-restart path below could never load a rate again.
type rateSnapshotJSON struct {
	NimUSD     float64   `json:"nim_usd"`
	BtcUSD     float64   `json:"btc_usd"`
	NimAt      time.Time `json:"nim_at"`
	BtcAt      time.Time `json:"btc_at"`
	NimSources int       `json:"nim_sources"`
	BtcSources int       `json:"btc_sources"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// MarshalJSON persists every field the refresher needs to serve a warm
// snapshot after a restart. `fetched` is runtime state and is not stored.
func (s rateSnapshot) MarshalJSON() ([]byte, error) {
	return json.Marshal(rateSnapshotJSON{
		NimUSD: s.nimUSD, BtcUSD: s.btcUSD,
		NimAt: s.nimAt, BtcAt: s.btcAt,
		NimSources: s.nimSources, BtcSources: s.btcSources,
		UpdatedAt: s.updatedAt,
	})
}

// UnmarshalJSON is the inverse of MarshalJSON; a legacy "{}" blob decodes to
// the zero snapshot, which the loader rejects (nimUSD == 0) exactly as before.
func (s *rateSnapshot) UnmarshalJSON(b []byte) error {
	var j rateSnapshotJSON
	if err := json.Unmarshal(b, &j); err != nil {
		return err
	}
	*s = rateSnapshot{
		nimUSD: j.NimUSD, btcUSD: j.BtcUSD,
		nimAt: j.NimAt, btcAt: j.BtcAt,
		nimSources: j.NimSources, btcSources: j.BtcSources,
		updatedAt: j.UpdatedAt,
	}
	return nil
}

var (
	ratesMu       sync.RWMutex
	ratesSnap     rateSnapshot
	ratesOnce     sync.Once
	ratesInterval = 60 * time.Second
)

// StartRatesRefresher launches the 60-second background rate refresher
// (idempotent). Called from main at boot; the first fetch runs immediately
// so the very first visitor already gets a warm snapshot. The refresher
// stops when the context is cancelled (process shutdown).
func (h *Handlers) StartRatesRefresher(stop context.Context) {
	// TEST MODE keeps REAL market rates on purpose: the only thing test mode
	// simulates is the PAYMENT. Prices, NIM/BTC rates and the cashback math
	// on top of them run on the live oracles exactly like production — the
	// old artificial 1:1 snapshot made every NIM amount in the sandbox a lie
	// (a $50 card read as "50 NIM"; at the real rate it is ~100k+ NIM).
	// If the oracles are unreachable the refresher simply keeps the last
	// persisted/observed rates — same behavior as production.
	ratesOnce.Do(func() {
		go func() {
			// Load the LAST persisted snapshot before the first oracle
			// fetch completes: a fresh process then serves warm rates
			// instantly instead of 503 "warming up".
			if h.Store != nil {
				if raw, err := h.Store.LoadMeta("rates_snapshot"); err == nil && len(raw) > 0 {
					var snap rateSnapshot
					if err := json.Unmarshal(raw, &snap); err == nil && snap.nimUSD > 0 && time.Since(snap.updatedAt) < 7*24*time.Hour {
						ratesMu.Lock()
						snap.fetched = true
						ratesSnap = snap
						ratesMu.Unlock()
					}
				}
			}
			h.refreshRatesNow()
			ticker := time.NewTicker(ratesInterval)
			defer ticker.Stop()
			for {
				select {
				case <-stop.Done():
					return
				case <-ticker.C:
					h.refreshRatesNow()
				}
			}
		}()
	})
}

// refreshRatesNow fetches both oracles in parallel and updates the
// snapshot. On failure the previous snapshot is preserved (stale beats
// empty) — the oracle already fails closed on bad spreads.
func (h *Handlers) refreshRatesNow() {
	var wg sync.WaitGroup
	var nim, btc OracleQuoteLite
	var nimErr, btcErr error
	wg.Add(2)
	go func() {
		defer wg.Done()
		c, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		q, err := h.Oracle.NIMUSD(c)
		nim, nimErr = OracleQuoteLite{Median: q.MedianUSD, At: q.ObservedAt, Sources: q.ValidSources}, err
	}()
	go func() {
		defer wg.Done()
		c, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		q, err := h.Oracle.BTCUSD(c)
		btc, btcErr = OracleQuoteLite{Median: q.MedianUSD, At: q.ObservedAt, Sources: q.ValidSources}, err
	}()
	wg.Wait()

	ratesMu.Lock()
	defer ratesMu.Unlock()
	if nimErr == nil && nim.Median > 0 {
		ratesSnap.nimUSD = nim.Median
		ratesSnap.nimAt = nim.At
		ratesSnap.nimSources = nim.Sources
	}
	if btcErr == nil && btc.Median > 0 {
		ratesSnap.btcUSD = btc.Median
		ratesSnap.btcAt = btc.At
		ratesSnap.btcSources = btc.Sources
	}
	if (nimErr == nil && nim.Median > 0) || (btcErr == nil && btc.Median > 0) {
		ratesSnap.updatedAt = time.Now().UTC()
		ratesSnap.fetched = true
		// Persist so RESTARTS are also warm (no 503 window after deploy).
		if h.Store != nil {
			ratesMu.Unlock()
			if b, err := json.Marshal(ratesSnap); err == nil {
				_ = h.Store.SaveMeta("rates_snapshot", b, 7*24*time.Hour)
			}
			ratesMu.Lock()
		}
	}
}

// OracleQuoteLite is the trimmed quote the snapshot stores.
type OracleQuoteLite struct {
	Median  float64
	At      time.Time
	Sources int
}

func currentRates() rateSnapshot {
	ratesMu.RLock()
	defer ratesMu.RUnlock()
	return ratesSnap
}
