package handlers

/* host_metrics.go — scope-aware CPU, memory, disk and network readings for
 * the operator console's Server card.
 *
 * The sampler distinguishes a dedicated API host from a container or
 * cgroup-limited service. In instance scope it uses process CPU time, cgroup
 * limits/counters where available, process RSS otherwise, and only separately
 * mounted shop data volumes. It intentionally omits shared-host load, model,
 * uptime, memory and root-disk readings instead of presenting them as the
 * user's deployment. Host-wide /proc metrics are used only when no instance
 * boundary is detected (or an operator explicitly sets
 * NIMSHOP_SERVER_METRICS_SCOPE=host).
 *
 * CPU rates are deltas between samples; the first instance sample reports a
 * process-lifetime average. Host memory uses MemAvailable; instance memory uses
 * the cgroup's current/limit pair. Missing or unisolatable values stay absent.
 * Sampling is cached briefly so a fast dashboard poll cannot turn noise into
 * apparently meaningful rates.
 */

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// hostSampleGap is how long a sample is reused. The console polls the dashboard
// every 15s; anything faster would compute deltas over a few milliseconds and
// report noise as load.
var hostSampleMinGap = 2 * time.Second

type hostSampler struct {
	mu   sync.Mutex
	last *hostSample
}

// hostSample is the previous reading the next one is differenced against:
// CPU jiffies and network byte counters are monotonic, so only their change
// over an interval means anything.
type hostSample struct {
	at           time.Time
	cpuBusy      float64 // host jiffies (host scope only)
	cpuTotal     float64 // host jiffies (host scope only)
	processCPU   float64 // seconds from getrusage (instance scope)
	processCPUOK bool
	rx, tx       uint64 // bytes, interfaces in the current network namespace
}

var hostStats hostSampler
var processStartedAt = time.Now()

// HostMetrics is the payload the Overview cards render. Every field is
// explicitly optional: a host that cannot answer simply omits it.
type HostMetrics struct {
	Available      bool              `json:"available"`
	Scope          string            `json:"scope,omitempty"` // "host" or this API service/container "instance"
	Reason         string            `json:"reason,omitempty"`
	SampledAt      time.Time         `json:"sampled_at"`
	UptimeSeconds  float64           `json:"uptime_seconds,omitempty"`
	CPU            *HostCPU          `json:"cpu,omitempty"`
	Memory         *HostMemory       `json:"memory,omitempty"`
	Disks          []HostDisk        `json:"disks,omitempty"`
	Network        *HostNetwork      `json:"network,omitempty"`
	Process        *HostProcess      `json:"process,omitempty"`
	SampleInterval float64           `json:"sample_interval_seconds,omitempty"`
	Platform       map[string]string `json:"platform,omitempty"`
}

type HostCPU struct {
	Percent          float64   `json:"percent"`            // over the sample interval, normalized to scope
	SinceBootPercent float64   `json:"since_boot_percent"` // cumulative average for this scope
	Cores            int       `json:"cores"`
	Capacity         float64   `json:"capacity,omitempty"` // vCPU capacity, may be fractional in instance scope
	CapacitySource   string    `json:"capacity_source,omitempty"`
	Load1            float64   `json:"load1,omitempty"`
	Load5            float64   `json:"load5,omitempty"`
	Load15           float64   `json:"load15,omitempty"`
	Model            string    `json:"model,omitempty"`
	PerCore          []float64 `json:"per_core,omitempty"`
}

type HostMemory struct {
	TotalBytes      uint64  `json:"total_bytes"`
	UsedBytes       uint64  `json:"used_bytes"`
	AvailableBytes  uint64  `json:"available_bytes"`
	Percent         float64 `json:"percent"`
	ProcessRSSBytes uint64  `json:"process_rss_bytes,omitempty"`
	Source          string  `json:"source,omitempty"` // host, cgroup, or process-only
	SwapTotalBytes  uint64  `json:"swap_total_bytes,omitempty"`
	SwapUsedBytes   uint64  `json:"swap_used_bytes,omitempty"`
}

