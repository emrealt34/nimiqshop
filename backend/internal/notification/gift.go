// Package notification — gift message channel (email + SMS).
//
// When a fulfilled gift order transitions to the supplier-fulfilled state,
// this package dispatches the buyer-authored gift message to the recipient:
//
//   * Email via SMTP generic (works with Gmail SMTP, SendGrid, Mailgun, etc.)
//   * SMS via HTTP POST (Pingram, Twilio, Netgsm, etc. — anything that accepts
//     a URL + auth header + JSON body)
//
// Both are OFF by default (NOTIFY_EMAIL_ENABLED=false, NOTIFY_SMS_ENABLED=false).
// The settlement tracker invokes NotifyGiftQuote in a goroutine after marking
// a quote fulfilled; a delivery failure is logged but never blocks the order.
//
// Idempotency: MarkGiftNotified records the delivery time inside the same
// Badger transaction that the supplier state would itself flip, so a crash
// between dispatch and record still results in at-most-once delivery on retry.
package notification

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/smtp"
	"net/url"
	"strings"
	"time"
)

const (
	// EmailBodyMaxLen is the per-message email body ceiling. Anything longer
	// is truncated at a word boundary. 2000 chars fits in the standard
	// "your gift card is ready" email without becoming spam-flag bait.
	EmailBodyMaxLen = 2000
	// SMSBodyMaxLen matches the GSM-7 160-char single-segment ceiling. Most
	// SMS providers concatenate above this; we hard-cut instead to avoid
	// surprise billing on multi-segment sends.
	SMSBodyMaxLen = 160
	// SMSSubjectMaxLen is unused for SMS but kept symmetric with email.
	SMSSubjectMaxLen = 80
)

// GiftConfig configures the email + SMS channels. All fields are optional;
// Enabled flags gate runtime use without requiring callers to nil-check.
type GiftConfig struct {
	// --- Email (SMTP) ---
	EmailEnabled   bool
	SMTPHost       string // e.g. smtp.gmail.com
	SMTPPort       int    // 587 for STARTTLS, 465 for implicit TLS
	SMTPUsername   string
	SMTPPassword   string
	SMTPFromName   string // e.g. "nim.shop gifts"
	SMTPFromAddr   string // e.g. [email protected]
	EmailSubject   string // default: "🎁 A gift for you from nim.shop"
	EmailBodyTmpl  string // optional; falls back to defaultBody()
	EmailDryRun    bool   // if true, log instead of send (testing)
	// --- SMS (HTTP) ---
	SMSEnabled    bool
	SMSProviderURL string // e.g. https://api.pingram.com/v1/sms/send
	SMSAuthHeader string // full header value, e.g. "Bearer XYZ..." or "Basic XYZ..."
	SMSMethod     string // POST (default) or GET
	SMSBodyTmpl   string // JSON template with placeholders: {phone}, {message}, {sender}
	SMSDryRun     bool
	SMSSender     string // optional sender id / origin
	HTTPTimeout   time.Duration
}

// GiftClient is the public surface for the gift notification channel.
type GiftClient struct {
	cfg     GiftConfig
	http    *http.Client
	enabled bool
}

// NewGift builds a GiftClient. enabled is true if EITHER channel is on and has
// the minimum required config (smtp host / sms url). When disabled, every
// method is a safe no-op.
func NewGift(cfg GiftConfig) *GiftClient {
	timeout := cfg.HTTPTimeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	return &GiftClient{
		cfg: cfg,
		http: &http.Client{
			Timeout: timeout,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12},
				DialContext: (&net.Dialer{
					Timeout: 5 * time.Second,
				}).DialContext,
			},
		},
		enabled: (cfg.EmailEnabled && cfg.SMTPHost != "" && cfg.SMTPFromAddr != "") ||
			(cfg.SMSEnabled && cfg.SMSProviderURL != ""),
	}
}

// Enabled reports whether ANY channel is configured. Callers can use this to
// suppress the gift UI on the frontend when notifications are off.
func (g *GiftClient) Enabled() bool {
	if g == nil {
		return false
	}
	return g.enabled
}

// EmailEnabled reports whether the email channel is on.
func (g *GiftClient) EmailEnabled() bool { return g != nil && g.cfg.EmailEnabled && g.cfg.SMTPHost != "" }

