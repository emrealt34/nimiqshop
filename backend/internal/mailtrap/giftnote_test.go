package mailtrap

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"image"
	"image/color"
	"image/png"
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
				TreesDonation: true, SiteName: "Fixture Shop", ProductLabel: "<Premium & Gift>", Message: "<script>alert(1)</script>\nEnjoy & share!",
				ShopURL: "https://shop.example.com/", SupportURL: "https://shop.example.com/support", StakeValidatorAddress: "nq00-validator",
				OrderID: "order-42", PurchasedAt: time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC), Delivery: delivery}
			m, err := n.Build(Config{Category: "fixture"})
			if err != nil {
				t.Fatal(err)
			}
			if len(m.To) != 1 || m.To[0] != n.Recipient || m.Category != "fixture-gift" || m.OrderID != "order-42" || m.CustomVariables["kind"] != "gift_note" || m.CustomVariables["product"] != n.ProductLabel {
				t.Fatalf("metadata: %+v", m)
			}
			for _, fragment := range []string{"order-42", "30 Sep 2026", "OneTreePlanted", "<Premium & Gift>", "<script>alert(1)</script>"} {
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

func TestIdenticonRenderingAndAlpha(t *testing.T) {
	for _, input := range []string{"", "https://tracker.invalid/pixel", "data:image/svg+xml;base64,abc", "data:image/png;base64,%%%", "data:image/png;base64," + base64.StdEncoding.EncodeToString([]byte("not a PNG")), pngURI(t, image.NewGray(image.Rect(0, 0, 2, 2)))} {
		if identiconTable(input) != "" {
			t.Fatal("invalid PNG accepted")
		}
	}
	img := image.NewNRGBA(image.Rect(0, 0, 32, 32))
	for y := 0; y < 32; y++ {
		for x := 0; x < 32; x++ {
			shade := uint8(0)
			if x%2 == 0 {
				shade = 200
			}
			img.SetNRGBA(x, y, color.NRGBA{R: shade, G: 20, B: 40, A: 128})
		}
	}
	uri := pngURI(t, img)
	mosaic := identiconTable(uri)
	if !strings.Contains(mosaic, `class="donor-img"`) || strings.Count(mosaic, "<tr ") != identN || strings.Count(mosaic, "<td ") != identN*identN {
		t.Fatal("mosaic cell layout")
	}
	solid := image.NewNRGBA(image.Rect(0, 0, 1, 1))
	solid.SetNRGBA(0, 0, color.NRGBA{R: 100, A: 128})
	if !strings.Contains(identiconTable(pngURI(t, solid)), `colspan="32"`) {
		t.Fatal("solid row not compressed")
	}
	if !strings.Contains(identityCard("NQ00", uri), "NQ00") || !strings.Contains(identityCard("", ""), "background:#efe4cd") || !strings.Contains(anonymousCard(), "anonymous") {
		t.Fatal("identity cards")
	}
	for _, tc := range []struct {
		c    color.NRGBA
		want string
	}{{color.NRGBA{R: 1, G: 2, B: 3, A: 255}, "#010203"}, {color.NRGBA{A: 0}, "#fffdf7"}, {color.NRGBA{R: 0, G: 0, B: 0, A: 128}, "#7f7e7b"}} {
		if got := pngComposite(tc.c); got != tc.want {
			t.Errorf("alpha: %s != %s", got, tc.want)
		}
	}
}