type HostDisk struct {
	Path       string  `json:"path"`
	Label      string  `json:"label"`
	TotalBytes uint64  `json:"total_bytes"`
	UsedBytes  uint64  `json:"used_bytes"`
	AvailBytes uint64  `json:"avail_bytes"`
	Percent    float64 `json:"percent"`
}

type HostNetwork struct {
	RxBytesTotal  uint64  `json:"rx_bytes_total"`
	TxBytesTotal  uint64  `json:"tx_bytes_total"`
	RxBytesPerSec float64 `json:"rx_bytes_per_sec"`
	TxBytesPerSec float64 `json:"tx_bytes_per_sec"`
}

type HostProcess struct {
	Goroutines int     `json:"goroutines"`
	HeapBytes  uint64  `json:"heap_bytes"`
	RSSBytes   uint64  `json:"rss_bytes,omitempty"`
	SysBytes   uint64  `json:"sys_bytes"`
	GoVersion  string  `json:"go_version"`
	GCPercent  float64 `json:"gc_percent,omitempty"`
}

// hostMetrics is scope-aware: it returns host-wide counters only on a dedicated
// machine. Containers and cgroup-limited services get their own process/cgroup
// readings instead, so a shared node's CPU, RAM, uptime and root disk never
// masquerade as this shop's resources.
func hostMetrics() HostMetrics {
	if runtime.GOOS == "linux" && instanceScoped() {
		return instanceMetrics()
	}
	return hostMetricsMachine()
}

// hostMetricsMachine collects machine-wide metrics only when the API is running
// directly on a dedicated host (not in a container or resource-limited cgroup).
func hostMetricsMachine() HostMetrics {
	out := HostMetrics{SampledAt: time.Now().UTC(), Scope: "host"}
	if runtime.GOOS != "linux" {
		out.Reason = "host metrics need Linux /proc; this build runs on " + runtime.GOOS
		return out
	}
	now := time.Now()
	busy, total, perCore, ok := readCPUStat()
	if !ok {
		out.Reason = "/proc/stat is not readable in this container"
		return out
	}
	rx, tx, netOK := readNetDev()
	disks := readDisks(false)
	processCPU, processCPUOK := readProcessCPUSeconds()

	sample := &hostSample{
		at: now, cpuBusy: busy, cpuTotal: total,
		processCPU: processCPU, processCPUOK: processCPUOK,
		rx: rx, tx: tx,
	}

	hostStats.mu.Lock()
	prev := hostStats.last
	reuse := prev != nil && now.Sub(prev.at) < hostSampleMinGap
	if !reuse {
		hostStats.last = sample
	}
	hostStats.mu.Unlock()

	out.Available = true
	out.Platform = map[string]string{"os": runtime.GOOS, "arch": runtime.GOARCH}

	if up, err := readUptime(); err == nil {
		out.UptimeSeconds = up
	}

	cpu := &HostCPU{
		Cores: runtime.NumCPU(), Capacity: float64(runtime.NumCPU()),
		CapacitySource: "host", PerCore: perCore,
	}
	if total > 0 {
		cpu.SinceBootPercent = round1(busy / total * 100)
		cpu.Percent = cpu.SinceBootPercent
	}
	if prev != nil {
		dt, db := total-prev.cpuTotal, busy-prev.cpuBusy
		if dt > 0 {
			cpu.Percent = round1(db / dt * 100)
			out.SampleInterval = round2(now.Sub(prev.at).Seconds())
		}
	}
	if l1, l5, l15, ok := readLoadAvg(); ok {
		cpu.Load1, cpu.Load5, cpu.Load15 = l1, l5, l15
	}
	cpu.Model = readCPUModel()
	out.CPU = cpu

	processRSS := readProcessRSSBytes()
	if mem, ok := readMemInfo(); ok {
		mem.ProcessRSSBytes = processRSS
		mem.Source = "host"
		out.Memory = mem
	}
	out.Disks = disks

	if netOK {
		net := &HostNetwork{RxBytesTotal: rx, TxBytesTotal: tx}
		if prev != nil {
			if dt := now.Sub(prev.at).Seconds(); dt > 0.5 {
				if rx >= prev.rx {
					net.RxBytesPerSec = round2(float64(rx-prev.rx) / dt)
				}
				if tx >= prev.tx {
					net.TxBytesPerSec = round2(float64(tx-prev.tx) / dt)
				}
			}
		}
		out.Network = net
	}

	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	out.Process = &HostProcess{
		Goroutines: runtime.NumGoroutine(),
		HeapBytes:  ms.HeapAlloc,
		RSSBytes:   processRSS,
		SysBytes:   ms.Sys,
		GoVersion:  runtime.Version(),
	}
	return out
}

