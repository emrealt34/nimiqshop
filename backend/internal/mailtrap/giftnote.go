package mailtrap

import (
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"html"
	"image/png"
	"net/url"
	"strconv"
	"strings"
	"time"

	"nimiqshop/internal/i18n"
	"nimiqshop/internal/itemtile"
)

// GiftNote is the "someone sent you a gift" email — the ONLY message the shop
// sends on behalf of a buyer.
//
// It now also carries the *donor's* on-chain identity, because shop.nimiqbase.com's gift
// notes are sent when the buyer has donated too (usually by routing their
// cashback to reforestation / OneTreePlanted). Showing the buyer's real Nimiq
// identicon + wallet address turns a generic "you got a gift" into "this person
// gave you this" — the emotional core of the message — and invites the recipient
// to give back through the shop link.
//
// The HTML body is FULLY RESPONSIVE and byte-for-byte the same document the
// mailer/ Python harness builds (mailer/mailbuilder.py build_html):
//
//   - fluid table layout (width:100% / max-width:600px): 600px card on desktop,
//     full-width on phones;
//   - Outlook desktop (Word rendering engine: no max-width, no media queries)
//     gets a fixed 600px MSO "ghost table" so the card never collapses;
//   - on phones (Gmail app/web, Apple Mail, Samsung — all support <style> +
//     @media) the donor identicon stacks ABOVE the name/address, centred, and
//     the "give back" button becomes a full-width block; Outlook gets a VML
//     roundrect button instead;
//   - every structural style is ALSO inline, so clients that strip <style>
//     still render the desktop layout;
//   - dark mode is pinned to the light palette (color-scheme metas) because the
//     cream/beige brand would invert badly;
//   - a hidden preheader feeds the inbox snippet.
//
// There is deliberately no field for a code, PIN, link or voucher value: the
// product itself is delivered by CryptoRefills to the recipient's inbox, and a
// second copy of it in this email would put the secret where the shop cannot
// revoke it. That is also why the checkout never shows the code on screen — the
// note and the delivery are different things, and only one of them is ours to
// write.
type GiftNote struct {
	// Recipient is who gets the note. Required.
	Recipient Address
	// Self marks a plain (non-gift) purchase: the buyer IS the recipient, so
	// the note drops the donor identity and the "someone sent you" framing
	// and reads as an order confirmation instead (owner, 2026-10-05).
	Self bool
	// Anonymous mirrors the checkout's anonymous flag. When true, NOTHING that
	// could identify the sender is rendered — no name in the subject, no
	// identicon, no wallet address — and the note says the sender chose to
	// stay anonymous, so the recipient knows the omission is intentional.
	Anonymous bool
	// GifterNimiqAddress is the buyer's Nimiq wallet address, shown to the
	// recipient as proof of who sent it. Optional.
	GifterNimiqAddress string
	// GifterIdenticonDataURI is the buyer's REAL Nimiq identicon as a
	// data:image/png;base64,… URI, rendered by the frontend's identicon tooling
	// and passed in. Gmail blocks data: image sources (and CID inline
	// attachments are unreliable in webmail), so the PNG is decoded here and
	// re-drawn as a bgcolor-cell mosaic — see identiconTable. Only data: URIs
	// are admitted; anything else is dropped so config cannot turn the email
	// into a tracking beacon. Optional.
	GifterIdenticonDataURI string
	// SiteName renders "from your friend at shop.nimiqbase.com". Optional.
	SiteName string
	// ProductLabel is the human item name, e.g. "Steam · 50 USD" or
	// "Turkcell · 100 TRY top-up". Optional but very worth setting: a gift email
	// that never says what arrived looks like phishing.
	ProductLabel string
	// Items is the product list, one entry per line of the order, shown the way
	// the site's order list shows it (brand tile, name, face value, quantity).
	// Empty falls back to ProductLabel alone.
	Items []GiftItem
	// itemsMore counts order lines beyond maxNoteItems; set by Build.
	itemsMore int
	// Message is the buyer's personal text. Escaped for HTML and never
	// interpreted as markup.
	Message string
	// ShopURL is the "give back" link offered to the recipient. Optional.
	ShopURL string
	// StakeValidatorAddress is shop.nimiqbase.com's NIM validator, offered in a small
	// "Built on NIM" footnote so supporters can stake inside Nimiq Pay. Optional.
	StakeValidatorAddress string
	// OrderID ties the send to a quote; it rides as a custom variable too.
	OrderID string
	// PurchasedAt, when set, is shown as the date under the item line.
	PurchasedAt time.Time
	// SupportURL is the one support link the note offers. Anything but http(s)
	// is dropped rather than rendered.
	SupportURL string
	// Delivery decides how the note explains delivery, because the three routes
	// genuinely differ: a code arrives in an inbox, an eSIM QR arrives in an
	// inbox, and a top-up is simply already on the phone number with nothing to
	// redeem. Empty infers it from ProductLabel, which is right for the labels
	// this shop renders ("… top-up", "… eSIM", otherwise a card).
	Delivery string
	// Subject overrides the generated subject. Empty = the default wording.
	Subject string
	// Category overrides the transport category for this send.
	Category string
	// Lang selects the language for the subject and body. Empty/unknown falls
	// back to English. All buyer-facing copy in the email (subject, headings,
	// delivery paragraph, CTA, footer, disclaimer) is rendered through the
	// i18n bundle for this language; ProductLabel/Message/SiteName stay as-is.
	Lang string
}

// Delivery kinds a note can describe (GiftNote.Delivery). They change only
// the wording, never the delivery itself — CryptoRefills owns that. They are
// deliberately NOT called Kind: mailtrap.Kind is the error class.
const (
	DeliveryCard  = "card"
	DeliveryEsim  = "esim"
	DeliveryTopUp = "topup"
)

// maxGiftNoteMessageChars bounds what is rendered even if a caller forgot to
// validate upstream. The stored value is already capped at giftMessageMax by
// the quote handler; this is the belt.
const maxGiftNoteMessageChars = 2000

// Shared building blocks of the responsive email.
const (
	emailFont = "-apple-system,'Segoe UI',Roboto,Helvetica,Arial,sans-serif"
	emailMono = "ui-monospace,Menlo,Consolas,'Courier New',monospace"
)

