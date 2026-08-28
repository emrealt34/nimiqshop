package handlers

import (
	"encoding/json"
	"log"
	"sync"
	"time"
)

// The catalog cache is FOUR layers deep, so that a supplier outage, a 429
// storm or even a process restart can never blank the storefront:
//
//	L1 hot        in-memory, fresh TTL (seconds-minutes) — serves 99% of traffic
//	L2 stale      in-memory, expired but < staleCap — served on supplier error
//	              (also refreshed in the background when past fresh TTL)
//	L3 disk       Badger snapshot of the last good payload — survives restarts,
//	              served whenever L1/L2 miss AND the supplier fails
//	L4 supplier   the live CryptoRefills API — hit at most once per key per
//	              fresh TTL, single-flighted (concurrent misses collapse)
//
// Rules that keep the cache honest:
//   - EMPTY results are never cached as fresh catalog data (an empty brand
//     list or empty family must never blank a working storefront for the
//     whole TTL — this was the "products disappeared" bug).
//   - Errors are never cached beyond a tiny negative TTL.
//   - Every successful fetch is persisted to L3.

const maxCacheEntries = 20000

type ttlCache struct {
	mu    sync.RWMutex
	items map[string]cacheItem
	ttl   time.Duration
}
type cacheItem struct {
	value   interface{}
	expires time.Time
}

func newTTLCache(ttl time.Duration) *ttlCache {
	return &ttlCache{items: make(map[string]cacheItem), ttl: ttl}
}
func (c *ttlCache) get(key string) (interface{}, bool) {
	c.mu.RLock()
	item, ok := c.items[key]
	c.mu.RUnlock()
	if !ok || time.Now().After(item.expires) {
		if ok {
			c.mu.Lock()
			delete(c.items, key)
			c.mu.Unlock()
		}
		return nil, false
	}
	return item.value, true
}

// peekStale returns the value even when expired, plus its age. Used as the
// L2 layer: on supplier failure a slightly-old catalog beats no catalog.
func (c *ttlCache) peekStale(key string) (interface{}, time.Duration, bool) {
	c.mu.RLock()
	item, ok := c.items[key]
	c.mu.RUnlock()
	if !ok {
		return nil, 0, false
	}
	age := time.Since(item.expires) + c.ttl
	if age < 0 {
		age = 0
	}
	return item.value, age, true
}

func (c *ttlCache) set(key string, value interface{}) {
	c.setTTL(key, value, c.ttl)
}

// setTTL is like set with a per-entry TTL (for fast-changing data like
// live prices).
func (c *ttlCache) setTTL(key string, value interface{}, ttl time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	if len(c.items) >= maxCacheEntries {
		for k, v := range c.items {
			if now.After(v.expires) {
				delete(c.items, k)
			}
		}
		if len(c.items) >= maxCacheEntries {
			// Remove oldest entry
			var oldestKey string
			var oldestExp time.Time
			for k, v := range c.items {
				if oldestExp.IsZero() || v.expires.Before(oldestExp) {
					oldestKey = k
					oldestExp = v.expires
				}
			}
			if oldestKey != "" {
				delete(c.items, oldestKey)
			}
		}
	}
	c.items[key] = cacheItem{value: value, expires: now.Add(ttl)}
}

/* ----------------------------- singleflight ------------------------------ */

// flightGroup collapses concurrent identical fetches into one. A follower
// whose leader FAILED re-fetches with its own context: a canceled leader
// context must not fail callers that are still alive.
type flightGroup struct {
	mu       sync.Mutex
	inFlight map[string]*flightCall
}

type flightCall struct {
	wg  sync.WaitGroup
	val interface{}
	err error
}

func (g *flightGroup) Do(key string, fn func() (interface{}, error)) (interface{}, error) {
	g.mu.Lock()
	if g.inFlight == nil {
		g.inFlight = make(map[string]*flightCall)
	}
	if f, ok := g.inFlight[key]; ok {
		g.mu.Unlock()
		f.wg.Wait()
		if f.err == nil {
			return f.val, nil
		}
		return g.Do(key, fn) // leader failed; one of us becomes the new leader
	}
	f := &flightCall{}
	f.wg.Add(1)
	g.inFlight[key] = f
	g.mu.Unlock()

	val, err := fn()

	g.mu.Lock()
	delete(g.inFlight, key)
	g.mu.Unlock()
	f.val, f.err = val, err
	f.wg.Done()
	return val, err
}

/* -------------------------- catalog snapshot L3 --------------------------- */

// snapshotEnvelope wraps a persisted catalog payload with its save time so
// the loader can reject absurdly old snapshots independently of the db TTL.
type snapshotEnvelope struct {
	SavedAt time.Time       `json:"saved_at"`
	Payload json.RawMessage `json:"payload"`
}

// saveCatalogSnapshot persists the last good supplier payload to Badger.
// Never fails the caller: a snapshot write failure is logged and ignored —
// the in-memory cache is still authoritative until it misses.
func (h *Handlers) saveCatalogSnapshot(key string, payload interface{}) {
	b, err := json.Marshal(payload)
	if err != nil {
		return
	}
	env, err := json.Marshal(snapshotEnvelope{SavedAt: time.Now().UTC(), Payload: b})
	if err != nil {
		return
	}
	if err := h.Store.SaveCatalogSnapshot(key, env); err != nil {
		logSnapshotErr(key, err)
	}
}

// loadCatalogSnapshot reads the persisted payload for key and decodes it
// into a fresh value. Returns ok=false when absent, undecodable, or older
// than maxSnapshotAge.
func (h *Handlers) loadCatalogSnapshot(key string, decode func(json.RawMessage) (interface{}, error)) (interface{}, bool) {
	raw, err := h.Store.LoadCatalogSnapshot(key)
	if err != nil || len(raw) == 0 {
		return nil, false
	}
	var env snapshotEnvelope
	if err := json.Unmarshal(raw, &env); err != nil || len(env.Payload) == 0 {
		return nil, false
	}
	if !env.SavedAt.IsZero() && time.Since(env.SavedAt) > maxSnapshotAge {
		return nil, false
	}
	v, err := decode(env.Payload)
	if err != nil {
		return nil, false
	}
	return v, true
}

// maxSnapshotAge bounds L3 independently of the Badger TTL: after 30 days
// a snapshot is too old to show even as a last resort.
const maxSnapshotAge = 30 * 24 * time.Hour

// catalogStaleCap bounds how long an in-memory expired entry may still be
// served on supplier failure.
const catalogStaleCap = 30 * 24 * time.Hour

func logSnapshotErr(key string, err error) {
	// Snapshot write failures are rare (disk full) and never fatal: the
	// in-memory cache is still authoritative until it misses.
	log.Printf("catalog snapshot save(%s): %v", key, err)
}