// instanceMetrics deliberately avoids host-wide /proc values. CPU is the API
// process's own CPU time normalized by the instance's effective CPU capacity;
// memory comes from this cgroup when a finite limit is exposed, otherwise only
// the API process RSS is shown. Host load, model, uptime and root-disk capacity
// are omitted because they may belong to a shared node.
func instanceMetrics() HostMetrics {
	now := time.Now()
	out := HostMetrics{
		SampledAt:     now.UTC(),
		Scope:         "instance",
		UptimeSeconds: now.Sub(processStartedAt).Seconds(),
		Platform:      map[string]string{"os": runtime.GOOS, "arch": runtime.GOARCH},
	}
	if runtime.GOOS != "linux" {
		out.Reason = "instance metrics need Linux process/cgroup counters; this build runs on " + runtime.GOOS
		return out
	}

	cpuTime, cpuOK := readProcessCPUSeconds()
	capacity, capacitySource := readCPUCapacity()
	rx, tx, netOK := readNetDev() // current network namespace only
	processRSS := readProcessRSSBytes()

	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	out.Process = &HostProcess{
		Goroutines: runtime.NumGoroutine(),
		HeapBytes:  ms.HeapAlloc,
		RSSBytes:   processRSS,
		SysBytes:   ms.Sys,
		GoVersion:  runtime.Version(),
	}
	out.Available = true

	sample := &hostSample{
		at: now, processCPU: cpuTime, processCPUOK: cpuOK,
		rx: rx, tx: tx,
	}
	hostStats.mu.Lock()
	prev := hostStats.last
	reuse := prev != nil && now.Sub(prev.at) < hostSampleMinGap
	if !reuse {
		hostStats.last = sample
	}
	hostStats.mu.Unlock()

	if cpuOK && capacity > 0 {
		cpu := &HostCPU{
			Cores: int(math.Ceil(capacity)), Capacity: capacity,
			CapacitySource: capacitySource,
		}
		if elapsed := now.Sub(processStartedAt).Seconds(); elapsed > 0 {
			cpu.SinceBootPercent = percentOfCapacity(cpuTime, elapsed, capacity)
			cpu.Percent = cpu.SinceBootPercent
		}
		if prev != nil && prev.processCPUOK {
			elapsed := now.Sub(prev.at).Seconds()
			used := cpuTime - prev.processCPU
			if elapsed > 0 && used >= 0 {
				cpu.Percent = percentOfCapacity(used, elapsed, capacity)
				out.SampleInterval = round2(elapsed)
			}
		}
		out.CPU = cpu
	}

	if memory, ok := readCgroupMemory(); ok {
		memory.ProcessRSSBytes = processRSS
		memory.Source = "cgroup"
		out.Memory = memory
	} else if processRSS > 0 {
		// No finite instance limit is exposed. Do not substitute /proc/meminfo:
		// inside many containers that is the shared node's RAM, not this app's.
		out.Memory = &HostMemory{ProcessRSSBytes: processRSS, Source: "process-only"}
	}

	out.Disks = readDisks(true)
	if netOK {
		net := &HostNetwork{RxBytesTotal: rx, TxBytesTotal: tx}
		if prev != nil {
			if elapsed := now.Sub(prev.at).Seconds(); elapsed > 0.5 {
				if rx >= prev.rx {
					net.RxBytesPerSec = round2(float64(rx-prev.rx) / elapsed)
				}
				if tx >= prev.tx {
					net.TxBytesPerSec = round2(float64(tx-prev.tx) / elapsed)
				}
			}
		}
		out.Network = net
	}
	return out
}

func percentOfCapacity(cpuSeconds, wallSeconds, capacity float64) float64 {
	if cpuSeconds <= 0 || wallSeconds <= 0 || capacity <= 0 {
		return 0
	}
	return round1(math.Max(0, math.Min(100, cpuSeconds/(wallSeconds*capacity)*100)))
}