// mailPalette is the SHOP's own palette — the kraft/cream tokens of
// src/styles/app.css — flattened to solid hex, because a mail client supports
// neither CSS variables nor rgba. Each value names the token it mirrors, and
// the alpha tokens are the token composited over the surface they sit on:
//
//	--bg #E7DAC0 kraft page · --surface-1 #F6EFDC card · --surface-2 #EFE4C6 panel
//	--ink #4E3D28 · --ink-dim #7C6A4E · --ink-faint #786446
//	--stamp #C7481D / --on-stamp #FFF6E8 (the primary action)
//	--gold-400 #C98A1B / --gold-600 #8A5E0B (brand accents, links on paper)
//
// Keeping the note on these tokens is what makes an inbox message look like it
// came from the same shop as the page it links to: same paper, same ink, same
// stamp-red button, same gold.
const (
	mailKraft    = "#e7dac0" // page behind the card
	mailPaper    = "#f6efdc" // the card itself
	mailPanel    = "#efe4c6" // recessed panels (donor, "where is it")
	mailInk      = "#4e3d28"
	mailInkDim   = "#7c6a4e"
	mailInkFaint = "#786446"
	mailLine     = "#d8cdb6" // --line over the card
	mailDash     = "#b09f8b" // --line-dash over the card
	mailStamp    = "#c7481d"
	mailOnStamp  = "#fff6e8"
	mailGold     = "#c98a1b"
	mailGoldDeep = "#8a5e0b"
	mailGreen    = "#3e6b4f" // --green: the "built on NIM" note
	mailGoldSoft = "#f2e3c2"
	mailGoldLine = "#e3cf9e"
)

// emailCSS is the single <style> block of the email: client resets plus ONE
// media query for phones. Only class rules live here — everything structural
// is inline so clients that drop <style> still render the desktop layout.
const emailCSS = `  :root{color-scheme:light;supported-color-schemes:light}
  html,body{margin:0!important;padding:0!important;width:100%!important}
  body{-webkit-text-size-adjust:100%;-ms-text-size-adjust:100%}
  table,td{mso-table-lspace:0pt!important;mso-table-rspace:0pt!important;border-collapse:collapse}
  img{border:0;height:auto;line-height:100%;outline:none;text-decoration:none;-ms-interpolation-mode:bicubic}
  a{padding:0}
  .ExternalClass{width:100%}
  .ExternalClass,.ExternalClass p,.ExternalClass span,.ExternalClass td,.ExternalClass div{line-height:inherit}
  /* The donor mosaic is a table of 1x1px cells: without these resets some
     clients give the cells a minimum height from the inherited line box. */
  @media only screen and (max-width:620px){
    .email-container{width:100%!important;max-width:100%!important}
    .px-outer{padding-left:10px!important;padding-right:10px!important}
    .px-card{padding-left:18px!important;padding-right:18px!important}
    .h1{font-size:20px!important;line-height:1.25!important}
    .greet{font-size:13px!important}
    .item-line{font-size:15px!important}
    .donor-cell{display:block!important;width:100%!important;padding:0 0 12px 0!important;text-align:center!important}
    .donor-info{display:block!important;width:100%!important;padding-left:0!important;text-align:center!important}
    .addr{font-size:11px!important}
    .cta-table{width:100%!important}
    .cta-link{display:block!important;text-align:center!important}
    .foot{font-size:11px!important}
    .hero-pad{padding:20px 18px 0 18px!important}
  }`

// tr returns the translator bound to this note's language (English fallback).
// Every buyer-facing string in the email goes through tr() so the recipient
// reads it in their own language.
func (n GiftNote) tr() i18n.T {
	return i18n.For(i18n.Clean(n.Lang))
}

// Build turns the note into a Message ready for Send. Text and HTML are
// generated from the same fields so the two can never disagree about what the
// buyer wrote.
func (n GiftNote) Build(cfg Config) (Message, error) {
	if err := n.validate(); err != nil {
		return Message{}, err
	}
	// AIRTIGHT anonymity: for an anonymous gift the identity fields are
	// stripped HERE, before any subject/body/HTML branch runs.
	if n.Anonymous {
		n.GifterNimiqAddress = ""
		n.GifterIdenticonDataURI = ""
	}
	n.Items, n.itemsMore = sanitizeItems(n.Items)
	site := fallback(strings.TrimSpace(n.SiteName), "shop.nimiqbase.com")
	product := strings.TrimSpace(n.ProductLabel)
	tr := n.tr()

	subject := strings.TrimSpace(n.Subject)
	if subject == "" {
		switch {
		case n.Self:
			subject = tr("email.subjectSelf")
		case n.Anonymous:
			subject = tr("email.subjectAnonymous")
		default:
			subject = tr("email.subjectNamed")
		}
	}

	body := strings.TrimSpace(n.Message)
	if r := []rune(body); len(r) > maxGiftNoteMessageChars {
		body = string(r[:maxGiftNoteMessageChars]) + "…"
	}

	text := n.textBody(site, product, body)
	htmlBody := n.htmlBody(site, product, body, subject)
	// Gmail clips an HTML body at ~102 KB. The body is text and layout only (the
	// avatar is an attachment), so this is a guard, not an expected cut.
	if len(htmlBody) > MailHTMLBudget {
		return Message{}, ErrMailTooLarge
	}
	var attachments []Attachment
	// The shop logo is the hero mark on every note (gift and receipt alike).
	attachments = append(attachments, shopLogoAttachment())
	seenTile := map[string]bool{}
	for _, it := range n.Items {
		cid := itemTileCID(it.tile)
		if seenTile[cid] {
			continue
		}
		seenTile[cid] = true
		attachments = append(attachments, Attachment{Filename: cid + ".png", ContentType: "image/png", Data: it.tile, ContentID: cid})
	}
	if icon, ok := identiconImage(n.GifterIdenticonDataURI); ok {
		attachments = append(attachments, Attachment{
			Filename:    "buyer-identicon.png",
			ContentType: "image/png",
			Data:        icon,
			ContentID:   identiconContentID,
		})
	}

	return Message{
		To:          []Address{n.Recipient},
		Subject:     subject,
		Text:        text,
		HTML:        htmlBody,
		Category:    fallback(n.Category, cfg.Category+GiftCategorySuffix),
		OrderID:     n.OrderID,
		Attachments: attachments,
		CustomVariables: map[string]any{
			"kind":    "gift_note",
			"product": product,
		},
	}, nil
}

// MailHTMLBudget is the largest HTML body Build will return. Gmail clips at
// about 102 KB, so this leaves a wide margin for headers and encoding.
const MailHTMLBudget = 90_000

// ErrMailTooLarge is returned if a body still exceeds MailHTMLBudget after the
// avatar has been dropped. It cannot happen with the bounded inputs above; it
// exists so an oversized mail is refused, never sent clipped.
var ErrMailTooLarge = errors.New("mailtrap: gift note body exceeds the mail size budget")

func (n GiftNote) validate() error {
	if !looksLikeAddress(n.Recipient.Email) {
		return ErrNoRecipient
	}
	return nil
}

