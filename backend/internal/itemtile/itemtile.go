// Package itemtile draws a product's brand tile for the order mail as a PNG,
// reproducing the storefront's .thumb tile (UnifiedThumb, with the .thumb rules
// in src/styles/fixes.css and the thumb-ph bag icon):
//
//   - 16:10 tile with a 2px --line-strong border, --r-s (6px) corners and the
//     3px 3px 0 rgba(78,61,40,.22) offset shadow;
//   - the brand's bg_color, with the shop's brand-icon.png artwork at 50% width,
//     both under a 45% veil (white on light brands, dark on dark ones);
//   - the logo fitted to 92% of the tile, with its 4px offset box shadow;
//   - with no usable logo, the bag icon in the tile's ink colour.
//
// The tile is baked into one PNG because mail clients ignore CSS layering,
// box-shadow and background-image on table cells. It is drawn at 2x so it
// stays sharp on high-density screens. Everything is in pixels of that 2x
// canvas, so the comments give the CSS value and its 2x equivalent.
package itemtile

import (
	"bytes"
	_ "embed"
	"image"
	"image/color"
	"image/png"
	"math"
	"regexp"
	"strconv"
	"strings"
	"sync"
)

//go:embed assets/brand-icon.png
var artworkPNG []byte

const (
	tileW       = 300 // 2x the 150px cell a two-column row gives
	tileH       = 188 // 16:10 (.thumb { aspect-ratio: 16 / 10 })
	border      = 4   // 2px solid var(--line-strong)
	radius      = 12  // var(--r-s) 6px
	innerRadius = radius - border
	shadowOff   = 6 // 3px 3px 0 rgba(78,61,40,.22)
	logoShadow  = 8 // img box-shadow 4px 4px 0 rgba(78,61,40,.2)
	logoRadius  = 8 // img border-radius 4px
	logoFit     = 0.92
	artScale    = 0.5 // background-size: 50% (of the padding box width)
	ss          = 4   // supersampling per axis, for smooth edges

	// Canvas size: the tile plus room for its offset shadow.
	W = tileW + shadowOff
	H = tileH + shadowOff
)

// ink is var(--line-strong) / --ink: #4E3D28.
var ink = px{78.0 / 255, 61.0 / 255, 40.0 / 255, 1}

// px is a straight-alpha colour with components in 0..1.
type px struct{ r, g, b, a float64 }

// over composites src over dst (both straight alpha).
func over(dst, src px) px {
	a := src.a + dst.a*(1-src.a)
	if a <= 0 {
		return px{}
	}
	return px{
		r: (src.r*src.a + dst.r*dst.a*(1-src.a)) / a,
		g: (src.g*src.a + dst.g*dst.a*(1-src.a)) / a,
		b: (src.b*src.a + dst.b*dst.a*(1-src.a)) / a,
		a: a,
	}
}

var (
	hexRe = regexp.MustCompile(`^#([0-9a-fA-F]{6})$`)
	rgbRe = regexp.MustCompile(`^rgba?\(\s*(\d{1,3})\s*,\s*(\d{1,3})\s*,\s*(\d{1,3})`)
	white = px{1, 1, 1, 1}
)

// parseBG reads a catalog bg_color. Only #rrggbb and rgb(r, g, b) are used;
// anything else is white, so catalog text never reaches the image.
func parseBG(s string) px {
	v := strings.TrimSpace(s)
	if m := hexRe.FindStringSubmatch(v); m != nil {
		n, _ := strconv.ParseUint(m[1], 16, 32)
		return px{float64(n>>16&255) / 255, float64(n>>8&255) / 255, float64(n&255) / 255, 1}
	}
	if m := rgbRe.FindStringSubmatch(v); m != nil {
		var c [3]float64
		for i := range c {
			n, err := strconv.Atoi(m[i+1])
			if err != nil || n > 255 {
				return white
			}
			c[i] = float64(n) / 255
		}
		return px{c[0], c[1], c[2], 1}
	}
	return white
}

// isDark matches UnifiedThumb's isDarkBg: relative luminance below one half.
func isDark(c px) bool {
	return (0.2126*c.r + 0.7152*c.g + 0.0722*c.b) < 0.5
}

var (
	artOnce sync.Once
	art     *image.NRGBA
)

func loadArtwork() *image.NRGBA {
	artOnce.Do(func() {
		img, err := png.Decode(bytes.NewReader(artworkPNG))
		if err != nil {
			panic("itemtile: embedded brand-icon.png is invalid: " + err.Error())
		}
		art = toNRGBA(img)
	})
	return art
}

