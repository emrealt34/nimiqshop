// Package memguard hands the memory a quiet process is no longer using back to
// the operating system, instead of keeping it "just in case".
//
// WHY IT EXISTS. The owner's complaint is precise: the backend sits at ~150 MB
// while nothing is happening. Nothing is leaking — the Go runtime simply never
// rushes to give memory back. Three separate mechanisms add up:
//
//  1. THE GO RUNTIME RETURNS MEMORY LAZILY. After a burst of traffic (or a
//     heavy start-up: Badger allocates its memtable arena up front) the heap
//     holds spans that no longer contain live objects. The background
//     scavenger does return them, but it is deliberately gentle — it walks at
//     a small percentage of CPU so it never competes with request handling.
//     On a shop that is busy for a minute and idle for an hour, the gentle
//     path is the slow path: RSS parks at the high-water mark.
//
//  2. RAILWAY (AND FRIENDS) METER cgroup memory.current. That number is
//     anon + page cache + kernel, with nothing subtracted for reclaimable
//     pages. Confirmed by Railway's own answer to a customer question
//     ("memory charged is cgroup memory.current plus swap, sampled every
//     30 s, without discounting inactive_file"). So a process that reads as
//     "150 MB idle" is usually ~50 MB of Go heap plus ~100 MB of kernel page
//     cache the database left behind on the volume — memory the kernel would
//     happily hand back under pressure, but which no one applies pressure to
//     because the container's limit is far away.
//
//  3. NOTHING ASKS FOR IT BACK. That is this package's whole job.
//
// WHAT IT DOES, AND WHAT IT REFUSES TO DO.
//
//   - Runtime layer (always on): when the process has seen no real request for
//     IdleAfter, call debug.FreeOSMemory. That is a full GC plus "scavenge
//     everything now", i.e. the same work the runtime would get around to
//     eventually, done at a moment when the CPU is free. It is skipped while
//     the runtime is holding little beyond its live heap (Sys-HeapAlloc below
//     MinReleasable), so an already-tight process is not forced through a GC
//     every tick for nothing.
//
//   - Page-cache layer (opt-in, MEM_RECLAIM_PAGE_CACHE=true): POSIX_FADV_
//     DONTNEED on the files under the data directory. This drops CLEAN cache
//     pages that the kernel can re-read from disk at any time; it is the only
//     way a process can stop paying the platform's memory meter for a warm
//     read cache it is not using. It is off by default because it trades a
//     few re-reads for the saving, and only the operator knows whether that
//     trade is right for their volume.
//
// SAFETY. FreeOSMemory is stdlib and idempotent. fadvise(DONTNEED) never
// touches dirty pages: the kernel skips them, and Badger has already fsynced
// every commit (SyncWrites=true is this service's default), so no write can be
// lost. Neither layer runs while requests are in flight: both are gated on the
// idle clock, and a single request re-arms that clock.
//
// HEALTH CHECKS DO NOT COUNT AS ACTIVITY. Railway probes /api/health every
// 30 s; counting that as traffic would mean the process is never idle and the
// reclaimer would never run. Health paths are filtered out here for that
// reason (see IsHealthPath).
package memguard

import (
	"context"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"golang.org/x/sys/unix"
)

const (
	// DefaultInterval is how often the guard looks at the clock. Cheap: one
	// atomic read and, at most, a MemStats read.
	DefaultInterval = 30 * time.Second
	// DefaultIdleAfter is how long the process must be quiet before memory is
	// handed back. A minute is longer than any gap between the requests of a
	// browsing visitor, so the reclaimer can never fire in the middle of a
	// session and force a collection a busy process did not need.
	DefaultIdleAfter = 60 * time.Second
	// DefaultMinReleasable is the smallest amount of idle-but-held heap worth
	// a forced collection. Below this the GC cycle costs more than it saves.
	DefaultMinReleasable = 16 << 20
)

// Config tunes the guard. The zero value is not useful; New normalizes.
type Config struct {
	// Interval is the tick period. <= 0 selects DefaultInterval.
	Interval time.Duration
	// IdleAfter is how long with no real request before memory is released.
	// <= 0 selects DefaultIdleAfter.
	IdleAfter time.Duration
	// MinReleasable is the smallest returnable heap worth a forced GC.
	// <= 0 selects DefaultMinReleasable.
	MinReleasable uint64
	// PageCache enables the kernel read-cache release for DataDir.
	PageCache bool
	// DataDir is the directory whose clean page cache may be dropped when
	// PageCache is on (the Badger directory). Empty disables that layer.
	DataDir string
	// Logger receives the one-line report of each release. Nil disables
	// logging (tests use that).
	Logger *log.Logger
}

