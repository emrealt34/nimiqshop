// Package brandlogo prepares a supplier brand logo as a small PNG tile for the
// gift and order mail.
//
// Why a server-side copy: Gmail blocks data: images, and a remote <img> would
// tell the logo host the reader's IP and open time. The mail therefore carries
// the logo as an inline attachment, and this package produces that attachment:
// https only, bounded download, bounded decode, one fixed tile size.
package brandlogo

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	_ "image/gif" // logos may arrive as GIF, PNG or JPEG
	_ "image/jpeg"
	// The catalog serves WebP (every storefront logo is .webp); the standard
	// library has no WebP decoder.
	_ "golang.org/x/image/webp"
	"image/png"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sync"
	"time"
)

const (
	// MaxSourceBytes caps the logo download. Brand logos are a few KB.
	MaxSourceBytes = 1 << 20
	// MaxSourcePixels caps the decoded size, so a small file that declares a
	// huge canvas cannot exhaust memory (decompression-bomb guard).
	MaxSourcePixels = 4000 * 4000
	// TileSide is the output edge in pixels: 2x the 40px the mail shows, so it
	// stays sharp on high-density screens.
	TileSide = 80
)

var urlRe = regexp.MustCompile(`https?://[^\s)\]]+`)

// URLFromBrandField extracts the usable https URL from a supplier logo field.
// The catalog sometimes wraps the URL in brackets or template text, so the
// first URL-looking run is taken, as the storefront does. Anything that is not
// https returns "".
func URLFromBrandField(raw string) string {
	m := urlRe.FindString(raw)
	if m == "" {
		return ""
	}
	u, err := url.Parse(m)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return ""
	}
	return u.String()
}

type entry struct {
	png []byte
	at  time.Time
	ttl time.Duration
}

// Resolver downloads and converts logos, caching results. A missing logo is
// cached too (shorter), so a broken URL does not slow every send.
type Resolver struct {
	HTTP    *http.Client
	OKTTL   time.Duration
	FailTTL time.Duration
	mu      sync.Mutex
	cache   map[string]entry
	now     func() time.Time
	fetchFn func(ctx context.Context, u string) ([]byte, error)
}

// New returns a Resolver with conservative network limits.
func New() *Resolver {
	r := &Resolver{
		OKTTL:   6 * time.Hour,
		FailTTL: 15 * time.Minute,
		cache:   map[string]entry{},
		now:     time.Now,
	}
	r.HTTP = &http.Client{
		Timeout: 8 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 3 || req.URL.Scheme != "https" {
				return errors.New("brandlogo: redirect refused")
			}
			return nil
		},
	}
	r.fetchFn = r.fetch
	return r
}

// PNG returns the tile for a logo field from the catalog, or nil when there is
// no usable logo. It never returns an error: the mail falls back to a neutral
// tile, which is the right outcome for a missing logo.
func (r *Resolver) PNG(ctx context.Context, rawField string) []byte {
	u := URLFromBrandField(rawField)
	if u == "" {
		return nil
	}
	r.mu.Lock()
	if e, ok := r.cache[u]; ok && r.now().Sub(e.at) < e.ttl {
		r.mu.Unlock()
		return e.png
	}
	r.mu.Unlock()

	data, err := r.fetchFn(ctx, u)
	e := entry{at: r.now(), ttl: r.FailTTL}
	if err == nil {
		if tile, terr := tileFromBytes(data); terr == nil {
			e = entry{png: tile, at: r.now(), ttl: r.OKTTL}
		}
	}
	r.mu.Lock()
	r.cache[u] = e
	r.mu.Unlock()
	return e.png
}

func (r *Resolver) fetch(ctx context.Context, u string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "image/png,image/jpeg,image/gif;q=0.9")
	resp, err := r.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, errors.New("brandlogo: status " + resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxSourceBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > MaxSourceBytes {
		return nil, errors.New("brandlogo: logo too large")
	}
	return body, nil
}

// tileFromBytes decodes a raster logo (PNG, JPEG, GIF or WebP) and returns a TileSide-bounded PNG,
// keeping the aspect ratio and transparency.
func tileFromBytes(data []byte) ([]byte, error) {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	if cfg.Width < 1 || cfg.Height < 1 || cfg.Width*cfg.Height > MaxSourcePixels {
		return nil, errors.New("brandlogo: unusable dimensions")
	}
	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	return encodeTile(src)
}

// encodeTile box-averages src into a TileSide-bounded NRGBA image (alpha
// weighted, so transparent pixels do not pull colours toward black) and
// encodes it as PNG.
func encodeTile(src image.Image) ([]byte, error) {
	b := src.Bounds()
	sw, sh := b.Dx(), b.Dy()
	if sw < 1 || sh < 1 {
		return nil, errors.New("brandlogo: empty image")
	}
	scale := 1.0
	if m := max(sw, sh); m > TileSide {
		scale = float64(TileSide) / float64(m)
	}
	dw := max(1, int(float64(sw)*scale+0.5))
	dh := max(1, int(float64(sh)*scale+0.5))
	dst := image.NewNRGBA(image.Rect(0, 0, dw, dh))
	for dy := 0; dy < dh; dy++ {
		y0 := dy * sh / dh
		y1 := max(y0+1, (dy+1)*sh/dh)
		for dx := 0; dx < dw; dx++ {
			x0 := dx * sw / dw
			x1 := max(x0+1, (dx+1)*sw/dw)
			var r, g, bl, a, n float64
			for y := y0; y < y1; y++ {
				for x := x0; x < x1; x++ {
					c := color.NRGBAModel.Convert(src.At(b.Min.X+x, b.Min.Y+y)).(color.NRGBA)
					af := float64(c.A) / 255
					r += float64(c.R) * af
					g += float64(c.G) * af
					bl += float64(c.B) * af
					a += af
					n++
				}
			}
			if a == 0 {
				continue // fully transparent: leave the pixel clear
			}
			dst.SetNRGBA(dx, dy, color.NRGBA{
				R: uint8(r/a + 0.5),
				G: uint8(g/a + 0.5),
				B: uint8(bl/a + 0.5),
				A: uint8(a/n*255 + 0.5),
			})
		}
	}
	var out bytes.Buffer
	if err := png.Encode(&out, dst); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}
