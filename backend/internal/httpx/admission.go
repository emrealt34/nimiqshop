package httpx

import (
	"runtime"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/valyala/fasthttp"
)

// Admission is a bounded in-flight request counter — the cheapest possible
// load shedder, and the one that keeps a flood from turning into an outage.
//
// Why it exists: fasthttp's Concurrency cap was raised to 1,000,000 so the
// accept loop could not wedge, which also means the process would happily
// hold a million live handlers. Each one owns request/response buffers, a
// Badger transaction or a supplier queue slot. Under a sustained flood the
// box dies of memory exhaustion — taking every legitimate shopper with it.
//
// Admission answers that with a counter, not a queue:
//
//   - The check is two atomic operations. A rejected request costs
//     microseconds and never touches the database, the catalog cache or the
//     supplier client, so the shed path stays cheap even at 500k rps.
//   - The ceiling is deliberately generous (see DefaultMaxInFlight) so a
//     normal visitor — or ten thousand of them — never observes it. It only
//     binds when the process is already past the point where it could serve
//     everyone well.
//   - Rejections are 503 + Retry-After, the honest signal: a browser or a
//     well-behaved client backs off instead of hammering harder.
//
// Health endpoints are exempt. A load balancer or an operator poking
// /api/health during an incident must still get an answer, otherwise the
// shedder hides the very thing it is protecting against.
type Admission struct {
	inFlight  atomic.Int64
	rejected  atomic.Int64
	admitted  atomic.Int64
	max       int64
	exemptFn  func(path []byte) bool
	retryWait string
}

// AdmissionConfig tunes the shedder.
type AdmissionConfig struct {
	// MaxInFlight is the ceiling on simultaneously executing handlers.
	// <= 0 selects DefaultMaxInFlight.
	MaxInFlight int
	// RetryAfterSeconds is advertised on a shed response.
	RetryAfterSeconds int
}

// DefaultMaxInFlight sizes the ceiling from the CPU count. The multiplier is
// high on purpose: handlers spend most of their wall-clock time waiting (on
// Badger, on the supplier, on the oracle), so a healthy process legitimately
// holds many more concurrent requests than it has cores. The number is a
// cliff edge, not a throughput target.
func DefaultMaxInFlight() int {
	cpus := runtime.NumCPU()
	if cpus < 1 {
		cpus = 1
	}
	n := cpus * 4096
	if n < 8192 {
		n = 8192
	}
	// Absolute ceiling: past this the memory footprint of the buffers alone
	// is the problem, regardless of how many cores the box has.
	if n > 262144 {
		n = 262144
	}
	return n
}

// NewAdmission builds the shedder.
func NewAdmission(cfg AdmissionConfig) *Admission {
	max := int64(cfg.MaxInFlight)
	if max <= 0 {
		max = int64(DefaultMaxInFlight())
	}
	wait := cfg.RetryAfterSeconds
	if wait <= 0 {
		wait = 1
	}
	return &Admission{
		max:       max,
		retryWait: strconv.Itoa(wait),
		exemptFn:  isExemptPath,
	}
}

// isExemptPath keeps liveness/readiness probes answerable under load.
func isExemptPath(path []byte) bool {
	switch len(path) {
	case len("/api/health"):
		return string(path) == "/api/health"
	case len("/healthz"):
		return string(path) == "/healthz"
	}
	return false
}

// Limit wraps next with the in-flight ceiling.
func (a *Admission) Limit(next fasthttp.RequestHandler) fasthttp.RequestHandler {
	if a == nil {
		return next
	}
	retry := []byte(a.retryWait)
	return func(ctx *fasthttp.RequestCtx) {
		if a.exemptFn(ctx.Path()) {
			next(ctx)
			return
		}
		cur := a.inFlight.Add(1)
		if cur > a.max {
			a.inFlight.Add(-1)
			a.rejected.Add(1)
			ctx.Response.Header.SetBytesV("Retry-After", retry)
			ctx.Response.Header.Set("Cache-Control", "no-store")
			ctx.SetStatusCode(fasthttp.StatusServiceUnavailable)
			ctx.SetContentType("application/json")
			ctx.SetBodyString(`{"error":"the shop is extremely busy right now","code":"SERVER_BUSY","retry_after_seconds":` + a.retryWait + `}`)
			return
		}
		a.admitted.Add(1)
		defer a.inFlight.Add(-1)
		next(ctx)
	}
}

// Stats is a point-in-time snapshot for /api/health and operator dashboards.
type AdmissionStats struct {
	InFlight int64 `json:"in_flight"`
	Max      int64 `json:"max_in_flight"`
	Admitted int64 `json:"admitted_total"`
	Rejected int64 `json:"rejected_total"`
}

// Stats reports the counters. Cheap enough to call on every health check.
func (a *Admission) Stats() AdmissionStats {
	if a == nil {
		return AdmissionStats{}
	}
	return AdmissionStats{
		InFlight: a.inFlight.Load(),
		Max:      a.max,
		Admitted: a.admitted.Load(),
		Rejected: a.rejected.Load(),
	}
}

// Utilisation is in-flight / ceiling, 0..1+. Used by the health endpoint so
// an operator can see the shedder approaching its cliff before it trips.
func (a *Admission) Utilisation() float64 {
	if a == nil || a.max <= 0 {
		return 0
	}
	return float64(a.inFlight.Load()) / float64(a.max)
}

/* --------------------------- slow-client defence -------------------------- */

// TimeoutGuard bounds how long a single request may occupy a handler.
//
// fasthttp's ReadTimeout/WriteTimeout already stop a stuck socket, but they
// do not stop a handler that is legitimately slow because a dependency is
// slow. This wraps the whole chain in a deadline that is enforced by the
// caller's own context plumbing; here it only records and sheds requests that
// have already been waiting longer than the budget when they arrive, which
// is the signature of a slowloris-style drip.
type TimeoutGuard struct {
	maxQueueWait time.Duration
	rejected     atomic.Int64
}

// NewTimeoutGuard builds the guard. maxQueueWait <= 0 disables it.
func NewTimeoutGuard(maxQueueWait time.Duration) *TimeoutGuard {
	return &TimeoutGuard{maxQueueWait: maxQueueWait}
}

// Limit rejects a request whose connection has already been idle-reading for
// longer than the budget — i.e. the client is dribbling bytes to hold a
// worker. fasthttp exposes this as the time since the connection was accepted.
func (g *TimeoutGuard) Limit(next fasthttp.RequestHandler) fasthttp.RequestHandler {
	if g == nil || g.maxQueueWait <= 0 {
		return next
	}
	return func(ctx *fasthttp.RequestCtx) {
		if d := time.Until(ctx.Time()); d < -g.maxQueueWait {
			g.rejected.Add(1)
			ctx.SetStatusCode(fasthttp.StatusRequestTimeout)
			ctx.SetContentType("application/json")
			ctx.SetBodyString(`{"error":"request timed out","code":"SLOW_CLIENT"}`)
			return
		}
		next(ctx)
	}
}

// Rejected reports how many slow-client requests were shed.
func (g *TimeoutGuard) Rejected() int64 {
	if g == nil {
		return 0
	}
	return g.rejected.Load()
}
