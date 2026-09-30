// Package presence tracks how many users are currently active on the site, for
// the live "X users are shopping right now" indicator on the public activity
// feed. It is deliberately in-memory and ephemeral: a heartbeat registers a
// visitor, and anyone who has not pinged within the window is considered gone.
//
// Scale design: a single mutex over one map turned every heartbeat into a
// serialized O(n) prune once the site passed ~500 live visitors — at 100k
// concurrent users that is quadratic work on the hot path. The tracker is now
// sharded (16 independent maps) and each shard prunes at most once per
// pruneInterval, so per-heartbeat cost is O(1) amortized and lock contention
// is divided by the shard count.
package presence

import (
	"hash/fnv"
	"sync"
	"time"
)

// Window is how long a heartbeat keeps a visitor "active".
const Window = 90 * time.Second

const (
	presenceShards   = 16
	pruneInterval    = 10 * time.Second
	maxShardCapacity = 1 << 18 // per-shard soft cap before an immediate prune
)

type presenceShard struct {
	mu        sync.Mutex
	seen      map[string]time.Time
	lastPrune time.Time
}

// Tracker is a concurrency-safe, sharded in-process presence map.
type Tracker struct {
	shards [presenceShards]presenceShard
}

func New() *Tracker {
	t := &Tracker{}
	for i := range t.shards {
		t.shards[i].seen = make(map[string]time.Time)
	}
	return t
}

func (t *Tracker) shardFor(id string) *presenceShard {
	h := fnv.New32a()
	_, _ = h.Write([]byte(id))
	return &t.shards[h.Sum32()%presenceShards]
}

// Ping registers a heartbeat from id (a wallet address when authed, otherwise a
// client-generated pseudonymous id). Stale entries are pruned at most once per
// pruneInterval per shard.
func (t *Tracker) Ping(id string) {
	if id == "" {
		return
	}
	s := t.shardFor(id)
	now := time.Now()

	s.mu.Lock()
	defer s.mu.Unlock()
	s.seen[id] = now
	if len(s.seen) > maxShardCapacity || now.Sub(s.lastPrune) >= pruneInterval {
		s.lastPrune = now
		cutoff := now.Add(-Window)
		for k, at := range s.seen {
			if at.Before(cutoff) {
				delete(s.seen, k)
			}
		}
	}
}

// ActiveCount returns the number of distinct visitors seen within Window.
// Counts are read per shard without blocking writers for the whole map.
func (t *Tracker) ActiveCount() int {
	now := time.Now()
	total := 0
	for i := range t.shards {
		s := &t.shards[i]
		s.mu.Lock()
		cutoff := now.Add(-Window)
		if now.Sub(s.lastPrune) >= pruneInterval {
			s.lastPrune = now
			for k, at := range s.seen {
				if at.Before(cutoff) {
					delete(s.seen, k)
				}
			}
		}
		total += len(s.seen)
		s.mu.Unlock()
	}
	return total
}
