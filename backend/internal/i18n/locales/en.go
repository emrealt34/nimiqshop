package locales

// En — English strings. Keys match src/i18n/locales/en.ts. Use {{var}}
// placeholders for interpolation (the i18n package replaces them at render
// time, mirroring the frontend's {{var}} syntax).
var En = map[string]string{
	// --- Gift email subjects ---
	"email.subjectAnonymous": "Someone sent you a gift 🎁",
	"email.subjectNamed":     "A gift arrived for you 🎁",
	"email.subjectSelf":      "Your order is on its way ✅",

	// --- Gift email body ---
	"email.youGotAGift":       "You received a gift.",
	"email.anonymousLine":     "Someone sent you this via {{site}} — the sender chose to stay anonymous.",
	"email.namedLine":         "Someone sent you this via {{site}}.",
	"email.sendersWallet":     "The sender's Nimiq wallet:",
	"email.item":              "Item",
	"email.sent":              "Sent",
	"email.theirMessage":      "Their message:",
	"email.noMessage":         "No personal message was left with the gift.",
	"email.whereIs":           "Where is {{item}}?",
	"email.deliveryCard":      "The code is sent to this address separately by our partner CryptoRefills; it may sit in the spam folder. For your security it is never shown on the shop's website and never included in this email.",
	"email.deliveryTopup":     "The credit is applied to your phone number directly by our partner CryptoRefills. There is no code to enter and nothing to redeem — if it is not on the number yet, support can chase it with the operator.",
	"email.deliveryEsim":      "The eSIM QR is sent to this address separately by our partner CryptoRefills; it may sit in the spam folder. For your security the QR is never shown on the shop's website and never included in this email.",
	"email.itemCard":          "the gift card code",
	"email.itemTopup":         "the top-up credit",
	"email.itemEsim":          "the eSIM QR",
	"email.giveBack":          "Want to give back? Browse gifts and top-ups:",
	"email.anythingMissing":   "Anything missing? {{url}}",
	"email.reference":         "Reference:",
	"email.builtOnNim":        "Built on NIM. To help secure the network, stake NIM to our validator inside Nimiq Pay:",
	"email.cta":               "Give a gift back at {{site}} →",
	"email.ctaSub":            "Gift cards, eSIMs and phone top-ups — pay with NIM or USDT.",
	"email.contactSupport":    "Something did not arrive? Contact support",
	"email.disclaimer":        "This email tells you about a gift someone sent. The {{item}} is delivered separately by {{site}}'s partner CryptoRefills and is never included in this email.",
	"email.footerTagline":     "Nimiq-native shop for gift cards, eSIMs & top-ups",
	"email.anonSender":        "An anonymous sender",
	"email.anonSenderBody":    "The sender chose to stay anonymous — no name or wallet is shown.",
	"email.preheader.anon":    "Someone (anonymous) sent you a gift via {{site}}",
	"email.preheader.named":   "Someone sent you a gift via {{site}}",
	"email.preheader.product": " — {{product}}",
	"email.preheader.end":     ". Here is what arrived and where to find it.",
	"email.fromAnon":          "from someone anonymous via {{site}}",
	"email.fromNamed":         "via {{site}}",
	"email.labelItem":         "Item",
	"email.giftTag":           "gift",
}