func (c Config) normalized() Config {
	if c.Interval <= 0 {
		c.Interval = DefaultInterval
	}
	if c.IdleAfter <= 0 {
		c.IdleAfter = DefaultIdleAfter
	}
	if c.MinReleasable == 0 {
		c.MinReleasable = DefaultMinReleasable
	}
	if c.DataDir == "" {
		// No directory named → there is nothing this package is allowed to
		// drop, so the layer is off regardless of what was asked for.
		c.PageCache = false
	}
	return c
}

// Stats is a point-in-time view of what the guard has done, for /api/health and
// the operator console. All counters are monotonic.
type Stats struct {
	Releases        uint64 `json:"releases"`
	ReleasedBytes   uint64 `json:"released_bytes"`
	PageCachePasses uint64 `json:"page_cache_passes"`
	PageCacheFiles  uint64 `json:"page_cache_files"`
	IdleSeconds     int64  `json:"idle_seconds"`
	LastReleaseUnix int64  `json:"last_release_unix,omitempty"`
}

// Guard is the reclaimer. It is safe for concurrent use: the request path calls
// Touch, the background loop calls step.
type Guard struct {
	cfg Config

	// Indirections, so the tests can drive time and observe the calls without
	// sleeping or touching the real runtime.
	now       func() time.Time
	freeOS    func()
	dropAll   func(string) (int, error)
	readStats func(*runtime.MemStats)

	lastTouch  atomic.Int64  // unix nanoseconds of the last REAL request
	lastRun    atomic.Int64  // unix nanos of the last successful runtime release
	lastSys    atomic.Uint64 // Sys observed right after the last release
	cacheErr   atomic.Bool   // the page-cache error has been logged once
	releases   atomic.Uint64
	released   atomic.Uint64
	cacheRuns  atomic.Uint64
	cacheFiles atomic.Uint64
}

// New builds a guard. It does not start anything; call Start (usually as
// `go guard.Start(ctx)`).
func New(cfg Config) *Guard {
	g := &Guard{
		cfg:       cfg.normalized(),
		now:       time.Now,
		freeOS:    debug.FreeOSMemory,
		dropAll:   dropPageCache,
		readStats: runtime.ReadMemStats,
	}
	// Born "just used", so a fresh process gets IdleAfter of quiet before the
	// first release — start-up is exactly when the heap is churning and a
	// forced GC would be wasted work.
	g.lastTouch.Store(g.now().UnixNano())
	return g
}

// Touch marks the process as busy. Called from the HTTP handler for every
// request EXCEPT health probes (see IsHealthPath): a 30-second liveness probe
// must not be able to make an idle shop look busy.
func (g *Guard) Touch() {
	if g == nil {
		return
	}
	g.lastTouch.Store(g.now().UnixNano())
}

// idleFor reports how long the process has been without a real request.
func (g *Guard) idleFor() time.Duration {
	return time.Duration(g.now().UnixNano() - g.lastTouch.Load())
}

// Start runs the reclaimer until ctx is cancelled. Blocking; run it in a
// goroutine.
func (g *Guard) Start(ctx context.Context) {
	if g == nil {
		return
	}
	t := time.NewTicker(g.cfg.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			g.step()
		}
	}
}

// step is one pass: release the runtime's hold on idle heap, then (if enabled)
// the kernel's cache of the data directory. It is a no-op unless the process
// has been quiet for IdleAfter.
func (g *Guard) step() {
	if g == nil {
		return
	}
	if idle := g.idleFor(); idle < g.cfg.IdleAfter {
		return
	}
	g.releaseRuntime()
	g.releasePageCache()
}