// deliveryKind resolves the note's wording: an explicit Delivery always wins,
// the product label is the fallback.
func (n GiftNote) deliveryKind() string {
	switch k := strings.ToLower(strings.TrimSpace(n.Delivery)); k {
	case DeliveryTopUp, DeliveryCard, DeliveryEsim:
		return k
	}
	p := strings.ToLower(n.ProductLabel)
	switch {
	case strings.Contains(p, "top-up"), strings.Contains(p, "topup"),
		strings.Contains(p, "recharge"), strings.Contains(p, "credit"),
		strings.Contains(p, "kontör"):
		return DeliveryTopUp
	case strings.Contains(p, "e-sim"), strings.Contains(p, "esim"):
		return DeliveryEsim
	default:
		return DeliveryCard
	}
}

// itemWord names what is arriving, in the wording the recipient expects.
func (n GiftNote) itemWord() string {
	switch n.deliveryKind() {
	case DeliveryTopUp:
		return "the top-up credit"
	case DeliveryEsim:
		return "the eSIM QR"
	default:
		return "the gift card code"
	}
}

// itemWordT is itemWord in the note's language.
func (n GiftNote) itemWordT(tl i18n.T) string {
	switch n.deliveryKind() {
	case DeliveryTopUp:
		return tl("email.itemTopup")
	case DeliveryEsim:
		return tl("email.itemEsim")
	}
	return tl("email.itemCard")
}

// deliveryLineT is deliveryLine in the note's language.
func (n GiftNote) deliveryLineT(tl i18n.T) string {
	switch n.deliveryKind() {
	case DeliveryTopUp:
		return tl("email.deliveryTopup")
	case DeliveryEsim:
		return tl("email.deliveryEsim")
	}
	return tl("email.deliveryCard")
}

// itemEmoji is the small icon next to the item line. Purely cosmetic.
func (n GiftNote) itemEmoji() string {
	switch n.deliveryKind() {
	case DeliveryTopUp:
		return "📞"
	case DeliveryEsim:
		return "📱"
	default:
		return "🎟️"
	}
}

// textBody is the plain-text fallback. A recipient with images/HTML blocked
// still gets every fact: who, what, the donation, where it is, and a way back.
func (n GiftNote) textBody(site, product, message string) string {
	tl := n.tr()
	var b strings.Builder
	if n.Self {
		b.WriteString(tl("mailtext.selfLine") + "\n\n")
	} else {
		b.WriteString(tl("email.youGotAGift") + "\n\n")
	}
	if !n.Self {
		if n.Anonymous {
			// Nothing that could identify the sender — and say WHY, so the
			// missing name reads as a choice, not a bug.
			b.WriteString(tl("email.anonymousLine", map[string]string{"site": site}) + "\n")
		} else {
			// No name anywhere: the sender is "someone", and the wallet below is
			// the honest identity of a named gift.
			b.WriteString(tl("email.namedLine", map[string]string{"site": site}) + "\n")
			if n.GifterNimiqAddress != "" {
				b.WriteString("\n" + tl("email.sendersWallet") + "\n")
				b.WriteString("  " + groupAddress(n.GifterNimiqAddress) + "\n")
			}
		}
	}
	if n.Self && n.GifterNimiqAddress != "" && !n.Anonymous {
		b.WriteString("\n" + tl("mailtext.paidFromWallet") + "\n")
		b.WriteString("  " + groupAddress(n.GifterNimiqAddress) + "\n")
	}
	sentLine := func() {
		if !n.PurchasedAt.IsZero() {
			b.WriteString(tl("email.sent") + ": " + n.PurchasedAt.UTC().Format("2 Jan 2006") + "\n")
		}
	}
	if len(n.Items) > 0 {
		b.WriteString("\n" + tl("mailtext.itemsLabel") + "\n" + itemsText(tl, n.Items, n.itemsMore))
		sentLine()
	} else if product != "" {
		b.WriteString("\n" + tl("mailtext.itemLabel") + " " + product + "\n")
		sentLine()
	}
	if !n.Self {
		if message != "" {
			b.WriteString("\n" + tl("email.theirMessage") + "\n" + indent(message) + "\n")
		} else {
			b.WriteString("\n" + tl("email.noMessage") + "\n")
		}
	}
	b.WriteString("\n" + tl("email.whereIs", map[string]string{"item": n.itemWordT(tl)}) + "\n")
	b.WriteString(wrap(n.deliveryLineT(tl), 76) + "\n")
	b.WriteString("\n" + tl("mailtext.arrivesFrom", map[string]string{"sender": "noreply@cryptorefills.com"}) + "\n")
	b.WriteString(tl("mailtext.openSenderMail") + " " + cryptorefillsInboxURL + "\n")
	b.WriteString(tl("mailtext.searchInbox", map[string]string{"query": "from:noreply@cryptorefills.com"}) + "\n")
	if u := safeURL(n.ShopURL); u != "" {
		b.WriteString("\n" + tl("email.giveBack") + "\n")
		b.WriteString("  " + u + "\n")
	}
	if u := safeURL(n.SupportURL); u != "" {
		b.WriteString("\n" + tl("email.anythingMissing", map[string]string{"url": u}) + "\n")
	}
	if ref := strings.TrimSpace(n.OrderID); ref != "" {
		b.WriteString("\n" + tl("email.reference") + " " + ref + "\n")
	}
	if sa := strings.TrimSpace(n.StakeValidatorAddress); sa != "" {
		b.WriteString("\n" + tl("email.builtOnNim") + "\n")
		b.WriteString("  " + groupAddress(sa) + "\n")
	}
	return strings.TrimRight(b.String(), "\n") + "\n"
}

