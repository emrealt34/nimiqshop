package handlers

/* host_metrics.go — the live numbers behind the operator console's server
 * cards: CPU, memory, disk and network on the machine the API runs on.
 *
 * WHY IT LIVES HERE. Everything else in the console describes the SHOP (users,
 * orders, cashback). These describe the HOST, and an operator needs them in the
 * same place: "is it the supplier that is slow, or is this box out of RAM?" is
 * the first question when something feels wrong, and until now answering it
 * meant opening a hosting dashboard the console does not know about.
 *
 * WHERE THE NUMBERS COME FROM. /proc on Linux — the same source `top`, `free`,
 * `df` and `ifstat` read — plus two Go runtime counters. No third-party agent,
 * no metrics service, nothing installed: the backend already runs there, so it
 * can read what the kernel publishes about itself.
 *
 * HONEST LIMITS, which the console repeats in the card's hint line:
 *   - CPU percent is a DELTA between two samples (this call and the previous
 *     one), so it describes the interval, not "now"; the very first call after
 *     a restart can only report the since-boot average, which is why both are
 *     sent and labelled.
 *   - Memory "used" is total minus available (the kernel's own definition, the
 *     one `free` prints), not total minus free: page cache is not pressure.
 *   - Disk is reported per filesystem the shop actually uses — the volume the
 *     database lives on and the root filesystem — because a full database
 *     volume is an outage while a full root is usually just noise.
 *   - On a host without /proc (a developer's Mac, Windows) the sampler reports
 *     available=false with the reason, and the console says so instead of
 *     drawing zeroes that look like a healthy machine.
 *
 * Sampling is cheap but not free, and the console polls: results are cached for
 * a couple of seconds, which also makes the deltas meaningful instead of
 * racing the poll interval.
 */

import (
	"math"
	"os"
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
const hostSampleGap = 2 * time.Second

type hostSampler struct {
	mu   sync.Mutex
	last *hostSample
	at   time.Time
}

type hostSample struct {
	at       time.Time
	cpuBusy  float64 // jiffies
	cpuTotal float64 // jiffies
	rx, tx   uint64  // bytes, all interfaces except loopback
	diskUsed map[string]uint64
	diskAll  map[string]uint64
}

var hostStats hostSampler

// HostMetrics is the payload the Overview cards render. Every field is
// explicitly optional: a host that cannot answer simply omits it.
type HostMetrics struct {
	Available      bool              `json:"available"`
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
	Percent          float64   `json:"percent"`            // over the sample interval
	SinceBootPercent float64   `json:"since_boot_percent"` // cumulative average
	Cores            int       `json:"cores"`
	Load1            float64   `json:"load1,omitempty"`
	Load5            float64   `json:"load5,omitempty"`
	Load15           float64   `json:"load15,omitempty"`
	Model            string    `json:"model,omitempty"`
	PerCore          []float64 `json:"per_core,omitempty"`
}

type HostMemory struct {
	TotalBytes     uint64  `json:"total_bytes"`
	UsedBytes      uint64  `json:"used_bytes"`
	AvailableBytes uint64  `json:"available_bytes"`
	Percent        float64 `json:"percent"`
	SwapTotalBytes uint64  `json:"swap_total_bytes,omitempty"`
	SwapUsedBytes  uint64  `json:"swap_used_bytes,omitempty"`
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
	SysBytes   uint64  `json:"sys_bytes"`
	GoVersion  string  `json:"go_version"`
	GCPercent  float64 `json:"gc_percent,omitempty"`
}

// hostMetrics collects a sample, reusing the previous one to turn the kernel's
// monotonic counters into rates.
func (h *Handlers) hostMetrics() HostMetrics {
	out := HostMetrics{SampledAt: time.Now().UTC()}
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
	disks, diskUsed, diskAll := readDisks()

	sample := &hostSample{at: now, cpuBusy: busy, cpuTotal: total, rx: rx, tx: tx, diskUsed: diskUsed, diskAll: diskAll}

	hostStats.mu.Lock()
	prev := hostStats.last
	reuse := prev != nil && now.Sub(prev.at) < hostSampleGap
	if !reuse {
		hostStats.last = sample
	}
	hostStats.mu.Unlock()

	out.Available = true
	out.Platform = map[string]string{"os": runtime.GOOS, "arch": runtime.GOARCH}

	if up, err := readUptime(); err == nil {
		out.UptimeSeconds = up
	}

	cpu := &HostCPU{Cores: runtime.NumCPU(), PerCore: perCore}
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

	if mem, ok := readMemInfo(); ok {
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
		SysBytes:   ms.Sys,
		GoVersion:  runtime.Version(),
	}
	return out
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

// readDisks reports the filesystems the shop cares about: the volume the
// database lives on (BADGER_DIR when set, /data otherwise — the doc pins it
// there) and the root filesystem. Missing ones are skipped rather than shown as
// zero-sized.
func readDisks() ([]HostDisk, map[string]uint64, map[string]uint64) {
	used := map[string]uint64{}
	all := map[string]uint64{}
	var out []HostDisk
	candidates := []struct{ path, label string }{
		{strings.TrimSpace(os.Getenv("BADGER_DIR")), "Data volume"},
		{"/data", "Data volume"},
		{"/", "Root filesystem"},
	}
	seen := map[string]bool{}
	for _, c := range candidates {
		path, label := c.path, c.label
		if path == "" || seen[path] {
			continue
		}
		var st syscall.Statfs_t
		if err := syscall.Statfs(path, &st); err != nil {
			continue
		}
		seen[path] = true
		bsize := uint64(st.Bsize)
		total := st.Blocks * bsize
		free := st.Bavail * bsize
		if total == 0 {
			continue
		}
		usd := total - min(free, total)
		out = append(out, HostDisk{
			Path:       path,
			Label:      label,
			TotalBytes: total,
			UsedBytes:  usd,
			AvailBytes: min(free, total),
			Percent:    round1(float64(usd) / float64(total) * 100),
		})
		used[path] = usd
		all[path] = total
	}
	return out, used, all
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
