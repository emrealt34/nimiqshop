package brandlogo

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func pngBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetNRGBA(x, y, color.NRGBA{R: 200, G: 30, B: 60, A: 255})
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestURLFromBrandFieldIsHTTPSOnly(t *testing.T) {
	if got := URLFromBrandField("[https://cdn.example.com/steam.png]"); got != "https://cdn.example.com/steam.png" {
		t.Fatalf("bracketed https logo: %q", got)
	}
	for _, bad := range []string{"", "http://cdn.example.com/a.png", "javascript:alert(1)", "not a url"} {
		if got := URLFromBrandField(bad); got != "" {
			t.Fatalf("%q accepted as %q", bad, got)
		}
	}
}

func TestTileKeepsAspectAndBound(t *testing.T) {
	tile, err := tileFromBytes(pngBytes(t, 300, 150))
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(bytes.NewReader(tile))
	if err != nil {
		t.Fatal(err)
	}
	b := img.Bounds()
	if b.Dx() != TileSide || b.Dy() != TileSide/2 {
		t.Fatalf("tile %dx%d, want %dx%d", b.Dx(), b.Dy(), TileSide, TileSide/2)
	}
}

func TestResolverFetchesOnceAndCaches(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write(pngBytes(t, 64, 64))
	}))
	defer srv.Close()
	r := New()
	r.HTTP = srv.Client()
	ctx := context.Background()
	if r.PNG(ctx, srv.URL+"/logo.png") == nil {
		t.Fatal("logo not produced")
	}
	if r.PNG(ctx, srv.URL+"/logo.png") == nil || hits.Load() != 1 {
		t.Fatalf("second call must come from cache, hits=%d", hits.Load())
	}
}

func TestResolverMissingLogoIsNilAndCached(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.NotFound(w, r)
	}))
	defer srv.Close()
	r := New()
	r.HTTP = srv.Client()
	ctx := context.Background()
	if r.PNG(ctx, srv.URL+"/gone.png") != nil || r.PNG(ctx, srv.URL+"/gone.png") != nil {
		t.Fatal("missing logo must be nil")
	}
	if hits.Load() != 1 {
		t.Fatalf("failed fetch must be cached, hits=%d", hits.Load())
	}
	if r.PNG(ctx, "http://insecure.example/logo.png") != nil {
		t.Fatal("non-https logo must never be fetched")
	}
}