// releaseRuntime gives back heap the runtime is holding but not using.
//
// The gate is Sys-HeapAlloc: how much the runtime owns beyond its live
// objects. When that is small there is nothing to win, and forcing a
// collection would only burn CPU, so the pass returns without doing anything.
func (g *Guard) releaseRuntime() {
	var before runtime.MemStats
	g.readStats(&before)
	// What the runtime is holding that is not live objects: freed spans,
	// per-P caches, stack memory, GC bookkeeping. That is the pool
	// FreeOSMemory draws from — measured, not guessed: a test process sitting
	// on a 2 MB "idle" reading still gave back 10 MB of RSS once the forced
	// release ran, because most of the win is outside HeapIdle.
	held := uint64(0)
	if before.Sys > before.HeapAlloc {
		held = before.Sys - before.HeapAlloc
	}
	if held < g.cfg.MinReleasable {
		return
	}
	// Back-off: a forced collection returns what it can, but some of Sys is
	// not returnable at all (goroutine stacks, GC bookkeeping, the database's
	// live memtable). Without this, a process whose floor sits above
	// MinReleasable would run — and log — a pointless collection every tick for
	// the rest of its life. A release only happens again once the process has
	// grown by half the threshold since the last one.
	if prev := g.lastSys.Load(); prev != 0 && before.Sys < prev+g.cfg.MinReleasable/2 {
		return
	}
	rssBefore := RSSBytes()

	g.freeOS()

	var after runtime.MemStats
	g.readStats(&after)
	// The number the platform meters is RSS, so that is what is reported as
	// "released". Falling back to the Sys delta keeps the counter honest on a
	// platform without /proc.
	released := uint64(0)
	if rssAfter := RSSBytes(); rssBefore > rssAfter {
		released = rssBefore - rssAfter
	} else if before.Sys > after.Sys {
		released = before.Sys - after.Sys
	}
	g.releases.Add(1)
	g.released.Add(released)
	g.lastRun.Store(g.now().UnixNano())
	g.lastSys.Store(after.Sys)
	if g.cfg.Logger != nil {
		g.cfg.Logger.Printf(
			"memguard: idle %s — rss %s -> %s (handed back %s; held %s, heap live %s, sys %s)",
			g.idleFor().Truncate(time.Second), humanBytes(rssBefore), humanBytes(RSSBytes()),
			humanBytes(released), humanBytes(held), humanBytes(after.HeapAlloc), humanBytes(after.Sys))
	}
}

// releasePageCache drops the kernel's clean cache pages for the data
// directory. Only runs when the operator opted in AND the files live under
// their own data directory — this is not a licence to touch the binary, the CA
// bundle or anything else this process happens to have read.
func (g *Guard) releasePageCache() {
	if !g.cfg.PageCache || g.cfg.DataDir == "" {
		return
	}
	n, err := g.dropAll(g.cfg.DataDir)
	if n > 0 {
		g.cacheRuns.Add(1)
		g.cacheFiles.Add(uint64(n))
	}
	if err != nil && g.cfg.Logger != nil && g.cacheErr.CompareAndSwap(false, true) {
		// Logged once: a volume that cannot be walked would otherwise repeat
		// the same line every tick for as long as the shop is idle.
		g.cfg.Logger.Printf("memguard: page-cache release stopped early after %d files: %v", n, err)
	}
}

// Stats reports the counters shown by /api/health.
func (g *Guard) Stats() Stats {
	if g == nil {
		return Stats{}
	}
	return Stats{
		Releases:        g.releases.Load(),
		ReleasedBytes:   g.released.Load(),
		PageCachePasses: g.cacheRuns.Load(),
		PageCacheFiles:  g.cacheFiles.Load(),
		IdleSeconds:     int64(g.idleFor() / time.Second),
		LastReleaseUnix: g.lastRun.Load() / int64(time.Second),
	}
}

// IsHealthPath reports whether a request path is machine liveness traffic
// rather than a visitor. Those must not reset the idle clock, or a platform
// that probes every 30 s would keep the reclaimer from ever running.
func IsHealthPath(path []byte) bool {
	// A short length gate first (no allocation), then an exact match. The
	// three paths are all 8-11 bytes, so anything longer than the longest one
	// is answered without converting to a string at all.
	if len(path) < 8 || len(path) > 11 {
		return false
	}
	switch string(path) {
	case "/api/health", "/healthz", "/_health":
		return true
	}
	return false
}

/* --------------------------- platform readings ---------------------------- */

// Usage is what the process and its cgroup are actually using right now. It
// exists so /api/health can answer "where do the 150 MB go?" with numbers
// instead of a theory: anon is ours, file is the kernel's cache of the volume,
// and the two together are what the hosting dashboard meters.
type Usage struct {
	RSSBytes        uint64 `json:"rss_bytes"`
	CgroupBytes     uint64 `json:"cgroup_used_bytes"`
	CgroupAnonBytes uint64 `json:"cgroup_anon_bytes"`
	CgroupFileBytes uint64 `json:"cgroup_file_bytes"`
	CgroupLimit     int64  `json:"cgroup_limit_bytes"`
	Source          string `json:"source,omitempty"`
}

