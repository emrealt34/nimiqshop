package itemtile

import (
	"bytes"
	"image/png"
	"testing"
)

// The catalog's bg_color is only ever used as a hex or rgb() colour; anything
// else is white.
func TestParseBGOnlyAcceptsColours(t *testing.T) {
	for _, bad := range []string{"red;background:url(x)", "rgb(300,0,0)", "expression(alert(1))", ""} {
		if got := parseBG(bad); got != white {
			t.Fatalf("parseBG(%q) = %+v, want white", bad, got)
		}
	}
	if got := parseBG("rgb(255, 153, 0)"); got.r != 1 || got.b != 0 {
		t.Fatalf("rgb colour parsed wrong: %+v", got)
	}
	if !isDark(parseBG("#181818")) || isDark(parseBG("#FFFFFF")) {
		t.Fatal("dark-background detection must match UnifiedThumb (Koton dark, Hepsiburada light)")
	}
}

// A tile is a PNG at the storefront's 16:10 proportion plus the shadow offset.
func TestRenderIsSizedLikeTheStorefrontTile(t *testing.T) {
	out := Render(nil, "#FFFFFF")
	img, err := png.Decode(bytes.NewReader(out))
	if err != nil {
		t.Fatal(err)
	}
	if b := img.Bounds(); b.Dx() != W || b.Dy() != H {
		t.Fatalf("tile %dx%d, want %dx%d", b.Dx(), b.Dy(), W, H)
	}
	// The border is the ink colour, drawn at the corner's straight edge.
	r, g, b, a := img.At(W/2, border/2).RGBA()
	if a == 0 || r>>8 != 78 || g>>8 != 61 || b>>8 != 40 {
		t.Fatalf("top border colour wrong: %d %d %d %d", r>>8, g>>8, b>>8, a>>8)
	}
}

// An unusable logo must still produce a tile (the bag icon), never an error.
func TestBadLogoFallsBackToBagIcon(t *testing.T) {
	if len(Render([]byte("not a png"), "#055ba9")) == 0 {
		t.Fatal("a bad logo must fall back to the bag icon, not produce nothing")
	}
}
