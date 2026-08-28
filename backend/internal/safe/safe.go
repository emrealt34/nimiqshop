// Package safe provides the process-wide panic safety net.
//
// A panic in a request handler is contained by fasthttp's per-connection
// recover, but a panic in ANY background goroutine (supplier queue
// dispatcher, reconciliation worker, price oracles, notification senders)
// takes the WHOLE process down — including every in-flight customer request
// and the supplier queue that protects the shared supplier (CryptoRefills) account.
//
// Go recover() depth rule (the trap this package is built around):
// recover() only works when called in the frame of the deferred function
// ITSELF. Delegating it to a helper called from inside a deferred closure
// silently returns nil and the panic keeps crashing the process. Every
// helper here therefore either IS the deferred function (Guard) or owns its
// own deferred closure that calls recover() directly (Go, Do, RunLoop), so
// the safety net cannot be broken by a wrong call shape.
package safe

import (
	"fmt"
	"log"
	"runtime/debug"
	"time"
)

const maxStackLogBytes = 8192

// logPanic logs the panic value and (truncated) stack. It never panics
// itself, so it is safe to call from a deferred recover.
func logPanic(name string, r any) {
	log.Printf("SAFE %s: recovered panic: %v\n%s", name, r, StackTrace())
}

// StackTrace returns the current goroutine's stack, truncated so one
// incident can never flood the log.
func StackTrace() []byte {
	stack := debug.Stack()
	if len(stack) > maxStackLogBytes {
		stack = append(stack[:maxStackLogBytes], []byte("\n... [stack truncated]")...)
	}
	return stack
}

// Guard recovers and logs a panic. It MUST be deferred directly — never
// called from inside a deferred closure:
//
//	defer safe.Guard("worker:tick", func(panicked bool) {
//		if panicked { backOff() }
//	})
//
// report receives true when a panic was recovered, false on a clean exit.
// It may be nil. report itself is panic-guarded so a broken reporter can
// never turn one incident into two.
func Guard(name string, report func(panicked bool)) {
	r := recover()
	if r == nil {
		if report != nil {
			safely(report, false)
		}
		return
	}
	logPanic(name, r)
	if report != nil {
		safely(report, true)
	}
}

func safely(fn func(bool), v bool) {
	defer func() { _ = recover() }()
	fn(v)
}

// Go starts fn in a new goroutine with a panic safety net. The panic is
// logged with a stack trace and swallowed, so one bad job can never kill
// the process. name identifies the work in logs (e.g. "oracle:coingecko").
func Go(name string, fn func()) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				logPanic(name, r)
			}
		}()
		fn()
	}()
}

// Do runs fn synchronously in the current goroutine but with a panic
// safety net: a panic becomes a logged error return instead of a crash.
// Use it for one-shot work inside background loops (e.g. a single supplier
// call dispatched by the queue).
func Do(name string, fn func() error) (err error) {
	defer func() {
		if r := recover(); r != nil {
			logPanic(name, r)
			err = fmt.Errorf("safe: %s panicked: %v", name, r)
		}
	}()
	return fn()
}

// RunLoop runs step in a dedicated goroutine, repeating it every interval
// until stop closes. Each iteration is panic-guarded: a panicking step is
// logged and skipped, and the loop backs off (capped at the interval) until
// a step succeeds cleanly, so a persistently broken step cannot spin the
// CPU or kill the process. This is the standard wrapper for all background
// workers (reconciler, sweepers, ...).
func RunLoop(name string, interval time.Duration, stop <-chan struct{}, step func()) {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	// Base backoff: a fifth of the interval, clamped to a sane range.
	base := interval / 5
	if base < 50*time.Millisecond {
		base = 50 * time.Millisecond
	}
	if base > interval {
		base = interval
	}
	backoff := base
	Go(name+":loop", func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			panicked := false
			func() {
				defer Guard(name+":step", func(p bool) { panicked = p })
				step()
			}()
			if panicked {
				// Pause before the next attempt; doubles each consecutive
				// failure, capped at the interval, resets on success.
				wait := backoff
				if wait > interval {
					wait = interval
				}
				select {
				case <-stop:
					return
				case <-time.After(wait):
				}
				backoff = wait * 2
			} else {
				backoff = base
			}
			select {
			case <-stop:
				return
			case <-t.C:
			}
		}
	})
}