// htmlBody is the fully responsive HTML document — the same one
// mailer/mailbuilder.py build_html() produces (see the GiftNote doc comment).
// site/product/message come from Build; subject is passed in so the <title>
// can never disagree with the Subject header.
func (n GiftNote) htmlBody(site, product, message, subject string) string {
	tl := n.tr()
	lines := strings.Split(message, "\n")
	for i, l := range lines {
		lines[i] = esc(l)
	}
	para := strings.Join(lines, "<br>")

	// No name anywhere, not even the inbox snippet: "someone" for named and
	// anonymous gifts alike, the wallet in the body is the named gift's identity.
	pre := tl("email.preheader.named", map[string]string{"site": site})
	if n.Anonymous {
		pre = tl("email.preheader.anon", map[string]string{"site": site})
	}
	if n.Self {
		pre = tl("mailtext.preSelf", map[string]string{"site": site})
	}
	if product != "" {
		pre += tl("email.preheader.product", map[string]string{"product": product})
	}
	pre += tl("email.preheader.end")
	// spacer entities pad the snippet so trailing UI text never shows
	pad := strings.Repeat("&#847;&zwnj;&nbsp;", 8)

	// ---- CTA target --------------------------------------------------------
	cta := ""
	if u := safeURL(n.ShopURL); u != "" {
		cta = strings.TrimRight(u, "/") + "/?utm_source=gift&utm_medium=email&utm_campaign=gift_note"
	}

	// ONE design, two voices: a gift someone sent, or the buyer's own receipt.
	// The badge is the shop logo (the site header's mark), never an emoji. It
	// ships as an inline PNG attachment; see shopLogoAttachment.
	badge := shopLogoImg
	heading := tl("mailcard.youReceived")
	sub := tl("email.fromNamed", map[string]string{"site": site})
	if n.Anonymous && !n.Self {
		sub = tl("email.fromAnon", map[string]string{"site": site})
	}
	if n.Self {
		heading = tl("mailcard.onTheWay")
	}

	// The card is built from nested tables with every structural style inline
	// (the stylesheet may be stripped) and the palette above — see mailPalette
	// for why each colour is what it is.
	font := emailFont
	var b strings.Builder
	b.WriteString("<!DOCTYPE html>\n")
	b.WriteString(`<html lang="` + i18n.Clean(n.Lang) + `" xmlns="http://www.w3.org/1999/xhtml" xmlns:v="urn:schemas-microsoft-com:vml" xmlns:o="urn:schemas-microsoft-com:office:office">` + "\n")
	b.WriteString("<head>\n")
	b.WriteString(`<meta charset="utf-8">` + "\n")
	b.WriteString(`<meta name="viewport" content="width=device-width,initial-scale=1">` + "\n")
	b.WriteString(`<meta http-equiv="X-UA-Compatible" content="IE=edge">` + "\n")
	b.WriteString(`<meta name="color-scheme" content="light">` + "\n")
	b.WriteString(`<meta name="supported-color-schemes" content="light">` + "\n")
	b.WriteString("<title>" + esc(subject) + "</title>\n")
	b.WriteString("<!--[if mso]>\n")
	b.WriteString(`<noscript><xml><o:OfficeDocumentSettings><o:PixelsPerInch>96</o:PixelsPerInch></o:OfficeDocumentSettings></xml></noscript>` + "\n")
	b.WriteString("<![endif]-->\n")
	b.WriteString("<style>\n")
	b.WriteString(emailCSS + "\n")
	b.WriteString("</style>\n")
	b.WriteString("</head>\n")
	b.WriteString(`<body style="margin:0;padding:0;background:` + mailKraft + `;-webkit-text-size-adjust:100%;-ms-text-size-adjust:100%">` + "\n")
	b.WriteString(`<div style="display:none;font-size:1px;line-height:1px;max-height:0;max-width:0;opacity:0;overflow:hidden;m-hide:1">` + esc(pre) + pad + "</div>\n")
	b.WriteString(`<table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0" style="background:` + mailKraft + `">` + "\n")
	b.WriteString("  <tr>\n")
	b.WriteString(`    <td align="center" valign="top" class="px-outer" style="padding:24px 12px 28px 12px">` + "\n")
	b.WriteString(`      <!--[if mso]><table role="presentation" width="600" align="center" cellpadding="0" cellspacing="0" border="0"><tr><td><![endif]-->` + "\n")
	b.WriteString(`      <table role="presentation" width="600" class="email-container" style="width:100%;max-width:600px" cellpadding="0" cellspacing="0" border="0">` + "\n")

	// ---- brand line: the shop's own wordmark, same gold dot as the navbar --
	b.WriteString("      <tr>\n")
	b.WriteString(`        <td style="padding:0 2px 12px 2px;font-family:` + font + `">` + "\n")
	b.WriteString(`          <span style="font-size:16px;font-weight:800;color:` + mailInk + `;letter-spacing:-.01em">` + wordmark(site) + `</span>` + "\n")
	b.WriteString(`          <span style="font-size:11px;font-weight:700;letter-spacing:.08em;text-transform:uppercase;color:` + mailInkFaint + `">&nbsp;&nbsp;` + esc(tl(badgeKey(n.Self))) + `</span>` + "\n")
	b.WriteString("        </td>\n")
	b.WriteString("      </tr>\n")

	// ---- the card ----------------------------------------------------------
	b.WriteString("      <tr>\n")
	b.WriteString(`        <td style="background:` + mailPaper + `;border:1px solid ` + mailLine + `;border-radius:18px">` + "\n")
	b.WriteString(`          <table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0">` + "\n")

	// hero: badge + headline
	b.WriteString("            <tr>\n")
	b.WriteString(`              <td class="px-card hero-pad" style="padding:26px 26px 0 26px;font-family:` + font + `">` + "\n")
	b.WriteString(`                <table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0"><tr>` + "\n")
	b.WriteString(`                  <td width="54" valign="top" style="width:54px">` + "\n")
	b.WriteString(`                    <table role="presentation" width="52" cellpadding="0" cellspacing="0" border="0" style="width:52px;background:` + mailGoldSoft + `;border:1px solid ` + mailGoldLine + `;border-radius:26px">` + "\n")
	b.WriteString(`                      <tr><td height="52" align="center" valign="middle" style="height:52px;font-size:24px;line-height:52px">` + badge + `</td></tr>` + "\n")
	b.WriteString("                    </table>\n")
	b.WriteString("                  </td>\n")
	b.WriteString(`                  <td style="padding-left:14px;vertical-align:middle">` + "\n")
	b.WriteString(`                    <div class="h1" style="font-size:22px;font-weight:800;color:` + mailInk + `;margin:0 0 3px 0;letter-spacing:-.01em">` + esc(heading) + `</div>` + "\n")
	b.WriteString(`                    <div class="greet" style="font-size:14px;color:` + mailInkDim + `">` + esc(sub) + `</div>` + "\n")
	b.WriteString("                  </td>\n")
	b.WriteString("                </tr></table>\n")
	b.WriteString("              </td>\n")
	b.WriteString("            </tr>\n")

	// identity. A gift shows the sender card (anonymous / wallet / plain). A
	// plain purchase shows the BUYER's own wallet and Nimiq identicon when the
	// order carries an address; without one it shows nothing.
	if !n.Self || (!n.Anonymous && strings.TrimSpace(n.GifterNimiqAddress) != "") {
		b.WriteString("            <tr>\n")
		b.WriteString(`              <td class="px-card" style="padding:18px 26px 0 26px">` + "\n")
		if n.Anonymous {
			b.WriteString(anonymousCard(tl))
		} else if strings.TrimSpace(n.GifterNimiqAddress) != "" {
			_, withIcon := identiconImage(n.GifterIdenticonDataURI)
			b.WriteString(identityCard(esc(groupAddress(n.GifterNimiqAddress)), withIcon, n.Self, tl))
		} else {
			b.WriteString(plainSenderCard(tl))
		}
		b.WriteString("              </td>\n")
		b.WriteString("            </tr>\n")
	}

	// item row: what arrived, and when it was sent. Centred on the card, like
	// the product grid under it, so the label never sits alone in a corner.
	b.WriteString("            <tr>\n")
	b.WriteString(`              <td class="px-card" style="padding:20px 26px 18px 26px;text-align:center">` + "\n")
	b.WriteString(`                ` + eyebrowCentered(font, itemLabelFor(n, tl)) + "\n")
	if len(n.Items) == 0 {
		item := n.itemEmoji()
		if product != "" {
			item += "&nbsp;" + esc(product)
		} else {
			item += "&nbsp;" + esc(n.itemWord())
		}
		b.WriteString(`                <div class="item-line" style="font-size:17px;font-weight:700;color:` + mailInk + `;font-family:` + font + `;text-align:center">` + item + `</div>` + "\n")
	} else {
		b.WriteString(`                ` + itemsBlock(font, n.Items, n.itemsMore, tl) + "\n")
	}
	sentLine := "just now"
	if !n.PurchasedAt.IsZero() {
		sentLine = n.PurchasedAt.UTC().Format("2 Jan 2006")
	}
	b.WriteString(`                <div style="padding-top:8px;font-size:13px;color:` + mailInkDim + `;font-family:` + font + `;text-align:center"><span style="font-size:11px;font-weight:700;text-transform:uppercase;letter-spacing:.08em;color:` + mailInkFaint + `">` + esc(tl("email.sent")) + `</span> &nbsp;` + esc(sentLine) + `</div>` + "\n")
	b.WriteString("              </td>\n")
	b.WriteString("            </tr>\n")

	// the buyer's own words
	if !n.Self {
		b.WriteString("            <tr>\n")
		b.WriteString(`              <td class="px-card" style="padding:0 26px 18px 26px">` + "\n")
		if para != "" {
			b.WriteString(`                ` + eyebrow(font, tl("mailcard.theirMessage")) + "\n")
			b.WriteString(`                <table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0" style="background:` + mailPanel + `;border-radius:12px"><tr>` + "\n")
			b.WriteString(`                  <td width="4" style="width:4px;background:` + mailGold + `;border-radius:12px 0 0 12px">&nbsp;</td>` + "\n")
			b.WriteString(`                  <td style="padding:12px 14px;font-size:15px;line-height:1.55;color:` + mailInk + `;font-style:italic;font-family:` + font + `">` + para + `</td>` + "\n")
			b.WriteString("                </tr></table>\n")
		} else {
			b.WriteString(`                <div style="font-size:13px;color:` + mailInkFaint + `;font-family:` + font + `">No personal message was left with the gift.</div>` + "\n")
		}
		b.WriteString("              </td>\n")
		b.WriteString("            </tr>\n")
	}

	// where is it — the one paragraph that must survive intact
	b.WriteString("            <tr>\n")
	b.WriteString(`              <td class="px-card" style="padding:0 26px 18px 26px">` + "\n")
	b.WriteString(`                <table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0" style="background:` + mailPanel + `;border:1px dashed ` + mailDash + `;border-radius:12px">` + "\n")
	b.WriteString(`                  <tr><td style="padding:13px 15px;font-size:13px;line-height:1.6;color:` + mailInkDim + `;font-family:` + font + `">` + "\n")
	b.WriteString(`                    <strong style="color:` + mailInk + `">` + esc(tl("email.whereIs", map[string]string{"item": n.itemWordT(tl)})) + `</strong><br>` + "\n")
	b.WriteString(`                    ` + esc(n.deliveryLineT(tl)) + ` ` + tl("mailtext.arrivesFromHTML", map[string]string{"sender": `<a href="` + cryptorefillsInboxURL + `" style="color:` + mailInk + `;font-weight:700;text-decoration:underline">noreply@cryptorefills.com</a>`, "query": `<strong style="color:` + mailInk + `">from:noreply@cryptorefills.com</strong>`}) + "\n")
	b.WriteString("                  </td></tr>\n")
	b.WriteString("                </table>\n")
	b.WriteString("              </td>\n")
	b.WriteString("            </tr>\n")

	// CTA (full-width block button on phones; VML roundrect in Outlook)
	if cta != "" {
		b.WriteString("            <tr>\n")
		b.WriteString(`              <td class="px-card" style="padding:0 26px 20px 26px">` + "\n")
		b.WriteString(`                <!--[if mso]><v:roundrect xmlns:v="urn:schemas-microsoft-com:vml" xmlns:w="urn:schemas-microsoft-com:office:word" href="` + esc(cta) + `" style="height:46px;v-text-anchor:middle;width:254px;" arcsize="22%" strokecolor="` + mailStamp + `" fillcolor="` + mailStamp + `"><w:anchorlock/><center style="color:` + mailOnStamp + `;font-family:Arial,sans-serif;font-size:15px;font-weight:bold">` + esc(tl("email.cta", map[string]string{"site": site})) + `</center></v:roundrect><![endif]-->` + "\n")
		b.WriteString("                <!--[if !mso]><!-->\n")
		b.WriteString(`                <table role="presentation" cellpadding="0" cellspacing="0" border="0" class="cta-table" style="width:auto">` + "\n")
		b.WriteString(`                  <tr><td bgcolor="` + mailStamp + `" style="border-radius:10px;background:` + mailStamp + `">` + "\n")
		b.WriteString(`                    <a href="` + esc(cta) + `" class="cta-link" style="display:inline-block;padding:14px 22px;font-size:15px;font-weight:700;color:` + mailOnStamp + `;text-decoration:none;border-radius:10px;font-family:` + font + `">` + esc(tl("email.cta", map[string]string{"site": site})) + `</a>` + "\n")
		b.WriteString("                  </td></tr>\n")
		b.WriteString("                </table>\n")
		b.WriteString("                <!--<![endif]-->\n")
		b.WriteString(`                <div style="font-size:12px;color:` + mailInkFaint + `;padding-top:10px;font-family:` + font + `">` + esc(tl("email.ctaSub")) + `</div>` + "\n")
		b.WriteString("              </td>\n")
		b.WriteString("            </tr>\n")
	}

	// built on NIM (stake)
	if sa := strings.TrimSpace(n.StakeValidatorAddress); sa != "" {
		b.WriteString("            <tr>\n")
		b.WriteString(`              <td class="px-card" style="padding:0 26px 22px 26px">` + "\n")
		b.WriteString(`                <table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0" style="border-top:1px dashed ` + mailDash + `">` + "\n")
		b.WriteString(`                  <tr><td class="foot" style="padding:16px 2px 0 2px;font-size:12px;line-height:1.6;color:` + mailInkFaint + `;font-family:` + font + `">` + "\n")
		b.WriteString(`                    <strong style="color:` + mailGreen + `">` + esc(tl("mailtext.builtOnNimLabel")) + `</strong> ` + esc(tl("mailtext.nativeShop", map[string]string{"site": site})) + `<br>` + "\n")
		b.WriteString(`                    <span style="font-family:` + emailMono + `;font-size:11.5px;color:` + mailInkDim + `">` + esc(groupAddress(sa)) + `</span>` + "\n")
		b.WriteString("                  </td></tr>\n")
		b.WriteString("                </table>\n")
		b.WriteString("              </td>\n")
		b.WriteString("            </tr>\n")
	}

	// footer
	b.WriteString("            <tr>\n")
	b.WriteString(`              <td class="px-card foot" style="padding:16px 26px 22px 26px;border-top:1px solid ` + mailLine + `;font-size:12px;line-height:1.6;color:` + mailInkFaint + `;font-family:` + font + `">` + "\n")
	if u := safeURL(n.SupportURL); u != "" {
		b.WriteString(`                <a href="` + u + `" style="color:` + mailGoldDeep + `;font-weight:700;text-decoration:none">` + esc(tl("email.contactSupport")) + `</a>` + "\n")
	}
	if ref := strings.TrimSpace(n.OrderID); ref != "" {
		b.WriteString(`                <div style="padding-top:8px;color:` + mailInkFaint + `;font-size:11px;font-family:` + emailMono + `">` + esc(tl("email.reference")) + ` ` + esc(ref) + `</div>` + "\n")
	}
	disclaimerKey := "email.disclaimer"
	if n.Self {
		disclaimerKey = "mailtext.disclaimerOrder"
	}
	b.WriteString(`                <div style="padding-top:8px;font-size:11px;color:` + mailInkFaint + `">` + esc(tl(disclaimerKey, map[string]string{"site": site})) + `</div>` + "\n")
	b.WriteString("              </td>\n")
	b.WriteString("            </tr>\n")
	b.WriteString("          </table>\n")
	b.WriteString("        </td>\n")
	b.WriteString("      </tr>\n")

	// below the card: the same footer line the site carries
	b.WriteString("      <tr>\n")
	b.WriteString(`        <td class="foot" style="padding:12px 4px 0 4px;color:` + mailInkFaint + `;font-size:11px;line-height:1.6;text-align:center;font-family:` + font + `">` + "\n")
	b.WriteString(`          ` + esc(site) + ` · ` + esc(tl("email.footerTagline")) + "\n")
	b.WriteString("        </td>\n")
	b.WriteString("      </tr>\n")
	b.WriteString("      </table>\n")
	b.WriteString(`      <!--[if mso]></td></tr></table><![endif]-->` + "\n")
	b.WriteString("    </td>\n")
	b.WriteString("  </tr>\n")
	b.WriteString("</table>\n")
	b.WriteString("</body>\n")
	b.WriteString("</html>\n")
	return b.String()
}

