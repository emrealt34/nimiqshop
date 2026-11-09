package handlers

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"github.com/valyala/fasthttp"
	"nimiqshop/internal/db"
	"nimiqshop/internal/nimiq"
	"testing"
	"time"
)

func groupSigner(t *testing.T, seed string) (string, ed25519.PrivateKey) {
	t.Helper()
	sum := sha256.Sum256([]byte(seed))
	priv := ed25519.NewKeyFromSeed(sum[:])
	addr, e := nimiq.AddressFromPublicKey(priv.Public().(ed25519.PublicKey))
	if e != nil {
		t.Fatal(e)
	}
	return nimiq.NormalizeAddress(addr), priv
}
func groupSignature(site, account, nonce string, private ed25519.PrivateKey) (string, string) {
	message := stakeLinkMessage(site, account, nonce)
	digest := sha256.Sum256([]byte(fmt.Sprintf("\x16Nimiq Signed Message:\n%d%s", len(message), message)))
	return hex.EncodeToString(private.Public().(ed25519.PublicKey)), hex.EncodeToString(ed25519.Sign(private, digest[:]))
}
func TestSignedStakeLinksShareTransitivelyAndRejectReplay(t *testing.T) {
	s, e := db.New(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	a, _ := groupSigner(t, "handler-a")
	b, bkey := groupSigner(t, "handler-b")
	c, ckey := groupSigner(t, "handler-c")
	ua, _ := s.FindOrCreateUserByAddress(a)
	ub, _ := s.FindOrCreateUserByAddress(b)
	for _, addr := range []string{b, c} {
		if _, e = s.ObserveStake(addr, 100000, time.Now()); e != nil {
			t.Fatal(e)
		}
	}
	h := &Handlers{Store: s}
	var ctx fasthttp.RequestCtx
	pub, sig := groupSignature(h.Cfg.SiteName(), a, "nonce-ab", bkey)
	if _, _, _, _, ok := h.completeStakeAddressLink(&ctx, ua, "nonce-ab", b, pub, sig); !ok {
		t.Fatalf("AB rejected %s", ctx.Response.Body())
	}
	pub2, sig2 := groupSignature(h.Cfg.SiteName(), b, "nonce-bc", ckey)
	if _, _, _, _, ok := h.completeStakeAddressLink(&ctx, ub, "nonce-bc", c, pub2, sig2); !ok {
		t.Fatalf("BC rejected %s", ctx.Response.Body())
	}
	for _, address := range []string{a, b, c} {
		u, e := s.GetUserByAddress(address)
		if e != nil || len(u.EarningStakeAddresses()) != 3 {
			t.Fatalf("%s: %v %v", address, u.EarningStakeAddresses(), e)
		}
	}
	if _, e = s.RemoveStakeAddress(ua.ID, b); e != nil {
		t.Fatal(e)
	}
	var replay fasthttp.RequestCtx
	if _, _, _, _, ok := h.completeStakeAddressLink(&replay, ua, "nonce-ab", b, pub, sig); ok || replay.Response.StatusCode() != 409 {
		t.Fatalf("replay accepted: %d", replay.Response.StatusCode())
	}
	var bad fasthttp.RequestCtx
	if _, _, _, _, ok := h.completeStakeAddressLink(&bad, ua, "nonce-ab", c, pub, sig); ok || bad.Response.StatusCode() != 401 {
		t.Fatalf("wrong wallet signature accepted: %d", bad.Response.StatusCode())
	}
}