// ReadUsage samples the process RSS and, when available, the cgroup's own
// accounting. Every field is best-effort: a platform that hides /proc simply
// reports zeroes rather than failing the health endpoint.
func ReadUsage() Usage {
	u := Usage{RSSBytes: RSSBytes()}
	for _, p := range []string{"/sys/fs/cgroup/memory.current", "/sys/fs/cgroup/memory/memory.usage_in_bytes"} {
		if v, ok := readUint(p); ok {
			u.CgroupBytes = v
			u.Source = "cgroup"
			break
		}
	}
	// The anon/file split is only meaningful next to a real cgroup boundary:
	// on a bare host memory.stat describes the MACHINE, and reporting that as
	// "this container's" usage would be a lie in the operator's dashboard.
	if u.Source != "" {
		reportCgroupSplit(&u)
	}
	if v, ok := readUint("/sys/fs/cgroup/memory.max"); ok && v < unlimitedSentinel {
		u.CgroupLimit = int64(v)
	} else if v, ok := readUint("/sys/fs/cgroup/memory/memory.limit_in_bytes"); ok && v < unlimitedSentinel {
		u.CgroupLimit = int64(v)
	}
	return u
}

// reportCgroupSplit fills the anon/file breakdown from memory.stat (v2) or the
// v1 equivalent.
func reportCgroupSplit(u *Usage) {
	if stat, err := os.ReadFile("/sys/fs/cgroup/memory.stat"); err == nil {
		for _, line := range strings.Split(string(stat), "\n") {
			key, rest, found := strings.Cut(line, " ")
			if !found {
				continue
			}
			switch key {
			case "anon", "total_rss":
				u.CgroupAnonBytes, _ = strconv.ParseUint(strings.TrimSpace(rest), 10, 64)
			case "file", "total_cache":
				u.CgroupFileBytes, _ = strconv.ParseUint(strings.TrimSpace(rest), 10, 64)
			}
		}
	}
}

// unlimitedSentinel matches the cgroup's "no cap" values (v2 writes the word
// "max", v1 a number near 2^63).
const unlimitedSentinel = uint64(1) << 62

// RSSBytes reads this process's resident set size from /proc. Zero when the
// platform does not expose it.
func RSSBytes() uint64 {
	raw, err := os.ReadFile("/proc/self/statm")
	if err != nil {
		return 0
	}
	fields := strings.Fields(string(raw))
	if len(fields) < 2 {
		return 0
	}
	pages, err := strconv.ParseUint(fields[1], 10, 64)
	if err != nil {
		return 0
	}
	return pages * uint64(os.Getpagesize())
}

func readUint(path string) (uint64, bool) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	v, err := strconv.ParseUint(strings.TrimSpace(string(raw)), 10, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

/* ----------------------------- page-cache drop ---------------------------- */

// dropPageCache walks dir and asks the kernel to forget the clean cache pages
// of every regular file in it.
//
// Why this is safe on a live database: POSIX_FADV_DONTNEED is advisory and, on
// Linux, only ever discards CLEAN pages — dirty ones are written back first and
// then skipped, never lost. Badger runs with SyncWrites=true here, so every
// committed write is already fsynced and the pages it leaves behind are pure
// cache. A page dropped now is re-read from the block device the next time the
// database touches it, which for a shop-sized database is a few milliseconds.
func dropPageCache(dir string) (int, error) {
	dropped := 0
	var firstErr error
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			return nil
		}
		if d.IsDir() || !d.Type().IsRegular() {
			return nil
		}
		fd, openErr := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC, 0)
		if openErr != nil {
			if firstErr == nil {
				firstErr = openErr
			}
			return nil
		}
		advErr := unix.Fadvise(fd, 0, 0, unix.FADV_DONTNEED)
		_ = unix.Close(fd)
		if advErr != nil {
			if firstErr == nil {
				firstErr = advErr
			}
			return nil
		}
		dropped++
		return nil
	})
	if err != nil && firstErr == nil {
		firstErr = err
	}
	return dropped, firstErr
}

// humanBytes formats a byte count for the log line, matching the style of the
// rest of the service's operator output (MB with one decimal).
func humanBytes(v uint64) string {
	const mb = 1 << 20
	if v < mb {
		return strconv.FormatUint(v/1024, 10) + " KB"
	}
	whole := v / mb
	frac := (v % mb) * 10 / mb
	return strconv.FormatUint(whole, 10) + "." + strconv.FormatUint(frac, 10) + " MB"
}