// giftWord names the note itself, so a receipt never calls itself a gift.
// cryptorefillsInboxURL opens Gmail filtered to mail from the CryptoRefills
// sender, so the buyer lands on the delivery mail with one tap.
const cryptorefillsInboxURL = "https://mail.google.com/mail/u/0/#search/from%3Anoreply%40cryptorefills.com"

// itemLabelFor is the eyebrow above the product list: singular for one line,
// plural when the order has several.
func itemLabelFor(n GiftNote, tl i18n.T) string {
	if len(n.Items) > 1 || n.itemsMore > 0 {
		if n.Self {
			return tl("mailcard.yourItems")
		}
		return tl("mailcard.theirGifts")
	}
	if n.Self {
		return tl("mailcard.yourItem")
	}
	return tl("mailcard.theirGift")
}

// eyebrow is the small all-caps label used above every block of the card.
func eyebrowCentered(font, text string) string {
	return `<div style="font-size:11px;font-weight:700;text-transform:uppercase;letter-spacing:.08em;color:` + mailInkFaint + `;padding-bottom:5px;font-family:` + font + `;text-align:center">` + text + `</div>`
}

func eyebrow(font, text string) string {
	return `<div style="font-size:11px;font-weight:700;text-transform:uppercase;letter-spacing:.08em;color:` + mailInkFaint + `;padding-bottom:5px;font-family:` + font + `">` + text + `</div>`
}

