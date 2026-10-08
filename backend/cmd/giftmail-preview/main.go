// Command giftmail-preview renders the REAL gift-note email (the same
// mailtrap.GiftNote.Build() the fulfillment chain sends) to standalone HTML
// files, so a preview/screenshot can show exactly what lands in an inbox —
// without touching Mailtrap.
//
// Usage:
//
//	go run ./cmd/giftmail-preview -out /tmp/preview
//
// Writes named.html (wallet identified, no name — names are not part of the
// note) and anonymous.html (anonymous card). Both carry a generated Nimiq-style
// identicon mosaic PNG so the avatar path runs for real.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"nimiqshop/internal/mailtrap"
	"nimiqshop/internal/sampleassets"
)

// identiconPNG is the REAL @nimiq/identicons face for the sample wallet,
// rasterized in a browser (Chromium) from the exact vendored module the site
// ships — the same 160px PNG the checkout now attaches to a gift quote. What
// this preview renders is byte-for-byte what a recipient's inbox gets.
func identiconPNG() string {
	return sampleassets.IdenticonDataURI()
}

func main() {
	outDir := flag.String("out", "/tmp/giftmail-preview", "output directory for the HTML files")
	flag.Parse()
	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		fmt.Println("OUT ERROR:", err)
		os.Exit(1)
	}

	const wallet = "NQ07 XQ4T R7EL A5A6 2L0X SD59 4RNE 3Y4L 0S8M"
	const validator = "NQ49 N8MB XYCR XBUP 404C KXKK L49M A7BT F082"

	base := mailtrap.GiftNote{
		Recipient:              mailtrap.Address{Email: "friend@mail.com", Name: "Friend"},
		GifterNimiqAddress:     wallet,
		GifterIdenticonDataURI: identiconPNG(),
		SiteName:               "nimiqshop.io",
		ProductLabel:           "Steam · 50 USD",
		Message:                "Happy birthday! 🎂 Enjoy the game!",
		OrderID:                "Q-8842",
		PurchasedAt:            time.Date(2026, 9, 11, 9, 24, 0, 0, time.UTC),
		ShopURL:                "https://nimiqshop.io",
		SupportURL:             "https://nimiqshop.io/support",
		StakeValidatorAddress:  validator,
	}

	for _, tc := range []struct {
		name string
		note mailtrap.GiftNote
	}{
		{"named", base},
		{"anonymous", func() mailtrap.GiftNote {
			n := base
			n.Anonymous = true
			return n
		}()},
		// Plain purchase (no gift): the buyer IS the recipient. This is the
		// order-confirmation mail a normal checkout sends (GiftNote.Self).
		{"purchase", func() mailtrap.GiftNote {
			n := base
			n.Self = true
			n.Recipient = mailtrap.Address{Email: "clientanti1s@gmail.com"}
			n.Message = ""
			return n
		}()},
	} {
		m, err := tc.note.Build(mailtrap.Config{Category: "nimshop"})
		if err != nil {
			fmt.Println("BUILD ERROR:", tc.name, err)
			os.Exit(1)
		}
		p := filepath.Join(*outDir, tc.name+".html")
		if err := os.WriteFile(p, []byte(m.HTML), 0o644); err != nil {
			fmt.Println("WRITE ERROR:", err)
			os.Exit(1)
		}
		fmt.Printf("wrote %s  (subject=%q, %d bytes)\n", p, m.Subject, len(m.HTML))
	}
}
