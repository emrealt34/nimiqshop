package handlers

import (
	"math"
	"runtime"
	"syscall"
	"testing"
	"time"
)

// TestHostMetricsSamplesItsScope verifies the values that the Server card may
// honestly display in this runtime. Containers must not leak host-wide load,
// model, uptime or root-volume statistics into the API instance view.
func TestHostMetricsSamplesItsScope(t *testing.T) {
	first := hostMetrics()
	if runtime.GOOS != "linux" {
		if first.Available || first.Reason == "" {
			t.Fatalf("non-Linux host should report unavailable with a reason: %+v", first)
		}
		return
	}
	if !first.Available {
		t.Fatalf("Linux runtime without metrics: %s", first.Reason)
	}
	if first.Scope != "host" && first.Scope != "instance" {
		t.Fatalf("metrics must identify their scope, got %q", first.Scope)
	}
	if first.Process == nil || first.Process.GoVersion == "" {
		t.Fatal("process block missing")
	}

	if first.CPU != nil {
		if first.CPU.Percent < 0 || first.CPU.Percent > 100 {
			t.Errorf("CPU percent out of range: %v", first.CPU.Percent)
		}
		if first.CPU.Capacity <= 0 || first.CPU.Cores < 1 {
			t.Errorf("CPU capacity missing: %+v", first.CPU)
		}
		if first.Scope == "instance" && (first.CPU.Model != "" || first.CPU.Load1 != 0 || len(first.CPU.PerCore) != 0) {
			t.Errorf("instance scope must not expose host-wide CPU data: %+v", first.CPU)
		}
	}

	if first.Memory != nil {
		if first.Memory.TotalBytes > 0 && first.Memory.UsedBytes > first.Memory.TotalBytes {
			t.Errorf("memory usage exceeds its reported limit: %+v", first.Memory)
		}
		if first.Scope == "instance" && first.Memory.TotalBytes == 0 && first.Memory.ProcessRSSBytes == 0 && first.Process.RSSBytes == 0 {
			t.Error("instance memory should expose cgroup usage or API process RSS")
		}
	}
	for _, disk := range first.Disks {
		if disk.TotalBytes == 0 || disk.UsedBytes > disk.TotalBytes || disk.Percent < 0 || disk.Percent > 100 {
			t.Errorf("implausible disk reading: %+v", disk)
		}
		if first.Scope == "instance" && disk.Path == "/" {
			t.Errorf("instance scope must not report the shared root filesystem: %+v", disk)
		}
	}
	if first.Scope == "instance" {
		if first.CPU != nil && first.CPU.Model != "" {
			t.Errorf("instance CPU model must not identify the physical host: %q", first.CPU.Model)
		}
		if first.UptimeSeconds > time.Since(processStartedAt).Seconds()+1 {
			t.Errorf("instance uptime should be API-process uptime, got %v seconds", first.UptimeSeconds)
		}
	}

	// Filesystem aliases such as /data/badger and /data must not be counted
	// twice when they resolve to the same mounted volume.
	seenFilesystems := make(map[string]string)
	for _, disk := range first.Disks {
		var stat syscall.Statfs_t
		if err := syscall.Statfs(disk.Path, &stat); err != nil {
			continue
		}
		identity := filesystemIdentity(stat)
		if prior, duplicate := seenFilesystems[identity]; duplicate {
			t.Errorf("filesystem shown twice at %s and %s", prior, disk.Path)
		}
		seenFilesystems[identity] = disk.Path
	}

	// A second sample is differenced against the first. The production reuse
	// gap is two seconds; shorten it so this test remains quick.
	saved := hostSampleMinGap
	hostSampleMinGap = 0
	defer func() { hostSampleMinGap = saved }()
	time.Sleep(600 * time.Millisecond)
	second := hostMetrics()
	if second.CPU != nil {
		if second.SampleInterval <= 0 {
			t.Errorf("second CPU sample should carry an interval, got %v", second.SampleInterval)
		}
		if second.CPU.Percent < 0 || second.CPU.Percent > 100 {
			t.Errorf("interval CPU percent out of range: %v", second.CPU.Percent)
		}
		if second.CPU.SinceBootPercent < 0 || second.CPU.SinceBootPercent > 100 {
			t.Errorf("since-start percent out of range: %v", second.CPU.SinceBootPercent)
		}
	}
}