// panel wraps a block in the recessed paper tone the site uses for notes.
func panel(font, inner string) string {
	return `<table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0" style="background:` + mailPanel + `;border:1px dashed ` + mailDash + `;border-radius:14px">` +
		`<tr><td style="padding:15px 16px;font-family:` + font + `">` + inner + `</td></tr></table>`
}

func paidOrFrom(self bool, tl i18n.T) string {
	if self {
		return tl("mailcard.paidFrom")
	}
	return tl("mailcard.from")
}

// badgeKey is the header badge label for the note's kind.
func badgeKey(self bool) string {
	if self {
		return "mailtext.badgeOrder"
	}
	return "mailtext.badgeGift"
}

func identityFootnote(tl i18n.T, self bool) string {
	if self {
		return tl("mailtext.identitySelf")
	}
	return tl("mailtext.identityGift")
}

// plainSenderCard is the gift-without-a-wallet case: the note still says a gift
// arrived, it just has no on-chain identity to show.
func plainSenderCard(tl i18n.T) string {
	font := emailFont
	inner := `<div style="font-size:13px;line-height:1.6;color:` + mailInkDim + `">` + esc(tl("mailtext.plainSender")) + `</div>`
	return panel(font, inner)
}

// wordmark renders the site name with the dot before the TLD in gold, the way
// the navbar does: nimiqshop.io -> nimiqshop<gold>.</gold>io.
func wordmark(site string) string {
	name, tld, ok := strings.Cut(strings.TrimSpace(site), ".")
	if !ok || name == "" || tld == "" {
		return html.EscapeString(strings.TrimSpace(site))
	}
	return html.EscapeString(name) + `<span style="color:` + mailGold + `">.</span>` + html.EscapeString(tld)
}

