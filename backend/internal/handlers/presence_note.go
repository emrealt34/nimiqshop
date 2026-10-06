package handlers

import (
	"sync"
	"time"

	"github.com/valyala/fasthttp"

	"nimiqshop/internal/clientip"
)

/* presence_note.go — where the operator console's "IP · country" comes from.
 *
 * The People panel shows the last observed IP and country of every customer,
 * but the store write lived in exactly ONE place: single-product quote
 * creation. So a customer who signed in, browsed, used the cart (batch quote)
 * or simply kept coming back showed "joined … · seen …" with nothing else,
 * and a person could be opened with no address at all — which is what the
 * operator noticed: "kişi ip adresi ülke de yazsa, basınca o yerde daha
 * güzel olur."
 *
 * Every authenticated touchpoint now notes the origin instead: session
 * restore (what a page load does, so it covers accounts that never check
 * out), Hub login, the presence heartbeat, and BOTH checkout paths.
 *
 * The write is one small user record, throttled per account, and it can never
 * fail a request: presence is operational metadata, never a gate. The IP is the
 * same value clientip.Resolve already gives the checkout for the supplier's
 * X-Forwarded-For. The country comes from the edge whenever the hop carries it
 * (CF-IPCountry, or the edge's own restatement), and otherwise from the hint the
 * browser fetched from the same-origin edge trace — see withEdgeCountry,
 * clientip.CountryHint and src/lib/edgeGeo.ts. No third-party geo lookup is
 * called, for this or anything else.
 */

// presenceNoteGap bounds how often one account's origin may be rewritten.
// Session restore fires on every page load and the heartbeat every few
// seconds; the operator needs a recent value, not one write per click.
const presenceNoteGap = 90 * time.Second

// presenceNoteLast is the last write time per user id for THIS process
// (unix nano). One tiny entry per account ever seen, gone with the process —
// throttling is a write-rate guard, not durable state, and the store itself
// is the record of truth.
var presenceNoteLast sync.Map

// noteUserPresence records the caller's origin, resolving the client IP
// itself. Safe to call from any authenticated handler; cheap enough for the
// heartbeat, which is why the panel finally has an address for everyone.
func (h *Handlers) noteUserPresence(ctx *fasthttp.RequestCtx, userID string) {
	info := clientip.Resolve(ctx, h.Cfg.TrustProxy, h.Cfg.ClientIPPolicy())
	h.noteUserPresenceFrom(userID, h.withEdgeCountry(ctx, info))
}

// withEdgeCountry adds the browser-reported country when the server could not
// settle one itself: the live deployment's edge does not pass Cloudflare's
// CF-IPCountry through, so without this every row in the People panel would
// keep a blank flag however often the customer visited.
//
// It is deliberately a separate step rather than something clientip.Resolve
// does: callers that resolved the address for their OWN use (the checkout
// hands it to the supplier) must keep exactly what the chain said, and only the
// presence note — a display label in the operator console — may fall back to a
// value a script could have set.
func (h *Handlers) withEdgeCountry(ctx *fasthttp.RequestCtx, info clientip.Info) clientip.Info {
	if info.Country == "" {
		info.Country = clientip.CountryHint(ctx)
	}
	return info
}

// noteUserPresenceFrom is the same for handlers that already resolved the IP
// for their own use (the checkout paths hand it to the supplier), so one
// request never parses the proxy headers twice.
func (h *Handlers) noteUserPresenceFrom(userID string, info clientip.Info) {
	if userID == "" || (info.IP == "" && info.Country == "") {
		return
	}
	now := time.Now().UnixNano()
	if prev, ok := presenceNoteLast.Load(userID); ok {
		if last, _ := prev.(int64); now-last < int64(presenceNoteGap) {
			return
		}
	}
	presenceNoteLast.Store(userID, now)
	// Best-effort by design: a store hiccup must not fail the request that
	// happened to carry the visitor.
	_ = h.Store.TouchUserPresence(userID, info.IP, info.Country)
}