// instanceScoped is intentionally conservative: a container, a PaaS runtime,
// or a cgroup-limited service must never fall back to the physical node's
// global counters. The explicit env override is useful on unusual hosts whose
// container markers are hidden by the platform.
func instanceScoped() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("NIMSHOP_SERVER_METRICS_SCOPE"))) {
	case "instance", "container", "process":
		return true
	case "host", "machine":
		return false
	}

	for _, marker := range []string{"/.dockerenv", "/run/.containerenv"} {
		if _, err := os.Stat(marker); err == nil {
			return true
		}
	}
	for _, key := range []string{
		"KUBERNETES_SERVICE_HOST", "RAILWAY_ENVIRONMENT", "FLY_APP_NAME",
		"DYNO", "ECS_CONTAINER_METADATA_URI", "ECS_CONTAINER_METADATA_URI_V4",
		"K_SERVICE", "WEBSITE_INSTANCE_ID", "RENDER_INSTANCE_ID",
	} {
		if strings.TrimSpace(os.Getenv(key)) != "" {
			return true
		}
	}

	if containerCgroupPath() || rootFilesystemIsOverlay() {
		return true
	}
	if readCPUQuota() > 0 || readCgroupMemoryLimitBytes() > 0 {
		return true
	}
	if cpus := readCPUSetCapacity(); cpus > 0 && cpus < runtime.NumCPU() {
		return true
	}
	return false
}

func containerCgroupPath() bool {
	raw, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return false
	}
	text := strings.ToLower(string(raw))
	for _, marker := range []string{
		"/docker/", "docker-", "/kubepods/", "kubepods.slice",
		"/containerd/", "cri-containerd-", "/libpod/", "/lxc/", "/ecs/",
	} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

func rootFilesystemIsOverlay() bool {
	raw, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(raw), "\n") {
		before, after, found := strings.Cut(line, " - ")
		if !found {
			continue
		}
		left, right := strings.Fields(before), strings.Fields(after)
		if len(left) >= 5 && left[4] == "/" && len(right) > 0 && (right[0] == "overlay" || right[0] == "aufs") {
			return true
		}
	}
	return false
}

func cgroupDirectories(controller string) []string {
	var unifiedPath string
	var controllerPath string
	var raw []byte
	if contents, err := os.ReadFile("/proc/self/cgroup"); err == nil {
		raw = contents
	}
	for _, line := range strings.Split(string(raw), "\n") {
		parts := strings.SplitN(line, ":", 3)
		if len(parts) != 3 {
			continue
		}
		if parts[1] == "" {
			unifiedPath = parts[2]
			continue
		}
		for _, name := range strings.Split(parts[1], ",") {
			if name == controller {
				controllerPath = parts[2]
				break
			}
		}
	}

	var dirs []string
	if unifiedPath != "" {
		appendCurrentCgroup(&dirs, "/sys/fs/cgroup", unifiedPath)
	}
	if controllerPath != "" {
		roots := []string{filepath.Join("/sys/fs/cgroup", controller)}
		if controller == "cpu" || controller == "cpuacct" {
			roots = append(roots, "/sys/fs/cgroup/cpu,cpuacct")
		}
		for _, root := range roots {
			appendCurrentCgroup(&dirs, root, controllerPath)
		}
	}
	return dirs
}

// appendCurrentCgroup resolves only the process's own cgroup directory. Its
// ancestors can be shared slices containing sibling workloads, so their
// current-memory or quota values must not be passed off as this API instance's.
func appendCurrentCgroup(dirs *[]string, root, cgroupPath string) {
	root = filepath.Clean(root)
	rel := strings.Trim(strings.TrimSpace(cgroupPath), "/")
	if rel != "" && !filepath.IsLocal(rel) {
		return
	}
	dir := root
	if rel != "" {
		dir = filepath.Join(root, rel)
	}
	*dirs = appendUniquePath(*dirs, dir)
}

func appendUniquePath(paths []string, candidate string) []string {
	candidate = filepath.Clean(candidate)
	for _, path := range paths {
		if filepath.Clean(path) == candidate {
			return paths
		}
	}
	return append(paths, candidate)
}