func TestInstanceMetricsAreScoped(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux process and cgroup metrics only")
	}

	metrics := instanceMetrics()
	if !metrics.Available || metrics.Scope != "instance" {
		t.Fatalf("instance sampler should identify its scope: %+v", metrics)
	}
	if metrics.CPU != nil && (metrics.CPU.Model != "" || metrics.CPU.Load1 != 0 || metrics.CPU.Load5 != 0 || metrics.CPU.Load15 != 0 || len(metrics.CPU.PerCore) != 0) {
		t.Errorf("instance sampler leaked host CPU details: %+v", metrics.CPU)
	}
	if metrics.UptimeSeconds > time.Since(processStartedAt).Seconds()+1 {
		t.Errorf("instance sampler returned host uptime: %v", metrics.UptimeSeconds)
	}
	if metrics.Memory != nil {
		if metrics.Memory.TotalBytes == 0 && metrics.Memory.Source != "process-only" {
			t.Errorf("unlimited instance memory should be process-scoped: %+v", metrics.Memory)
		}
		if metrics.Memory.TotalBytes > 0 && metrics.Memory.Source != "cgroup" {
			t.Errorf("limited instance memory should identify its cgroup source: %+v", metrics.Memory)
		}
	}
	for _, disk := range metrics.Disks {
		if disk.Path == "/" {
			t.Errorf("instance sampler returned shared root-disk capacity: %+v", disk)
		}
	}
}

func TestInstanceScopeOverride(t *testing.T) {
	t.Setenv("NIMSHOP_SERVER_METRICS_SCOPE", "instance")
	if !instanceScoped() {
		t.Fatal("instance override should force instance scope")
	}
	t.Setenv("NIMSHOP_SERVER_METRICS_SCOPE", "host")
	if instanceScoped() {
		t.Fatal("host override should force host scope")
	}
}

func TestParseCPUQuota(t *testing.T) {
	cases := []struct {
		input string
		want  float64
	}{
		{"200000 100000", 2},
		{"50000 100000", 0.5},
		{"max 100000", 0},
		{"-1 100000", 0},
		{"broken", 0},
	}
	for _, tc := range cases {
		if got := parseCPUQuota(tc.input); got != tc.want {
			t.Errorf("parseCPUQuota(%q) = %v, want %v", tc.input, got, tc.want)
		}
	}
}

func TestCountCPUs(t *testing.T) {
	if got, want := countCPUs("0-3,6,8-9"), 7; got != want {
		t.Fatalf("countCPUs returned %d, want %d", got, want)
	}
	if got, want := countCPUs("0-2,2-4"), 5; got != want {
		t.Fatalf("overlapping cpuset ranges counted as %d CPUs, want %d", got, want)
	}
	if got := countCPUs("not-a-cpuset"); got != 0 {
		t.Fatalf("invalid cpuset should count as 0, got %d", got)
	}
}

func TestParseCgroupMemoryLimit(t *testing.T) {
	if got, want := parseCgroupMemoryLimit("1073741824\n"), uint64(1073741824); got != want {
		t.Fatalf("parseCgroupMemoryLimit returned %d, want %d", got, want)
	}
	for _, value := range []string{"max", "0", "", "9223372036854771712"} {
		if got := parseCgroupMemoryLimit(value); got != 0 {
			t.Errorf("unlimited/invalid memory limit %q parsed as %d", value, got)
		}
	}
}

func TestPercentOfCapacity(t *testing.T) {
	if got := percentOfCapacity(1, 2, 2); math.Abs(got-25) > 0.01 {
		t.Fatalf("percentOfCapacity = %v, want 25", got)
	}
	if got := percentOfCapacity(3, 1, 1); got != 100 {
		t.Fatalf("percentOfCapacity should clamp to 100, got %v", got)
	}
}
