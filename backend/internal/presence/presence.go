// Package presence tracks how many users are currently active on the site, for
// the live "X users are shopping right now" indicator on the public activity
// feed. It is deliberately in-memory and ephemeral: a heartbeat registers a
// visitor, and anyone who has not pinged within the window is considered gone.
package presence

import (
	"sync"
	"time"
)

// Window is how long a heartbeat keeps a visitor "active".
const Window = 90 * time.Second

// Tracker is a concurrency-safe, in-process presence map.
type Tracker struct {
	mu   sync.Mutex
	seen map[string]time.Time
}

func New() *Tracker { return &Tracker{seen: make(map[string]time.Time)} }

// Ping registers a heartbeat from id (a wallet address when authed, otherwise a
// client-generated pseudonymous id). Stale entries are pruned opportunistically.
func (t *Tracker) Ping(id string) {
	if id == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.seen[id] = time.Now()
	if len(t.seen) > 512 {
		t.prune()
	}
}

// ActiveCount returns the number of distinct visitors seen within Window.
func (t *Tracker) ActiveCount() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.prune()
	return len(t.seen)
}

func (t *Tracker) prune() {
	cutoff := time.Now().Add(-Window)
	for id, at := range t.seen {
		if at.Before(cutoff) {
			delete(t.seen, id)
		}
	}
}