func toNRGBA(src image.Image) *image.NRGBA {
	b := src.Bounds()
	dst := image.NewNRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	for y := 0; y < b.Dy(); y++ {
		for x := 0; x < b.Dx(); x++ {
			dst.SetNRGBA(x, y, color.NRGBAModel.Convert(src.At(b.Min.X+x, b.Min.Y+y)).(color.NRGBA))
		}
	}
	return dst
}

// sampleNRGBA reads img at fractional pixel coordinates (bilinear, premultiplied
// weights so transparent neighbours do not darken the colour).
func sampleNRGBA(img *image.NRGBA, fx, fy float64) px {
	w, h := img.Bounds().Dx(), img.Bounds().Dy()
	x, y := fx-0.5, fy-0.5
	x0, y0 := int(math.Floor(x)), int(math.Floor(y))
	tx, ty := x-float64(x0), y-float64(y0)
	var r, g, b, a float64
	for j := 0; j < 2; j++ {
		for i := 0; i < 2; i++ {
			xi := min(max(x0+i, 0), w-1)
			yi := min(max(y0+j, 0), h-1)
			wx := 1 - tx
			if i == 1 {
				wx = tx
			}
			wy := 1 - ty
			if j == 1 {
				wy = ty
			}
			c := img.NRGBAAt(img.Bounds().Min.X+xi, img.Bounds().Min.Y+yi)
			wa := float64(c.A) / 255 * wx * wy
			r += float64(c.R) / 255 * wa
			g += float64(c.G) / 255 * wa
			b += float64(c.B) / 255 * wa
			a += wa
		}
	}
	if a <= 0 {
		return px{}
	}
	return px{r / a, g / a, b / a, a}
}

// inRR reports whether p lies inside a rounded rectangle.
func inRR(p [2]float64, x, y, w, h, r float64) bool {
	if p[0] < x || p[0] >= x+w || p[1] < y || p[1] >= y+h {
		return false
	}
	cx := math.Min(math.Max(p[0], x+r), x+w-r)
	cy := math.Min(math.Max(p[1], y+r), y+h-r)
	dx, dy := p[0]-cx, p[1]-cy
	return dx*dx+dy*dy <= r*r
}

// tile carries the per-image state used by every sample.
type tile struct {
	bg    px
	dark  bool
	logo  *image.NRGBA // nil = bag icon
	art   *image.NRGBA
	veil  px
	glyph px
}

// Render returns the tile PNG for a logo (a PNG, or nil for the bag icon) on a
// catalog bg_color. It never fails for bad input: a bad logo falls back to the
// bag icon, which is what the storefront shows.
func Render(logo []byte, bgColor string) []byte {
	t := &tile{bg: parseBG(bgColor), art: loadArtwork()}
	t.dark = isDark(t.bg)
	if t.dark {
		t.veil = px{24.0 / 255, 24.0 / 255, 24.0 / 255, 0.45}
		t.glyph = px{1, 1, 1, 0.8}
	} else {
		t.veil = px{1, 1, 1, 0.45}
		t.glyph = px{ink.r, ink.g, ink.b, 0.55}
	}
	if len(logo) > 0 {
		if img, err := png.Decode(bytes.NewReader(logo)); err == nil {
			t.logo = toNRGBA(img)
		}
	}

	out := image.NewNRGBA(image.Rect(0, 0, W, H))
	n := float64(ss * ss)
	for y := 0; y < H; y++ {
		for x := 0; x < W; x++ {
			var r, g, b, a float64
			for sy := 0; sy < ss; sy++ {
				for sx := 0; sx < ss; sx++ {
					p := [2]float64{float64(x) + (float64(sx)+0.5)/ss, float64(y) + (float64(sy)+0.5)/ss}
					c := t.at(p)
					r += c.r * c.a
					g += c.g * c.a
					b += c.b * c.a
					a += c.a
				}
			}
			if a <= 0 {
				continue
			}
			out.SetNRGBA(x, y, color.NRGBA{
				R: uint8(math.Round(r / a * 255)),
				G: uint8(math.Round(g / a * 255)),
				B: uint8(math.Round(b / a * 255)),
				A: uint8(math.Round(a / n * 255)),
			})
		}
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, out)
	return buf.Bytes()
}

func (t *tile) at(p [2]float64) px {
	if inRR(p, 0, 0, tileW, tileH, radius) {
		return t.face(p)
	}
	if inRR(p, shadowOff, shadowOff, tileW, tileH, radius) {
		return px{ink.r, ink.g, ink.b, 0.22}
	}
	return px{}
}

