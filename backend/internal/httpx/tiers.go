package httpx

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/valyala/fasthttp"

	"nimiqshop/internal/clientip"
)

// Tier is a named request-cost class. The global limiter stays generous so a
// normal shopper is never throttled; these tiers are what stop one client
// from monopolising the resources that are actually scarce — supplier API
// budget, on-chain RPC calls, outbound email, and Argon2id CPU.
//
// The numbers below are chosen so a legitimate human cannot reach them:
// nobody logs in 20 times a minute, nobody opens 6 support tickets a minute,
// nobody checks out 10 times a minute. An attacker, on the other hand, hits
// them on the first second of a scripted run.
type Tier struct {
	Name       string
	PerMinute  int
	Burst      int
	RetryAfter int
}

var (
	// TierLogin guards signature verification and user-record creation. A
	// fresh Nimiq keypair is free, so without this an attacker could mint
	// millions of user rows and fill the disk.
	TierLogin = Tier{Name: "login", PerMinute: 20, Burst: 8, RetryAfter: 30}
	// TierCheckout guards the supplier dry-run + order-creation path: the
	// single most expensive thing a customer can ask for.
	TierCheckout = Tier{Name: "checkout", PerMinute: 12, Burst: 4, RetryAfter: 20}
	// TierRefresh guards endpoints that force an upstream supplier fetch.
	TierRefresh = Tier{Name: "refresh", PerMinute: 30, Burst: 10, RetryAfter: 15}
	// TierPool guards the pool-stake re-ask (an outbound HTTP call).
	TierPool = Tier{Name: "pool", PerMinute: 20, Burst: 6, RetryAfter: 20}
	// TierPromo guards the promo-code lookup oracle. A code table is small
	// and guessable ("NIM10", "WELCOME"); at 3600/min an attacker could
	// dictionary it in minutes. 30/min still lets a real shopper mistype
	// several times.
	TierPromo = Tier{Name: "promo", PerMinute: 30, Burst: 10, RetryAfter: 60}
	// TierSupport guards ticket creation and replies: unbounded stored text
	// is a disk-exhaustion vector.
	TierSupport = Tier{Name: "support", PerMinute: 12, Burst: 4, RetryAfter: 60}
	// TierPresence guards the public heartbeat. It is cheap, but it writes
	// to an in-memory map keyed by a client-supplied id.
	TierPresence = Tier{Name: "presence", PerMinute: 60, Burst: 20, RetryAfter: 30}
	// TierWrite guards any other authenticated state change (ratings,
	// notification prefs, tree prefs).
	TierWrite = Tier{Name: "write", PerMinute: 60, Burst: 20, RetryAfter: 30}
	// TierAdminLogin is the tightest tier in the system: the operations
	// console is one human being.
	TierAdminLogin = Tier{Name: "admin-login", PerMinute: 10, Burst: 5, RetryAfter: 60}
	// TierAdmin is the authenticated console: dashboards load several
	// panels at once, so it stays roomy.
	TierAdmin = Tier{Name: "admin", PerMinute: 600, Burst: 200, RetryAfter: 10}
)

// BucketPool is a sharded token-bucket set shared by every tier limiter. It
// is a copy of the design already proven in internal/middleware (256 shards,
// bounded bucket count, evict-oldest instead of rejecting new clients) but
// parameterised per tier, so one instance can serve several independent
// budgets keyed on (tier, client IP).
type BucketPool struct {
	shards [bucketShards]bucketShard
	live   atomic.Int64
}

const (
	bucketShards    = 256
	bucketShardSize = 8 * 1024
	maxPoolBuckets  = 2_000_000
	bucketMaxIdle   = 30 * time.Minute
	bucketSweep     = time.Minute
)

type bucketShard struct {
	mu        sync.Mutex
	clients   map[bucketKey]*tokenBucket
	lastSweep atomic.Value
}

type bucketKey struct {
	tier string
	ip   string
}

type tokenBucket struct {
	tokens float64
	seen   time.Time
}

// NewBucketPool allocates the shards. One pool serves the whole process.
func NewBucketPool() *BucketPool {
	p := &BucketPool{}
	for i := range p.shards {
		p.shards[i].clients = make(map[bucketKey]*tokenBucket)
	}
	return p
}

func (p *BucketPool) shardFor(k bucketKey) *bucketShard {
	// FNV-1a over tier+ip. Inlined rather than using hash/fnv to avoid an
	// allocation per request on the hottest path in the process.
	var h uint32 = 2166136261
	for i := 0; i < len(k.tier); i++ {
		h ^= uint32(k.tier[i])
		h *= 16777619
	}
	h ^= '/'
	h *= 16777619
	for i := 0; i < len(k.ip); i++ {
		h ^= uint32(k.ip[i])
		h *= 16777619
	}
	return &p.shards[h%bucketShards]
}

