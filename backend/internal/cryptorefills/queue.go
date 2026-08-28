package cryptorefills

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"nimiqshop/internal/safe"
)

// The CryptoRefills partner key is shared by the whole application. A
// limiter attached to an HTTP handler is not enough: catalog traffic,
// quote validation, order creation, fulfillment polling and webhook
// verification all use the same partner account. Scheduler is therefore
// the single admission point for every CryptoRefills request made by
// Client.
var ErrQueueFull = errors.New("cryptorefills global queue is full")

// ErrBudgetWait is returned at admission time when the next endpoint/actor
// budget slot is further away than MaxAdmissionWait: the request is
// rejected fast (429 semantics) instead of being queued for a long window
// wait and later killed by the caller's context. Fast rejection keeps the
// queue shallow under load, bounds handler latency, and lets clients retry
// (with idempotency keys where available) instead of hanging.
//
// NOTE: despite the name, this is OUR OWN local admission budget, not the
// supplier's rate limit. The message says so explicitly — the operator log
// previously read "supplier budget exhausted", which sent everyone chasing
// a supplier outage while the local scheduler was the one rejecting.
var ErrBudgetWait = errors.New("cryptorefills local queue budget reached (our own admission limit, supplier is fine); retrying with backoff")

// QueueConfig controls the process-wide supplier queue. The defaults are
// deliberately conservative so clock skew and a second application
// instance cannot immediately exhaust the partner account.
type QueueConfig struct {
	MaxQueue            int
	MaxQueuePerActor    int
	ActorRequestsPerMin int
	ActorBurst          int
	// MaxAdmissionWait bounds how long the next budget slot may be for a
	// request to be admitted at all; beyond that the call is rejected with
	// ErrBudgetWait. Non-positive values take the default (5s). Keeping this
	// small is what makes the queue drain quickly under load: rejected calls
	// fail fast instead of parking in the queue until their caller context
	// fires.
	MaxAdmissionWait time.Duration
}

func defaultQueueConfig() QueueConfig {
	return QueueConfig{
		MaxQueue:            2000,
		MaxQueuePerActor:    100,
		ActorRequestsPerMin: 30,
		ActorBurst:          6,
		MaxAdmissionWait:    5 * time.Second,
	}
}

// actorKey is intentionally private. Callers can only set it through
// WithActor, so an HTTP request cannot accidentally treat a user supplied
// header as an identity.
type actorKey struct{}

// WithActor associates a stable application identity with a supplier call.
// Authenticated handlers pass user:<user-id>; public handlers pass ip:<peer>;
// workers pass system:<worker>. The scheduler uses it for fair round-robin
// service and a second, per-actor budget. It is not an authorization claim.
func WithActor(ctx context.Context, actor string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if strings.TrimSpace(actor) == "" {
		actor = "anonymous"
	}
	return context.WithValue(ctx, actorKey{}, actor)
}

func actorFromContext(ctx context.Context) string {
	if ctx != nil {
		if v, ok := ctx.Value(actorKey{}).(string); ok && v != "" {
			return v
		}
	}
	return "anonymous"
}

// endUserIPKey carries the end user's IP for the X-Forwarded-For header.
type endUserIPKey struct{}

// WithEndUserIP attaches the customer's IP (required by the supplier API).
func WithEndUserIP(ctx context.Context, ip string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, endUserIPKey{}, ip)
}

func endUserIPFromContext(ctx context.Context) string {
	if ctx != nil {
		if v, ok := ctx.Value(endUserIPKey{}).(string); ok && v != "" {
			return v
		}
	}
	return ""
}

type endpointPolicy struct {
	name   string
	limit  int
	window time.Duration
}

