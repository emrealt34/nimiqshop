package mailtrap

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"image"
	"image/color"
	"image/png"
	"math/rand"

	"nimiqshop/internal/sampleassets"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func pngURI(t *testing.T, img image.Image) string {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(b.Bytes())
}

func TestGiftBuildDeliveryAndAnonymity(t *testing.T) {
	for _, anonymous := range []bool{false, true} {
		for _, delivery := range []string{DeliveryCard, DeliveryEsim, DeliveryTopUp} {
			n := GiftNote{Recipient: Address{Email: "recipient@example.com", Name: "Recipient"}, Anonymous: anonymous,
				GifterNimiqAddress: "nq12-abcd-efgh", GifterIdenticonDataURI: "https://tracker.invalid/pixel",
				SiteName: "Fixture Shop", ProductLabel: "<Premium & Gift>", Message: "<script>alert(1)</script>\nEnjoy & share!",
				ShopURL: "https://shop.example.com/", SupportURL: "https://shop.example.com/support", StakeValidatorAddress: "nq00-validator",
				OrderID: "order-42", PurchasedAt: time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC), Delivery: delivery}
			m, err := n.Build(Config{Category: "fixture"})
			if err != nil {
				t.Fatal(err)
			}
			if len(m.To) != 1 || m.To[0] != n.Recipient || m.Category != "fixture-gift" || m.OrderID != "order-42" || m.CustomVariables["kind"] != "gift_note" || m.CustomVariables["product"] != n.ProductLabel {
				t.Fatalf("metadata: %+v", m)
			}
			for _, fragment := range []string{"order-42", "30 Sep 2026", "<Premium & Gift>", "<script>alert(1)</script>"} {
				if !strings.Contains(m.Text, fragment) {
					t.Errorf("text missing %q", fragment)
				}
			}
			if strings.Contains(m.HTML, "<script>") || !strings.Contains(m.HTML, "&lt;script&gt;") || !strings.Contains(m.HTML, "&lt;Premium &amp; Gift&gt;") || strings.Contains(m.HTML, "tracker.invalid") {
				t.Fatal("HTML escaped/tracking safety")
			}
			if !strings.Contains(m.Text, n.itemWord()) || !strings.Contains(m.HTML, n.itemEmoji()) {
				t.Errorf("delivery %s missing", delivery)
			}
			combined := m.Subject + m.Text + m.HTML
			if anonymous {
				if strings.Contains(combined, "NQ12 ABCD EFGH") || strings.Contains(combined, "nq12-abcd-efgh") || !strings.Contains(m.Text, "anonymous") {
					t.Fatal("anonymous sender identity leaked")
				}
			} else if !strings.Contains(m.Text, "NQ12 ABCD EFGH") {
				t.Fatal("named wallet missing")
			}
		}
	}
	n := GiftNote{Recipient: Address{Email: "r@example.com"}, Subject: " Custom subject ", Category: "custom", Message: strings.Repeat("🌳", 2001)}
	m, err := n.Build(Config{})
	if err != nil || m.Subject != "Custom subject" || m.Category != "custom" || !utf8.ValidString(m.Text) || !strings.Contains(m.Text, strings.Repeat("🌳", 2000)+"…") || strings.Contains(m.Text, strings.Repeat("🌳", 2001)) {
		t.Fatalf("unicode truncation or override: %v", err)
	}
	m, err = (GiftNote{Recipient: Address{Email: "r@example.com"}}).Build(Config{})
	if err != nil || !strings.Contains(m.Text, "No personal message") || !strings.Contains(m.HTML, "<!DOCTYPE html>") {
		t.Fatalf("minimal gift: %v", err)
	}
	c, err := New(Config{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.SendGiftNote(context.Background(), GiftNote{}); !errors.Is(err, ErrNoRecipient) {
		t.Fatalf("invalid gift: %v", err)
	}
	if _, err = c.SendGiftNote(context.Background(), GiftNote{Recipient: Address{Email: "r@example.com"}}); !errors.Is(err, ErrDisabled) {
		t.Fatalf("disabled gift: %v", err)
	}
}

func TestDeliveryInferenceAndFormatting(t *testing.T) {
	for _, tc := range []struct{ label, explicit, want string }{
		{"normal card", "", DeliveryCard}, {"mobile top-up", "", DeliveryTopUp}, {"recharge", "", DeliveryTopUp}, {"kontör", "", DeliveryTopUp},
		{"travel eSIM", "", DeliveryEsim}, {"travel e-sim", "", DeliveryEsim}, {"mobile topup", " CARD ", DeliveryCard}, {"travel eSIM", "unknown", DeliveryEsim},
	} {
		if got := (GiftNote{ProductLabel: tc.label, Delivery: tc.explicit}).deliveryKind(); got != tc.want {
			t.Errorf("%q/%q: %s", tc.label, tc.explicit, got)
		}
	}
	if viaSite(" ") != "" || viaSite(" shop ") != " via shop" {
		t.Fatal("via site")
	}
	if wrap("  ", 10) != "" || wrap("a bb ccc", 4) != "a bb\nccc" {
		t.Fatal("word wrap")
	}
	if indent("a\nb\n") != "  | a\n  | b" {
		t.Fatal("indent")
	}
	if groupAddress(" \t\n--__ ") != "" || groupAddress(" nq12-abcd_efgh\t1234\n5678 ") != "NQ12 ABCD EFGH 1234 5678" {
		t.Fatal("address grouping")
	}
	if fallback(" ", "default") != "default" || fallback(" custom ", "default") != "custom" {
		t.Fatal("fallback")
	}
	for _, bad := range []string{"", "javascript:alert(1)", "data:text/html,evil", "//example.com", "https://%", "https:///missing"} {
		if safeURL(bad) != "" {
			t.Errorf("unsafe URL %q", bad)
		}
	}
	if safeURL(" https://example.com/path ") != "https://example.com/path" || safeURL("http://example.com") != "http://example.com" {
		t.Fatal("safe URL rejected")
	}
	for _, bad := range []string{"", "https://tracker.invalid", "data:text/html;base64,abc", "data:image/png;base64," + strings.Repeat("a", 600001)} {
		if safeDataImage(bad) != "" {
			t.Error("unsafe image admitted")
		}
	}
	for _, good := range []string{"data:image/png;base64,abc", "data:image/svg+xml;base64,abc"} {
		if safeDataImage(" "+good+" ") != good {
			t.Fatal("supported image rejected")
		}
	}
}

// The avatar ships as an inline PNG attachment, not a cell mosaic, so it keeps
// every feature of the real identicon. These tests pin the accept/reject rules,
// the cid: wiring, and that the bytes the recipient gets are the bytes we got.
func TestIdenticonAttachment(t *testing.T) {
	for _, input := range []string{"", "https://tracker.invalid/pixel", "data:image/svg+xml;base64,abc", "data:image/png;base64,%%%", "data:image/png;base64,\u0000" + base64.StdEncoding.EncodeToString([]byte("not a PNG"))} {
		if _, ok := identiconImage(input); ok {
			t.Fatalf("invalid avatar accepted: %q", input)
		}
	}
	// Any PNG the decoder understands is accepted, not just 8-bit RGBA.
	gray := pngURI(t, image.NewGray(image.Rect(0, 0, 8, 8)))
	if _, ok := identiconImage(gray); !ok {
		t.Fatal("gray PNG rejected")
	}
	// Oversized payloads are refused so config cannot smuggle bulk through here.
	big := image.NewNRGBA(image.Rect(0, 0, 1024, 1024))
	rng := rand.New(rand.NewSource(3))
	for i := range big.Pix {
		big.Pix[i] = uint8(rng.Intn(256))
	}
	if _, ok := identiconImage(pngURI(t, big)); ok {
		t.Fatal("oversized avatar accepted")
	}

	img := image.NewNRGBA(image.Rect(0, 0, 32, 32))
	for y := 0; y < 32; y++ {
		for x := 0; x < 32; x++ {
			img.SetNRGBA(x, y, color.NRGBA{R: uint8(x * 8), G: 20, B: uint8(y * 8), A: 255})
		}
	}
	uri := pngURI(t, img)
	raw, ok := identiconImage(uri)
	if !ok {
		t.Fatal("valid PNG rejected")
	}
	n := GiftNote{Recipient: Address{Email: "friend@example.com"}, GifterNimiqAddress: "NQ00", GifterIdenticonDataURI: uri, ProductLabel: "Steam · 50 USD", SiteName: "nimiqshop.io", OrderID: "Q-9"}
	msg, err := n.Build(Config{})
	if err != nil {
		t.Fatal(err)
	}
	if len(msg.Attachments) != 1 {
		t.Fatalf("want 1 attachment, got %d", len(msg.Attachments))
	}
	att := msg.Attachments[0]
	if att.ContentID != identiconContentID || att.ContentType != "image/png" || !bytes.Equal(att.Data, raw) {
		t.Fatal("identicon attachment does not carry the buyer's exact PNG")
	}
	if !strings.Contains(msg.HTML, `src="cid:`+identiconContentID+`"`) {
		t.Fatal("HTML does not reference the inline identicon")
	}
	if strings.Contains(msg.HTML, "data:image") || strings.Contains(msg.HTML, "donor-img") {
		t.Fatal("HTML must not embed the bitmap or a cell mosaic")
	}

	// No identicon: no attachment, no image, no empty avatar box.
	bare := GiftNote{Recipient: Address{Email: "friend@example.com"}, GifterNimiqAddress: "NQ00", ProductLabel: "Steam · 50 USD", SiteName: "nimiqshop.io", OrderID: "Q-10"}
	m2, err := bare.Build(Config{})
	if err != nil {
		t.Fatal(err)
	}
	if len(m2.Attachments) != 0 || strings.Contains(m2.HTML, "cid:") || strings.Contains(m2.HTML, `class="donor-cell"`) {
		t.Fatal("no identicon must mean no attachment and no avatar box")
	}
	// Anonymous orders never carry the identicon, even when one was sent.
	anon := n
	anon.Anonymous = true
	m3, err := anon.Build(Config{})
	if err != nil {
		t.Fatal(err)
	}
	if len(m3.Attachments) != 0 || strings.Contains(m3.HTML, "cid:") {
		t.Fatal("anonymous mail carried the identicon")
	}
	if strings.Contains(identityCard("NQ00", false, false), `class="donor-cell"`) {
		t.Fatal("card without an icon must have no avatar cell")
	}
	if !strings.Contains(anonymousCard(), "anonymous") || !strings.Contains(anonymousCard(), "background:"+mailPanel) {
		t.Fatal("anonymous card")
	}
}

// TestGiftNoteCardSpeaksTheSitesDesignSystem pins the palette and the phone
// behaviour of the card the owner asked to be transformed: the note has to look
// like it came from the same shop as the page it links to, not like a generic
// transactional mail. Drifting off the tokens is a design regression, so it
// fails here rather than in someone's inbox.
func TestGiftNoteCardSpeaksTheSitesDesignSystem(t *testing.T) {
	note := GiftNote{
		Recipient:          Address{Email: "buyer@example.com"},
		ProductLabel:       "Steam · 50 USD",
		GifterNimiqAddress: "NQ73SE1XYRRFQ8NCDQCPHLJMNR858P7V2HPD",
		SiteName:           "shop.nimiqbase.com",
		ShopURL:            "https://shop.nimiqbase.com",
		SupportURL:         "https://shop.nimiqbase.com/support",
		OrderID:            "ord_1234567890",
		// The stake block is only rendered when a validator is named, and it
		// carries the one green token on the card.
		StakeValidatorAddress: "NQ73SE1XYRRFQ8NCDQCPHLJMNR858P7V2HPD",
	}
	msg, err := note.Build(Config{})
	if err != nil {
		t.Fatal(err)
	}
	h := msg.HTML
	for _, token := range []string{
		mailKraft, mailPaper, mailPanel, mailInk, mailInkDim, mailStamp, mailOnStamp, mailGold, mailGreen,
	} {
		if !strings.Contains(h, token) {
			t.Errorf("card dropped the site token %s", token)
		}
	}
	for _, piece := range []string{
		`class="email-container"`, `class="px-card`, `class="addr"`, `class="cta-link"`,
		"Where is the gift card code?", "noreply@cryptorefills.com", "Reference",
	} {
		if !strings.Contains(h, piece) {
			t.Errorf("card lost %q", piece)
		}
	}
	// One inline style for the structural pieces and a stylesheet for the phone
	// query: a client that strips <style> must still render the desktop layout.
	if !strings.Contains(msg.HTML, "@media only screen and (max-width:620px)") {
		t.Error("phone media query missing")
	}
	// A receipt must not call itself a gift.
	self := note
	self.Self = true
	selfMsg, err := self.Build(Config{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(selfMsg.HTML, "gift note") {
		t.Error("a self-purchase still labels itself a gift note")
	}
	if !strings.Contains(selfMsg.HTML, "Your order is on its way") {
		t.Error("self-purchase headline missing")
	}
}

// Gmail clips an HTML body at ~102 KB. Whatever the inputs, Build must return a
// body under MailHTMLBudget, never a clipped one: worst-case text, escape-heavy
// characters, a long product label and a noisy (non-identicon) avatar.
func TestGmailBudgetHoldsForWorstCaseInputs(t *testing.T) {
	noisy := image.NewNRGBA(image.Rect(0, 0, 160, 160))
	rng := rand.New(rand.NewSource(7))
	for y := 0; y < 160; y++ {
		for x := 0; x < 160; x++ {
			noisy.SetNRGBA(x, y, color.NRGBA{R: uint8(rng.Intn(256)), G: uint8(rng.Intn(256)), B: uint8(rng.Intn(256)), A: 255})
		}
	}
	longMsg := strings.Repeat("<&>\"' hi ", 400) // 3600 chars, cut to the 2000 cap
	cases := map[string]GiftNote{
		"noisy avatar + escape-heavy message": {
			Recipient: Address{Email: "friend@example.com"}, Message: longMsg,
			GifterNimiqAddress: "NQ73 SE1X YRRF Q8NC DQCP HLJM NR85 8P7V 2HPD", GifterIdenticonDataURI: pngURI(t, noisy),
			ProductLabel: strings.Repeat("Steam · 500 USD ", 30), SiteName: "nimiqshop.io", OrderID: "Q-1",
		},
		"sample identicon, plain purchase": {
			Recipient: Address{Email: "buyer@example.com"}, Self: true,
			GifterNimiqAddress: "NQ73 SE1X YRRF Q8NC DQCP HLJM NR85 8P7V 2HPD", GifterIdenticonDataURI: sampleassets.IdenticonDataURI(),
			ProductLabel: "Steam · 50 USD", SiteName: "nimiqshop.io", OrderID: "Q-2",
		},
	}
	for name, note := range cases {
		msg, err := note.Build(Config{})
		if err != nil {
			t.Fatalf("%s: build failed: %v", name, err)
		}
		t.Logf("%s: HTML %d bytes (budget %d, Gmail clips ~102000)", name, len(msg.HTML), MailHTMLBudget)
		if len(msg.HTML) > MailHTMLBudget {
			t.Fatalf("%s: HTML is %d bytes, over the %d budget", name, len(msg.HTML), MailHTMLBudget)
		}
		if !strings.Contains(msg.HTML, "from:noreply@cryptorefills.com") || !strings.Contains(msg.Text, "from:noreply@cryptorefills.com") {
			t.Fatalf("%s: the sender search fallback is missing", name)
		}
		for _, a := range msg.Attachments {
			if len(a.Data) > identiconMaxBytes {
				t.Fatalf("%s: avatar attachment %d bytes is over the cap", name, len(a.Data))
			}
		}
	}
}

// The footer reads as clean English: "an order" / "a gift", one "The" per sentence.
func TestFooterGrammar(t *testing.T) {
	for _, self := range []bool{true, false} {
		n := GiftNote{Recipient: Address{Email: "a@example.com"}, Self: self, SiteName: "nimiqshop.io"}
		msg, err := n.Build(Config{})
		if err != nil {
			t.Fatal(err)
		}
		for _, bad := range []string{"The the", "about a order", "about an gift"} {
			if strings.Contains(msg.HTML, bad) || strings.Contains(msg.Text, bad) {
				t.Fatalf("self=%v: bad copy %q", self, bad)
			}
		}
		want := "about a gift"
		if self {
			want = "about an order"
		}
		if !strings.Contains(msg.HTML, want) {
			t.Fatalf("self=%v: footer missing %q", self, want)
		}
	}
}

// The purchase mail shows the shop logo (inline PNG), never the old tick emoji;
// gift mail keeps its gift icon and carries no logo.
func TestOrderMailShowsShopLogoNotTick(t *testing.T) {
	order := GiftNote{Recipient: Address{Email: "buyer@example.com"}, Self: true, ProductLabel: "Steam · 50 USD", SiteName: "nimiqshop.io", OrderID: "Q-20"}
	msg, err := order.Build(Config{})
	if err != nil {
		t.Fatal(err)
	}
	// The subject/<title> keeps its own emoji; the body's hero icon must not.
	body := msg.HTML[strings.Index(msg.HTML, "</title>"):]
	if strings.Contains(body, "✅") {
		t.Fatal("order mail body still shows the tick emoji")
	}
	if !strings.Contains(msg.HTML, `src="cid:`+shopLogoContentID+`"`) {
		t.Fatal("order mail does not reference the shop logo")
	}
	found := false
	for _, a := range msg.Attachments {
		if a.ContentID == shopLogoContentID {
			found = bytes.Equal(a.Data, shopLogoPNG) && a.ContentType == "image/png"
		}
	}
	if !found {
		t.Fatal("shop logo attachment missing or not the PNG")
	}
	gift := GiftNote{Recipient: Address{Email: "friend@example.com"}, ProductLabel: "Steam · 50 USD", SiteName: "nimiqshop.io", OrderID: "Q-21"}
	gm, err := gift.Build(Config{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(gm.HTML, "🎁") || strings.Contains(gm.HTML, "cid:"+shopLogoContentID) {
		t.Fatal("gift mail icon changed or logo leaked into a gift")
	}
}
