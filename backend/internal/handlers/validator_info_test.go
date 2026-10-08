package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

const testValidatorAddr = "NQ51 SURF MCTM AQXM 15PA K9FX 8EEX 29BE VGH8"

func TestValidatorDirectoryLookupMatchesCanonicalAddress(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[{"name":"Nimiq Surf","address":"` + testValidatorAddr + `","logo":"data:image/svg+xml;base64,AAAA","accentColor":"#e19d3e"}]`))
	}))
	defer srv.Close()
	d := &validatorDirectory{client: srv.Client(), url: srv.URL}

	// Lookup with the address written without spaces: still a match.
	e, ok, err := d.lookup(context.Background(), "NQ51SURFMCTMAQXM15PAK9FX8EEX29BEVGH8")
	if err != nil || !ok {
		t.Fatalf("want a match, got ok=%v err=%v", ok, err)
	}
	if e.Name != "Nimiq Surf" || e.AccentColor != "#e19d3e" {
		t.Fatalf("unexpected entry: %+v", e)
	}
}

func TestValidatorDirectoryKeepsLastGoodListOnFailure(t *testing.T) {
	fail := true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if fail {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		_, _ = w.Write([]byte(`[{"name":"Nimiq Surf","address":"` + testValidatorAddr + `"}]`))
	}))
	defer srv.Close()
	d := &validatorDirectory{client: srv.Client(), url: srv.URL}

	// First refresh fails: nothing known, error reported.
	if _, ok, err := d.lookup(context.Background(), testValidatorAddr); ok || err == nil {
		t.Fatalf("want not found + error, got ok=%v err=%v", ok, err)
	}
	// Upstream recovers; once the short retry window passes, the list refreshes.
	fail = false
	d.expires = time.Now().Add(-time.Second)
	if e, ok, err := d.lookup(context.Background(), testValidatorAddr); !ok || err != nil || e.Name != "Nimiq Surf" {
		t.Fatalf("want refreshed match, got %+v ok=%v err=%v", e, ok, err)
	}
	// A later failure keeps the good list instead of blanking names.
	fail = true
	d.expires = time.Now().Add(-time.Second)
	if e, ok, _ := d.lookup(context.Background(), testValidatorAddr); !ok || e.Name != "Nimiq Surf" {
		t.Fatalf("want last good list kept, got %+v ok=%v", e, ok)
	}
}