func readCPUQuota() float64 {
	best := 0.0
	for _, dir := range cgroupDirectories("cpu") {
		if raw, err := os.ReadFile(filepath.Join(dir, "cpu.max")); err == nil {
			if quota := parseCPUQuota(string(raw)); quota > 0 && (best == 0 || quota < best) {
				best = quota
			}
		}
		quotaRaw, qErr := os.ReadFile(filepath.Join(dir, "cpu.cfs_quota_us"))
		periodRaw, pErr := os.ReadFile(filepath.Join(dir, "cpu.cfs_period_us"))
		if qErr == nil && pErr == nil {
			q, qe := strconv.ParseFloat(strings.TrimSpace(string(quotaRaw)), 64)
			p, pe := strconv.ParseFloat(strings.TrimSpace(string(periodRaw)), 64)
			if qe == nil && pe == nil && q > 0 && p > 0 {
				quota := q / p
				if best == 0 || quota < best {
					best = quota
				}
			}
		}
	}
	return best
}

func parseCPUQuota(raw string) float64 {
	fields := strings.Fields(raw)
	if len(fields) < 2 || fields[0] == "max" {
		return 0
	}
	quota, qErr := strconv.ParseFloat(fields[0], 64)
	period, pErr := strconv.ParseFloat(fields[1], 64)
	if qErr != nil || pErr != nil || quota <= 0 || period <= 0 {
		return 0
	}
	return quota / period
}

func readCPUSetCapacity() int {
	for _, dir := range cgroupDirectories("cpuset") {
		for _, name := range []string{"cpuset.cpus.effective", "cpuset.cpus"} {
			raw, err := os.ReadFile(filepath.Join(dir, name))
			if err != nil {
				continue
			}
			if count := countCPUs(string(raw)); count > 0 {
				return count
			}
		}
	}
	return 0
}

func countCPUs(raw string) int {
	seen := make(map[int]struct{})
	for _, item := range strings.Split(strings.TrimSpace(raw), ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if lowRaw, highRaw, ranged := strings.Cut(item, "-"); ranged {
			low, lowErr := strconv.Atoi(strings.TrimSpace(lowRaw))
			high, highErr := strconv.Atoi(strings.TrimSpace(highRaw))
			if lowErr != nil || highErr != nil || low < 0 || high < low || high-low > 4096 {
				continue
			}
			for cpu := low; cpu <= high; cpu++ {
				seen[cpu] = struct{}{}
			}
			continue
		}
		if cpu, err := strconv.Atoi(item); err == nil && cpu >= 0 {
			seen[cpu] = struct{}{}
		}
	}
	return len(seen)
}

func readCPUCapacity() (float64, string) {
	quota := readCPUQuota()
	cpuset := readCPUSetCapacity()
	if quota > 0 && cpuset > 0 && float64(cpuset) < quota {
		return float64(cpuset), "cpuset"
	}
	if quota > 0 {
		return quota, "cgroup quota"
	}
	if cpuset > 0 {
		return float64(cpuset), "cpuset"
	}
	procs := runtime.GOMAXPROCS(0)
	if procs < 1 {
		procs = runtime.NumCPU()
	}
	if procs < 1 {
		procs = 1
	}
	return float64(procs), "Go runtime capacity"
}

func findCgroupMemoryLimit() (uint64, string, string) {
	var best uint64
	var bestDir, bestUsage string
	for _, dir := range cgroupDirectories("memory") {
		candidates := []struct{ limit, usage string }{
			{"memory.max", "memory.current"},
			{"memory.limit_in_bytes", "memory.usage_in_bytes"},
		}
		for _, candidate := range candidates {
			raw, err := os.ReadFile(filepath.Join(dir, candidate.limit))
			if err != nil {
				continue
			}
			limit := parseCgroupMemoryLimit(string(raw))
			if limit > 0 && (best == 0 || limit < best) {
				best, bestDir, bestUsage = limit, dir, candidate.usage
			}
		}
	}
	return best, bestDir, bestUsage
}

