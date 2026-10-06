package handlers

import (
	"runtime"
	"testing"
	"time"
)

// TestHostMetricsSamplesTheMachine exercises the sampler against the machine
// the test runs on. CI is Linux, which is the only platform the console's
// server cards claim to support — anywhere else the payload must SAY so rather
// than report zeroes that look like an idle host.
func TestHostMetricsSamplesTheMachine(t *testing.T) {
	first := hostMetrics()
	if runtime.GOOS != "linux" {
		if first.Available || first.Reason == "" {
			t.Fatalf("non-Linux host should report unavailable with a reason: %+v", first)
		}
		return
	}
	if !first.Available {
		t.Fatalf("Linux host without metrics: %s", first.Reason)
	}
	if first.CPU == nil || first.CPU.Cores < 1 {
		t.Fatalf("no CPU cores reported: %+v", first.CPU)
	}
	if first.CPU.Percent < 0 || first.CPU.Percent > 100 {
		t.Fatalf("CPU percent out of range: %v", first.CPU.Percent)
	}
	if len(first.CPU.PerCore) != first.CPU.Cores {
		t.Errorf("per-core readings %d != %d cores", len(first.CPU.PerCore), first.CPU.Cores)
	}
	if first.Memory == nil || first.Memory.TotalBytes == 0 || first.Memory.UsedBytes > first.Memory.TotalBytes {
		t.Fatalf("implausible memory reading: %+v", first.Memory)
	}
	if len(first.Disks) == 0 {
		t.Error("no filesystem reported, not even root")
	}
	for _, d := range first.Disks {
		if d.TotalBytes == 0 || d.UsedBytes > d.TotalBytes || d.Percent < 0 || d.Percent > 100 {
			t.Errorf("implausible disk reading: %+v", d)
		}
	}
	if first.Process == nil || first.Process.GoVersion == "" {
		t.Error("process block missing")
	}

	// A second sample is differenced against the first: that is where the
	// interval CPU percent and the network rates come from. The production
	// reuse gap is two seconds to stop a fast poll producing noise; the test
	// shortens it so the differencing path runs without sleeping through it.
	saved := hostSampleMinGap
	hostSampleMinGap = 0
	defer func() { hostSampleMinGap = saved }()
	time.Sleep(600 * time.Millisecond)
	second := hostMetrics()
	if second.SampleInterval <= 0 {
		t.Errorf("second sample should carry an interval, got %v", second.SampleInterval)
	}
	if second.CPU.Percent < 0 || second.CPU.Percent > 100 {
		t.Errorf("interval CPU percent out of range: %v", second.CPU.Percent)
	}
	if second.CPU.SinceBootPercent < 0 || second.CPU.SinceBootPercent > 100 {
		t.Errorf("since-boot percent out of range: %v", second.CPU.SinceBootPercent)
	}
}