// policyFor maps the documented routes to independent rolling windows. The
// query string is ignored. Unknown routes are still admitted through a
// small conservative bucket, so adding a client method can never bypass
// the queue.
func policyFor(method, rawPath string) endpointPolicy {
	method = strings.ToUpper(method)
	path := rawPath
	if i := strings.IndexByte(path, '?'); i >= 0 {
		path = path[:i]
	}
	path = strings.Trim(path, "/")
	parts := strings.Split(path, "/")
	p := func(name string, limit int, window time.Duration) endpointPolicy {
		return endpointPolicy{name: name, limit: limit, window: window}
	}
	if method == http.MethodGet {
		switch {
		case path == "v3/payment_vias":
			return p("GET /v3/payment_vias", 30, time.Minute)
		case path == "v2/brands":
			return p("GET /v2/brands", 60, time.Minute)
		case path == "v2/homepage":
			return p("GET /v2/homepage", 30, time.Minute)
		case len(parts) >= 4 && parts[0] == "v5" && parts[1] == "products" && parts[2] == "country":
			return p("GET /v5/products/country/{cc}", 60, time.Minute)
		case path == "v4/products/price":
			return p("GET /v4/products/price", 120, time.Minute)
		case len(parts) == 2 && parts[0] == "v5" && parts[1] == "orders":
			// /v5/orders/{id} (polling) and /v5/orders/{id}/subscribe
			return p("GET /v5/orders/{id}", 120, time.Minute)
		}
	}
	if method == http.MethodPost {
		switch {
		case path == "v5/orders/validations":
			return p("POST /v5/orders/validations", 60, 10*time.Minute)
		case path == "v5/orders":
			// Order creation is precious: every created order counts
			// against the partner key, so keep the local budget tight.
			return p("POST /v5/orders", 30, 10*time.Minute)
		}
	}
	return p(method+" /unknown", 10, time.Minute)
}

type windowCounter struct {
	entries            []time.Time
	throttleUntil      time.Time
	observedLimit      int // upstream limit, only used when stricter than local
	remoteRemaining    int
	remoteRemainingSet bool
	remoteReset        time.Time
}

func (w *windowCounter) purge(now time.Time, window time.Duration) {
	cutoff := now.Add(-window)
	i := 0
	for i < len(w.entries) && w.entries[i].Before(cutoff) {
		i++
	}
	if i > 0 {
		w.entries = append(w.entries[:0], w.entries[i:]...)
	}
}

func (w *windowCounter) wait(now time.Time, limit int, window time.Duration) time.Duration {
	w.purge(now, window)
	if w.remoteRemainingSet && !w.remoteReset.IsZero() && !w.remoteReset.After(now) {
		w.remoteRemainingSet = false
		w.remoteRemaining = 0
		w.remoteReset = time.Time{}
	}
	until := w.throttleUntil
	if w.remoteRemainingSet && w.remoteRemaining <= 0 {
		reset := w.remoteReset
		if reset.IsZero() {
			reset = now.Add(window)
		}
		if reset.After(until) {
			until = reset
		}
	}
	if len(w.entries) >= limit {
		candidate := w.entries[0].Add(window)
		if candidate.After(until) {
			until = candidate
		}
	}
	if until.After(now) {
		return until.Sub(now)
	}
	return 0
}

type queuedCall struct {
	ctx    context.Context
	actor  string
	policy endpointPolicy
	run    func() error
	done   chan error
}

// Scheduler is a fair, single-dispatcher queue. Serial dispatch is
// intentional: the partner-key limits apply to one shared account, and a
// single dispatcher prevents a burst of concurrent handlers from racing
// the same rolling-window calculation. Slow upstream calls still honour
// the caller's context and do not hold a database lock.
type Scheduler struct {
	mu         sync.Mutex
	cond       *sync.Cond
	queues     map[string][]*queuedCall
	actors     []string
	cursor     int
	total      int
	closed     bool
	config     QueueConfig
	endpoint   map[string]*windowCounter
	actorWin   map[string]*windowCounter
	actorBurst map[string]*windowCounter
}

