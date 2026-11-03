package memguard

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"
)

// clock is a manually advanced clock so the tests never sleep.
type clock struct{ nanos atomic.Int64 }

func (c *clock) now() time.Time { return time.Unix(0, c.nanos.Load()) }
func (c *clock) advance(d time.Duration) {
	c.nanos.Add(int64(d))
}

// newTestGuard builds a guard with a manual clock, a fake FreeOSMemory and a
// fake MemStats reader, so the release path can be exercised deterministically
// instead of waiting for a real allocation pattern.
func newTestGuard(t *testing.T, cfg Config) (*Guard, *clock, *atomic.Int64) {
	t.Helper()
	return newTestGuardSized(t, cfg, 64<<20, 24<<20)
}

// newTestGuardSized is newTestGuard with an explicit "how much the runtime is
// holding beyond its live heap" size and "how much the release frees" size.
func newTestGuardSized(t *testing.T, cfg Config, held, released uint64) (*Guard, *clock, *atomic.Int64) {
	t.Helper()
	g := New(cfg)
	c := &clock{}
	c.nanos.Store(g.now().UnixNano())
	freed := &atomic.Int64{}
	g.now = c.now
	g.freeOS = func() { freed.Add(1) }
	// A stateful fake runtime: Sys only moves when a release actually frees
	// something, exactly like the real one (FreeOSMemory madvises pages away
	// rather than shrinking Sys metadata).
	const heapLive = uint64(20 << 20)
	sys := heapLive + held
	g.readStats = func(ms *runtime.MemStats) {
		ms.HeapAlloc = heapLive
		ms.HeapIdle = 0
		ms.HeapReleased = 0
		ms.Sys = sys
	}
	g.freeOS = func() {
		freed.Add(1)
		if sys > released {
			sys -= released
		}
	}
	return g, c, freed
}

func TestNewNormalizesDefaults(t *testing.T) {
	g := New(Config{PageCache: true}) // no DataDir → the cache layer stays off
	if g.cfg.Interval != DefaultInterval {
		t.Fatalf("interval = %s, want %s", g.cfg.Interval, DefaultInterval)
	}
	if g.cfg.IdleAfter != DefaultIdleAfter {
		t.Fatalf("idleAfter = %s, want %s", g.cfg.IdleAfter, DefaultIdleAfter)
	}
	if g.cfg.MinReleasable != DefaultMinReleasable {
		t.Fatalf("minReleasable = %d, want %d", g.cfg.MinReleasable, DefaultMinReleasable)
	}
	if g.cfg.PageCache || g.cfg.DataDir != "" {
		t.Fatal("page-cache layer must stay off without a data dir")
	}
}

func TestNilGuardIsSafe(t *testing.T) {
	var g *Guard
	g.Touch()
	g.step()
	if s := g.Stats(); s != (Stats{}) {
		t.Fatalf("nil guard stats = %+v", s)
	}
	done := make(chan struct{})
	go func() { g.Start(context.Background()); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("nil guard Start blocked")
	}
}

// A busy process must never be forced through a collection: the whole point is
// to spend CPU only when nobody is waiting for an answer.
func TestBusyProcessIsNeverCollected(t *testing.T) {
	g, c, freed := newTestGuard(t, Config{IdleAfter: time.Minute, MinReleasable: 1})
	c.advance(30 * time.Second)
	g.Touch() // 30 s of quiet, then a request
	c.advance(30 * time.Second)
	g.step()
	if freed.Load() != 0 {
		t.Fatal("collected while recently active")
	}
	// Idle clock runs from the last real request, not from the last tick.
	c.advance(31 * time.Second)
	g.step()
	if freed.Load() == 0 {
		t.Fatal("did not collect after the idle window")
	}
}

// With almost nothing returnable there is nothing to win, and a forced GC would
// be pure cost.
func TestNoCollectionWhenNothingToRelease(t *testing.T) {
	g, c, freed := newTestGuardSized(t, Config{IdleAfter: time.Second, MinReleasable: 1 << 20}, 4<<10, 0)
	c.advance(2 * time.Second)
	g.step()
	if freed.Load() != 0 {
		t.Fatal("collected with an unreachable threshold")
	}
}

func TestCollectionCountsWhatItReleased(t *testing.T) {
	g, c, freed := newTestGuard(t, Config{IdleAfter: time.Second, MinReleasable: 1})
	c.advance(2 * time.Second)
	g.step()
	if freed.Load() != 1 {
		t.Fatalf("freeOS calls = %d, want 1", freed.Load())
	}
	s := g.Stats()
	if s.Releases != 1 {
		t.Fatalf("releases = %d, want 1", s.Releases)
	}
	if s.LastReleaseUnix == 0 {
		t.Fatal("last release timestamp not recorded")
	}
	if s.IdleSeconds < 2 {
		t.Fatalf("idle seconds = %d, want >= 2", s.IdleSeconds)
	}
	if s.ReleasedBytes != 24<<20 {
		t.Fatalf("released bytes = %d, want %d", s.ReleasedBytes, uint64(24<<20))
	}
}

