// Command mailtrap-gift-test is the end-to-end check for the gift-note mailer.
//
// It uses the SHIPPED package (internal/mailtrap) — the same Config, the same
// GiftNote builder, the same client — so what it proves is the real path, not a
// paraphrase of it. It sends one email and reads it back from Mailtrap's own
// email logs.
//
// Run it from the backend directory with the same environment the shop uses:
//
//	export MAILTRAP_API_TOKEN=<your API token>
//	export MAILTRAP_FROM_EMAIL=hello@demomailtrap.co
//	go run ./cmd/mailtrap-gift-test
//
// Optional: GIFT_TEST_TO (default clientanti1s@gmail.com) picks the recipient,
// and GIFT_TEST_KIND=card|esim|topup switches the delivery wording, which is the
// part of the note that used to be wrong for a top-up.
//
// Nothing here is imported by the shop, no order is touched, and no address is
// contacted that you did not put in GIFT_TEST_TO.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	mailtrapsdk "github.com/mailtrap/mailtrap-go"

	"nimiqshop/internal/mailtrap"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	// ConfigFromEnv, not hand-built: the point is to prove the shop's own setup
	// path works. The token is read from MAILTRAP_API_TOKEN and nowhere else.
	cfg := mailtrap.ConfigFromEnv()
	to := strings.TrimSpace(os.Getenv("GIFT_TEST_TO"))
	if to == "" {
		to = "clientanti1s@gmail.com"
	}

	c, err := mailtrap.New(cfg)
	if err != nil {
		fmt.Println("SETUP ERROR:", err)
		fmt.Println("(mailtrap.New reports a config problem here — e.g. sandbox mode without MAILTRAP_SANDBOX_ID)")
		os.Exit(1)
	}
	fmt.Printf("transport  enabled=%v sandbox=%v from=%q category=%q\n",
		c.Enabled(), c.Sandbox(), cfg.FromEmail, cfg.Category)
	if !c.Enabled() {
		fmt.Println("NOTHING SENT: export MAILTRAP_API_TOKEN and MAILTRAP_FROM_EMAIL first.")
		fmt.Println("With no token configured the shop behaves exactly like this — a missing")
		fmt.Println("send never fails an order, it just does not go out.")
		return
	}

	kind := mailtrap.DeliveryCard
	switch strings.ToLower(strings.TrimSpace(os.Getenv("GIFT_TEST_KIND"))) {
	case "topup":
		kind = mailtrap.DeliveryTopUp
	case "esim":
		kind = mailtrap.DeliveryEsim
	}

	note := mailtrap.GiftNote{
		Recipient:    mailtrap.Address{Email: to},
		SiteName:     "shop.nimiqbase.com",
		ProductLabel: "Turkcell · 100 TRY top-up",
		Message:      "Happy birthday! 🎂 The credit is on its way — and no, this email does not contain a code.",
		OrderID:      fmt.Sprintf("GIFTTEST-%d", time.Now().Unix()),
		PurchasedAt:  time.Now().UTC(),
		SupportURL:   "https://shop.nimiqbase.com/support",
		Delivery:     kind,
	}
	// A subject we control, so the log read-back below can find THIS row and
	// not the most recent email in the account.
	note.Subject = fmt.Sprintf("shop.nimiqbase.com gift-note test (%s) %s", kind, note.OrderID)
	if kind != mailtrap.DeliveryTopUp {
		note.ProductLabel = "Steam · 50 USD"
	}

	ids, err := c.SendGiftNote(ctx, note)
	if err != nil {
		fmt.Println("SEND ERROR:", err, "| kind:", mailtrap.Classify(err))
		if errors.Is(err, mailtrap.ErrDisabled) {
			fmt.Println("→ the transport is off; nothing was attempted.")
		} else if strings.Contains(err.Error(), "unverified_domain") {
			fmt.Println("→", cfg.FromEmail, "is not a verified sender on this account.")
			fmt.Println("  Finish the DNS setup for the domain, or send from the demo domain")
			fmt.Println("  (anything@mailtrap.app / hello@demomailtrap.co) for a smoke test.")
		}
		os.Exit(1)
	}
	fmt.Println("SENT       message ids:", strings.Join(ids, ", "))
	fmt.Println("IN THE UI  https://mailtrap.io/sending/email_logs")

	// Read it back from the API — same screen, no guessing whether it went out.
	// The logs service lives on mailtrap.io, not on the send host, so this uses
	// the SDK client directly; the shipped package deliberately wraps only sends.
	api, err := mailtrapsdk.NewClient(cfg.APIToken)
	if err != nil {
		fmt.Println("LOGS CLIENT ERROR:", err)
		return
	}
	since := time.Now().UTC().Add(-10 * time.Minute).Format(time.RFC3339)
	for attempt := 1; attempt <= 6; attempt++ {
		list, _, err := api.EmailLogs.List(ctx, &mailtrapsdk.EmailLogsListOptions{SentAfter: since})
		if err != nil {
			fmt.Println("LOGS ERROR:", err)
			fmt.Println("The send itself succeeded — check https://mailtrap.io/sending/email_logs manually.")
			return
		}
		for _, m := range list.Messages {
			if !strings.Contains(m.To, to) || m.Subject != note.Subject {
				continue
			}
			fmt.Printf("LOG ROW    id=%s status=%s category=%q sent_at=%s\n", m.MessageID, m.Status, m.Category, m.SentAt)
			full, _, err := api.EmailLogs.Get(ctx, m.MessageID)
			if err != nil {
				fmt.Println("(events not readable yet:", err, ")")
				return
			}
			for _, e := range full.Events {
				fmt.Printf("EVENT      %s at=%s provider=%s smtp=%s\n             response=%q\n",
					e.EventType, e.CreatedAt, e.Details.EmailServiceProvider,
					e.Details.EmailServiceProviderStatus, e.Details.EmailServiceProviderResponse)
			}
			return
		}
		time.Sleep(3 * time.Second)
	}
	fmt.Println("No log row yet — Mailtrap indexes a fresh send within a few seconds.")
	fmt.Println("Open https://mailtrap.io/sending/email_logs and look for the subject above.")
}
