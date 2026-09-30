// Package sampleassets carries the few binary fixtures the backend embeds
// (rather than inlining them as base64 string literals in Go source).
package sampleassets

import (
	_ "embed"
	"encoding/base64"
)

// IdenticonPNG is the real @nimiq/identicons face for the sample wallet,
// rasterized in a browser (Chromium) from the exact vendored module the site
// ships — the same 160px PNG the checkout attaches to a gift quote. Test
// emails and the gift-mail preview embed it as the sender avatar, so what they
// render is byte-for-byte what a recipient's inbox gets.
//
//go:embed sample-identicon.png
var IdenticonPNG []byte

// IdenticonDataURI returns IdenticonPNG as a data: URI for inline <img> use.
func IdenticonDataURI() string {
	if len(IdenticonPNG) == 0 {
		return ""
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(IdenticonPNG)
}
