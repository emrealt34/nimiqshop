package safe

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestGuardNil(t *testing.T) {
	var got bool
	func() {
		defer Guard("t", func(p bool) { got = p })
	}()
	if got {
		t.Fatal("Guard reported panic for clean function")
	}
}

func TestGuardPanic(t *testing.T) {
	// If this process survives this test, Guard recovered the panic.
	var got bool
	func() {
		defer Guard("t", func(p bool) { got = p })
		panic("boom")
	}()
	if !got {
		t.Fatal("Guard did not report panic")
	}
}

func TestGuardReportPanicSafe(t *testing.T) {
	var got bool
	func() {
		defer Guard("t", func(p bool) {
			if p {
				got = true
			}
			panic("reporter itself broke")
		})
		panic("boom")
	}()
	if !got {
		t.Fatal("Guard report was not delivered")
	}
}

func TestGoSwallowsPanic(t *testing.T) {
	done := make(chan struct{})
	Go("test", func() {
		defer close(done)
		panic("goroutine boom")
	})
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("goroutine never completed")
	}
}

func TestDoPanicsBecomeErrors(t *testing.T) {
	err := Do("test", func() error {
		panic("do boom")
	})
	if err == nil {
		t.Fatal("Do returned nil for panicking fn")
	}
	clean := Do("test", func() error { return nil })
	if clean != nil {
		t.Fatalf("Do returned %v for clean fn", clean)
	}
	business := errors.New("business error")
	if err := Do("test", func() error { return business }); !errors.Is(err, business) {
		t.Fatalf("Do lost business error: %v", err)
	}
}

func TestRunLoopSurvivesPanickingStep(t *testing.T) {
	stop := make(chan struct{})
	defer close(stop)
	var runs int32
	RunLoop("test-loop", 10*time.Millisecond, stop, func() {
		n := atomic.AddInt32(&runs, 1)
		if n <= 2 {
			panic("step boom")
		}
	})
	deadline := time.Now().Add(3 * time.Second)
	for atomic.LoadInt32(&runs) < 5 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if atomic.LoadInt32(&runs) < 5 {
		t.Fatalf("loop stopped after panics: only %d runs", runs)
	}
}
