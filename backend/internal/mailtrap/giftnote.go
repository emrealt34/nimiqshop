package mailtrap

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"html"
	"image"
	"image/color"
	"image/png"
	"net/url"
	"strconv"
	"strings"
	"time"

	"nimiqshop/internal/i18n"
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

// Shared building blocks of the responsive email. These MUST stay in sync with
// mailer/mailbuilder.py (FONT/MONO/EMAIL_CSS there).
const (
	emailFont = "-apple-system,'Segoe UI',Roboto,Helvetica,Arial,sans-serif"
	emailMono = "ui-monospace,Menlo,Consolas,'Courier New',monospace"
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
  @media only screen and (max-width:620px){
    .email-container{width:100%!important;max-width:100%!important}
    .px-outer{padding-left:10px!important;padding-right:10px!important}
    .px-card{padding-left:18px!important;padding-right:18px!important}
    .h1{font-size:20px!important;line-height:1.25!important}
    .greet{font-size:13px!important}
    .item-line{font-size:15px!important}
    .donor-cell{display:block!important;width:100%!important;padding:0 0 12px 0!important;text-align:center!important}
    .donor-info{display:block!important;width:100%!important;padding-left:0!important;text-align:center!important}
    .donor-img{margin:0 auto!important}
    .addr{font-size:11px!important}
    .cta-table{width:100%!important}
    .cta-link{display:block!important;text-align:center!important}
    .foot{font-size:11px!important}
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
	site := fallback(strings.TrimSpace(n.SiteName), "shop.nimiqbase.com")
	product := strings.TrimSpace(n.ProductLabel)
	tr := n.tr()

	subject := strings.TrimSpace(n.Subject)
	if subject == "" {
		if n.Anonymous {
			subject = tr("email.subjectAnonymous")
		} else {
			subject = tr("email.subjectNamed")
		}
	}

	body := strings.TrimSpace(n.Message)
	if r := []rune(body); len(r) > maxGiftNoteMessageChars {
		body = string(r[:maxGiftNoteMessageChars]) + "…"
	}

	text := n.textBody(site, product, body)
	htmlBody := n.htmlBody(site, product, body, subject)

	return Message{
		To:       []Address{n.Recipient},
		Subject:  subject,
		Text:     text,
		HTML:     htmlBody,
		Category: fallback(n.Category, cfg.Category+GiftCategorySuffix),
		OrderID:  n.OrderID,
		CustomVariables: map[string]any{
			"kind":    "gift_note",
			"product": product,
		},
	}, nil
}

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

// deliveryLine is the "where is my gift?" paragraph. It is route-specific on
// purpose: telling a top-up recipient to look for a code in their inbox — or
// telling them a code is "never shown in the app" when there is no code at all
// — is exactly the mismatch the rest of the shop works to avoid.
func (n GiftNote) deliveryLine() string {
	switch n.deliveryKind() {
	case DeliveryTopUp:
		return "The credit is applied to your phone number directly by our partner CryptoRefills. " +
			"There is no code to enter and nothing to redeem — if it is not on the number yet, " +
			"support can chase it with the operator."
	case DeliveryEsim:
		return "The eSIM QR is sent to this address separately by our partner CryptoRefills; it may sit in " +
			"the spam folder. For your security the QR is never shown on the shop's website and never " +
			"included in this email."
	default:
		return "The code is sent to this address separately by our partner CryptoRefills; it may sit in the " +
			"spam folder. For your security it is never shown on the shop's website and never included in " +
			"this email."
	}
}

// textBody is the plain-text fallback. A recipient with images/HTML blocked
// still gets every fact: who, what, the donation, where it is, and a way back.
func (n GiftNote) textBody(site, product, message string) string {
	var b strings.Builder
	b.WriteString("You received a gift.\n\n")
	if n.Anonymous {
		// Nothing that could identify the sender — and say WHY, so the
		// missing name reads as a choice, not a bug.
		b.WriteString("Someone sent you this" + viaSite(site) + " — the sender chose to stay anonymous.\n")
	} else {
		// No name anywhere: the sender is "someone", and the wallet below is
		// the honest identity of a named gift.
		b.WriteString("Someone sent you this" + viaSite(site) + ".\n")
		if n.GifterNimiqAddress != "" {
			b.WriteString("\nThe sender's Nimiq wallet:\n")
			b.WriteString("  " + groupAddress(n.GifterNimiqAddress) + "\n")
		}
	}
	if product != "" {
		b.WriteString("\nItem: " + product + "\n")
		if !n.PurchasedAt.IsZero() {
			b.WriteString("Sent: " + n.PurchasedAt.UTC().Format("2 Jan 2006") + "\n")
		}
	}
	if message != "" {
		b.WriteString("\nTheir message:\n" + indent(message) + "\n")
	} else {
		b.WriteString("\nNo personal message was left with the gift.\n")
	}
	b.WriteString("\nWhere is " + n.itemWord() + "?\n")
	b.WriteString(wrap(n.deliveryLine(), 76) + "\n")
	if u := safeURL(n.ShopURL); u != "" {
		b.WriteString("\nWant to give back? Browse gifts and top-ups:\n")
		b.WriteString("  " + u + "\n")
	}
	if u := safeURL(n.SupportURL); u != "" {
		b.WriteString("\nAnything missing? " + u + "\n")
	}
	if ref := strings.TrimSpace(n.OrderID); ref != "" {
		b.WriteString("\nReference: " + ref + "\n")
	}
	if sa := strings.TrimSpace(n.StakeValidatorAddress); sa != "" {
		b.WriteString("\nBuilt on NIM. To help secure the network, stake NIM to our validator inside Nimiq Pay:\n")
		b.WriteString("  " + groupAddress(sa) + "\n")
	}
	return strings.TrimRight(b.String(), "\n") + "\n"
}

// htmlBody is the fully responsive HTML document — the same one
// mailer/mailbuilder.py build_html() produces (see the GiftNote doc comment).
// site/product/message come from Build; subject is passed in so the <title>
// can never disagree with the Subject header.
func (n GiftNote) htmlBody(site, product, message, subject string) string {
	esc := func(v string) string { return html.EscapeString(strings.TrimSpace(v)) }
	lines := strings.Split(message, "\n")
	for i, l := range lines {
		lines[i] = esc(l)
	}
	para := strings.Join(lines, "<br>")

	// No name anywhere, not even the inbox snippet: the preheader says
	// "someone", and the wallet in the body is the named gift's identity.
	preWho := "Someone"
	if n.Anonymous {
		preWho = "Someone (anonymous)"
	}

	// ---- preheader (inbox snippet) ---------------------------------------
	pre := preWho + " sent you a gift via " + site
	if product != "" {
		pre += " — " + product
	}
	pre += ". Here is what arrived and where to find it."
	// spacer entities pad the snippet so trailing UI text never shows
	pad := strings.Repeat("&#847;&zwnj;&nbsp;", 8)

	// ---- CTA target --------------------------------------------------------
	cta := ""
	if u := safeURL(n.ShopURL); u != "" {
		cta = strings.TrimRight(u, "/") + "/?utm_source=gift&utm_medium=email&utm_campaign=gift_note"
	}

	var b strings.Builder
	b.WriteString("<!DOCTYPE html>\n")
	b.WriteString(`<html lang="en" xmlns="http://www.w3.org/1999/xhtml" xmlns:v="urn:schemas-microsoft-com:vml" xmlns:o="urn:schemas-microsoft-com:office:office">` + "\n")
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
	b.WriteString(`<body style="margin:0;padding:0;background:#f6f1e7;-webkit-text-size-adjust:100%;-ms-text-size-adjust:100%">` + "\n")
	b.WriteString(`<div style="display:none;font-size:1px;line-height:1px;max-height:0;max-width:0;opacity:0;overflow:hidden;m-hide:1">` + esc(pre) + pad + "</div>\n")
	b.WriteString(`  <table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0" style="background:#f6f1e7">` + "\n")
	b.WriteString("    <tr>\n")
	b.WriteString(`      <td align="center" valign="top" class="px-outer" style="padding:22px 14px">` + "\n")
	b.WriteString(`        <!--[if mso]><table role="presentation" width="600" align="center" cellpadding="0" cellspacing="0" border="0"><tr><td><![endif]-->` + "\n")
	b.WriteString(`        <table role="presentation" width="600" class="email-container" style="width:100%;max-width:600px" cellpadding="0" cellspacing="0" border="0">` + "\n")
	b.WriteString("        <tr>\n")
	b.WriteString(`          <td style="padding:0 2px 14px 2px;font-family:` + emailFont + `">` + "\n")
	b.WriteString(`<span style="font-size:15px;font-weight:800;color:#2f2a24">nim<span style="color:#b98a2e">.shop</span></span>` + "\n")
	b.WriteString(`<span style="font-size:12px;color:#8a7f72">&nbsp;·&nbsp;gift</span>` + "\n")
	b.WriteString("          </td>\n")
	b.WriteString("        </tr>\n")
	b.WriteString("        <tr>\n")
	b.WriteString(`          <td style="background:#fffdf7;border:1px solid #e3d9c6;border-radius:16px;overflow:hidden">` + "\n")
	b.WriteString("            <!-- card -->\n")
	b.WriteString(`            <table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0">` + "\n")
	b.WriteString("              <tr>\n")
	b.WriteString(`                <td class="px-card" style="padding:26px 26px 0 26px;font-family:` + emailFont + `">` + "\n")
	b.WriteString(`<div style="font-size:26px">🎁</div>` + "\n")
	b.WriteString(`<div class="h1" style="font-size:22px;font-weight:800;color:#2f2a24;margin:6px 0 2px">You received a gift</div>` + "\n")
	if n.Anonymous {
		b.WriteString(`<div class="greet" style="font-size:14px;color:#6b6157">from someone anonymous via ` + esc(site) + "</div>\n")
	} else {
		b.WriteString(`<div class="greet" style="font-size:14px;color:#6b6157">via ` + esc(site) + "</div>\n")
	}
	b.WriteString("                </td>\n")
	b.WriteString("              </tr>\n")
	b.WriteString("\n")
	b.WriteString("              <!-- donor identity: side-by-side on desktop, stacked+centred on phones -->\n")
	if n.Anonymous {
		b.WriteString(anonymousCard())
	} else if strings.TrimSpace(n.GifterNimiqAddress) != "" {
		b.WriteString(identityCard(esc(groupAddress(n.GifterNimiqAddress)), n.GifterIdenticonDataURI))
	}
	b.WriteString("\n\n" + `<tr><td class="px-card" style="padding:0 26px"><div style="border-top:1px dashed #e3d9c6;margin-top:18px"></div></td></tr>` + "\n")
	b.WriteString("\n              <!-- body -->\n")

	// ---- item ------------------------------------------------------------
	if product != "" {
		b.WriteString("\n          <tr>\n")
		b.WriteString(`            <td class="px-card" style="padding:18px 26px 16px 26px">` + "\n")
		b.WriteString(`<div style="font-size:11px;text-transform:uppercase;letter-spacing:.06em;color:#b0a287;padding-bottom:3px;font-family:` + emailFont + `">Item</div>` + "\n")
		b.WriteString(`<div class="item-line" style="font-size:17px;font-weight:700;color:#2f2a24;font-family:` + emailFont + `">` + n.itemEmoji() + "&nbsp;" + esc(product) + "</div>\n")
		b.WriteString("              ")
		if !n.PurchasedAt.IsZero() {
			b.WriteString(`<div style="font-size:12px;color:#b0a287;margin-top:2px;font-family:` + emailFont + `">Sent ` + esc(n.PurchasedAt.UTC().Format("2 Jan 2006")) + "</div>")
		}
		b.WriteString("\n")
		b.WriteString("            </td>\n")
		b.WriteString("          </tr>")
	}
	b.WriteString("\n")

	// ---- the buyer's message ----------------------------------------------
	if para != "" {
		b.WriteString("\n          <tr>\n")
		b.WriteString(`            <td class="px-card" style="padding:0 26px 16px 26px">` + "\n")
		b.WriteString(`<div style="font-size:11px;text-transform:uppercase;letter-spacing:.06em;color:#b0a287;margin-bottom:4px;font-family:` + emailFont + `">Their message</div>` + "\n")
		b.WriteString(`<div style="border-left:3px solid #d8c9a8;padding:2px 0 2px 12px;font-size:15px;line-height:1.55;color:#2f2a24;font-style:italic;font-family:` + emailFont + `">` + para + "</div>\n")
		b.WriteString("            </td>\n")
		b.WriteString("          </tr>")
	} else {
		b.WriteString(`<tr><td class="px-card" style="padding:0 26px 6px 26px;color:#8a7f72;font-size:14px;font-family:` + emailFont + `">No personal message was left with the gift.</td></tr>`)
	}
	b.WriteString("\n")

	// ---- "where is it" box --------------------------------------------------
	b.WriteString("\n          <tr>\n")
	b.WriteString(`            <td class="px-card" style="padding:0 26px">` + "\n")
	b.WriteString(`              <table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0" style="background:#fbf6ea;border:1px dashed #e3d9c6;border-radius:10px">` + "\n")
	b.WriteString(`                <tr><td style="padding:12px 14px;font-size:13px;line-height:1.55;color:#5d544a;font-family:` + emailFont + `"><strong>Where is ` + esc(n.itemWord()) + `?</strong> ` + esc(n.deliveryLine()) + "</td></tr>\n")
	b.WriteString("              </table>\n")
	b.WriteString("            </td>\n")
	b.WriteString("          </tr>")
	b.WriteString("\n")
	b.WriteString("\n              <!-- CTA (full-width block button on phones; VML roundrect in Outlook) -->\n")

	// ---- CTA (give back) ----------------------------------------------------
	if cta != "" {
		b.WriteString("\n          <tr>\n")
		b.WriteString(`            <td class="px-card" style="padding:20px 26px 0 26px">` + "\n")
		b.WriteString(`              <!--[if mso]><v:roundrect xmlns:v="urn:schemas-microsoft-com:vml" xmlns:w="urn:schemas-microsoft-com:office:word" href="` + esc(cta) + `" style="height:46px;v-text-anchor:middle;width:254px;" arcsize="22%" strokecolor="#2f2a24" fillcolor="#2f2a24"><w:anchorlock/><center style="color:#fffdf7;font-family:Arial,sans-serif;font-size:15px;font-weight:bold">Give a gift back at ` + esc(site) + ` →</center></v:roundrect><![endif]-->` + "\n")
		b.WriteString("              <!--[if !mso]><!-->\n")
		b.WriteString(`              <table role="presentation" cellpadding="0" cellspacing="0" border="0" class="cta-table" style="width:auto">` + "\n")
		b.WriteString("                <tr>\n")
		b.WriteString(`                  <td bgcolor="#2f2a24" style="border-radius:10px;background:#2f2a24">` + "\n")
		b.WriteString(`                    <a href="` + esc(cta) + `" class="cta-link" style="display:inline-block;padding:13px 22px;font-size:15px;font-weight:700;color:#fffdf7;text-decoration:none;border-radius:10px;font-family:` + emailFont + `">Give a gift back at ` + esc(site) + ` →</a>` + "\n")
		b.WriteString("                  </td>\n")
		b.WriteString("                </tr>\n")
		b.WriteString("              </table>\n")
		b.WriteString("              <!--<![endif]-->\n")
		b.WriteString(`<div style="font-size:12px;color:#8a7f72;margin-top:8px;font-family:` + emailFont + `">Gift cards, eSIMs and phone top-ups — pay with NIM or USDT.</div>` + "\n")
		b.WriteString("            </td>\n")
		b.WriteString("          </tr>")
	}
	b.WriteString("\n")
	b.WriteString("\n")

	// ---- built on NIM (stake) ----------------------------------------------
	if sa := strings.TrimSpace(n.StakeValidatorAddress); sa != "" {
		b.WriteString("\n          <tr>\n")
		b.WriteString(`            <td class="px-card" style="padding:18px 26px 0 26px">` + "\n")
		b.WriteString(`              <table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0" style="border-top:1px dashed #e3d9c6">` + "\n")
		b.WriteString("                <tr>\n")
		b.WriteString(`                  <td class="foot" style="padding:16px 4px 0 4px;font-size:12px;line-height:1.5;color:#8a7f72;font-family:` + emailFont + `">` + "\n")
		b.WriteString(`<strong>Built on NIM.</strong> ` + esc(site) + ` is a Nimiq-native shop. To help` + "\n")
		b.WriteString(`                    secure the network, stake your NIM to our validator inside Nimiq Pay:<br>` + "\n")
		b.WriteString(`<span style="font-family:` + emailMono + `;color:#6b6157">` + esc(groupAddress(sa)) + "</span>\n")
		b.WriteString("                  </td>\n")
		b.WriteString("                </tr>\n")
		b.WriteString("              </table>\n")
		b.WriteString("            </td>\n")
		b.WriteString("          </tr>")
	}
	b.WriteString("\n")
	b.WriteString("\n              <!-- footer -->\n")
	b.WriteString("              <tr>\n")
	b.WriteString(`                <td class="px-card foot" style="padding:18px 26px 24px 26px;color:#8a7f72;font-size:12px;line-height:1.6;border-top:1px solid #eee2c8;font-family:` + emailFont + `">` + "\n")
	b.WriteString("                  ")
	if u := safeURL(n.SupportURL); u != "" {
		b.WriteString(`<a href="` + u + `" style="color:#8a6d1f;text-decoration:none;font-family:` + emailFont + `">Something did not arrive? Contact support</a>`)
	}
	b.WriteString("\n")
	b.WriteString("                  ")
	if ref := strings.TrimSpace(n.OrderID); ref != "" {
		b.WriteString(`<div style="margin-top:8px;color:#b0a287;font-size:11px;font-family:` + emailFont + `">Reference: ` + esc(ref) + "</div>")
	}
	b.WriteString("\n")
	b.WriteString(`<div style="color:#b0a287;margin-top:8px">This email tells you about a gift someone sent. The ` + esc(n.itemWord()) + ` is delivered separately by ` + esc(site) + `&rsquo;s partner CryptoRefills and is never included in this email.</div>` + "\n")
	b.WriteString("                </td>\n")
	b.WriteString("              </tr>\n")
	b.WriteString("            </table>\n")
	b.WriteString("          </td>\n")
	b.WriteString("        </tr>\n")
	b.WriteString("        <tr>\n")
	b.WriteString(`          <td class="foot" style="padding:14px 6px 0 6px;color:#b0a287;font-size:11px;line-height:1.5;text-align:center;font-family:` + emailFont + `">` + "\n")
	b.WriteString(`            ` + esc(site) + ` · Nimiq-native shop for gift cards, eSIMs &amp; top-ups` + "\n")
	b.WriteString("          </td>\n")
	b.WriteString("        </tr>\n")
	b.WriteString("        </table>\n")
	b.WriteString(`        <!--[if mso]></td></tr></table><![endif]-->` + "\n")
	b.WriteString("      </td>\n")
	b.WriteString("    </tr>\n")
	b.WriteString("  </table>\n")
	b.WriteString("</body>\n")
	b.WriteString("</html>\n")
	return b.String()
}

// Mosaic geometry of the donor avatar: a 32x32-cell grid, 2px cells, rendered
// inside the 88px-wide donor table (browsers stretch the 64px of cells to the
// table width; Outlook keeps 64px — still a solid avatar). 32x32 is the
// measured sweet spot for the REAL @nimiq/identicons face rasterized at its
// native 160px: ~300-500 run-compressed cells ≈ 20-30KB of HTML for the whole
// note — safely under Gmail's 102KB clipping cutoff (88x88 cells measured
// 95-107KB: clipped). The cell-size reset (font-size/line-height 0) is
// emitted once per <tr> — not per <td> — so the resets inherit to the cells
// and Outlook stays solid.
const (
	identN    = 32
	identCell = 2
)

// identiconTable re-draws the donor's identicon PNG as a bgcolor-cell mosaic.
//
// Why not an <img>: Gmail strips data: URIs from image sources and CID inline
// attachments render unreliably in webmail, so an embedded avatar arrives as
// an empty box for a chunk of recipients. An 88x88 table of bgcolor cells is
// the one graphics primitive every mail client renders, so that is what ships.
// The URI must still pass safeDataImage, and only an 8-bit RGBA PNG decodes
// (the Python harness decodes exactly the same subset); anything else keeps
// the placeholder avatar. Identicon gradients become stepped cell colors —
// the deterministic pattern, which is the identity, survives exactly.
func identiconTable(dataURI string) string {
	u := safeDataImage(dataURI)
	if u == "" {
		return ""
	}
	const pngPrefix = "data:image/png;base64,"
	if !strings.HasPrefix(u, pngPrefix) {
		return "" // an SVG cannot be rasterized here; the placeholder keeps the layout
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(strings.TrimPrefix(u, pngPrefix)))
	if err != nil {
		return ""
	}
	img, err := png.Decode(bytes.NewReader(raw))
	if err != nil {
		return ""
	}
	nrgba, ok := img.(*image.NRGBA)
	if !ok {
		return "" // palette/gray PNGs keep the placeholder (Python parity)
	}
	b := nrgba.Bounds()
	clampX := func(v int) int {
		if v > b.Max.X-1 {
			return b.Max.X - 1
		}
		return v
	}
	clampY := func(v int) int {
		if v > b.Max.Y-1 {
			return b.Max.Y - 1
		}
		return v
	}
	var sb strings.Builder
	sb.WriteString(`<table class="donor-img" role="presentation" width="88" cellpadding="0" cellspacing="0" border="0" style="margin:0 auto;border-collapse:separate;border-spacing:0;border-radius:16px;overflow:hidden;border:2px solid #d8c9a8;font-size:0;line-height:0">`)
	for iy := 0; iy < identN; iy++ {
		sy := clampY(int((float64(iy)+0.5)*float64(b.Dy())/identN) + b.Min.Y)
		// The font-size/line-height reset lives on the <tr> (inherited by the
		// cells), not each <td> — keeps the 88x88 mosaic under Gmail's 102KB cap.
		sb.WriteString(`<tr style="font-size:0;line-height:0">`)
		for ix := 0; ix < identN; {
			sx := clampX(int((float64(ix)+0.5)*float64(b.Dx())/identN) + b.Min.X)
			col := pngComposite(nrgba.At(sx, sy))
			run := 1
			for ix+run < identN {
				sx2 := clampX(int((float64(ix+run)+0.5)*float64(b.Dx())/identN) + b.Min.X)
				if pngComposite(nrgba.At(sx2, sy)) != col {
					break
				}
				run++
			}
			if run == 1 {
				sb.WriteString(`<td bgcolor="` + col + `" width="` + strconv.Itoa(identCell) + `" height="` + strconv.Itoa(identCell) + `">&nbsp;</td>`)
			} else {
				sb.WriteString(`<td bgcolor="` + col + `" colspan="` + strconv.Itoa(run) + `" width="` + strconv.Itoa(run*identCell) + `" height="` + strconv.Itoa(identCell) + `">&nbsp;</td>`)
			}
			ix += run
		}
		sb.WriteString("</tr>")
	}
	sb.WriteString("</table>")
	return sb.String()
}

// pngComposite is one mosaic cell's color: the source pixel, alpha-composited
// over the email card background (#fffdf7) with the same thresholds and the
// same truncating blend as the Python _mosaic_hex — byte parity by construction.
func pngComposite(c color.Color) string {
	nc := color.NRGBAModel.Convert(c).(color.NRGBA)
	if nc.A >= 250 {
		return fmt.Sprintf("#%02x%02x%02x", nc.R, nc.G, nc.B)
	}
	if nc.A <= 5 {
		return "#fffdf7"
	}
	ar := float64(nc.A) / 255.0
	r := int(float64(nc.R)*ar + 255*(1-ar))
	g := int(float64(nc.G)*ar + 253*(1-ar))
	b := int(float64(nc.B)*ar + 247*(1-ar))
	return fmt.Sprintf("#%02x%02x%02x", r, g, b)
}

// anonymousCard replaces identityCard for anonymous gifts: no identicon, no
// name, no address — just a calm "the sender chose to stay anonymous" so the
// recipient understands the omission is deliberate, not a rendering bug.
func anonymousCard() string {
	return `
            <!-- anonymous sender: identity withheld by request -->
            <table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0">
              <tr>
                <td class="px-card" style="padding:18px 26px 0 26px;font-family:` + emailFont + `">
                <table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0" style="background:#f3eee2;border:1px dashed #d8cbb0;border-radius:14px">
                  <tr>
                    <td style="padding:16px 18px;text-align:center">
                      <div style="font-size:26px;line-height:1">&#128374;</div>
                      <div style="font-size:15px;font-weight:700;color:#2f2a24;margin-top:4px">An anonymous sender</div>
                      <div style="font-size:12.5px;color:#6b6157;margin-top:3px">The sender chose to stay anonymous — no name or wallet is shown.</div>
                    </td>
                  </tr>
                </table>
                </td>
              </tr>
            </table>`
}

// identityCard is the "who sent this" block: the buyer's real Nimiq
// identicon and their Nimiq wallet address — never a name. On phones the two
// cells become display:block (see emailCSS) and stack, centred; Outlook never
// sees the media query and keeps the side-by-side row.
func identityCard(addr, identURI string) string {
	img := identiconTable(identURI)
	if img == "" {
		img = `<div class="donor-img" style="width:88px;height:88px;border-radius:16px;background:#efe4cd"></div>`
	}
	addrCell := ""
	if addr != "" {
		addrCell = `<div class="addr" style="font-family:` + emailMono + `;font-size:11.5px;color:#6b6157;word-break:break-all">` + addr + `</div>` +
			`<div style="font-size:11px;color:#b0a287;margin-top:3px;font-family:` + emailFont + `">the sender&rsquo;s Nimiq wallet</div>`
	}
	return `
              <tr>
                <td class="px-card" style="padding:20px 26px 0 26px">
                  <table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0">
                    <tr>
                      <td class="donor-cell" width="88" valign="middle" style="text-align:center">` + img + `</td>
                      <td class="donor-info" style="padding-left:14px;vertical-align:middle">
                        ` + addrCell + `
                      </td>
                    </tr>
                  </table>
                </td>
              </tr>`
}

// wrap breaks a sentence at word boundaries so the plain-text part reads like a
// paragraph and not one long line in a monospace client.
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
