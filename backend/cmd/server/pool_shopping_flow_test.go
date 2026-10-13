package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	cbworker "nimiqshop/internal/cashback/worker"
	"nimiqshop/internal/db"
	"nimiqshop/internal/nimiq"
)

// Only loopback traffic can leave this fixture. Market inputs, merchant
// Lightning settlement and gift-card fulfillment are explicit test doubles.
// Native validator rewards, pool API, shop storage and NIM payouts are real.
type shoppingOracleTransport struct{ local http.RoundTripper }

func (s shoppingOracleTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Hostname() == "127.0.0.1" || r.URL.Hostname() == "localhost" {
		return s.local.RoundTrip(r)
	}
	var body string
	switch r.URL.Hostname() {
	case "api.coingecko.com":
		body = `{"nimiq-2":{"usd":1},"bitcoin":{"usd":50000}}`
	case "api.coinpaprika.com":
		price := 1
		if strings.Contains(r.URL.Path, "btc-") {
			price = 50000
		}
		body = fmt.Sprintf(`{"quotes":{"USD":{"price":%d}}}`, price)
	default:
		return nil, fmt.Errorf("isolated shopping test blocks external host %s", r.URL.Hostname())
	}
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
}

func TestLivePoolShoppingCashback(t *testing.T) {
	path := os.Getenv("NIMSHOP_POOL_FLOW_FIXTURE")
	if path == "" {
		t.Skip("actual native earned fixture not configured")
	}
	var f struct {
		Scope      string `json:"scope"`
		RPC        string `json:"rpc_url"`
		Pool       string `json:"pool_url"`
		Key        string `json:"feed_key"`
		Owner      string `json:"pool_owner_address"`
		Buyer      string `json:"buyer_address"`
		BuyerSeed  string `json:"buyer_seed"`
		Payout     string `json:"payout_address"`
		PayoutSeed string `json:"payout_seed"`
		Fee        int64  `json:"pool_fee_luna"`
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(data, &f); err != nil {
		t.Fatal(err)
	}
	if f.Scope != "isolated-unit-albatross" || f.Fee <= 0 {
		t.Fatal("wrong actual earned fixture")
	}
	payoutSeed := make([]byte, 32)
	if _, err = rand.Read(payoutSeed); err != nil {
		t.Fatal(err)
	}
	f.PayoutSeed = hex.EncodeToString(payoutSeed)
	f.Payout, err = nimiq.AddressFromKeyHex(f.PayoutSeed)
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{f.RPC, f.Pool} {
		u, e := url.Parse(raw)
		if e != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" {
			t.Fatal("only isolated loopback fixtures allowed")
		}
	}
	http.DefaultTransport = shoppingOracleTransport{local: http.DefaultTransport}
	root := os.Getenv("NIMSHOP_FLOW_ROOT")
	output := os.Getenv("NIMSHOP_POOL_FLOW_OUTPUT")
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	origin := "http://" + listener.Addr().String()
	s := bootStack(t, stackOptions{listener: listener, env: map[string]string{"FRONTEND_URL": origin, "ALLOWED_ORIGINS": origin, "STATIC_DIR": filepath.Join(root, "dist"), "POOL_API_URL": f.Pool, "POOL_FEED_API_KEY": f.Key, "POOL_VALIDATOR_ADDRESS": f.Owner, "NIMIQ_RPC_URL": f.RPC, "NIMIQ_RPC_URL_2": "", "CASHBACK_WALLET_SEED": f.PayoutSeed, "CASHBACK_ENABLED": "false", "BADGER_SYNC_WRITES": "true", "ORACLE_MIN_SOURCES": "2", "POOL_STAKE_CACHE_TTL_SECONDS": "0"}})
	s.h.NIMUSDPrice = func(context.Context) (float64, error) { return 1, nil }
	s.h.Pool.SetFeedKey(f.Key)
	ratesCtx, cancelRates := context.WithCancel(t.Context())
	defer cancelRates()
	s.h.StartRatesRefresher(ratesCtx)
	seed, err := hex.DecodeString(f.BuyerSeed)
	if err != nil || len(seed) != 32 {
		t.Fatal("bad fresh buyer key")
	}
	priv := ed25519.NewKeyFromSeed(seed)
	ch := s.do("POST", "/api/auth/challenge", map[string]any{}, nil).json(t)
	message := ch["message"].(string)
	digest := sha256.Sum256([]byte(fmt.Sprintf("\x16Nimiq Signed Message:\n%d%s", len(message), message)))
	login := s.do("POST", "/api/auth/hub-login", map[string]any{"challenge_token": ch["challenge_token"], "address": f.Buyer, "public_key": hex.EncodeToString(priv.Public().(ed25519.PublicKey)), "signature": hex.EncodeToString(ed25519.Sign(priv, digest[:]))}, nil)
	if login.status != 200 {
		t.Fatalf("actual login %d %s", login.status, login.body)
	}
	parsed := (&http.Response{Header: login.headers}).Cookies()
	pieces := make([]string, 0, len(parsed))
	cookies := make([]map[string]any, 0, len(parsed))
	csrf := ""
	for _, c := range parsed {
		pieces = append(pieces, c.Name+"="+c.Value)
		cookies = append(cookies, map[string]any{"name": c.Name, "value": c.Value, "url": s.baseURL, "httpOnly": c.HttpOnly, "sameSite": "Lax"})
		if c.Name == "nimshop_csrf" {
			csrf = c.Value
		}
	}
	auth := map[string]string{"Cookie": strings.Join(pieces, "; "), "Origin": s.baseURL, "X-CSRF-Token": csrf}
	user, err := s.h.Store.GetUserByAddress(f.Buyer)
	if err != nil {
		t.Fatal(err)
	}
	check := func(condition bool, message string) {
		t.Helper()
		if !condition {
			t.Fatal(message)
		}
	}
	status := s.do("GET", "/api/poolstake/me", nil, auth)
	check(status.status == 200, "actual earned stake API failed")
	initialState := status.json(t)
	check(initialState["staked"] == true && initialState["stake_luna"] == float64(10000000) && initialState["cashback_bps"] == float64(1100), "actual stake and earned boost were not reflected in the shop rate")
	t.Logf("actual earned stake response: %s", status.body)
	ledger, ok, err := s.h.Store.ReadStakeLedger(f.Buyer)
	if err != nil || !ok || ledger.ProfitLuna != f.Fee || ledger.A <= 0 {
		t.Fatalf("earned ledger %+v %v", ledger, err)
	}
	initialCredit := ledger.A
	for i := 0; i < 3; i++ {
		s.do("GET", "/api/poolstake/me", nil, auth)
	}
	again, _, _ := s.h.Store.ReadStakeLedger(f.Buyer)
	check(again.A == initialCredit, "same earned profit was credited again")
	s.h.Pool.SetFeedKey("incorrect-fixture-key")
	s.do("GET", "/api/poolstake/me", nil, auth)
	preserved, _, _ := s.h.Store.ReadStakeLedger(f.Buyer)
	check(preserved.A == initialCredit, "pool feed authentication failure erased earned credit")
	s.h.Pool.SetFeedKey(f.Key)
	rpc := nimiq.NewClient(f.RPC)
	rpcCall := func(method string, params []any, target any) {
		t.Helper()
		body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
		r, e := http.Post(f.RPC, "application/json", bytes.NewReader(body))
		if e != nil {
			t.Fatal(e)
		}
		defer func() { _ = r.Body.Close() }()
		var result struct {
			Result json.RawMessage `json:"result"`
			Error  json.RawMessage `json:"error"`
		}
		if e = json.NewDecoder(r.Body).Decode(&result); e != nil {
			t.Fatal(e)
		}
		if len(result.Error) > 0 && string(result.Error) != "null" {
			t.Fatalf("native RPC %s: %s", method, result.Error)
		}
		var wrapper struct {
			Data json.RawMessage `json:"data"`
		}
		_ = json.Unmarshal(result.Result, &wrapper)
		if len(wrapper.Data) > 0 {
			result.Result = wrapper.Data
		}
		if target != nil {
			if e = json.Unmarshal(result.Result, target); e != nil {
				t.Fatal(e)
			}
		}
	}
	wait := func(label string, fn func() bool) {
		t.Helper()
		deadline := time.Now().Add(100 * time.Second)
		for !fn() {
			if time.Now().After(deadline) {
				t.Fatal("timeout: " + label)
			}
			time.Sleep(500 * time.Millisecond)
		}
	}
	receipt := func(hash string) {
		wait("macro-final "+hash, func() bool {
			found, confirmed, e := rpc.GetTransactionByHash(t.Context(), hash)
			return e == nil && found && confirmed
		})
	}
	balance := func(address string) int64 {
		value, e := rpc.GetAccountBalance(t.Context(), address)
		if e != nil {
			t.Fatal(e)
		}
		return value
	}
	capture := func(label string, expectedPending, expectedPaid int) {
		t.Helper()
		state := s.do("GET", "/api/poolstake/me", nil, auth)
		config := map[string]any{"base": s.baseURL, "cookies": cookies, "address": f.Buyer, "uid": user.ID, "label": label, "pending": expectedPending, "paid": expectedPaid, "state": state.json(t), "output": output, "executable": os.Getenv("NIMSHOP_BROWSER_EXECUTABLE")}
		b, _ := json.Marshal(config)
		file := filepath.Join(t.TempDir(), "browser.json")
		if e := os.WriteFile(file, b, 0600); e != nil {
			t.Fatal(e)
		}
		cmd := exec.CommandContext(t.Context(), "node", filepath.Join(root, "scripts/integration/pool-shopping-browser.mjs"), file)
		cmd.Dir = root
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if e := cmd.Run(); e != nil {
			t.Fatal("actual shop browser: ", e)
		}
	}
	capture("earned-before-shopping", 0, 0)
	spending := make([]string, 0, 3)
	for index, quantity := range []int{1, 2, 1} {
		dir := t.TempDir()
		result := filepath.Join(dir, "quote-id")
		browserConfig := map[string]any{"base": s.baseURL, "cookies": cookies, "address": f.Buyer, "uid": user.ID, "checkout": true, "quantity": quantity, "result": result, "label": fmt.Sprintf("purchase-%d", index+1), "output": output, "executable": os.Getenv("NIMSHOP_BROWSER_EXECUTABLE")}
		browserData, e := json.Marshal(browserConfig)
		if e != nil {
			t.Fatal(e)
		}
		browserFile := filepath.Join(dir, "checkout.json")
		if e = os.WriteFile(browserFile, browserData, 0600); e != nil {
			t.Fatal(e)
		}
		command := exec.CommandContext(t.Context(), "node", filepath.Join(root, "scripts/integration/pool-shopping-browser.mjs"), browserFile)
		command.Dir = root
		command.Stdout = os.Stdout
		command.Stderr = os.Stderr
		if e = command.Run(); e != nil {
			t.Fatal("actual browser checkout", e)
		}
		quoteID, e := os.ReadFile(result)
		if e != nil {
			t.Fatal(e)
		}
		id := string(quoteID)
		q, e := s.h.Store.GetQuote(id)
		if e != nil {
			t.Fatal(e)
		}
		check(!q.TestMode && q.Status == "awaiting_payment", "purchase bypassed real settlement states")
		rows, _ := s.h.Store.ListCashbacksByUser(user.ID, 0)
		check(len(rows) == index, "cashback created before merchant settlement")
		amount := int64(math.Round(q.EstimatedNIM * 1.05 * 100000))
		check(amount > 0, "missing locked purchase NIM amount")
		var hash string
		rpcCall("sendBasicTransaction", []any{f.Buyer, f.Owner, amount, 1, "+0"}, &hash)
		receipt(hash)
		spending = append(spending, hash)
		proof := s.do("POST", "/api/quotes/"+id+"/lightning-proof", map[string]any{"hash": hash, "swap_id": "isolated-swap-" + id}, auth)
		check(proof.status == 200, "submission metadata not persisted")
		current, _ := s.h.Store.GetQuote(id)
		check(current.Status == "awaiting_payment", "submission was incorrectly treated as Lightning settlement")
		check(current.LightningFundingHint == hash && current.LightningSwapID == "isolated-swap-"+id, "owner submission identifiers were lost")
		public := s.do("GET", "/api/track/"+id, nil, nil)
		check(!bytes.Contains(public.body, []byte(current.LightningSwapID)), "private swap identifier leaked to public activity")
		for _, action := range []string{"pay", "advance"} {
			r, e := http.Post(s.supplier.URL+"/mock/orders/"+q.SupplierOrderID+"/"+action, "application/json", strings.NewReader("{}"))
			if e != nil {
				t.Fatal(e)
			}
			_ = r.Body.Close()
			check(r.StatusCode == http.StatusOK, "supplier fixture failed "+action)
		}
		wait("actual fulfillment", func() bool {
			s.do("POST", "/api/quotes/"+id+"/refresh", nil, auth)
			got, e := s.h.Store.GetQuote(id)
			return e == nil && got.Status == "fulfilled"
		})
		for i := 0; i < 3; i++ {
			s.do("POST", "/api/quotes/"+id+"/refresh", nil, auth)
		}
		delivered, deliveryErr := s.h.Store.GetQuote(id)
		check(deliveryErr == nil && delivered.PaidAt != nil && delivered.DeliveredAt != nil && !delivered.DeliveredAt.Before(*delivered.PaidAt), "delivery duration is missing or negative")
		rows, _ = s.h.Store.ListCashbacksByUser(user.ID, 0)
		check(len(rows) == index+1, "fulfillment replay duplicated cashback")
		for _, row := range rows {
			check(row.Status == db.CashbackQueued && row.PaidAt == nil && row.TxHash == "" && !row.TestMode, "unpaid cashback mislabeled paid")
		}
		capture(fmt.Sprintf("after-purchase-%d", index+1), index+1, 0)
	}
	before := balance(f.Buyer)
	check(balance(f.Payout) == 0, "test payout wallet must begin empty")
	var stopWorker context.CancelFunc
	startWorker := func() {
		ctx, cancel := context.WithCancel(t.Context())
		stopWorker = cancel
		w := &cbworker.Worker{Store: cbworker.WithQuizPayouts(s.h.Store), RPC: rpc, SeedHex: f.PayoutSeed, NetworkID: 7, Enabled: true, FeeLuna: 1, Interval: time.Second}
		w.Run(ctx)
	}
	startWorker()
	defer func() { stopWorker() }()
	time.Sleep(3 * time.Second)
	unpaid, _ := s.h.Store.ListCashbacksByUser(user.ID, 0)
	for _, row := range unpaid {
		check(row.Status == db.CashbackQueued && row.PaidAt == nil, "empty wallet lost pending obligation")
	}
	capture("empty-wallet-pending", 3, 0)
	var funding string
	rpcCall("sendBasicTransaction", []any{f.Owner, f.Payout, 20 * 100000, 1, "+0"}, &funding)
	receipt(funding)
	wait("all cashback paid", func() bool {
		rows, _ := s.h.Store.ListCashbacksByUser(user.ID, 0)
		if len(rows) != 3 {
			return false
		}
		for _, r := range rows {
			if r.Status != db.CashbackPaid {
				return false
			}
		}
		return true
	})
	rows, err := s.h.Store.ListCashbacksByUser(user.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	var total, boost int64
	for _, row := range rows {
		check(row.PaidAt != nil && nimiq.IsTxHash(row.TxHash) && !seen[row.TxHash], "missing or duplicate paid receipt")
		seen[row.TxHash] = true
		detail, found, e := rpc.GetTransactionDetail(t.Context(), row.TxHash)
		if e != nil || !found || !detail.Success || detail.To != nimiq.NormalizeAddress(f.Buyer) || detail.From != nimiq.NormalizeAddress(f.Payout) || detail.Value != row.AmountLuna {
			t.Fatalf("actual cashback receipt %+v %v", detail, e)
		}
		total += row.AmountLuna
		boost += row.BoostLuna
		check(row.PaidBaseNIM == row.ProductNIM, "cashback basis differs from quoted product price")
		check(row.Memo != "" && bytes.Equal(detail.Data, []byte(row.Memo)), "cashback payment memo was not preserved on chain")
	}
	check(balance(f.Buyer)-before == total, "cashback wallet delta differs from receipts")
	for i := 0; i < 5; i++ {
		stopWorker()
		startWorker()
		time.Sleep(1100 * time.Millisecond)
	}
	check(balance(f.Buyer)-before == total, "worker restart paid twice")
	for i := 0; i < 3; i++ {
		s.do("GET", "/api/poolstake/me", nil, auth)
	}
	final, _, _ := s.h.Store.ReadStakeLedger(f.Buyer)
	check(final.ProfitLuna == f.Fee && final.A <= 0.00001, "consumed earnings were recredited")
	check(math.Abs(float64(boost)/100000-initialCredit) < 0.00002, "boost does not equal consumed earned credit")
	capture("confirmed-paid-after-restarts", 0, 3)
	report := map[string]any{"scope": f.Scope, "actual_pool_fee_luna": f.Fee, "earned_credit_nim": initialCredit, "final_credit_nim": final.A, "cashback_paid_luna": total, "boost_paid_luna": boost, "recipient_gain_luna": balance(f.Buyer) - before, "purchase_hashes": spending, "payout_hashes": seen, "funding_hash": funding, "worker_restarts": 5, "doubles": []string{"fixed market inputs", "merchant Lightning settlement", "gift-card fulfillment", "local basic NIM spend replaces the wallet atomic swap"}, "purchase_entry": "actual cart and checkout browser UI"}
	b, _ := json.MarshalIndent(report, "", "  ")
	if err = os.WriteFile(filepath.Join(output, "verified.json"), b, 0600); err != nil {
		t.Fatal(err)
	}
	t.Logf("SHOP FLOW VERIFIED: native earned fee=%d cashback=%d boost=%d recipient_gain=%d restarts=5", f.Fee, total, boost, balance(f.Buyer)-before)
}