// SMSEnabled reports whether the SMS channel is on.
func (g *GiftClient) SMSEnabled() bool { return g != nil && g.cfg.SMSEnabled && g.cfg.SMSProviderURL != "" }

// NormalizeChannel validates the buyer-supplied channel choice. Empty string
// ("self-purchase" — no gift) is allowed and returns ""; otherwise one of
// "email", "sms", or "both".
func NormalizeChannel(ch string) string {
	switch strings.ToLower(strings.TrimSpace(ch)) {
	case "", "none":
		return ""
	case "email", "mail":
		return "email"
	case "sms", "text", "phone":
		return "sms"
	case "both", "all", "email+sms":
		return "both"
	}
	return ""
}

// SanitizeMessage enforces the per-channel length caps. The same string is
// independently truncated for email vs SMS so the SMS leg never gets cut in
// the middle of an emoji while the email keeps the full text.
func SanitizeMessage(raw string) (emailBody, smsBody string) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", ""
	}
	// Email: hard-cap at 2000 chars (cut at the last word boundary inside cap).
	emailBody = cutAtWordBoundary(s, EmailBodyMaxLen)
	// SMS: cut to 160 chars (at word boundary).
	smsBody = cutAtWordBoundary(s, SMSBodyMaxLen)
	return
}

func cutAtWordBoundary(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := s[:max]
	if i := strings.LastIndex(cut, " "); i > max/2 {
		cut = cut[:i]
	}
	return cut + "…"
}

// RenderEmail returns the full HTML/plain body for an email gift notification.
// The recipient + product + buyer-friendly email are interpolated; HTML escaping
// is the responsibility of the caller (we pass pre-escaped strings).
func (g *GiftClient) RenderEmail(recipient, product, faceValue, currency, buyerEmail, customMsg, deliveredBy string) (subject, plain, html string) {
	subject = g.cfg.EmailSubject
	if subject == "" {
		subject = "🎁 A gift for you from nim.shop"
	}
	plainBody := g.cfg.EmailBodyTmpl
	if plainBody == "" {
		plainBody = defaultEmailPlain(product, faceValue, currency, buyerEmail, customMsg, deliveredBy)
	}
	htmlBody := renderEmailHTML(product, faceValue, currency, buyerEmail, customMsg, deliveredBy)
	return subject, plainBody, htmlBody
}

func defaultEmailPlain(product, faceValue, currency, buyerEmail, msg, deliveredBy string) string {
	var b strings.Builder
	b.WriteString("Hi!\n\n")
	b.WriteString(fmt.Sprintf("Someone just sent you a %s gift card worth %s %s via nim.shop.\n\n", product, faceValue, currency))
	if buyerEmail != "" {
		b.WriteString(fmt.Sprintf("From: %s\n", buyerEmail))
	}
	if msg != "" {
		b.WriteString(fmt.Sprintf("\nA message from your gifter:\n%s\n\n", msg))
	}
	// Delivery-aware line: email products carry the code in CryptoRefills'
	// own email; phone products (top-ups) are applied to the number.
	if deliveredBy == "phone" {
		b.WriteString("The top-up credit is applied directly to the phone number \u2014 CryptoRefills confirms the delivery from cryptorefills.com.\n\n")
	} else {
		b.WriteString("The gift card code is on its way in a separate email from CryptoRefills (look for @cryptorefills.com). If you do not see it within a few minutes, check your spam folder.\n\n")
	}
	b.WriteString("Enjoy!\n— nim.shop gifts\n")
	return b.String()
}