func NewScheduler(cfg QueueConfig) *Scheduler {
	defaults := defaultQueueConfig()
	if cfg.MaxQueue <= 0 {
		cfg.MaxQueue = defaults.MaxQueue
	}
	if cfg.MaxQueuePerActor <= 0 {
		cfg.MaxQueuePerActor = defaults.MaxQueuePerActor
	}
	if cfg.ActorRequestsPerMin <= 0 {
		cfg.ActorRequestsPerMin = defaults.ActorRequestsPerMin
	}
	if cfg.ActorBurst <= 0 {
		cfg.ActorBurst = defaults.ActorBurst
	}
	if cfg.MaxAdmissionWait == 0 {
		cfg.MaxAdmissionWait = defaults.MaxAdmissionWait
	}
	s := &Scheduler{
		queues:     make(map[string][]*queuedCall),
		config:     cfg,
		endpoint:   make(map[string]*windowCounter),
		actorWin:   make(map[string]*windowCounter),
		actorBurst: make(map[string]*windowCounter),
	}
	s.cond = sync.NewCond(&s.mu)
	go s.loop()
	return s
}

func (s *Scheduler) Submit(ctx context.Context, run func() error, policy endpointPolicy) error {
	if ctx == nil {
		ctx = context.Background()
	}
	actor := actorFromContext(ctx)
	job := &queuedCall{ctx: ctx, actor: actor, policy: policy, run: run, done: make(chan error, 1)}

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return context.Canceled
	}
	if s.total >= s.config.MaxQueue || len(s.queues[actor]) >= s.config.MaxQueuePerActor {
		s.mu.Unlock()
		return ErrQueueFull
	}
	// Fail-fast admission: if the next budget slot is further out than
	// MaxAdmissionWait, reject now (429) instead of parking the job in the
	// queue where it would head-of-line-block its actor and only die on its
	// caller context. This is what keeps the queue shallow and drainable
	// under load.
	if s.config.MaxAdmissionWait > 0 {
		if wait := s.waitLocked(time.Now(), job); wait > s.config.MaxAdmissionWait {
			s.mu.Unlock()
			return ErrBudgetWait
		}
	}
	if _, ok := s.queues[actor]; !ok {
		s.actors = append(s.actors, actor)
	}
	s.queues[actor] = append(s.queues[actor], job)
	s.total++
	s.cond.Signal()
	s.mu.Unlock()

	select {
	case err := <-job.done:
		return err
	case <-ctx.Done():
		// The dispatcher will discard a cancelled job. Returning
		// immediately is important for HTTP handlers; the bounded queue
		// prevents the abandoned call from accumulating indefinitely.
		return ctx.Err()
	}
}

func (s *Scheduler) loop() {
	for {
		s.mu.Lock()
		for s.total == 0 && !s.closed {
			s.cond.Wait()
		}
		if s.closed && s.total == 0 {
			s.mu.Unlock()
			return
		}
		job, wait := s.nextLocked(time.Now())
		s.mu.Unlock()
		if job == nil {
			if wait <= 0 {
				wait = 10 * time.Millisecond
			}
			if wait > 100*time.Millisecond {
				wait = 100 * time.Millisecond
			}
			time.Sleep(wait)
			continue
		}
		// The dispatcher is the single most critical goroutine in the
		// process: if it dies, every supplier call (catalog, quotes,
		// fulfillment polling, webhook verification) hangs forever.
		// Guard each job so a panic inside one call is logged, reported
		// to its caller, and skipped — never allowed to kill the loop.
		err := safe.Do("cryptorefills:scheduler", job.run)
		job.done <- err
	}
}