func parseCgroupMemoryLimit(raw string) uint64 {
	value := strings.TrimSpace(raw)
	if value == "" || value == "max" {
		return 0
	}
	limit, err := strconv.ParseUint(value, 10, 64)
	if err != nil || limit == 0 || limit >= uint64(1)<<62 {
		return 0
	}
	return limit
}

func readCgroupMemoryLimitBytes() uint64 {
	limit, _, _ := findCgroupMemoryLimit()
	return limit
}

func readCgroupMemory() (*HostMemory, bool) {
	limit, dir, usageFile := findCgroupMemoryLimit()
	if limit == 0 || dir == "" || usageFile == "" {
		return nil, false
	}
	raw, err := os.ReadFile(filepath.Join(dir, usageFile))
	if err != nil {
		return nil, false
	}
	used, err := strconv.ParseUint(strings.TrimSpace(string(raw)), 10, 64)
	if err != nil {
		return nil, false
	}
	used = min(used, limit)
	available := limit - used
	return &HostMemory{
		TotalBytes: limit, UsedBytes: used, AvailableBytes: available,
		Percent: round1(float64(used) / float64(limit) * 100),
	}, true
}

func readProcessRSSBytes() uint64 {
	raw, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if !strings.HasPrefix(line, "VmRSS:") {
			continue
		}
		fields := strings.Fields(strings.TrimPrefix(line, "VmRSS:"))
		if len(fields) == 0 {
			return 0
		}
		if kb, err := strconv.ParseUint(fields[0], 10, 64); err == nil {
			return kb * 1024
		}
	}
	return 0
}

func readProcessCPUSeconds() (float64, bool) {
	var usage syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &usage); err != nil {
		return 0, false
	}
	user := float64(usage.Utime.Sec) + float64(usage.Utime.Usec)/1_000_000
	system := float64(usage.Stime.Sec) + float64(usage.Stime.Usec)/1_000_000
	return user + system, true
}

func round1(v float64) float64 { return math.Round(v*10) / 10 }
func round2(v float64) float64 { return math.Round(v*100) / 100 }

// readCPUStat sums /proc/stat's aggregate cpu line, plus each core. busy
// excludes idle AND iowait: a disk wait is not CPU work, and counting it made
// a healthy box look pegged.
func readCPUStat() (busy, total float64, perCore []float64, ok bool) {
	raw, err := os.ReadFile("/proc/stat")
	if err != nil {
		return 0, 0, nil, false
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if !strings.HasPrefix(line, "cpu") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}
		var sum, idle float64
		for i, f := range fields[1:] {
			v, err := strconv.ParseFloat(f, 64)
			if err != nil {
				sum = 0
				break
			}
			sum += v
			if i == 3 || i == 4 { // idle, iowait
				idle += v
			}
		}
		if sum == 0 {
			continue
		}
		if fields[0] == "cpu" {
			busy, total, ok = sum-idle, sum, true
			continue
		}
		if pct := (sum - idle) / sum * 100; len(perCore) < 64 {
			perCore = append(perCore, round1(pct))
		}
	}
	return busy, total, perCore, ok
}

func readLoadAvg() (l1, l5, l15 float64, ok bool) {
	raw, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return 0, 0, 0, false
	}
	f := strings.Fields(string(raw))
	if len(f) < 3 {
		return 0, 0, 0, false
	}
	for i, dst := range []*float64{&l1, &l5, &l15} {
		if v, err := strconv.ParseFloat(f[i], 64); err == nil {
			*dst = v
		}
	}
	return l1, l5, l15, true
}

func readUptime() (float64, error) {
	raw, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0, err
	}
	f := strings.Fields(string(raw))
	if len(f) == 0 {
		return 0, os.ErrInvalid
	}
	return strconv.ParseFloat(f[0], 64)
}

