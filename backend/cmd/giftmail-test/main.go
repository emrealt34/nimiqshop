// Command giftmail-test is the end-to-end check for the shop.nimiqbase.com gift/donation
// mailer. It uses the SHIPPED package (internal/mailtrap) — the same Config,
// the same GiftNote builder, the same client — so what it runs is the real
// path, not a paraphrase. It builds the donation-aware gift note (donor Nimiq
// identicon + wallet + tree-donation + shop CTA + stake footnote) and sends it.
//
// Targets:
//
//	sandbox  (default, needs a Mailtrap *account/Email-Testing* token):
//	    MAILTRAP_USE_SANDBOX=true
//	    MAILTRAP_SANDBOX_ID=4902439
//	    MAILTRAP_ACCOUNT_TOKEN=<account token>
//	    MAILTRAP_FROM_EMAIL=test@shop.nimiqbase.com
//	    -> captured in the "My Sandbox" inbox, never delivered. Proof target.
//
//	send  (real delivery, needs the *Email-Sending* token):
//	    MAILTRAP_TARGET_IS_SEND=true            (or just unset USE_SANDBOX)
//	    MAILTRAP_API_TOKEN=<sending API token>
//	    MAILTRAP_FROM_EMAIL=hello@shop.nimiqbase.com   (verified sending domain)
//	    -> real delivery to GIFT_TEST_TO.
//
// Optional: GIFT_IDENTICON="data:image/png;base64,…" is the donor's real Nimiq
// identicon rendered by the shop's identicon tooling; if empty the note renders
// a placeholder avatar and still proves the rest of the pipeline.
//
// Optional: GIFT_ANONYMOUS=true sends the same note with Anonymous set — the
// identity fields are still filled below on purpose, so the run PROVES the
// Build() seal strips the name, wallet and identicon from what Mailtrap gets.
// Check the captured payload: neither the NQ08 address nor the mosaic bytes
// may appear (there is no name in the note at all, named or anonymous).
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"nimiqshop/internal/mailtrap"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	cfg := mailtrap.ConfigFromEnv()
	to := strings.TrimSpace(os.Getenv("GIFT_TEST_TO"))
	if to == "" {
		to = "you@gmail.com"
	}
	if strings.TrimSpace(os.Getenv("MAILTRAP_ACCOUNT_TOKEN")) != "" {
		// account (Email-Testing) token given -> the SDK wants it as the bearer
		// for the sandbox host, so hand it to the same env the config reads.
		if os.Getenv("MAILTRAP_API_TOKEN") == "" {
			_ = os.Setenv("MAILTRAP_API_TOKEN", os.Getenv("MAILTRAP_ACCOUNT_TOKEN"))
		}
		if os.Getenv("MAILTRAP_FROM_EMAIL") == "" {
			_ = os.Setenv("MAILTRAP_FROM_EMAIL", "test@shop.nimiqbase.com")
		}
		_ = os.Setenv("MAILTRAP_USE_SANDBOX", "true")
		cfg = mailtrap.ConfigFromEnv()
	}

	c, err := mailtrap.New(cfg)
	if err != nil {
		fmt.Println("SETUP ERROR:", err)
		os.Exit(1)
	}
	fmt.Printf("transport  enabled=%v sandbox=%v from=%q category=%q\n",
		c.Enabled(), c.Sandbox(), cfg.FromEmail, cfg.Category)
	if !c.Enabled() {
		fmt.Println("NOTHING SENT: set MAILTRAP_API_TOKEN (or MAILTRAP_ACCOUNT_TOKEN for the sandbox) and MAILTRAP_FROM_EMAIL.")
		return
	}

	note := mailtrap.GiftNote{
		Recipient:              mailtrap.Address{Email: to},
		GifterNimiqAddress:     "NQ08 D44A 44B9 0F77 2E22 8C13 C345 6FBD XKH9", // sample
		GifterIdenticonDataURI: strings.TrimSpace(os.Getenv("GIFT_IDENTICON")),
		SiteName:               "shop.nimiqbase.com",
		ProductLabel:           "Turkcell · 100 TRY top-up",
		Message:                "I topped you up on shop.nimiqbase.com! 📞",
		OrderID:                fmt.Sprintf("GIFTTEST-%d", time.Now().Unix()),
		PurchasedAt:            time.Now().UTC(),
		ShopURL:                "https://shop.nimiqbase.com",
		SupportURL:             "https://shop.nimiqbase.com/support",
		StakeValidatorAddress:  "NQ49 N8MB XYCR XBUP 404C KXKK L49M A7BT F082",
		Delivery:               mailtrap.DeliveryTopUp,
	}
	note.Subject = fmt.Sprintf("shop.nimiqbase.com gift/donation test %s", note.OrderID)
	if strings.TrimSpace(os.Getenv("GIFT_ANONYMOUS")) == "true" {
		// Deliberately keeps the identity fields set: the Build() seal must
		// strip them, and this run is how we prove it did.
		note.Anonymous = true
		note.Subject = fmt.Sprintf("shop.nimiqbase.com ANONYMOUS gift test %s", note.OrderID)
		fmt.Println("ANONYMOUS  GIFT_ANONYMOUS=true — the seal must strip name/wallet/identicon from the payload")
	}

	ids, err := c.SendGiftNote(ctx, note)
	if err != nil {
		fmt.Println("SEND ERROR:", err, "| kind:", mailtrap.Classify(err))
		if errors.Is(err, mailtrap.ErrDisabled) {
			fmt.Println("-> the transport is off; nothing was attempted.")
		} else if strings.Contains(err.Error(), "unverified_domain") {
			fmt.Println("->", cfg.FromEmail, "is not a verified sender on this account.")
		}
		os.Exit(1)
	}
	fmt.Println("SENT       message ids:", strings.Join(ids, ", "))
	fmt.Println("IN THE UI  sandbox: https://mailtrap.io/inboxes/4902439  |  live: https://mailtrap.io/sending/email_logs")
}
