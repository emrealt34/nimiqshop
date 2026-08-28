package middleware

import (
	"nimiqshop/internal/clientip"
	"sync"
	"time"

	"github.com/valyala/fasthttp"
)

// RateLimiter is an in-process, IP keyed token bucket. It resolves the real
// client IP the same way as the rest of the API: when trustProxy is on it uses
// Cloudflare's CF-Connecting-IP / X-Forwarded-For, otherwise the TCP peer.
// Deploy a shared WAF/rate limiter when running more than one pod.
type RateLimiter struct {
	mu         sync.Mutex
	clients    map[string]*bucket
	rate       float64
	burst      float64
	trustProxy bool
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
	return &RateLimiter{clients: make(map[string]*bucket), rate: float64(perMinute) / 60, burst: float64(burst), trustProxy: tp}
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
func (r *RateLimiter) allow(ip string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	b := r.clients[ip]
	if b == nil {
		if len(r.clients) >= 10000 {
			for k, v := range r.clients {
				if now.Sub(v.seen) > 10*time.Minute {
					delete(r.clients, k)
				}
			}
			if len(r.clients) >= 10000 {
				return false
			}
		}
		b = &bucket{tokens: r.burst, seen: now}
		r.clients[ip] = b
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
func min(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}
