package memlimit

import "testing"

func TestParseLimit(t *testing.T) {
	for _, tc := range []struct {
		name, raw string
		want      int64
	}{
		{"bytes", "536870912", 536870912},
		{"newline", "1073741824\n", 1073741824},
		{"cgroup v2 unlimited", "max", 0},
		{"v1 sentinel unlimited", "9223372036854771712", 0},
		{"empty", "", 0},
		{"garbage", "not-a-number", 0},
		{"zero", "0", 0},
		{"negative", "-1", 0},
	} {
		if got := parseLimit(tc.raw); got != tc.want {
			t.Errorf("%s: parseLimit(%q) = %d, want %d", tc.name, tc.raw, got, tc.want)
		}
	}
}

// TestLimitBytesFindsSomething documents what the function is for: on a Linux
// CI runner (cgroup-limited or not) it must answer with a positive budget and
// name where the number came from, because both the heap ceiling and the
// database cache sizes are derived from it.
func TestLimitBytesFindsSomething(t *testing.T) {
	limit, source := LimitBytes()
	if limit <= 0 || source == "" {
		t.Skipf("no memory limit discoverable on this host (limit=%d source=%q)", limit, source)
	}
	if limit < 64<<20 {
		t.Errorf("implausible limit: %d bytes from %s", limit, source)
	}
	if source != "cgroup" && source != "host" {
		t.Errorf("unexpected source: %q", source)
	}
}
