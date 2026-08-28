package handlers

import (
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"nimiqshop/internal/db"
)

// TestFlightGroupCollapses: N concurrent identical fetches = exactly 1
// upstream call; everyone gets the same result.
func TestFlightGroupCollapses(t *testing.T) {
	var g flightGroup
	var calls int32
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, err := g.Do("key", func() (interface{}, error) {
				atomic.AddInt32(&calls, 1)
				time.Sleep(20 * time.Millisecond) // simulate upstream latency
				return "payload", nil
			})
			if err != nil || v != "payload" {
				t.Errorf("follower got %v, %v", v, err)
			}
		}()
	}
	wg.Wait()
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("upstream calls = %d, want 1 (singleflight broken)", got)
	}
}

// TestFlightGroupFollowerRetriesAfterLeaderFailure: a canceled/failed
// leader must not poison followers that are still alive.
func TestFlightGroupFollowerRetriesAfterLeaderFailure(t *testing.T) {
	var g flightGroup
	var calls int32
	v, err := g.Do("k", func() (interface{}, error) {
		atomic.AddInt32(&calls, 1)
		return nil, errors.New("leader context canceled")
	})
	if err == nil {
		t.Fatal("expected leader error")
	}
	_ = v
	// A second, fresh caller must actually fetch (not inherit the error).
	v2, err := g.Do("k", func() (interface{}, error) {
		atomic.AddInt32(&calls, 1)
		return "fresh", nil
	})
	if err != nil || v2 != "fresh" {
		t.Fatalf("fresh caller got %v, %v", v2, err)
	}
	if atomic.LoadInt32(&calls) != 2 {
		t.Fatalf("calls = %d, want 2", atomic.LoadInt32(&calls))
	}
}

// TestPeekStaleServesExpired: the L2 layer must return expired values with
// their age — that is what keeps the storefront alive during outages.
func TestPeekStaleServesExpired(t *testing.T) {
	c := newTTLCache(10 * time.Millisecond)
	c.set("k", "v")
	time.Sleep(20 * time.Millisecond)
	// peekStale FIRST (the production order: fresh lookup misses, then the
	// stale layer on supplier error) — a plain get() would have deleted
	// the expired entry.
	v, age, ok := c.peekStale("k")
	if !ok || v != "v" {
		t.Fatalf("peekStale = %v, %v", v, ok)
	}
	if age < 10*time.Millisecond {
		t.Fatalf("age = %s, want >= 10ms", age)
	}
	if _, ok := c.get("k"); ok {
		t.Fatal("expired entry must not be served as fresh")
	}
}

// TestSnapshotRoundtrip: L3 persistence survives a reopen of the store.
func TestSnapshotRoundtrip(t *testing.T) {
	dir := t.TempDir()
	store, err := db.New(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	h := &Handlers{Store: store}
	h.saveCatalogSnapshot("snap:test", map[string]string{"hello": "world"})
	if err := store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	store2, err := db.New(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer store2.Close()
	h2 := &Handlers{Store: store2}
	v, ok := h2.loadCatalogSnapshot("snap:test", func(raw json.RawMessage) (interface{}, error) {
		var m map[string]string
		if err := json.Unmarshal(raw, &m); err != nil {
			return nil, err
		}
		return m, nil
	})
	if !ok {
		t.Fatal("snapshot lost across reopen")
	}
	m, _ := v.(map[string]string)
	if m["hello"] != "world" {
		t.Fatalf("snapshot payload = %v", m)
	}
	// Absent key must read as "no fallback", not an error.
	if _, ok := h2.loadCatalogSnapshot("snap:missing", func(json.RawMessage) (interface{}, error) { return nil, nil }); ok {
		t.Fatal("missing snapshot reported as present")
	}
}