// Allow consumes one token from (tier, ip). It reports the retry-after the
// caller should advertise when it returns false.
func (p *BucketPool) Allow(tier Tier, ip string) (bool, int) {
	retry := tier.RetryAfter
	if retry <= 0 {
		retry = 60
	}
	if ip == "" {
		// Unattributable requests share one bucket rather than escaping
		// the limiter entirely.
		ip = "?"
	}
	key := bucketKey{tier: tier.Name, ip: ip}
	s := p.shardFor(key)
	now := time.Now()

	rate := float64(tier.PerMinute) / 60
	if rate <= 0 {
		rate = 1
	}
	burst := float64(tier.Burst)
	if burst < 1 {
		burst = 1
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	b := s.clients[key]
	if b == nil {
		if p.live.Load() >= maxPoolBuckets {
			p.evictOldest(s, now)
		}
		if len(s.clients) >= bucketShardSize && s.sweepDue(now) {
			for k, v := range s.clients {
				if now.Sub(v.seen) > bucketMaxIdle {
					delete(s.clients, k)
					p.live.Add(-1)
				}
			}
		}
		b = &tokenBucket{tokens: burst, seen: now}
		s.clients[key] = b
		p.live.Add(1)
	}
	elapsed := now.Sub(b.seen).Seconds()
	if elapsed > 0 {
		b.tokens += elapsed * rate
		if b.tokens > burst {
			b.tokens = burst
		}
	}
	b.seen = now
	if b.tokens < 1 {
		// Tell the client how long until a token exists, rather than a
		// flat 60s: it is both more honest and gentler on a real user who
		// merely double-clicked.
		wait := (1 - b.tokens) / rate
		if wait < 1 {
			wait = 1
		}
		if int(wait) < retry {
			retry = int(wait)
		}
		if retry < 1 {
			retry = 1
		}
		return false, retry
	}
	b.tokens--
	return true, 0
}

func (p *BucketPool) evictOldest(s *bucketShard, now time.Time) {
	var oldestKey bucketKey
	oldest := now
	found := false
	for k, v := range s.clients {
		if !found || v.seen.Before(oldest) {
			oldest = v.seen
			oldestKey = k
			found = true
		}
	}
	if found {
		delete(s.clients, oldestKey)
		p.live.Add(-1)
	}
}

func (sh *bucketShard) sweepDue(now time.Time) bool {
	last, _ := sh.lastSweep.Load().(time.Time)
	if now.Sub(last) <= bucketSweep {
		return false
	}
	sh.lastSweep.Store(now)
	return true
}

// Live reports the current bucket count (memory pressure indicator).
func (p *BucketPool) Live() int64 { return p.live.Load() }

// TierLimiter binds one Tier to a BucketPool and a trust-proxy setting.
type TierLimiter struct {
	pool       *BucketPool
	tier       Tier
	trustProxy bool
	policy     clientip.Policy
	rejected   atomic.Int64
	// Subject, when set, is asked for the AUTHENTICATED identity of the
	// caller before the IP is consulted. Returning "" falls back to the IP.
	//
	// This exists because keying an expensive tier purely on IP is wrong for
	// real shoppers, not just for tests. A large fraction of consumer traffic
	// arrives behind CGNAT, a corporate egress, a university wifi or a mobile
	// carrier pool — hundreds of distinct people presenting ONE IP address. A
	// 12-checkouts-a-minute IP budget would be shared by all of them, so the
	// hundredth person to click "pay" in a busy minute gets a 429 for
	// somebody else's shopping. That is exactly the "hardening must never
	// touch a normal user" failure this whole pass is meant to avoid.
	//
	// Keying on the verified JWT subject instead gives every account its own
	// budget while STILL bounding a single abuser, and it bounds them more
	// precisely: an attacker cannot dilute their own limit by arriving from a
	// shared network. Anonymous callers (no token, or a token that failed
	// verification) keep the IP key, so the protection is unchanged for them.
	//
	// It is a function rather than an import of internal/middleware so this
	// package stays a leaf: httpx is wired at the edge and must not depend on
	// the auth layer it sits underneath.
	Subject func(*fasthttp.RequestCtx) string

	// Shared, when set, is a SECOND budget applied to the client IP in
	// addition to the per-account one — and only when an account was
	// identified. It exists to close the hole that subject-keying opens on
	// its own.
	//
	// A Nimiq identity is free: Hub login is a signature over a freshly
	// generated keypair, so an attacker can mint as many accounts as they
	// like and each one arrives with a full, unused per-account budget.
	// Keying purely on the account would therefore let a single machine
	// consume unlimited supplier budget by rotating wallets. Keying purely on
	// the IP punishes the CGNAT shoppers described above. Doing BOTH is the
	// answer: the account budget is tight and personal, and the IP budget is
	// a loose aggregate ceiling that one physical host cannot exceed no
	// matter how many wallets it invents.
	//
	// The shared ceiling should be roughly "how many distinct real people
	// could plausibly be behind one IP at once" times the per-account rate —
	// generous enough that a campus or a carrier pool never touches it, and
	// finite enough that a scripted wallet-rotator does.
	Shared *Tier
}

// NewTierLimiter builds a limiter for one tier, keyed on client IP.
func NewTierLimiter(pool *BucketPool, tier Tier, trustProxy bool, policy clientip.Policy) *TierLimiter {
	return &TierLimiter{pool: pool, tier: tier, trustProxy: trustProxy, policy: policy}
}

// NewSubjectTierLimiter builds a limiter that budgets the AUTHENTICATED
// account when there is one and the client IP when there is not. Use it for
// every tier mounted behind RequireAuth/OptionalAuth.
func NewSubjectTierLimiter(pool *BucketPool, tier Tier, trustProxy bool, policy clientip.Policy, subject func(*fasthttp.RequestCtx) string) *TierLimiter {
	return &TierLimiter{pool: pool, tier: tier, trustProxy: trustProxy, policy: policy, Subject: subject}
}

// WithSharedIPCeiling attaches the aggregate per-IP budget described on
// Shared. It returns the limiter so the call reads as one construction.
func (l *TierLimiter) WithSharedIPCeiling(t Tier) *TierLimiter {
	l.Shared = &t
	return l
}

// Limit wraps next with the tier budget.
func (l *TierLimiter) Limit(next fasthttp.RequestHandler) fasthttp.RequestHandler {
	tier := l.tier
	return func(ctx *fasthttp.RequestCtx) {
		// Authenticated subject first; IP only when there is no verified
		// identity to budget. The "u:"/"ip:" prefixes keep the two key spaces
		// from colliding — a user id that happened to look like an address
		// must not inherit some stranger's IP bucket.
		ip := clientip.Resolve(ctx, l.trustProxy, l.policy).IP
		subject := ""
		if l.Subject != nil {
			subject = l.Subject(ctx)
		}
		// An identified account is budgeted per account; an anonymous caller
		// is budgeted per IP. The "u:"/"ip:" prefixes keep the two key spaces
		// from colliding — a user id that happened to look like an address
		// must not inherit some stranger's IP bucket.
		key := "ip:" + ip
		if subject != "" {
			key = "u:" + subject
		}
		reject := func(name string, retry int) {
			l.rejected.Add(1)
			ctx.Response.Header.Set("Retry-After", itoa(retry))
			ctx.Response.Header.Set("Cache-Control", "no-store")
			ctx.SetStatusCode(fasthttp.StatusTooManyRequests)
			ctx.SetContentType("application/json")
			ctx.SetBodyString(`{"error":"too many requests — please slow down","code":"RATE_LIMITED","tier":"` +
				name + `","retry_after_seconds":` + itoa(retry) + `}`)
		}
		ok, retry := l.pool.Allow(tier, key)
		if !ok {
			reject(tier.Name, retry)
			return
		}
		// Second, looser budget on the source address, but ONLY for callers
		// who presented an identity — that is the wallet-rotation case the
		// per-account budget cannot see. Anonymous callers were already
		// charged to this same IP above, so charging them twice would halve
		// their real allowance.
		if subject != "" && l.Shared != nil {
			if ok2, retry2 := l.pool.Allow(*l.Shared, "ip:"+ip); !ok2 {
				reject(l.Shared.Name, retry2)
				return
			}
		}
		next(ctx)
	}
}

// Rejected reports how many requests this tier shed.
func (l *TierLimiter) Rejected() int64 { return l.rejected.Load() }

// Name is the tier's identifier, for the health payload.
func (l *TierLimiter) Name() string { return l.tier.Name }

// itoa avoids pulling strconv into the hot path's allocation profile for the
// tiny non-negative integers we ever print here.
func itoa(n int) string {
	if n <= 0 {
		return "0"
	}
	var buf [12]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
