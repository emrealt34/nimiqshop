package cryptorefills

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestGetCacheCollapsesConcurrentFetches: identical GETs collapse into one
// upstream call — the webhook + tracker + refresh coalescing guarantee.
func TestGetCacheCollapsesConcurrentFetches(t *testing.T) {
	var c Client
	var calls int32
	var wg sync.WaitGroup
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, err := c.gets.do(50*time.Millisecond, "GET /v5/orders/o1", func() (interface{}, error) {
				atomic.AddInt32(&calls, 1)
				time.Sleep(15 * time.Millisecond)
				return "order", nil
			})
			if err != nil || v != "order" {
				t.Errorf("got %v, %v", v, err)
			}
		}()
	}
	wg.Wait()
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("upstream calls = %d, want 1", got)
	}
}

// TestGetCacheExpiresAndNeverCachesErrors: after TTL the fetch runs again;
// failures are never stored.
func TestGetCacheExpiresAndNeverCachesErrors(t *testing.T) {
	var c Client
	var calls int32
	fetch := func() (interface{}, error) {
		atomic.AddInt32(&calls, 1)
		if atomic.LoadInt32(&calls) == 1 {
			return nil, errors.New("transient upstream 429")
		}
		return "ok", nil
	}
	if _, err := c.gets.do(time.Hour, "k", fetch); err == nil {
		t.Fatal("first call must surface the error")
	}
	v, err := c.gets.do(time.Hour, "k", fetch)
	if err != nil || v != "ok" {
		t.Fatalf("error was cached: v=%v err=%v", v, err)
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("calls = %d, want 2 (error must not be cached)", got)
	}
	// Expiry: a short TTL re-fetches.
	calls = 0
	if _, err := c.gets.do(5*time.Millisecond, "t", func() (interface{}, error) {
		atomic.AddInt32(&calls, 1)
		return "v1", nil
	}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(15 * time.Millisecond)
	if _, err := c.gets.do(5*time.Millisecond, "t", func() (interface{}, error) {
		atomic.AddInt32(&calls, 1)
		return "v2", nil
	}); err != nil {
		t.Fatal(err)
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("TTL expiry ignored: calls = %d, want 2", got)
	}
}
