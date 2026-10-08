package handlers

import (
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/valyala/fasthttp"

	"nimiqshop/internal/db"
	"nimiqshop/internal/nimiq"
	"nimiqshop/internal/ratings"
)

// testUserKey mirrors middleware's unexported user-id context key, so a test
// can act as an authenticated buyer without a real session.
const testUserKey = "user_id"

const (
	testShop  = "NQ07 0000 0000 0000 0000 0000 0000 0000 0000"
	testBuyer = "NQ22 1111 1111 1111 1111 1111 1111 1111 1111"
	testOther = "NQ33 2222 2222 2222 2222 2222 2222 2222 2222"
)

// fakeChain is a JSON-RPC stand-in for the Nimiq node. It answers the two
// methods the rating verifier uses, in the wrapped shape the live RPC returns.
type fakeChain struct {
	mu      sync.Mutex
	txs     map[string]map[string]interface{} // hash -> getTransactionByHash payload
	history []map[string]interface{}          // getTransactionsByAddress payload
}

func (f *fakeChain) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Method string        `json:"method"`
		Params []interface{} `json:"params"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	f.mu.Lock()
	defer f.mu.Unlock()
	var data interface{}
	switch req.Method {
	case "getTransactionByHash":
		if d, ok := f.txs[req.Params[0].(string)]; ok {
			data = d
		}
	case "getTransactionsByAddress":
		data = f.history
	}
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"jsonrpc": "2.0",
		"result":  map[string]interface{}{"data": data, "metadata": nil},
		"id":      1,
	})
}

func tx(hash, from, to string, value int64, memo string, mined bool) map[string]interface{} {
	d := map[string]interface{}{
		"hash":            hash,
		"from":            from,
		"to":              to,
		"value":           value,
		"recipientData":   hex.EncodeToString([]byte(memo)),
		"executionResult": true,
		"confirmations":   3,
	}
	if mined {
		d["blockNumber"] = 63680000
	}
	return d
}

type ratingEnv struct {
	store *db.Store
	chain *fakeChain
	h     *Handlers
}

func newRatingEnv(t *testing.T) *ratingEnv {
	t.Helper()
	store, err := db.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.GetOrCreateUserByID("buyer", testBuyer); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetOrCreateUserByID("other", testOther); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateOrder(db.Order{ID: "o1", UserID: "buyer", Status: "delivered"}); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateOrder(db.Order{ID: "o2", UserID: "buyer", Status: "delivered"}); err != nil {
		t.Fatal(err)
	}
	chain := &fakeChain{txs: map[string]map[string]interface{}{}}
	srv := httptest.NewServer(chain)
	t.Cleanup(srv.Close)
	h := &Handlers{
		Store:           store,
		RatingRPC:       nimiq.NewClient(srv.URL),
		RatingRecipient: testShop,
		RatingFeeLuna:   1,
	}
	return &ratingEnv{store: store, chain: chain, h: h}
}

// call runs one handler as the given user and returns the status and body.
func call(t *testing.T, fn func(*fasthttp.RequestCtx), userID, orderID string, body map[string]interface{}) (int, map[string]interface{}) {
	t.Helper()
	var ctx fasthttp.RequestCtx
	raw, _ := json.Marshal(body)
	ctx.Request.Header.SetMethod(http.MethodPost)
	ctx.Request.Header.SetContentType("application/json")
	ctx.Request.SetBody(raw)
	ctx.SetUserValue("id", orderID)
	ctx.SetUserValue(testUserKey, userID)
	fn(&ctx)
	var out map[string]interface{}
	_ = json.Unmarshal(ctx.Response.Body(), &out)
	return ctx.Response.StatusCode(), out
}

func TestRatingIntentReturnsTheMemoToSign(t *testing.T) {
	e := newRatingEnv(t)
	status, out := call(t, e.h.RatingIntentOrder, "buyer", "o1", map[string]interface{}{"stars": 5, "comment": "fast delivery"})
	if status != 200 {
		t.Fatalf("intent: %d %v", status, out)
	}
	if out["memo"] != "5 fast delivery" || out["recipient"] != testShop || out["value_luna"] != float64(1) || out["fee_luna"] != float64(1) {
		t.Fatalf("intent body = %v", out)
	}
	if strings.Contains(out["memo"].(string), "nimiq") || strings.Contains(out["memo"].(string), "rating") {
		t.Fatal("memo must not carry a field name or the site name")
	}
}

func TestIntentRefusesBadInput(t *testing.T) {
	e := newRatingEnv(t)
	cases := []struct {
		name string
		body map[string]interface{}
		want int
	}{
		{"stars too high", map[string]interface{}{"stars": 6}, 400},
		{"stars zero", map[string]interface{}{"stars": 0}, 400},
		{"link in comment", map[string]interface{}{"stars": 5, "comment": "see https://x.io"}, 400},
		{"turkish letters", map[string]interface{}{"stars": 5, "comment": "çok iyi"}, 400},
		{"comment too long", map[string]interface{}{"stars": 5, "comment": strings.Repeat("a", ratings.MaxComment+1)}, 400},
	}
	for _, c := range cases {
		if status, out := call(t, e.h.RatingIntentOrder, "buyer", "o1", c.body); status != c.want {
			t.Errorf("%s: status %d (%v), want %d", c.name, status, out, c.want)
		}
	}
}

func TestRatingNotWiredIsUnavailable(t *testing.T) {
	e := newRatingEnv(t)
	e.h.RatingRPC = nil
	if status, _ := call(t, e.h.RateOrder, "buyer", "o1", map[string]interface{}{"stars": 5}); status != 503 {
		t.Fatalf("unwired rating: %d, want 503", status)
	}
}

func TestRatingSavedOnlyAfterTheBuyersOwnTransaction(t *testing.T) {
	e := newRatingEnv(t)
	memo := "5 fast delivery"
	const hash = "aa11bb22cc33dd44ee55ff66aa11bb22cc33dd44ee55ff66aa11bb22cc33dd44"
	e.chain.txs[hash] = tx(hash, testBuyer, testShop, 1, memo, true)

	status, out := call(t, e.h.RateOrder, "buyer", "o1", map[string]interface{}{
		"stars": 5, "comment": "fast delivery", "tx_hash": hash,
	})
	if status != 200 {
		t.Fatalf("valid rating: %d %v", status, out)
	}
	if out["rating_tx"] != hash || out["rating"] != float64(5) || out["comment"] != "fast delivery" {
		t.Fatalf("response = %v", out)
	}
	st, _ := e.store.GetRatingState(db.RatingKindOrder, "o1", "buyer")
	if !st.Rated || st.Stars != 5 || st.RatingTx != hash {
		t.Fatalf("stored state = %+v", st)
	}
}

func TestRatingRejectsTransactionsThatAreNotThisRating(t *testing.T) {
	const memo = "4 ok"
	cases := []struct {
		name string
		fix  func(d map[string]interface{})
	}{
		{"sent by someone else", func(d map[string]interface{}) { d["from"] = testOther }},
		{"paid to another address", func(d map[string]interface{}) { d["to"] = testOther }},
		{"wrong amount", func(d map[string]interface{}) { d["value"] = int64(2) }},
		{"memo differs", func(d map[string]interface{}) {
			d["recipientData"] = hex.EncodeToString([]byte("5 ok"))
		}},
		{"transaction failed", func(d map[string]interface{}) { d["executionResult"] = false }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newRatingEnv(t)
			const hash = "bb22cc33dd44ee55ff66aa11bb22cc33dd44ee55ff66aa11bb22cc33dd44ee55"
			d := tx(hash, testBuyer, testShop, 1, memo, true)
			c.fix(d)
			e.chain.txs[hash] = d
			status, out := call(t, e.h.RateOrder, "buyer", "o1", map[string]interface{}{
				"stars": 4, "comment": "ok", "tx_hash": hash,
			})
			if status != 400 {
				t.Fatalf("%s: %d %v, want 400", c.name, status, out)
			}
			if st, _ := e.store.GetRatingState(db.RatingKindOrder, "o1", "buyer"); st.Rated {
				t.Fatal("a rejected transaction still saved a rating")
			}
		})
	}
}

func TestUnconfirmedOrUnknownTransactionIsPending(t *testing.T) {
	e := newRatingEnv(t)
	const hash = "cc33dd44ee55ff66aa11bb22cc33dd44ee55ff66aa11bb22cc33dd44ee55ff66"
	// Not known to the chain yet.
	if status, out := call(t, e.h.RateOrder, "buyer", "o1", map[string]interface{}{"stars": 3, "tx_hash": hash}); status != 202 {
		t.Fatalf("unknown tx: %d %v, want 202", status, out)
	}
	// Known but still in the mempool.
	e.chain.txs[hash] = tx(hash, testBuyer, testShop, 1, "3", false)
	if status, out := call(t, e.h.RateOrder, "buyer", "o1", map[string]interface{}{"stars": 3, "tx_hash": hash}); status != 202 {
		t.Fatalf("mempool tx: %d %v, want 202", status, out)
	}
	if st, _ := e.store.GetRatingState(db.RatingKindOrder, "o1", "buyer"); st.Rated {
		t.Fatal("pending transaction saved a rating")
	}
}

func TestWithoutHashTheShopFindsTheBuyersTransaction(t *testing.T) {
	e := newRatingEnv(t)
	const mine = "dd44ee55ff66aa11bb22cc33dd44ee55ff66aa11bb22cc33dd44ee55ff66aa11"
	const unrelated = "ee55ff66aa11bb22cc33dd44ee55ff66aa11bb22cc33dd44ee55ff66aa11bb22"
	e.chain.history = []map[string]interface{}{
		tx(unrelated, testBuyer, testOther, 1, "5 wrong shop", true),
		tx(mine, testBuyer, testShop, 1, "5 great", true),
	}
	e.chain.txs[mine] = tx(mine, testBuyer, testShop, 1, "5 great", true)
	e.chain.txs[unrelated] = tx(unrelated, testBuyer, testOther, 1, "5 wrong shop", true)

	status, out := call(t, e.h.RateOrder, "buyer", "o1", map[string]interface{}{"stars": 5, "comment": "great"})
	if status != 200 {
		t.Fatalf("scan: %d %v", status, out)
	}
	if out["rating_tx"] != mine {
		t.Fatalf("scan picked %v, want %s", out["rating_tx"], mine)
	}
}

func TestOneTransactionProvesOneRating(t *testing.T) {
	e := newRatingEnv(t)
	const hash = "ff66aa11bb22cc33dd44ee55ff66aa11bb22cc33dd44ee55ff66aa11bb22cc33"
	e.chain.txs[hash] = tx(hash, testBuyer, testShop, 1, "5", true)
	if status, _ := call(t, e.h.RateOrder, "buyer", "o1", map[string]interface{}{"stars": 5, "tx_hash": hash}); status != 200 {
		t.Fatalf("first use: %d", status)
	}
	// The very same transaction offered for another purchase is refused.
	if status, out := call(t, e.h.RateOrder, "buyer", "o2", map[string]interface{}{"stars": 5, "tx_hash": hash}); status != 409 {
		t.Fatalf("replay on o2: %d %v, want 409", status, out)
	}
}

func TestRepeatedRatingNeedsNoSecondTransaction(t *testing.T) {
	e := newRatingEnv(t)
	const hash = "0a1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f9"
	e.chain.txs[hash] = tx(hash, testBuyer, testShop, 1, "5 nice", true)
	if status, _ := call(t, e.h.RateOrder, "buyer", "o1", map[string]interface{}{"stars": 5, "comment": "nice", "tx_hash": hash}); status != 200 {
		t.Fatalf("first: %d", status)
	}
	// Same stars and comment again: refused before any chain call or payment.
	if status, out := call(t, e.h.RatingIntentOrder, "buyer", "o1", map[string]interface{}{"stars": 5, "comment": "nice"}); status != 409 {
		t.Fatalf("intent repeat: %d %v, want 409", status, out)
	}
}

func TestOnlyTheOwnerCanRate(t *testing.T) {
	e := newRatingEnv(t)
	const hash = "1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f90a"
	e.chain.txs[hash] = tx(hash, testOther, testShop, 1, "5", true)
	if status, _ := call(t, e.h.RateOrder, "other", "o1", map[string]interface{}{"stars": 5, "tx_hash": hash}); status != 404 {
		t.Fatalf("non-owner rating: %d, want 404", status)
	}
}

func TestAdminCanHideCommentsAndSwitchThemOff(t *testing.T) {
	e := newRatingEnv(t)
	const hash = "2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f90a1b"
	e.chain.txs[hash] = tx(hash, testBuyer, testShop, 1, "5 bad words", true)
	if status, _ := call(t, e.h.RateOrder, "buyer", "o1", map[string]interface{}{"stars": 5, "comment": "bad words", "tx_hash": hash}); status != 200 {
		t.Fatalf("rate: %d", status)
	}

	// Hide the comment through the admin handler.
	var ctx fasthttp.RequestCtx
	ctx.Request.Header.SetMethod(http.MethodPost)
	ctx.Request.SetBody([]byte(`{"hidden":true}`))
	ctx.SetUserValue("kind", "order")
	ctx.SetUserValue("id", "o1")
	e.h.AdminHideRatingComment(&ctx)
	if ctx.Response.StatusCode() != 200 {
		t.Fatalf("hide: %d %s", ctx.Response.StatusCode(), ctx.Response.Body())
	}
	var pub fasthttp.RequestCtx
	e.h.RatingComments(&pub)
	if strings.Contains(string(pub.Response.Body()), "bad words") {
		t.Fatal("hidden comment still served publicly")
	}

	// Switch comments off: a new comment is refused, a stars-only rating still works.
	var off fasthttp.RequestCtx
	off.Request.Header.SetMethod(http.MethodPost)
	off.Request.SetBody([]byte(`{"enabled":false}`))
	e.h.AdminSetRatingComments(&off)
	if off.Response.StatusCode() != 200 {
		t.Fatalf("switch off: %d", off.Response.StatusCode())
	}
	if status, _ := call(t, e.h.RatingIntentOrder, "buyer", "o2", map[string]interface{}{"stars": 4, "comment": "words"}); status != 403 {
		t.Fatalf("comment while off: %d, want 403", status)
	}
	if status, _ := call(t, e.h.RatingIntentOrder, "buyer", "o2", map[string]interface{}{"stars": 4}); status != 200 {
		t.Fatalf("stars-only while off: %d, want 200", status)
	}
}
