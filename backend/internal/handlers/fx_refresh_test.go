package handlers

import (
	"testing"
)

func TestParseERRates(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		wantN   int
		wantErr bool
	}{
		{
			name:  "canonical er-api payload",
			body:  `{"result":"success","base_code":"USD","time_last_update_unix":1780000000,"rates":{"USD":1,"TRY":49.13,"EUR":0.92}}`,
			wantN: 3,
		},
		{
			name:  "mirror without result/base_code still parses",
			body:  `{"rates":{"USD":1,"TRY":49.13}}`,
			wantN: 2,
		},
		{name: "error result rejected", body: `{"result":"error","rates":{"USD":1}}`, wantErr: true},
		{name: "non-USD base rejected", body: `{"result":"success","base_code":"EUR","rates":{"EUR":1,"TRY":53}}`, wantErr: true},
		{name: "empty rates rejected", body: `{"result":"success","base_code":"USD","rates":{}}`, wantErr: true},
		{name: "not json rejected", body: `<html>maintenance</html>`, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rates, err := ParseERRates([]byte(tc.body))
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got rates=%v", rates)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(rates) != tc.wantN {
				t.Fatalf("want %d rates, got %d (%v)", tc.wantN, len(rates), rates)
			}
		})
	}
}

func TestERAPIObservedAt(t *testing.T) {
	if _, ok := erAPIObservedAt([]byte(`{"rates":{"USD":1}}`)); ok {
		t.Fatal("no timestamp field must not yield a time")
	}
	at, ok := erAPIObservedAt([]byte(`{"time_last_update_unix":1780000000}`))
	if !ok || at.Unix() != 1780000000 {
		t.Fatalf("want unix 1780000000, got %v ok=%v", at, ok)
	}
	// A stamp far in the future is clock skew, not an observation.
	if _, ok := erAPIObservedAt([]byte(`{"time_last_update_unix":9999999999}`)); ok {
		t.Fatal("future timestamp must be rejected")
	}
}
