package httpx

import (
	"bytes"
	"strconv"
	"strings"
	"sync"

	"github.com/klauspost/compress/gzip"
	"github.com/valyala/fasthttp"
)

// Compressor gzips JSON responses on the way out.
//
// Rationale: the catalog payloads this API serves are hundreds of kilobytes
// of highly repetitive JSON. Even with Cloudflare compressing the
// client-facing hop, the ORIGIN → edge hop still carries the full body, and
// in a same-origin nginx deployment nothing compresses it at all. Gzipping
// at the source cuts those bytes ~10x.
//
// It is written to be a no-op in the common case:
//   - only application/json bodies are touched (images, fonts and already
//     encoded payloads are skipped by content type);
//   - only bodies above MinBytes are touched, because compressing a 90-byte
//     error costs more CPU than the ~30 bytes it saves;
//   - only when the client actually advertised gzip/deflate;
//   - a response that already carries Content-Encoding is left alone, so a
//     pre-compressed body is never double-encoded.
//
// Writers come from a sync.Pool: at six-figure request rates the
// allocation of a gzip.Writer (and its 256 KB deflate window) per response
// would dominate the GC profile.
type Compressor struct {
	level    int
	minBytes int
	enabled  bool
	pool     sync.Pool
}

// CompressConfig tunes the compressor.
type CompressConfig struct {
	// Enabled turns compression on. Default true.
	Enabled bool
	// Level is the gzip level, 1 (fastest) .. 9 (smallest). Default 5:
	// measurably smaller than 1 for a fraction of the CPU of 9.
	Level int
	// MinBytes is the smallest body worth compressing. Default 1024.
	MinBytes int
}

// DefaultCompressConfig is the production profile.
func DefaultCompressConfig() CompressConfig {
	return CompressConfig{Enabled: true, Level: 5, MinBytes: 1024}
}

// NewCompressor builds the middleware.
func NewCompressor(cfg CompressConfig) *Compressor {
	level := cfg.Level
	if level < gzip.BestSpeed || level > gzip.BestCompression {
		level = 5
	}
	min := cfg.MinBytes
	if min <= 0 {
		min = 1024
	}
	c := &Compressor{level: level, minBytes: min, enabled: cfg.Enabled}
	c.pool.New = func() any {
		w, _ := gzip.NewWriterLevel(nil, c.level)
		return w
	}
	return c
}

// clientAcceptsGzip reports whether the request's Accept-Encoding allows
// gzip. A wildcard ("*") and an explicit q=0 are both handled, because a
// client that says "gzip;q=0" must not be sent gzip.
//
// fasthttp exposes no Accept-Encoding parser, so this implements the RFC 9110
// q-value rules directly: highest matching q wins, q=0 means "refuse", and
// "*" matches anything not explicitly refused.
func clientAcceptsGzip(ctx *fasthttp.RequestCtx) bool {
	ae := ctx.Request.Header.Peek("Accept-Encoding")
	if len(ae) == 0 {
		return false
	}
	// Bound the work: a hostile client can send a very long header, and
	// parsing it must never cost more than the compression would save.
	if len(ae) > 512 {
		ae = ae[:512]
	}
	bestGzip, bestWild := -1.0, -1.0
	for _, part := range bytes.Split(ae, comma) {
		part = bytes.TrimSpace(part)
		if len(part) == 0 {
			continue
		}
		enc := part
		q := 1.0
		if i := bytes.IndexByte(part, ';'); i >= 0 {
			enc = bytes.TrimSpace(part[:i])
			for _, param := range bytes.Split(part[i+1:], semicolon) {
				param = bytes.TrimSpace(param)
				if !bytes.HasPrefix(param, qPrefix) {
					continue
				}
				if v, err := strconv.ParseFloat(strings.TrimSpace(string(param[len(qPrefix):])), 64); err == nil {
					q = v
				} else {
					q = 0
				}
			}
		}
		enc = bytes.ToLower(enc)
		switch {
		case bytes.Equal(enc, gzipToken):
			if q > bestGzip {
				bestGzip = q
			}
		case bytes.Equal(enc, starToken):
			if q > bestWild {
				bestWild = q
			}
		}
	}
	if bestGzip >= 0 {
		return bestGzip > 0
	}
	return bestWild > 0
}

var (
	comma     = []byte(",")
	semicolon = []byte(";")
	qPrefix   = []byte("q=")
	gzipToken = []byte("gzip")
	starToken = []byte("*")
)

// Wrap applies compression around next.
func (c *Compressor) Wrap(next fasthttp.RequestHandler) fasthttp.RequestHandler {
	if c == nil || !c.enabled {
		return next
	}
	return func(ctx *fasthttp.RequestCtx) {
		next(ctx)
		c.compressResponse(ctx)
	}
}

func (c *Compressor) compressResponse(ctx *fasthttp.RequestCtx) {
	h := &ctx.Response.Header
	if len(h.Peek("Content-Encoding")) > 0 {
		return // already encoded somewhere upstream
	}
	body := ctx.Response.Body()
	if len(body) < c.minBytes {
		return
	}
	if !isCompressibleContentType(string(h.Peek("Content-Type"))) {
		return
	}
	if !clientAcceptsGzip(ctx) {
		return
	}
	// HEAD and 204/304 carry no body by definition.
	if ctx.Response.StatusCode() == fasthttp.StatusNoContent ||
		ctx.Response.StatusCode() == fasthttp.StatusNotModified ||
		ctx.Request.Header.IsHead() {
		return
	}

	w := c.pool.Get().(*gzip.Writer)
	var out bytes.Buffer
	// Grow once to a realistic compressed size so the deflate loop does not
	// reallocate repeatedly.
	out.Grow(len(body)/3 + 64)
	w.Reset(&out)
	if _, err := w.Write(body); err != nil {
		c.pool.Put(w)
		return
	}
	if err := w.Close(); err != nil {
		c.pool.Put(w)
		return
	}
	c.pool.Put(w)

	if out.Len() >= len(body) {
		return // incompressible: keep the original bytes
	}
	ctx.Response.SetBody(out.Bytes())
	h.Set("Content-Encoding", "gzip")
	h.Set("Vary", "Accept-Encoding")
	h.Del("Content-Length") // fasthttp recomputes it from the new body
}

// alreadyCompressed lists content types whose bytes are entropy-coded and
// therefore pointless (and slightly harmful) to gzip again.
var alreadyCompressed = []string{
	"image/", "video/", "audio/", "font/",
	"application/zip", "application/gzip", "application/octet-stream",
	"application/wasm", "application/pdf",
}

func isCompressibleContentType(ct string) bool {
	if ct == "" {
		return false
	}
	// Strip parameters ("application/json; charset=utf-8").
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = ct[:i]
	}
	ct = strings.TrimSpace(strings.ToLower(ct))
	switch ct {
	case "application/json", "text/plain", "text/css", "text/html",
		"application/javascript", "text/javascript", "application/xml",
		"text/xml", "application/manifest+json", "image/svg+xml":
		return true
	}
	for _, p := range alreadyCompressed {
		if strings.HasPrefix(ct, p) {
			return false
		}
	}
	return false
}

// SizeString is a tiny helper for log lines.
func SizeString(n int) string { return strconv.Itoa(n) + "B" }