// anonymousCard replaces identityCard for anonymous gifts: no identicon, no
// name, no address — just a calm "the sender chose to stay anonymous" so the
// recipient understands the omission is deliberate, not a rendering bug.
func anonymousCard(tl i18n.T) string {
	font := emailFont
	inner := `<table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0"><tr>` +
		`<td width="42" valign="middle" style="width:42px">` +
		`<table role="presentation" width="40" cellpadding="0" cellspacing="0" border="0" style="width:40px;background:` + mailPaper + `;border:1px solid ` + mailDash + `;border-radius:20px">` +
		`<tr><td height="40" align="center" valign="middle" style="height:40px;font-size:18px;line-height:40px">🤍</td></tr></table>` +
		`</td>` +
		`<td style="padding-left:14px;vertical-align:middle">` +
		eyebrow(font, tl("mailcard.from")) +
		`<div style="font-size:13px;line-height:1.55;color:` + mailInk + `"><strong>` + esc(tl("email.anonSender")) + `</strong></div>` +
		`<div style="font-size:11.5px;color:` + mailInkFaint + `;padding-top:2px">` + esc(tl("email.anonSenderBody")) + `</div>` +
		`</td></tr></table>`
	return panel(font, inner)
}

// identityCard is the donor block: their REAL Nimiq identicon (as a mosaic,
// see identiconTable) and their wallet address — never a name. On phones the
// two stack, centred, inside the same panel.
// esc escapes text for HTML content and attribute values.
func esc(v string) string { return html.EscapeString(strings.TrimSpace(v)) }

// GiftItem is one product line in the note.
type GiftItem struct {
	// Name is the brand or product family, e.g. "Steam".
	Name string
	// Detail is the face value, e.g. "50 USD". Optional.
	Detail string
	// Qty above 1 shows as "×N".
	Qty int
	// BgColor is the brand's own tile background (hex or rgb()). Empty = white.
	BgColor string
	// Logo is a small PNG tile (see brandlogo). Nil = the storefront's bag icon.
	Logo []byte
	// tile is the brand tile drawn for the mail (see itemtile); set by sanitizeItems.
	tile []byte
}

// maxNoteItems bounds the product list. A bigger order says how many more it
// has instead of growing the mail.
const maxNoteItems = 8

// maxItemLogoBytes bounds each logo attachment. Logos are prepared by
// brandlogo at a few KB; anything larger is dropped rather than mailed.
const maxItemLogoBytes = 64 << 10

// sanitizeItems keeps at most maxNoteItems entries with a usable name, quantity
// and bounded logo. A dropped logo leaves a letter tile, never a broken image.
func sanitizeItems(in []GiftItem) ([]GiftItem, int) {
	var out []GiftItem
	more := 0
	for _, it := range in {
		if strings.TrimSpace(it.Name) == "" {
			continue
		}
		if it.Qty < 1 {
			it.Qty = 1
		}
		if len(it.Logo) > maxItemLogoBytes {
			it.Logo = nil
		}
		if len(out) == maxNoteItems {
			more++
			continue
		}
		it.tile = itemtile.Render(it.Logo, it.BgColor)
		out = append(out, it)
	}
	return out, more
}

// itemTileCID is the stable Content-ID of a rendered tile: the same brand
// shared by two lines is attached once.
func itemTileCID(tile []byte) string {
	sum := sha256.Sum256(tile)
	return "item-" + hex.EncodeToString(sum[:6])
}

// itemsBlock renders the product list as a two-column grid, like the storefront's
// product tiles. Each cell is the brand tile, the name with its quantity, and the
// face value underneath. Rows hold at most two items.
func itemsBlock(font string, items []GiftItem, more int, tl i18n.T) string {
	var b strings.Builder
	b.WriteString(`<table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0" style="margin-top:6px">`)
	for i := 0; i < len(items); i += 2 {
		b.WriteString("<tr>")
		b.WriteString(itemCell(font, items[i], true))
		if i+1 < len(items) {
			b.WriteString(itemCell(font, items[i+1], false))
		} else {
			b.WriteString(`<td width="50%" style="width:50%"></td>`)
		}
		b.WriteString("</tr>")
	}
	if more > 0 {
		b.WriteString(`<tr><td colspan="2" style="padding:4px 0 10px 0;font-size:13px;color:` + mailInkFaint + `;font-family:` + font + `">` + esc(tl("mailtext.moreInOrder", map[string]string{"n": strconv.Itoa(more)})) + `</td></tr>`)
	}
	b.WriteString("</table>")
	return b.String()
}

// itemCell is one product in the grid. The tile is 137px wide, the storefront's
// product tile size on a phone (136.5px), drawn from the 2x PNG; it shrinks on
// narrow screens.
func itemCell(font string, it GiftItem, left bool) string {
	// Both columns get equal side padding and centred content, so the grid
	// sits in the middle of the panel rather than hugging the left edge.
	pad := "0 8px 16px 8px"
	label := strings.TrimSpace(it.Name)
	if it.Qty > 1 {
		label += " ×" + strconv.Itoa(it.Qty)
	}
	detail := ""
	if d := strings.TrimSpace(it.Detail); d != "" {
		detail = `<div style="font-size:13px;line-height:1.4;color:` + mailInkDim + `;font-family:` + font + `;text-align:center">` + esc(d) + `</div>`
	}
	img := `<img src="cid:` + itemTileCID(it.tile) + `" width="137" height="86" alt="` + esc(it.Name) + `" style="display:block;margin:0 auto;width:137px;max-width:100%;height:auto;border:0;outline:none">`
	return `<td width="50%" valign="top" align="center" style="width:50%;padding:` + pad + `;text-align:center">` + img +
		`<div style="font-size:14px;font-weight:700;line-height:1.3;color:` + mailInk + `;font-family:` + font + `;padding-top:8px;text-align:center">` + esc(label) + `</div>` + detail + `</td>`
}

// itemsText is the plain-text twin of itemsBlock.
func itemsText(tl i18n.T, items []GiftItem, more int) string {
	var b strings.Builder
	for _, it := range items {
		line := "  - " + strings.TrimSpace(it.Name)
		if it.Qty > 1 {
			line += " x" + strconv.Itoa(it.Qty)
		}
		if d := strings.TrimSpace(it.Detail); d != "" {
			line += " (" + d + ")"
		}
		b.WriteString(line + "\n")
	}
	if more > 0 {
		b.WriteString("  " + tl("mailtext.moreInOrder", map[string]string{"n": strconv.Itoa(more)}) + "\n")
	}
	return b.String()
}

// shopLogoContentID is the Content-ID of the shop logo shown on order mail.
const shopLogoContentID = "shop-logo"

//go:embed assets/brand-icon.png
var shopLogoPNG []byte