// readCPUModel names the processor when the kernel publishes it. Cosmetic, and
// absent on some hosts — the card simply omits the line.
func readCPUModel() string {
	raw, err := os.ReadFile("/proc/cpuinfo")
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if !strings.HasPrefix(line, "model name") && !strings.HasPrefix(line, "Hardware") {
			continue
		}
		if _, v, found := strings.Cut(line, ":"); found {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func readMemInfo() (*HostMemory, bool) {
	raw, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return nil, false
	}
	kv := map[string]uint64{}
	for _, line := range strings.Split(string(raw), "\n") {
		key, rest, found := strings.Cut(line, ":")
		if !found {
			continue
		}
		f := strings.Fields(rest)
		if len(f) == 0 {
			continue
		}
		if v, err := strconv.ParseUint(f[0], 10, 64); err == nil {
			kv[strings.TrimSpace(key)] = v * 1024 // kB → bytes
		}
	}
	total := kv["MemTotal"]
	if total == 0 {
		return nil, false
	}
	avail := kv["MemAvailable"]
	if avail == 0 { // pre-3.14 kernels
		avail = kv["MemFree"] + kv["Buffers"] + kv["Cached"]
	}
	used := total - min(avail, total)
	swapTotal := kv["SwapTotal"]
	swapUsed := uint64(0)
	if swapTotal > 0 {
		swapUsed = swapTotal - min(kv["SwapFree"], swapTotal)
	}
	return &HostMemory{
		TotalBytes:     total,
		UsedBytes:      used,
		AvailableBytes: min(avail, total),
		Percent:        round1(float64(used) / float64(total) * 100),
		SwapTotalBytes: swapTotal,
		SwapUsedBytes:  swapUsed,
	}, true
}

// readDisks reports only filesystems relevant to the shop. In instance scope,
// the node root is excluded, and mounts with the same filesystem identity are
// listed once (e.g. BADGER_DIR=/data/badger and /data on the same volume).
func readDisks(instanceScope bool) []HostDisk {
	var out []HostDisk
	candidates := []struct{ path, label string }{
		{strings.TrimSpace(os.Getenv("BADGER_DIR")), "Data volume"},
		{"/data", "Data volume"},
	}
	if !instanceScope {
		candidates = append(candidates, struct{ path, label string }{"/", "Root filesystem"})
	}

	var rootIdentity string
	if instanceScope {
		var root syscall.Statfs_t
		if syscall.Statfs("/", &root) == nil {
			rootIdentity = filesystemIdentity(root)
		}
	}
	seenPaths := make(map[string]bool)
	seenFilesystems := make(map[string]bool)
	for _, candidate := range candidates {
		path := strings.TrimSpace(candidate.path)
		if path == "" || seenPaths[filepath.Clean(path)] {
			continue
		}
		var st syscall.Statfs_t
		if err := syscall.Statfs(path, &st); err != nil {
			continue
		}
		seenPaths[filepath.Clean(path)] = true
		identity := filesystemIdentity(st)
		if identity == rootIdentity || seenFilesystems[identity] {
			continue
		}
		bsize := uint64(st.Bsize)
		total := st.Blocks * bsize
		free := st.Bavail * bsize
		if total == 0 {
			continue
		}
		used := total - min(free, total)
		seenFilesystems[identity] = true
		out = append(out, HostDisk{
			Path:       path,
			Label:      candidate.label,
			TotalBytes: total,
			UsedBytes:  used,
			AvailBytes: min(free, total),
			Percent:    round1(float64(used) / float64(total) * 100),
		})
	}
	return out
}

func filesystemIdentity(st syscall.Statfs_t) string {
	return fmt.Sprintf("%v:%v", st.Fsid, st.Type)
}

// readNetDev sums byte counters over every interface except loopback. Loopback
// traffic is the shop talking to itself (the Badger pages, the proxy hop) and
// would otherwise dominate the number.
func readNetDev() (rx, tx uint64, ok bool) {
	raw, err := os.ReadFile("/proc/net/dev")
	if err != nil {
		return 0, 0, false
	}
	for _, line := range strings.Split(string(raw), "\n") {
		name, rest, found := strings.Cut(line, ":")
		if !found {
			continue
		}
		name = strings.TrimSpace(name)
		if name == "" || name == "lo" || strings.HasPrefix(name, "lo:") {
			continue
		}
		f := strings.Fields(rest)
		if len(f) < 9 {
			continue
		}
		r, err1 := strconv.ParseUint(f[0], 10, 64)
		t, err2 := strconv.ParseUint(f[8], 10, 64)
		if err1 != nil || err2 != nil {
			continue
		}
		rx += r
		tx += t
		ok = true
	}
	return rx, tx, ok
}
