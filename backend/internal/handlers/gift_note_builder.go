package handlers

/*
 * gift_note_builder.go — the ONE place a fulfilled quote becomes a Mailtrap
 * gift note. Both senders build from here so the automatic fulfillment mail
 * (settlement tracker, wired in cmd/server/main.go) and the admin retry
 * (AdminSendGiftNotification) can never drift apart: what the tracker sent is
 * what a retry re-sends. It only assembles the note — no store writes, no
 * sends; callers keep their own send-once discipline (db.Quote.GiftNotifiedAt).
 *
 * The note is anonymous-first: on an anonymous quote the buyer's identity is
 * not even looked up, and mailtrap.GiftNote.Build() strips the identity
 * fields regardless — two seals, either one enough.
 */

import (
	"strings"

	"nimiqshop/internal/db"
	"nimiqshop/internal/mailtrap"
)

// stakeValidatorAddress is the shop's own NIM validator, offered in the note's
// "Built on NIM" footnote so a recipient can stake inside Nimiq Pay. (Also
// used by the admin test-email surface.)
const stakeValidatorAddress = "NQ49 N8MB XYCR XBUP 404C KXKK L49M A7BT F082"

// BuildGiftNoteFromQuote renders the recipient-facing gift note for a
// fulfilled quote. The lang argument selects the email language (typically
// parsed from the buyer's request via i18n.Parse); a Quote remembered the
// buyer's language at purchase time so retries and async fulfillment keep
// the language that was active when they checked out.
func (h *Handlers) BuildGiftNoteFromQuote(q db.Quote, lang string) mailtrap.GiftNote {
	faceValue, currency, product := adminQuoteRenderFields(q)
	note := mailtrap.GiftNote{
		Recipient:    mailtrap.Address{Email: strings.TrimSpace(q.CustomerEmail)},
		Anonymous:    q.Anonymous,
		SiteName:     h.Cfg.SiteName(),
		ProductLabel: product + " · " + faceValue + " " + currency,
		Message:      q.GiftMessage,
		OrderID:      q.ID,
		PurchasedAt:  q.CreatedAt,
		ShopURL:      h.Cfg.SiteURL(),
		SupportURL:   h.Cfg.SiteURL() + "/support",
		Delivery:     giftNoteDelivery(q),
		Lang:         lang,
	}
	note.Self = strings.TrimSpace(q.GiftChannel) != "email"
	if !q.Anonymous {
		if q.UserID != "" {
			if u, err := h.Store.GetUser(q.UserID); err == nil {
				note.GifterNimiqAddress = u.NimiqAddress
			}
		}
		note.GifterIdenticonDataURI = q.GifterIdenticonDataURI
	}
	return note
}

// giftNoteDelivery maps the supplier's delivery route onto the note's wording
// kinds: a phone-delivered top-up is the one route that has to be named.
func giftNoteDelivery(q db.Quote) string {
	if strings.HasPrefix(strings.TrimSpace(q.BeneficiaryAccount), "+") {
		return mailtrap.DeliveryTopUp
	}
	return ""
}

// operatorEmailHTML wraps an operator-authored plain body in the shop's mail
// look (no gift theme, no product card) so a direct admin email is not a raw
// text dump. Local twin of the removed notification.WrapOperatorEmailHTML.
func (h *Handlers) operatorEmailHTML(plain, subject string) string {
	var b strings.Builder
	b.WriteString(`<!doctype html><html><body style="font-family:-apple-system,Segoe UI,Helvetica,Arial,sans-serif;background:#E7DAC0;padding:24px;color:#4E3D28;">`)
	b.WriteString(`<div style="max-width:560px;margin:0 auto;background:#fff8e8;border:1.5px dashed #4E3D28;border-radius:10px;padding:24px;">`)
	if subject != "" {
		b.WriteString(`<h2 style="margin:0 0 12px;color:#4E3D28;font-family:Georgia,serif;">`)
		b.WriteString(htmlEscapeText(subject))
		b.WriteString(`</h2>`)
	}
	b.WriteString(`<div style="white-space:pre-wrap;line-height:1.55">`)
	b.WriteString(htmlEscapeText(plain))
	b.WriteString(`</div>`)
	b.WriteString(`<p style="text-align:center;margin-top:20px;color:#9B8763;font-size:12px">Sent by ` + h.Cfg.SiteName() + ` admin</p>`)
	b.WriteString(`</div></body></html>`)
	return b.String()
}

func htmlEscapeText(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&#39;")
	return r.Replace(s)
}