// shopLogoImg is the logo as an inline image. It is trusted, constant markup;
// the alt text is what a client with images blocked shows.
const shopLogoImg = `<img src="cid:` + shopLogoContentID + `" width="48" height="48" alt="nimiqshop.io" style="display:block;margin:0 auto;width:48px;height:48px;border:0;outline:none">`

func shopLogoAttachment() Attachment {
	return Attachment{Filename: "nimiqshop-logo.png", ContentType: "image/png", Data: shopLogoPNG, ContentID: shopLogoContentID}
}

// identiconContentID is the Content-ID of the buyer's identicon attachment.
const identiconContentID = "buyer-identicon"

// identiconMaxBytes bounds the avatar PNG so config cannot smuggle a large
// payload through the identicon field.
const identiconMaxBytes = 256 << 10

// identiconImage returns the buyer's Nimiq identicon as PNG bytes. The frontend
// sends a data:image/png URI; it is accepted only if it decodes as a real PNG of
// sane size. ok=false means the mail goes out without an avatar.
//
// The PNG ships as an INLINE ATTACHMENT (cid:) and is shown at full resolution.
// The earlier cell mosaic was a workaround for Gmail blocking data: images, but
// at 72 cells it lost the face (mouth, lower body). Attachments do not count
// toward Gmail's ~102 KB clip, so the size budget no longer depends on the
// avatar.
func identiconImage(dataURI string) ([]byte, bool) {
	u := safeDataImage(dataURI)
	const pngPrefix = "data:image/png;base64,"
	if u == "" || !strings.HasPrefix(u, pngPrefix) {
		return nil, false
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(strings.TrimPrefix(u, pngPrefix)))
	if err != nil || len(raw) == 0 || len(raw) > identiconMaxBytes {
		return nil, false
	}
	cfg, err := png.DecodeConfig(bytes.NewReader(raw))
	if err != nil || cfg.Width < 1 || cfg.Height < 1 || cfg.Width > 1024 || cfg.Height > 1024 {
		return nil, false
	}
	return raw, true
}

// identityCard renders the wallet/identicon card. self=true is the buyer's own
// plain purchase: "Paid from" instead of "From", and no gift wording. withIcon
// says whether the identicon attachment is part of the message.
func identityCard(addr string, withIcon bool, self bool, tl i18n.T) string {
	font := emailFont
	addrCell := `<div class="addr" style="font-family:` + emailMono + `;font-size:12px;line-height:1.5;color:` + mailInk + `;word-break:break-all">` + addr + `</div>`
	avatar := ""
	gap := "0"
	if withIcon {
		gap = "16px"
		// The face sits on the panel with no frame or background: the identicon
		// PNG is transparent around the hexagon, so the avatar is drawn as-is.
		// The img alt text is what a client with images blocked shows instead.
		img := `<img src="cid:` + identiconContentID + `" width="72" height="72" alt="Nimiq identicon of the buyer's wallet" style="display:block;width:72px;height:72px;border:0;outline:none;background:transparent">`
		frame := `<table role="presentation" cellpadding="0" cellspacing="0" border="0" style="margin:0 auto;background:transparent;border:0"><tr><td style="padding:0;font-size:0;line-height:0;background:transparent">` + img + `</td></tr></table>`
		avatar = `<td class="donor-cell" width="84" valign="middle" style="width:84px;text-align:center">` + frame + `</td>`
	}
	info := `<td class="donor-info" style="vertical-align:middle;padding-left:` + gap + `">` +
		eyebrow(font, paidOrFrom(self, tl)) +
		addrCell +
		`<div style="font-size:11.5px;line-height:1.5;color:` + mailInkFaint + `;padding-top:4px">` + esc(identityFootnote(tl, self)) + `</div>` +
		`</td>`
	inner := `<table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0"><tr>` + avatar + info + `</tr></table>`
	return panel(font, inner)
}

func wrap(s string, width int) string {
	words := strings.Fields(s)
	if len(words) == 0 {
		return ""
	}
	var out strings.Builder
	line := 0
	for i, w := range words {
		if i > 0 && line+len(w)+1 > width {
			out.WriteString("\n")
			line = 0
		} else if i > 0 {
			out.WriteString(" ")
			line++
		}
		out.WriteString(w)
		line += len(w)
	}
	return out.String()
}

// SendGiftNote is the one call the notification worker needs: it builds the
// message from the note and hands it to the transport.
func (c *Client) SendGiftNote(ctx context.Context, n GiftNote) ([]string, error) {
	m, err := n.Build(c.cfg)
	if err != nil {
		return nil, err
	}
	return c.Send(ctx, m)
}

// viaSite renders the " via <site>" sentence for the plain-text intro line, so the text
// and HTML parts agree on the wording.
func viaSite(site string) string {
	if strings.TrimSpace(site) == "" {
		return ""
	}
	return " via " + strings.TrimSpace(site)
}

func indent(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i, l := range lines {
		lines[i] = "  | " + l
	}
	return strings.Join(lines, "\n")
}

func fallback(v, other string) string {
	if strings.TrimSpace(v) == "" {
		return other
	}
	return strings.TrimSpace(v)
}

// groupAddress canonicalizes an address's spacing: uppercase and collapse runs
// of whitespace/hyphens to single spaces, so however the order stored it, the
// email shows one cleanly spaced, copy-pasteable string (e.g.
// "NQ49 N8MB XYCR XBUP 404C KXKK L49M A7BT F082").
func groupAddress(v string) string {
	split := func(r rune) bool { return r == ' ' || r == '\t' || r == '\n' || r == '-' || r == '_' }
	fields := strings.FieldsFunc(strings.ToUpper(strings.TrimSpace(v)), split)
	if len(fields) == 0 {
		return ""
	}
	return strings.Join(fields, " ")
}

// safeURL admits http(s) only, so a support/shop URL that came from configuration
// cannot turn the email into a javascript: or data: link.
func safeURL(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	u, err := url.Parse(v)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return ""
	}
	return v
}

// safeDataImage admits only data: URIs that carry an image. Without this, a
// "data:" value from config could smuggle arbitrary bytes or an external URL
// into an <img src>. The caller (frontend identicon tooling) always produces
// data:image/png;base64 or data:image/svg+xml.
func safeDataImage(v string) string {
	v = strings.TrimSpace(v)
	if !strings.HasPrefix(v, "data:image/png;base64,") &&
		!strings.HasPrefix(v, "data:image/svg+xml;base64,") {
		return ""
	}
	if len(v) > 600_000 { // a 320px identicon PNG is ~25 KB; cap generously
		return ""
	}
	return v
}