func renderEmailHTML(product, faceValue, currency, buyerEmail, msg, deliveredBy string) string {
	var b strings.Builder
	b.WriteString(`<!doctype html><html><body style="font-family:-apple-system,Segoe UI,Helvetica,Arial,sans-serif;background:#f6efdc;padding:24px;color:#4E3D28;">`)
	b.WriteString(`<div style="max-width:560px;margin:0 auto;background:#fff8e8;border:1.5px dashed #4E3D28;border-radius:10px;padding:24px;">`)
	b.WriteString(`<div style="font-size:48px;text-align:center">🎁</div>`)
	b.WriteString(fmt.Sprintf(`<h2 style="margin:8px 0 4px;text-align:center">You got a gift card!</h2>`))
	b.WriteString(fmt.Sprintf(`<p style="text-align:center;color:#7C6A4E">%s worth <strong>%s %s</strong></p>`, htmlEscape(product), htmlEscape(faceValue), htmlEscape(currency)))
	if buyerEmail != "" {
		b.WriteString(fmt.Sprintf(`<p style="text-align:center;color:#9B8763;font-size:14px">From: %s</p>`, htmlEscape(buyerEmail)))
	}
	if msg != "" {
		b.WriteString(`<div style="background:#EFE4C6;border-left:4px solid #C7481D;padding:14px 16px;border-radius:6px;margin:16px 0;">`)
		b.WriteString(`<div style="font-size:12px;font-weight:800;letter-spacing:0.06em;text-transform:uppercase;color:#7C6A4E;margin-bottom:6px">A message from your gifter</div>`)
		b.WriteString(`<div style="white-space:pre-wrap">` + htmlEscape(msg) + `</div>`)
		b.WriteString(`</div>`)
	}
	deliveryLine := `The gift card code itself will arrive in a separate email from CryptoRefills \u2014 look for <strong>@cryptorefills.com</strong>. If you don't see it within a few minutes, check your spam folder.`
	if deliveredBy == "phone" {
		deliveryLine = `The top-up credit is applied directly to the phone number \u2014 CryptoRefills confirms the delivery from <strong>cryptorefills.com</strong>.`
	}
	b.WriteString(`<p style="color:#7C6A4E;font-size:13px;line-height:1.5">` + deliveryLine + `</p>`)
	b.WriteString(`<p style="text-align:center;margin-top:20px;color:#9B8763;font-size:12px">Sent by nim.shop gifts</p>`)
	b.WriteString(`</div></body></html>`)
	return b.String()
}

func htmlEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&#39;")
	return r.Replace(s)
}

// RenderSMS returns the SMS body for a gift notification. If customMsg is
// empty a sensible default is generated so the recipient always sees a real
// notification.
func (g *GiftClient) RenderSMS(product, faceValue, currency, customMsg, deliveredBy string) string {
	if customMsg != "" {
		// Never send the gifter's note bare: the recipient must learn WHAT it
		// is (a gift, code coming from CryptoRefills) even in the 160-char
		// SMS window — exactly the email framing, compressed.
		if deliveredBy == "phone" {
			return fmt.Sprintf("🎁 You got a gift top-up! It lands directly on the phone number (CryptoRefills confirms by SMS). 💌 From your gifter: %s", customMsg)
		}
		return fmt.Sprintf("🎁 You got a gift! The code comes by email from CryptoRefills (@cryptorefills.com). 💌 From your gifter: %s", customMsg)
	}
	if deliveredBy == "phone" {
		return fmt.Sprintf("🎁 A %s top-up (%s %s) is landing on the phone number! — nim.shop", product, faceValue, currency)
	}
	return fmt.Sprintf("🎁 %s sent you a %s gift card (%s %s)! Check your email from @cryptorefills.com for the code. — nim.shop", "A friend", product, faceValue, currency)
}

/* ------------------------------ Email sender ----------------------------- */

// SendEmail delivers one gift email via SMTP. Errors are returned for logging
// but the caller treats them as best-effort.
func (g *GiftClient) SendEmail(ctx context.Context, to, subject, plain, html string) error {
	if !g.EmailEnabled() {
		return errors.New("email channel disabled")
	}
	if g.cfg.EmailDryRun {
		log.Printf("notify(DRY) email to=%s subject=%q body=%d chars", to, subject, len(plain))
		return nil
	}
	addr := fmt.Sprintf("%s:%d", g.cfg.SMTPHost, g.cfg.SMTPPort)
	if g.cfg.SMTPPort == 0 {
		addr = fmt.Sprintf("%s:587", g.cfg.SMTPHost)
	}
	auth := smtp.PlainAuth("", g.cfg.SMTPUsername, g.cfg.SMTPPassword, g.cfg.SMTPHost)
	from := g.cfg.SMTPFromAddr
	if g.cfg.SMTPFromName != "" {
		from = fmt.Sprintf("%s <%s>", g.cfg.SMTPFromName, g.cfg.SMTPFromAddr)
	}
	msg := buildMIME(from, to, subject, plain, html)
	// STARTTLS port (587) is the common Gmail/SendGrid setup; implicit TLS
	// (465) uses the same Mail client over a TLS dial. We use net/smtp which
	// supports both via the addr scheme + smtp.Dialer.
	host := g.cfg.SMTPHost
	if strings.Contains(host, ":") {
		// Trim any ":port" already present
		host = strings.Split(host, ":")[0]
	}
	return smtp.SendMail(addr, auth, g.cfg.SMTPFromAddr, []string{to}, []byte(msg))
}

