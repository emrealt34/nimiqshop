// Package memlimit answers one question: how much memory is this process
// actually allowed to use?
//
// WHY IT EXISTS. The backend's cost is dominated by things that are sized in
// memory — Badger's memtables and block cache, the fasthttp connection
// ceiling, and the Go heap's own soft limit. Every one of those carries a
// sensible default, and every default was chosen for a machine of unknown
// size; on a container with a hard cap, the defaults of a big host are how a
// service gets OOM-killed while reporting a perfectly healthy heap.
//
// On a bare metal or VM host the kernel tells us the total RAM and the process
// may use as much of it as it likes. In a container it is the CGROUP limit
// that ends the process, and that number is usually far smaller than the host's
// RAM — which is exactly the case this package detects. The value is exposed to
// the operator (see RuntimeEnvelope) so the memory story is visible in the
// console rather than buried in a hosting dashboard.
package memlimit

import (
	"os"
	"strconv"
	"strings"
)

// cgroupPaths are the places the kernel publishes the memory ceiling, newest
// first: cgroup v2, then v1, then the v1 container-manager variant.
var cgroupPaths = []string{
	"/sys/fs/cgroup/memory.max",
	"/sys/fs/cgroup/memory/memory.limit_in_bytes",
	"/sys/fs/cgroup/memory.limit_in_bytes",
}

// unlimited is what the cgroup files say when no cap is configured: cgroup v2
// writes "max", v1 writes a huge sentinel value. Anything at or above this
// threshold is treated as "no container limit".
const unlimited = int64(1) << 62

// LimitBytes returns the memory ceiling this process runs under, in bytes, and
// where that number came from ("cgroup" when a container cap applies). A return
// of 0 means no limit was found — the caller should then keep its own defaults.
func LimitBytes() (int64, string) {
	for _, path := range cgroupPaths {
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		if limit := parseLimit(string(raw)); limit > 0 {
			return limit, "cgroup"
		}
	}
	if total := hostRAMBytes(); total > 0 {
		// Not a container, but still worth knowing: on a small VM the envelope
		// should be computed from the box rather than from a default meant for
		// a bigger one.
		return total, "host"
	}
	return 0, ""
}

// parseLimit reads one cgroup limit file's contents. "max" and the v1 sentinel
// both mean unlimited; a trailing newline is normal.
func parseLimit(raw string) int64 {
	v := strings.TrimSpace(raw)
	if v == "" || v == "max" {
		return 0
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n <= 0 || n >= unlimited {
		return 0
	}
	return n
}

// hostRAMBytes reads MemTotal from /proc/meminfo. Empty on platforms that do
// not have it (the caller then keeps its defaults).
func hostRAMBytes() int64 {
	raw, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(raw), "\n") {
		key, rest, found := strings.Cut(line, ":")
		if !found || strings.TrimSpace(key) != "MemTotal" {
			continue
		}
		fields := strings.Fields(rest)
		if len(fields) == 0 {
			return 0
		}
		if kb, err := strconv.ParseInt(fields[0], 10, 64); err == nil {
			return kb * 1024
		}
	}
	return 0
}