// face is the tile surface: border, background, artwork, veil, then logo or icon.
func (t *tile) face(p [2]float64) px {
	inner := float64(tileW - 2*border)
	innerH := float64(tileH - 2*border)
	if !inRR(p, border, border, inner, innerH, innerRadius) {
		return ink
	}
	c := t.bg
	cx, cy := tileW/2.0, tileH/2.0
	aw := inner * artScale
	ah := aw * float64(t.art.Bounds().Dy()) / float64(t.art.Bounds().Dx())
	ax, ay := cx-aw/2, cy-ah/2
	if p[0] >= ax && p[0] < ax+aw && p[1] >= ay && p[1] < ay+ah {
		c = over(c, sampleNRGBA(t.art, (p[0]-ax)/aw*float64(t.art.Bounds().Dx()), (p[1]-ay)/ah*float64(t.art.Bounds().Dy())))
	}
	c = over(c, t.veil)
	if t.logo != nil {
		return over(c, t.logoAt(p, inner, innerH))
	}
	return over(c, t.iconAt(p, inner, innerH))
}

// logoAt draws the logo box (fitted to 92% of the padding box), its outer
// offset shadow, and the logo pixels.
func (t *tile) logoAt(p [2]float64, inner, innerH float64) px {
	lw := float64(t.logo.Bounds().Dx())
	lh := float64(t.logo.Bounds().Dy())
	s := math.Min(inner*logoFit/lw, innerH*logoFit/lh)
	bw, bh := lw*s, lh*s
	lx, ly := tileW/2.0-bw/2, tileH/2.0-bh/2
	var out px
	if inRR(p, lx+logoShadow, ly+logoShadow, bw, bh, logoRadius) && !inRR(p, lx, ly, bw, bh, logoRadius) {
		out = px{ink.r, ink.g, ink.b, 0.2}
	}
	if inRR(p, lx, ly, bw, bh, logoRadius) {
		out = over(out, sampleNRGBA(t.logo, (p[0]-lx)/bw*lw, (p[1]-ly)/bh*lh))
	}
	return out
}

// bag is the storefront's bag icon (lucide "shopping-bag", 24-unit grid),
// approximated as polylines. The handle is the lower half of a radius-4 circle.
var bagBody = [][2]float64{
	{3.4, 5.467}, {3.0, 6.667}, {3.0, 20}, {5.0, 22}, {19, 22}, {21, 20},
	{21, 6.667}, {20.6, 5.467}, {19, 2.8}, {17, 2}, {7, 2}, {5.4, 2.8}, {3.4, 5.467},
}

func bagSegments() [][2][2]float64 {
	var segs [][2][2]float64
	for i := 0; i+1 < len(bagBody); i++ {
		segs = append(segs, [2][2]float64{bagBody[i], bagBody[i+1]})
	}
	segs = append(segs, [2][2]float64{{3.103, 6.034}, {20.897, 6.034}})
	const steps = 24
	prev := [2]float64{16, 10}
	for i := 1; i <= steps; i++ {
		th := math.Pi * float64(i) / steps
		cur := [2]float64{12 + 4*math.Cos(th), 10 + 4*math.Sin(th)}
		segs = append(segs, [2][2]float64{prev, cur})
		prev = cur
	}
	return segs
}

var bagSegs = bagSegments()

// iconAt draws the bag icon: 38% of the padding box, centred, 2-unit stroke.
func (t *tile) iconAt(p [2]float64, inner, innerH float64) px {
	s := math.Min(inner*0.38, innerH*0.38)
	k := s / 24
	ox, oy := tileW/2.0-s/2, tileH/2.0-s/2
	best := math.Inf(1)
	for _, sg := range bagSegs {
		a := [2]float64{ox + sg[0][0]*k, oy + sg[0][1]*k}
		b := [2]float64{ox + sg[1][0]*k, oy + sg[1][1]*k}
		if d := distToSeg(p, a, b); d < best {
			best = d
		}
	}
	if best <= k { // half of the 2-unit stroke
		return t.glyph
	}
	return px{}
}

func distToSeg(p, a, b [2]float64) float64 {
	dx, dy := b[0]-a[0], b[1]-a[1]
	l2 := dx*dx + dy*dy
	t := 0.0
	if l2 > 0 {
		t = math.Max(0, math.Min(1, ((p[0]-a[0])*dx+(p[1]-a[1])*dy)/l2))
	}
	qx, qy := a[0]+t*dx-p[0], a[1]+t*dy-p[1]
	return math.Hypot(qx, qy)
}
