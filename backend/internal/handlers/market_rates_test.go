package handlers

import (
	"encoding/json"
	"testing"
	"time"
)

// The refresher persists its snapshot so a restarted process serves warm
// rates instead of a 503 "warming up" window. The struct keeps unexported
// fields, so the round trip only works through the explicit JSON mapping —
// this test pins that mapping (a plain struct would silently persist "{}").
func TestRateSnapshotJSONRoundTrip(t *testing.T) {
	now := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	in := rateSnapshot{
		nimUSD:     0.00123,
		btcUSD:     71234.5,
		nimAt:      now.Add(-time.Minute),
		btcAt:      now.Add(-2 * time.Minute),
		nimSources: 3,
		btcSources: 2,
		updatedAt:  now,
		fetched:    true,
	}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(raw) == "{}" {
		t.Fatalf("snapshot serialised as an empty object: %s", raw)
	}
	var out rateSnapshot
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.nimUSD != in.nimUSD || out.btcUSD != in.btcUSD ||
		!out.nimAt.Equal(in.nimAt) || !out.btcAt.Equal(in.btcAt) ||
		out.nimSources != in.nimSources || out.btcSources != in.btcSources ||
		!out.updatedAt.Equal(in.updatedAt) {
		t.Fatalf("round trip mismatch:\n in=%+v\nout=%+v", in, out)
	}
	if out.fetched {
		t.Fatalf("fetched is runtime state and must not be persisted")
	}
}

// A legacy "{}" blob (written by the pre-fix code) must decode to the zero
// snapshot so the loader's nimUSD > 0 guard rejects it, not to an error.
func TestRateSnapshotJSONLegacyEmpty(t *testing.T) {
	var out rateSnapshot
	if err := json.Unmarshal([]byte(`{}`), &out); err != nil {
		t.Fatalf("legacy blob must decode: %v", err)
	}
	if out.nimUSD != 0 || out.fetched {
		t.Fatalf("legacy blob must yield the zero snapshot, got %+v", out)
	}
}