func TestPageCacheLayerIsOptIn(t *testing.T) {
	dir := t.TempDir()
	g, c, _ := newTestGuardSized(t, Config{IdleAfter: time.Second, MinReleasable: 1 << 20, DataDir: dir}, 4<<10, 0)
	var calls atomic.Int64
	g.dropAll = func(string) (int, error) { calls.Add(1); return 3, nil }
	c.advance(2 * time.Second)
	g.step()
	if calls.Load() != 0 {
		t.Fatal("page cache dropped without MEM_RECLAIM_PAGE_CACHE")
	}
	if g.Stats().PageCachePasses != 0 {
		t.Fatal("page cache pass counted while disabled")
	}
}

func TestPageCacheLayerRunsWhenEnabled(t *testing.T) {
	dir := t.TempDir()
	g, c, _ := newTestGuardSized(t, Config{IdleAfter: time.Second, MinReleasable: 1 << 20, PageCache: true, DataDir: dir}, 4<<10, 0)
	got := ""
	g.dropAll = func(d string) (int, error) { got = d; return 5, errors.New("one file locked") }
	c.advance(2 * time.Second)
	g.step()
	if got != dir {
		t.Fatalf("dropped page cache for %q, want %q", got, dir)
	}
	s := g.Stats()
	if s.PageCachePasses != 1 || s.PageCacheFiles != 5 {
		t.Fatalf("stats = %+v", s)
	}
}

func TestDropPageCacheTouchesOnlyRealFiles(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "00001.sst"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "nested"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "nested", "MANIFEST"), []byte("y"), 0o600); err != nil {
		t.Fatal(err)
	}
	n, err := dropPageCache(dir)
	if err != nil {
		t.Fatalf("dropPageCache: %v", err)
	}
	if n != 2 {
		t.Fatalf("dropped %d files, want 2", n)
	}
	// A missing directory reports the problem instead of panicking.
	if _, err := dropPageCache(filepath.Join(dir, "nope")); err == nil {
		t.Fatal("expected an error for a missing directory")
	}
}

func TestIsHealthPath(t *testing.T) {
	yes := []string{"/api/health", "/healthz", "/_health"}
	for _, p := range yes {
		if !IsHealthPath([]byte(p)) {
			t.Fatalf("%q must not count as traffic", p)
		}
	}
	no := []string{"/api/health/", "/api/catalog/brands", "/api/healthz", "/", ""}
	for _, p := range no {
		if IsHealthPath([]byte(p)) {
			t.Fatalf("%q must count as traffic", p)
		}
	}
}

func TestPlatformReadingsAreBestEffort(t *testing.T) {
	// Neither call may panic or block on an exotic platform; zero is a fine
	// answer for "the kernel did not say".
	_ = RSSBytes()
	u := ReadUsage()
	if u.CgroupBytes != 0 && u.CgroupAnonBytes+u.CgroupFileBytes == 0 {
		t.Skip("cgroup v1 accounting without anon/file split")
	}
}

func TestStartStopsWithContext(t *testing.T) {
	g := New(Config{Interval: 5 * time.Millisecond, IdleAfter: time.Millisecond, MinReleasable: 1})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { g.Start(ctx); close(done) }()
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Start did not stop after cancel")
	}
}

// A process whose Sys floor sits above the threshold must not run a pointless
// collection (and log a line) on every tick forever: the second release has to
// wait for real growth.
func TestNoRepeatCollectionWithoutGrowth(t *testing.T) {
	g, c, freed := newTestGuardSized(t, Config{IdleAfter: time.Second, MinReleasable: 8 << 20}, 40<<20, 4<<20)
	c.advance(2 * time.Second)
	g.step()
	if freed.Load() != 1 {
		t.Fatalf("freeOS calls = %d, want 1", freed.Load())
	}
	// Same numbers again, a full tick later: nothing new to release.
	c.advance(30 * time.Second)
	g.step()
	if freed.Load() != 1 {
		t.Fatalf("freeOS calls = %d after an unchanged tick, want 1", freed.Load())
	}
	if g.Stats().Releases != 1 {
		t.Fatalf("releases = %d, want 1", g.Stats().Releases)
	}
}

// Once the process has grown past the back-off window, it is worth another
// look — otherwise memory could climb back to the high-water mark unnoticed.
func TestCollectionResumesAfterGrowth(t *testing.T) {
	g, c, freed := newTestGuardSized(t, Config{IdleAfter: time.Second, MinReleasable: 8 << 20}, 40<<20, 4<<20)
	c.advance(2 * time.Second)
	g.step()
	if freed.Load() != 1 {
		t.Fatalf("first release did not happen")
	}
	// Grow the heap by 8 MB (>= MinReleasable/2) with no requests, then idle.
	g.readStats = func(ms *runtime.MemStats) {
		ms.HeapAlloc = 20 << 20
		ms.Sys = 20<<20 + 48<<20
	}
	c.advance(30 * time.Second)
	g.step()
	if freed.Load() != 2 {
		t.Fatalf("freeOS calls = %d after growth, want 2", freed.Load())
	}
}
