package handlers

import (
	"encoding/json"
	"fmt"
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

// maxCacheEntriesPerShard bounds one shard. The old cache held 20,000 entries
// behind ONE RWMutex and paid for it twice: every read took that lock (so
// catalogue throughput was serialised by a single mutex no matter how many
// cores the box had), and the eviction sweep walked all 20,000 entries while
// holding the WRITE lock — a multi-millisecond stall with every request in
// the process queued behind it.
//
// Sharding divides both costs by cacheShards. 64 shards × 512 entries keeps
// the same 32k ceiling overall while making the worst-case sweep 512 entries
// on 1/64 of the keyspace.
const (
	cacheShards          = 64
	maxCacheEntriesTotal = 32768
)

const maxCacheEntries = maxCacheEntriesTotal / cacheShards

type ttlCache struct {
	ttl    time.Duration
	shards [cacheShards]cacheShard
}

// cacheShard is one independent lock + map. Pad the mutex into its own cache
// line implicitly by keeping the struct small and letting the array stride do
// the work; at 64 shards false sharing between neighbours is not measurable
// against the contention this removes.
type cacheShard struct {
	mu    sync.RWMutex
	items map[string]cacheItem
}

type cacheItem struct {
	value   interface{}
	expires time.Time
}

func newTTLCache(ttl time.Duration) *ttlCache {
	c := &ttlCache{ttl: ttl}
	for i := range c.shards {
		c.shards[i].items = make(map[string]cacheItem, 64)
	}
	return c
}

// shardFor is an inlined FNV-1a. hash/fnv allocates a hasher per call, which
// on a cache that is read on every single request shows up in the profile.
func (c *ttlCache) shardFor(key string) *cacheShard {
	var h uint32 = 2166136261
	for i := 0; i < len(key); i++ {
		h ^= uint32(key[i])
		h *= 16777619
	}
	return &c.shards[h%cacheShards]
}

func (c *ttlCache) get(key string) (interface{}, bool) {
	sh := c.shardFor(key)
	sh.mu.RLock()
	item, ok := sh.items[key]
	sh.mu.RUnlock()
	if !ok {
		return nil, false
	}
	if time.Now().After(item.expires) {
		// Delete-on-expiry used to be part of the read path, which turned a
		// read into a write lock. Expired entries are reclaimed by the
		// periodic sweep instead: a stale hit is reported as a miss either
		// way, so nothing about the answer changes.
		return nil, false
	}
	return item.value, true
}

// peekStale returns the value even when expired, plus its age. Used as the
// L2 layer: on supplier failure a slightly-old catalog beats no catalog.
func (c *ttlCache) peekStale(key string) (interface{}, time.Duration, bool) {
	sh := c.shardFor(key)
	sh.mu.RLock()
	item, ok := sh.items[key]
	sh.mu.RUnlock()
	if !ok {
		return nil, 0, false
	}
	age := time.Since(item.expires) + c.ttl
	if age < 0 {
		age = 0
	}
	return item.value, age, true
}

// setTTL is like set with a per-entry TTL (for fast-changing data like
// live prices). Eviction is confined to the one shard being written, so a
// full cache costs a 512-entry sweep under one of 64 locks instead of a
// 20,000-entry sweep under the only lock in the process.
func (c *ttlCache) setTTL(key string, value interface{}, ttl time.Duration) {
	sh := c.shardFor(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()
	now := time.Now()
	if len(sh.items) >= maxCacheEntries {
		for k, v := range sh.items {
			if now.After(v.expires) {
				delete(sh.items, k)
			}
		}
		if len(sh.items) >= maxCacheEntries {
			// Still full of live entries: drop the one closest to expiry,
			// which is the least useful thing in the shard.
			var oldestKey string
			var oldestExp time.Time
			for k, v := range sh.items {
				if oldestExp.IsZero() || v.expires.Before(oldestExp) {
					oldestKey = k
					oldestExp = v.expires
				}
			}
			if oldestKey != "" {
				delete(sh.items, oldestKey)
			}
		}
	}
	sh.items[key] = cacheItem{value: value, expires: now.Add(ttl)}
}

// Len reports the live entry count across all shards. Diagnostic only.
func (c *ttlCache) Len() int {
	total := 0
	for i := range c.shards {
		c.shards[i].mu.RLock()
		total += len(c.shards[i].items)
		c.shards[i].mu.RUnlock()
	}
	return total
}

/* ----------------------------- singleflight ------------------------------ */

// flightGroup collapses concurrent identical fetches into one. A follower
// whose leader FAILED re-fetches with its own context: a canceled leader
// context must not fail callers that are still alive.
// flightShards divides the singleflight map the same way the cache is
// divided. The leader-election lock is taken on EVERY cache miss, so a
// single mutex there is a global serialisation point at exactly the moment
// (a cold key under load) the process can least afford one.
const flightShards = 64

type flightGroup struct {
	shards [flightShards]flightShard
}

type flightShard struct {
	mu       sync.Mutex
	inFlight map[string]*flightCall
}

type flightCall struct {
	wg  sync.WaitGroup
	val interface{}
	err error
}

func (g *flightGroup) shardFor(key string) *flightShard {
	var h uint32 = 2166136261
	for i := 0; i < len(key); i++ {
		h ^= uint32(key[i])
		h *= 16777619
	}
	return &g.shards[h%flightShards]
}

// Do collapses concurrent identical fetches into one. A follower whose leader
// FAILED re-fetches with its own context: a canceled leader must not fail
// callers that are still alive. The retry is bounded so a key that fails
// forever cannot recurse forever.
func (g *flightGroup) Do(key string, fn func() (interface{}, error)) (interface{}, error) {
	return g.do(key, fn, 0)
}

// flightMaxRetries bounds the leader-failed re-election chain. Without it, N
// concurrent followers of a permanently failing leader recurse N deep.
const flightMaxRetries = 8

func (g *flightGroup) do(key string, fn func() (interface{}, error), attempt int) (interface{}, error) {
	sh := g.shardFor(key)
	sh.mu.Lock()
	if sh.inFlight == nil {
		sh.inFlight = make(map[string]*flightCall, 8)
	}
	if f, ok := sh.inFlight[key]; ok {
		sh.mu.Unlock()
		f.wg.Wait()
		if f.err == nil {
			return f.val, nil
		}
		if attempt >= flightMaxRetries {
			// The leader keeps failing; stop re-electing and report the
			// leader's error rather than adding more load to a broken
			// upstream.
			return f.val, f.err
		}
		return g.do(key, fn, attempt+1)
	}
	f := &flightCall{}
	f.wg.Add(1)
	sh.inFlight[key] = f
	sh.mu.Unlock()

	// A panicking fetcher must still release its followers, or they wait
	// forever on a WaitGroup nobody will ever Done().
	val, err := func() (v interface{}, e error) {
		defer func() {
			if r := recover(); r != nil {
				e = fmt.Errorf("cache fetch panicked: %v", r)
			}
		}()
		return fn()
	}()

	sh.mu.Lock()
	delete(sh.inFlight, key)
	sh.mu.Unlock()
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