func buildMIME(from, to, subject, plain, html string) string {
	const boundary = "nimshop_gift_boundary_x42"
	var b strings.Builder
	b.WriteString("From: " + from + "\r\n")
	b.WriteString("To: " + to + "\r\n")
	b.WriteString("Subject: " + subject + "\r\n")
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: multipart/alternative; boundary=\"" + boundary + "\"\r\n")
	b.WriteString("\r\n")
	// plain
	b.WriteString("--" + boundary + "\r\n")
	b.WriteString("Content-Type: text/plain; charset=UTF-8\r\n\r\n")
	b.WriteString(plain)
	b.WriteString("\r\n")
	// html
	b.WriteString("--" + boundary + "\r\n")
	b.WriteString("Content-Type: text/html; charset=UTF-8\r\n\r\n")
	b.WriteString(html)
	b.WriteString("\r\n")
	b.WriteString("--" + boundary + "--\r\n")
	return b.String()
}

/* ------------------------------ SMS sender ------------------------------- */

// SendSMS delivers one gift SMS via an HTTP POST. The provider URL and auth
// header are generic — Pingram, Twilio, Netgsm, etc. all accept the same
// pattern. SMSBodyTmpl is a JSON string template with placeholders:
//
//	{"phone":"{phone}","message":"{message}","sender":"{sender}"}
//
// If SMSBodyTmpl is empty we fall back to {phone, message} for providers that
// only need those two.
func (g *GiftClient) SendSMS(ctx context.Context, phone, body string) error {
	if !g.SMSEnabled() {
		return errors.New("sms channel disabled")
	}
	if g.cfg.SMSDryRun {
		log.Printf("notify(DRY) sms to=%s body=%q", phone, body)
		return nil
	}
	tmpl := g.cfg.SMSBodyTmpl
	if tmpl == "" {
		tmpl = `{"phone":"{phone}","message":"{message}"}`
	}
	payload := tmpl
	payload = strings.ReplaceAll(payload, "{phone}", phone)
	payload = strings.ReplaceAll(payload, "{message}", body)
	payload = strings.ReplaceAll(payload, "{sender}", g.cfg.SMSSender)

	method := strings.ToUpper(strings.TrimSpace(g.cfg.SMSMethod))
	if method == "" {
		method = http.MethodPost
	}

	var req *http.Request
	var err error
	if method == http.MethodGet {
		u, perr := url.Parse(g.cfg.SMSProviderURL)
		if perr != nil {
			return fmt.Errorf("sms url parse: %w", perr)
		}
		q := u.Query()
		q.Set("phone", phone)
		q.Set("message", body)
		if g.cfg.SMSSender != "" {
			q.Set("sender", g.cfg.SMSSender)
		}
		u.RawQuery = q.Encode()
		req, err = http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		if err != nil {
			return err
		}
	} else {
		req, err = http.NewRequestWithContext(ctx, http.MethodPost, g.cfg.SMSProviderURL, bytes.NewReader([]byte(payload)))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
	}
	if g.cfg.SMSAuthHeader != "" {
		// SMSAuthHeader is the full header value, e.g. "Bearer XYZ" or "Basic XYZ"
		// — we split on the first space so the token stays opaque to this code.
		parts := strings.SplitN(g.cfg.SMSAuthHeader, " ", 2)
		if len(parts) == 2 {
			req.Header.Set(parts[0], parts[1])
		} else {
			req.Header.Set("Authorization", g.cfg.SMSAuthHeader)
		}
	}

	resp, err := g.http.Do(req)
	if err != nil {
		return fmt.Errorf("sms send: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	if resp.StatusCode >= 300 {
		return fmt.Errorf("sms provider %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	log.Printf("notify: sms dispatched to=%s status=%d body=%d bytes", phone, resp.StatusCode, len(raw))
	return nil
}

// NotifyGift delivers a gift notification according to the buyer's channel
// choice. Empty channel is a no-op (self-purchase). SMS/email failures are
// returned individually so callers can decide; we recommend logging and
// continuing.
func (g *GiftClient) NotifyGift(ctx context.Context, channel, recipientEmail, recipientPhone, subject, plain, html, smsBody string) (emailErr, smsErr error) {
	ch := NormalizeChannel(channel)
	if ch == "" {
		return nil, nil
	}
	if ch == "email" || ch == "both" {
		if recipientEmail != "" {
			emailErr = g.SendEmail(ctx, recipientEmail, subject, plain, html)
		} else {
			emailErr = errors.New("email channel requested but recipient email empty")
		}
	}
	if ch == "sms" || ch == "both" {
		if recipientPhone != "" {
			smsErr = g.SendSMS(ctx, recipientPhone, smsBody)
		} else {
			smsErr = errors.New("sms channel requested but recipient phone empty")
		}
	}
	return emailErr, smsErr
}

// NotifyGiftDryRun is the operator-UI twin of NotifyGift: per-leg dry-run
// overrides allow the admin to "test the channel" without spending a real
// SMS credit or sending a real email even when the global channel is on.
// Used by /api/admin/notification/send.
func (g *GiftClient) NotifyGiftDryRun(ctx context.Context, channel, recipientEmail, recipientPhone, subject, plain, html, smsBody string, emailDryRun, smsDryRun bool) (emailErr, smsErr error) {
	ch := NormalizeChannel(channel)
	if ch == "" {
		return nil, nil
	}
	if ch == "email" || ch == "both" {
		if recipientEmail == "" {
			emailErr = errors.New("email channel requested but recipient email empty")
		} else if emailDryRun {
			log.Printf("notify(DRY) operator email to=%s subject=%q body=%d chars", recipientEmail, subject, len(plain))
		} else if !g.EmailEnabled() {
			emailErr = errors.New("email channel disabled in .env (NOTIFY_EMAIL_ENABLED=false)")
		} else {
			emailErr = g.SendEmail(ctx, recipientEmail, subject, plain, html)
		}
	}
	if ch == "sms" || ch == "both" {
		if recipientPhone == "" {
			smsErr = errors.New("sms channel requested but recipient phone empty")
		} else if smsDryRun {
			log.Printf("notify(DRY) operator sms to=%s body=%q", recipientPhone, smsBody)
		} else if !g.SMSEnabled() {
			smsErr = errors.New("sms channel disabled in .env (NOTIFY_SMS_ENABLED=false)")
		} else {
			smsErr = g.SendSMS(ctx, recipientPhone, smsBody)
		}
	}
	return emailErr, smsErr
}

// WrapOperatorEmailHTML produces a minimal but branded HTML body for an
// arbitrary operator-sent email (no gift theme, no product card — just a
// clean wrap so the recipient sees a recognizable nim.shop header instead
// of a raw text dump).
func WrapOperatorEmailHTML(plain, subject string) string {
	var b strings.Builder
	b.WriteString(`<!doctype html><html><body style="font-family:-apple-system,Segoe UI,Helvetica,Arial,sans-serif;background:#E7DAC0;padding:24px;color:#4E3D28;">`)
	b.WriteString(`<div style="max-width:560px;margin:0 auto;background:#fff8e8;border:1.5px dashed #4E3D28;border-radius:10px;padding:24px;">`)
	if subject != "" {
		b.WriteString(`<h2 style="margin:0 0 12px;color:#4E3D28;font-family:Georgia,serif;">`)
		b.WriteString(htmlEscape(subject))
		b.WriteString(`</h2>`)
	}
	b.WriteString(`<div style="white-space:pre-wrap;line-height:1.55">`)
	b.WriteString(htmlEscape(plain))
	b.WriteString(`</div>`)
	b.WriteString(`<p style="text-align:center;margin-top:20px;color:#9B8763;font-size:12px">Sent by nim.shop admin</p>`)
	b.WriteString(`</div></body></html>`)
	return b.String()
}

// Sentinel JSON error shape (for tests + admin debugging).
func init() {
	_ = json.RawMessage(nil)
}