func (s *Scheduler) nextLocked(now time.Time) (*queuedCall, time.Duration) {
	var earliest time.Duration
	if len(s.actors) == 0 {
		return nil, 0
	}
	for n := 0; n < len(s.actors); n++ {
		i := (s.cursor + n) % len(s.actors)
		actor := s.actors[i]
		q := s.queues[actor]
		if len(q) == 0 {
			continue
		}
		job := q[0]
		if job.ctx.Err() != nil {
			s.removeHeadLocked(actor)
			n--
			continue
		}
		wait := s.waitLocked(now, job)
		if wait == 0 {
			s.removeHeadLocked(actor)
			s.reserveLocked(now, job)
			if len(s.actors) == 0 {
				s.cursor = 0
			} else if _, stillQueued := s.queues[actor]; stillQueued {
				s.cursor = (i + 1) % len(s.actors)
			} else if i >= len(s.actors) {
				s.cursor = 0
			} else {
				// The selected actor was removed; index i now points at
				// the actor that follows it in round-robin order.
				s.cursor = i
			}
			return job, 0
		}
		if earliest == 0 || wait < earliest {
			earliest = wait
		}
	}
	return nil, earliest
}

func (s *Scheduler) waitLocked(now time.Time, job *queuedCall) time.Duration {
	endpoint := s.endpoint[job.policy.name]
	if endpoint == nil {
		endpoint = &windowCounter{}
		s.endpoint[job.policy.name] = endpoint
	}
	limit := job.policy.limit
	if endpoint.observedLimit > 0 && endpoint.observedLimit < limit {
		limit = endpoint.observedLimit
	}
	wait := endpoint.wait(now, limit, job.policy.window)
	actor := s.actorWin[job.actor]
	if actor == nil {
		actor = &windowCounter{}
		s.actorWin[job.actor] = actor
	}
	if w := actor.wait(now, s.config.ActorRequestsPerMin, time.Minute); w > wait {
		wait = w
	}
	burst := s.actorBurst[job.actor]
	if burst == nil {
		burst = &windowCounter{}
		s.actorBurst[job.actor] = burst
	}
	if w := burst.wait(now, s.config.ActorBurst, time.Second); w > wait {
		wait = w
	}
	return wait
}

func (s *Scheduler) reserveLocked(now time.Time, job *queuedCall) {
	endpoint := s.endpoint[job.policy.name]
	endpoint.entries = append(endpoint.entries, now)
	if endpoint.remoteRemainingSet && endpoint.remoteRemaining > 0 {
		endpoint.remoteRemaining--
	}
	s.actorWin[job.actor].entries = append(s.actorWin[job.actor].entries, now)
	s.actorBurst[job.actor].entries = append(s.actorBurst[job.actor].entries, now)
}

func (s *Scheduler) removeHeadLocked(actor string) {
	q := s.queues[actor]
	if len(q) == 0 {
		return
	}
	s.queues[actor] = q[1:]
	s.total--
	if len(s.queues[actor]) == 0 {
		delete(s.queues, actor)
		// Keep the rolling budget after a queue drains; otherwise a user
		// could evade the per-actor cap by sending one request at a time.
		if len(s.actorWin) > 10000 {
			s.pruneActorCountersLocked(time.Now())
		}
		for i, v := range s.actors {
			if v == actor {
				s.actors = append(s.actors[:i], s.actors[i+1:]...)
				if len(s.actors) == 0 {
					s.cursor = 0
				} else if s.cursor >= len(s.actors) {
					s.cursor = 0
				}
				break
			}
		}
	}
}

func (s *Scheduler) pruneActorCountersLocked(now time.Time) {
	for actor, counter := range s.actorWin {
		counter.purge(now, time.Minute)
		burst := s.actorBurst[actor]
		if burst != nil {
			burst.purge(now, time.Second)
		}
		if len(counter.entries) == 0 && (burst == nil || len(burst.entries) == 0) && s.queues[actor] == nil {
			delete(s.actorWin, actor)
			delete(s.actorBurst, actor)
		}
	}
}

