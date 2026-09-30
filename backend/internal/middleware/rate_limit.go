package middleware

import (
	"nimiqshop/internal/clientip"
	"sync"
	"sync/atomic"
	"time"

	"github.com/valyala/fasthttp"

	"hash/fnv"
)

// RateLimiter is an in-process, IP keyed token bucket built for six-figure
// concurrent clients. It resolves the real client IP the same way as the
// rest of the API: when trustProxy is on it uses Cloudflare's
// CF-Connecting-IP / X-Forwarded-For, otherwise the TCP peer.
//
// Scale design (the old single-mutex map stalled at ~10k IPs and — far
// worse — answered 429 to every NEW client once the map filled, a self-DoS
// an IP-rotating attacker could trigger at will):
//
//   - 256 independent shards, each with its own mutex: request-path lock
//     contention is divided by 256 instead of serializing every request
//     behind one lock.
//   - up to maxBuckets total buckets (1M) — enough for 100k+ live clients
//     with 10x headroom for IPv6 churn.
//   - a FULL map never rejects a new client: the shard evicts its
//     longest-idle bucket instead. Under a genuine >1M-IP flood the limiter
//     keeps protecting the API rather than locking the door.
//   - opportunistic idle sweep (buckets idle > maxIdle are dropped), so
//     memory tracks ACTIVE clients, not lifetime unique IPs.
type RateLimiter struct {
	shards     [rateShards]rateShard
	rate       float64
	burst      float64
	trustProxy bool
	live       atomic.Int64 // current bucket count across all shards
}

const (
	rateShards = 256
	// maxBuckets bounds worst-case memory: a bucket is ~48 bytes of struct
	// plus its key; 1M buckets stay well under 150 MB.
	maxBuckets = 1_000_000
	// maxIdle is how long an untouched bucket survives a sweep.
	maxIdle = 30 * time.Minute
)

type rateShard struct {
	mu        sync.Mutex
	clients   map[string]*bucket
	lastSweep atomic.Value // time.Time
}

type bucket struct {
	tokens float64
	seen   time.Time
}

func NewRateLimiter(perMinute, burst int, trustProxy ...bool) *RateLimiter {
	if perMinute <= 0 {
		perMinute = 60
	}
	if burst <= 0 {
		burst = 20
	}
	tp := false
	if len(trustProxy) > 0 {
		tp = trustProxy[0]
	}
	r := &RateLimiter{rate: float64(perMinute) / 60, burst: float64(burst), trustProxy: tp}
	for i := range r.shards {
		r.shards[i].clients = make(map[string]*bucket)
	}
	return r
}

func (r *RateLimiter) Limit(next fasthttp.RequestHandler) fasthttp.RequestHandler {
	return func(ctx *fasthttp.RequestCtx) {
		ip := clientip.Resolve(ctx, r.trustProxy).IP
		if !r.allow(ip) {
			ctx.Response.Header.Set("Retry-After", "60")
			ctx.Error(`{"error":"rate limit exceeded"}`, fasthttp.StatusTooManyRequests)
			return
		}
		next(ctx)
	}
}

func (r *RateLimiter) shardFor(ip string) *rateShard {
	h := fnv.New32a()
	_, _ = h.Write([]byte(ip))
	return &r.shards[h.Sum32()%rateShards]
}

func (r *RateLimiter) allow(ip string) bool {
	if ip == "" {
		// Unattributable requests (broken context) must not share one
		// global bucket nor bypass the limiter: bucket them together.
		ip = "?"
	}
	s := r.shardFor(ip)
	now := time.Now()

	s.mu.Lock()
	defer s.mu.Unlock()
	b := s.clients[ip]
	if b == nil {
		// Sweep cheaply on the growth path only.
		if r.live.Load() >= maxBuckets {
			r.evictOldest(s, now)
		}
		if len(s.clients) >= rateShardSweepSize && s.sweepDue(now) {
			for k, v := range s.clients {
				if now.Sub(v.seen) > maxIdle {
					delete(s.clients, k)
					r.live.Add(-1)
				}
			}
		}
		b = &bucket{tokens: r.burst, seen: now}
		s.clients[ip] = b
		r.live.Add(1)
	}
	elapsed := now.Sub(b.seen).Seconds()
	b.tokens = min(r.burst, b.tokens+elapsed*r.rate)
	b.seen = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// evictOldest drops the single longest-idle bucket in the shard so a brand
// new client is never refused just because the map is at capacity.
func (r *RateLimiter) evictOldest(s *rateShard, now time.Time) {
	var oldestKey string
	oldest := now
	for k, v := range s.clients {
		if v.seen.Before(oldest) {
			oldest = v.seen
			oldestKey = k
		}
	}
	if oldestKey != "" {
		delete(s.clients, oldestKey)
		r.live.Add(-1)
	}
}

const (
	rateShardSweepSize = 8 * 1024
	sweepInterval      = time.Minute
)

// sweepDue is a tiny atomic-typed helper: true at most once per interval.
func (sh *rateShard) sweepDue(now time.Time) bool {
	last, _ := sh.lastSweep.Load().(time.Time)
	if now.Sub(last) <= sweepInterval {
		return false
	}
	sh.lastSweep.Store(now)
	return true
}

func min(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}