// Observe keeps the local scheduler conservative when the supplier reports
// a lower remaining budget or a reset time. It never raises a local limit
// based on an upstream header.
func (s *Scheduler) Observe(policy endpointPolicy, headers http.Header) {
	reset := parseReset(headers)
	remaining, remErr := strconv.Atoi(headers.Get("X-RateLimit-Remaining"))
	remOK := remErr == nil
	if reset.IsZero() && !remOK {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	counter := s.endpoint[policy.name]
	if counter == nil {
		counter = &windowCounter{}
		s.endpoint[policy.name] = counter
	}
	if upstreamLimit, err := strconv.Atoi(strings.TrimSpace(headers.Get("X-RateLimit-Limit"))); err == nil && upstreamLimit > 0 && (counter.observedLimit == 0 || upstreamLimit < counter.observedLimit) {
		counter.observedLimit = upstreamLimit
	}
	if remOK {
		counter.remoteRemaining = remaining
		counter.remoteRemainingSet = true
		if reset.IsZero() {
			reset = time.Now().Add(policy.window)
		}
		counter.remoteReset = reset
	}
	if !reset.IsZero() && remOK && remaining <= 0 && reset.After(counter.throttleUntil) {
		counter.throttleUntil = reset
	}
}

func parseReset(headers http.Header) time.Time {
	if raw := strings.TrimSpace(headers.Get("X-RateLimit-Reset")); raw != "" {
		if unix, err := strconv.ParseInt(raw, 10, 64); err == nil {
			return time.Unix(unix, 0)
		}
	}
	if raw := strings.TrimSpace(headers.Get("Retry-After")); raw != "" {
		if secs, err := strconv.ParseInt(raw, 10, 64); err == nil && secs > 0 {
			return time.Now().Add(time.Duration(secs) * time.Second)
		}
	}
	return time.Time{}
}

// Throttle is called for a 429 even when the supplier omitted headers. The
// short fallback prevents a hot retry loop while preserving normal recovery.
func (s *Scheduler) Throttle(policy endpointPolicy, reset time.Time) {
	if reset.IsZero() || !reset.After(time.Now()) {
		reset = time.Now().Add(policy.window)
	}
	s.mu.Lock()
	counter := s.endpoint[policy.name]
	if counter == nil {
		counter = &windowCounter{}
		s.endpoint[policy.name] = counter
	}
	if reset.After(counter.throttleUntil) {
		counter.throttleUntil = reset
	}
	s.mu.Unlock()
}

// Close stops admission. It is mainly useful for tests and graceful
// shutdown; an in-flight HTTP request is still cancelled by its own context.
func (s *Scheduler) Close() {
	s.mu.Lock()
	s.closed = true
	s.cond.Broadcast()
	for actor, q := range s.queues {
		for _, job := range q {
			job.done <- context.Canceled
		}
		delete(s.queues, actor)
	}
	s.total = 0
	s.actors = nil
	s.cursor = 0
	s.mu.Unlock()
}

// Stats exposes queue health without exposing user identities.
type QueueStats struct {
	Queued int `json:"queued"`
	Actors int `json:"actors"`
	// Throttled lists endpoint policies currently cooling down (supplier
	// 429 / remote budget observed). Diagnostic only.
	Throttled []string `json:"throttled,omitempty"`
}

func (s *Scheduler) Stats() QueueStats {
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	out := QueueStats{Queued: s.total, Actors: len(s.actors)}
	for name, counter := range s.endpoint {
		if counter.throttleUntil.After(now) {
			out.Throttled = append(out.Throttled, name)
		}
	}
	sort.Strings(out.Throttled)
	return out
}

// endpointWait reports how long the NEXT call to this policy would have to
// wait before it can be admitted (0 = immediately). Background workers use
// it to skip entire passes while an endpoint is cooling down instead of
// burning one rejection per order per tick.
func (s *Scheduler) endpointWait(policy endpointPolicy) time.Duration {
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.waitLocked(now, &queuedCall{policy: policy})
}

