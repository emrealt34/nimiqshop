// Package crashtest is the "horror" suite for the Cryptorefills
// rail: it drives the REAL server binary against a faithful CryptoRefills
// mock and tries to kill the backend in every way that is NOT an ordinary
// validation error:
//
//   - garbage/binary/malformed HTTP at the socket level (C01)
//   - a concurrent firehose over every endpoint (C02)
//   - supplier-queue exhaustion with hundreds of parallel order calls (C03)
//   - SIGKILL in the middle of order creation, then restart (C04)
//   - webhook flood with garbage and duplicate deliveries (C05)
//   - corrupted BadgerDB data files (C06)
//   - repeated SIGKILL restart storm on a live dataset (C07)
//   - the happy path end-to-end with crash-safe fulfillment (C08)
//
// The invariant checked after EVERY phase: the server process must still be
// the same living PID, and /api/health must still answer. A panic anywhere
// (goroutine crash) fails the test with the server log dump.
//
// Run one crash scenario per process (recommended):
//
//	./run-crashtests.sh
//
// The runner builds the server/mockstack/test binaries ONCE and then runs
// all scenarios IN PARALLEL (each scenario uses a fresh temp dir and its
// own free ports, so they are independent) — wall time is the slowest
// scenario, not the sum. Set CRASH_SERIAL=1 for one-at-a-time execution.
//
// Each scenario has its own hard timeout, fresh server and fresh BadgerDB.
// Do not run this package behind one blanket 30-minute timeout: it conceals
// which scenario stalled and leaves an unnecessarily large failure window.
package crashtest

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"nimiqshop/internal/auth"
	"nimiqshop/internal/nimiq"
)

const testEmail = "hasan1altinok@gmail.com"

/* ------------------------------- harness --------------------------------- */

type crashStack struct {
	workDir    string
	badgerDir  string
	mockCmd    *exec.Cmd
	serverCmd  *exec.Cmd
	mockBin    string
	serverBin  string
	baseURL    string
	crBase     string
	jwtSecret  string
	webhookKey string
	userIDs    []string
}

var shared *crashStack
var sharedMu sync.Mutex

func TestMain(m *testing.M) {
	code := m.Run()
	sharedMu.Lock()
	if shared != nil {
		if shared.serverCmd != nil && shared.serverCmd.Process != nil {
			_ = shared.serverCmd.Process.Kill()
		}
		if shared.mockCmd != nil && shared.mockCmd.Process != nil {
			_ = shared.mockCmd.Process.Kill()
		}
	}
	sharedMu.Unlock()
	os.Exit(code)
}

func once() *crashStack {
	sharedMu.Lock()
	defer sharedMu.Unlock()
	if shared != nil {
		return shared
	}
	shared = buildStack()
	return shared
}

func buildStack() *crashStack {
	st := &crashStack{}

	work, err := os.MkdirTemp("", "nimshop-crash-*")
	if err != nil {
		panic(err)
	}
	st.workDir = work
	st.badgerDir = filepath.Join(work, "badger")

	build := func(name, pkg string) string {
		bin := filepath.Join(work, name)
		cmd := exec.Command("go", "build", "-o", bin, pkg)
		cmd.Dir = ".."
		out, err := cmd.CombinedOutput()
		if err != nil {
			panic(fmt.Sprintf("build %s: %v\n%s", name, err, out))
		}
		return bin
	}
	// Fast mode: run-crashtests.sh builds every binary ONCE and hands the
	// paths in via the environment, so parallel scenarios skip per-case
	// rebuilds entirely.
	if p := strings.TrimSpace(os.Getenv("CRASH_MOCK_BIN")); p != "" {
		if _, err := os.Stat(p); err != nil {
			panic("CRASH_MOCK_BIN not usable: " + err.Error())
		}
		st.mockBin = p
	} else {
		st.mockBin = build("mockstack", "./cmd/mockstack")
	}
	if p := strings.TrimSpace(os.Getenv("CRASH_SERVER_BIN")); p != "" {
		if _, err := os.Stat(p); err != nil {
			panic("CRASH_SERVER_BIN not usable: " + err.Error())
		}
		st.serverBin = p
	} else {
		st.serverBin = build("nimshop-server", "./cmd/server")
	}

	freePort := func() int {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			panic(err)
		}
		p := l.Addr().(*net.TCPAddr).Port
		l.Close()
		return p
	}
	crPort, serverPort := freePort(), freePort()
	st.crBase = fmt.Sprintf("http://127.0.0.1:%d", crPort)
	st.baseURL = fmt.Sprintf("http://127.0.0.1:%d", serverPort)

	st.jwtSecret = "crashtest-jwt-secret-0123456789abcdef0123456789ab"
	st.webhookKey = "crashtest-webhook-key-0123456789abcdef0123456789"

	for i := 0; i < 40; i++ {
		seed := make([]byte, 32)
		if _, err := rand.Read(seed); err != nil {
			panic(err)
		}
		pub := ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey)
		addr, err := nimiq.AddressFromPublicKey(pub)
		if err != nil {
			panic(err)
		}
		st.userIDs = append(st.userIDs, addr)
	}

	mockLog, _ := os.Create(filepath.Join(work, "mockstack.log"))
	st.mockCmd = exec.Command(st.mockBin)
	st.mockCmd.Env = append(os.Environ(), fmt.Sprintf("MOCK_CR_PORT=%d", crPort))
	st.mockCmd.Stdout = mockLog
	st.mockCmd.Stderr = mockLog
	if err := st.mockCmd.Start(); err != nil {
		panic(err)
	}

	st.startServer()
	st.waitHTTP(st.baseURL+"/api/health", 30*time.Second)
	st.waitHTTP(st.crBase+"/v2/ping", 30*time.Second)
	return st
}

func (st *crashStack) serverEnv() []string {
	return []string{
		"PATH=" + os.Getenv("PATH"),
		fmt.Sprintf("LISTEN_ADDR=127.0.0.1:%d", portOf(st.baseURL)),
		"BADGER_DIR=" + st.badgerDir,
		"JWT_SECRET=" + st.jwtSecret,
		"ALLOW_HTTP_LOCAL=true",
		"ORACLE_MIN_SOURCES=2",
		"ORACLE_MAX_SPREAD_BPS=800",
		"DAILY_ORDER_LIMIT=1000000",
		"DAILY_SPEND_LIMIT_USD=100000000",
		fmt.Sprintf("CRYPTOREFILLS_BASE_URL=%s", st.crBase),
		"CRYPTOREFILLS_PARTNER_ID=crashtest-partner",
		"CRYPTOREFILLS_WEBHOOK_KEY=" + st.webhookKey,
		"PUBLIC_WEBHOOK_BASE_URL=" + st.baseURL,
		// Tight queue bounds so exhaustion is reachable fast.
		"CRYPTOREFILLS_QUEUE_MAX=200",
		"CRYPTOREFILLS_QUEUE_PER_ACTOR_MAX=15",
		"CRYPTOREFILLS_ACTOR_REQUESTS_PER_MINUTE=60",
		"CRYPTOREFILLS_ACTOR_BURST=15",
		"WORKER_ORDER_POLL_SECS=2",
		// Must exceed the 20s supplier call timeout (config enforces >=25):
		// the stale clock runs from the durable request-start marker, and an
		// in-flight creation must never be flagged stale while it can still
		// legitimately complete. 30s keeps C04's restart window fast.
		"WORKER_ORDER_STALE_SECS=30",
		"TEST_MODE=true",
		"MAX_REQUEST_BODY_BYTES=1048576",
		"RATE_LIMIT_PER_MINUTE=6000",
		"RATE_LIMIT_BURST=2000",
		"TRUST_PROXY=false",
	}
}

func portOf(base string) int {
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(base, "http://"))
	var p int
	fmt.Sscanf(port, "%d", &p)
	return p
}

func (st *crashStack) startServer() {
	logFile, err := os.OpenFile(filepath.Join(st.workDir, "server.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		panic(err)
	}
	cmd := exec.Command(st.serverBin)
	cmd.Dir = st.workDir
	cmd.Env = st.serverEnv()
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	if err := cmd.Start(); err != nil {
		panic(err)
	}
	st.serverCmd = cmd
}

func (st *crashStack) killServer() {
	if st.serverCmd != nil && st.serverCmd.Process != nil {
		_ = st.serverCmd.Process.Kill()
		// Cmd.Wait (not Process.Wait): only Cmd.Wait populates
		// st.serverCmd.ProcessState, which serverAlive() relies on to prove
		// the process is gone. Process.Wait reaps the OS process but leaves
		// ProcessState nil, so the "server survived SIGKILL?" check would
		// always fire even though the process is dead.
		_ = st.serverCmd.Wait()
	}
}

func (st *crashStack) restartServer() {
	st.killServer()
	time.Sleep(300 * time.Millisecond)
	st.startServer()
	st.waitHTTP(st.baseURL+"/api/health", 60*time.Second)
}

func (st *crashStack) serverAlive() bool {
	if st.serverCmd == nil || st.serverCmd.Process == nil {
		return false
	}
	return st.serverCmd.ProcessState == nil
}

func (st *crashStack) assertAlive(t *testing.T, phase string) {
	t.Helper()
	if !st.serverAlive() {
		st.dumpLogs(phase + ": SERVER PROCESS DIED (panic or abort escaped)")
		t.Fatalf("server process died during %s", phase)
	}
	resp, err := http.Get(st.baseURL + "/api/health")
	if err != nil {
		st.dumpLogs(phase + ": health check failed")
		t.Fatalf("health check failed after %s: %v", phase, err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !bytes.Contains(body, []byte(`"ok":true`)) {
		st.dumpLogs(phase + ": health returned bad state")
		t.Fatalf("health not ok after %s: %d %s", phase, resp.StatusCode, body)
	}
}

func (st *crashStack) assertNoPanicInLog(t *testing.T, phase string) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(st.workDir, "server.log"))
	if err != nil {
		return
	}
	if idx := bytes.Index(b, []byte("panic:")); idx != -1 {
		tail := string(b)
		if len(tail) > 8000 {
			tail = tail[len(tail)-8000:]
		}
		t.Fatalf("server log contains a Go panic during %s:\n%s", phase, tail)
	}
}

func (st *crashStack) waitHTTP(url string, timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := http.Get(url)
		if err == nil {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode < 500 {
				_ = body
				return
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	st.dumpLogs("waitHTTP timeout for " + url)
	panic("waitHTTP timeout: " + url)
}

func (st *crashStack) dumpLogs(why string) {
	b, _ := os.ReadFile(filepath.Join(st.workDir, "server.log"))
	tail := string(b)
	if len(tail) > 12000 {
		tail = tail[len(tail)-12000:]
	}
	fmt.Printf("\n===== server log (%s) =====\n%s\n===== end log =====\n", why, tail)
}

/* ------------------------------- http helpers ---------------------------- */

func (st *crashStack) token(userID string) string {
	tok, err := auth.IssueToken(st.jwtSecret, userID, 60)
	if err != nil {
		panic(err)
	}
	return tok
}

func (st *crashStack) api(method, path, userID, body string) (int, string) {
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, st.baseURL+path, rdr)
	if err != nil {
		return 0, err.Error()
	}
	req.Header.Set("Content-Type", "application/json")
	if userID != "" {
		req.Header.Set("Authorization", "Bearer "+st.token(userID))
	}
	// Crash tests must never turn a server-side stall into a permanently
	// blocked test goroutine. In particular C02 intentionally saturates the
	// supplier queue; every probe needs a finite deadline.
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return 0, err.Error()
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(raw)
}

func (st *crashStack) apiCode(method, path, userID, body string) int {
	code, _ := st.api(method, path, userID, body)
	return code
}

func (st *crashStack) apiRaw(method, path string, headers map[string]string, body []byte, timeout time.Duration) (int, string) {
	req, err := http.NewRequest(method, st.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return 0, err.Error()
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err.Error()
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(raw)
}

func (st *crashStack) postControl(base, path, body string) map[string]interface{} {
	// Control-plane calls participate in fault injection; give them a deadline
	// too, otherwise a failed scenario can hang while trying to reset the mock.
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Post(base+path, "application/json", strings.NewReader(body))
	if err != nil {
		return map[string]interface{}{"error": err.Error()}
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var out map[string]interface{}
	_ = json.Unmarshal(raw, &out)
	return out
}

func (st *crashStack) crState() map[string]interface{} {
	return st.postControl(st.crBase, "/mock/state", "")
}

func (st *crashStack) crFault(faultJSON string) {
	st.postControl(st.crBase, "/mock/fault", faultJSON)
}

func (st *crashStack) crReset() {
	st.postControl(st.crBase, "/mock/reset", "{}")
}

// enableWebhooks points the mock's webhook delivery at this server.
func (st *crashStack) enableWebhooks() {
	st.crFault(fmt.Sprintf(`{"webhook_base_url":%q,"webhook_key":%q}`, st.baseURL, st.webhookKey))
}

func (st *crashStack) orderCount() int {
	s := st.crState()
	if n, ok := s["order_count"].(float64); ok {
		return int(n)
	}
	return 0
}

func (st *crashStack) waitFor(desc string, timeout time.Duration, cond func() bool) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
	st.dumpLogs(desc)
	panic("timeout waiting for: " + desc)
}

func (st *crashStack) queueStats() (queued int) {
	_, body := st.api("GET", "/api/health", "", "")
	var out struct {
		CRQueue struct {
			Queued int `json:"queued"`
		} `json:"cr_queue"`
	}
	_ = json.Unmarshal([]byte(body), &out)
	return out.CRQueue.Queued
}

// createQuote posts a quote for a test product; returns (quoteID, wallet, status).
func (st *crashStack) createQuote(t *testing.T, userID, idemKey string, timeout time.Duration) (string, string, int) {
	t.Helper()
	body := fmt.Sprintf(`{"product_id":"test-airbnb","country":"US","denomination":"range","product_value":10,"quantity":1,"email":%q,"coin":"USDT"}`, testEmail)
	code, raw := st.quoteRequest(userID, body, idemKey, timeout)
	var out map[string]interface{}
	_ = json.Unmarshal([]byte(raw), &out)
	quoteID, _ := out["quote_id"].(string)
	wallet, _ := out["wallet_address"].(string)
	return quoteID, wallet, code
}

func (st *crashStack) quoteRequest(userID, body, idemKey string, timeout time.Duration) (int, string) {
	req, _ := http.NewRequest("POST", st.baseURL+"/api/quotes", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+st.token(userID))
	if idemKey != "" {
		req.Header.Set("Idempotency-Key", idemKey)
	}
	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err.Error()
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(raw)
}

/* ------------------------------ C01 garbage -------------------------------- */

func TestC01_GarbageStorm(t *testing.T) {
	st := once()

	rawGarbage := [][]byte{
		bytes.Repeat([]byte{0x00, 0xFF, 0x01, 0xFE, 0x80, 0x7F}, 20000),
		[]byte("GARBAGE\r\n\r\n"),
		[]byte("GET HTTP/9.9\r\nHost: nowhere\r\n\r\n"),
		[]byte("GET / HTTP/1.1\r\nHost: 127.0.0.1\r\nContent-Length: 99999999\r\n\r\n"),
		[]byte("GET /\x00api HTTP/1.1\r\nHost: 127.0.0.1\r\n\r\n"),
		[]byte("POST /api/quotes HTTP/1.1\r\nHost: 127.0.0.1\r\nTransfer-Encoding: chunked\r\n\r\nZZZZ\r\n\r\n"),
		[]byte("GET /api/health HTTP/1.1\r\n" + strings.Repeat("X-Big-Header: "+strings.Repeat("a", 9000)+"\r\n", 10) + "Host: 127.0.0.1\r\n\r\n"),
		[]byte("\r\n\r\n"),
		[]byte("FOO /api/health HTTP/1.1\r\nHost: 127.0.0.1\r\n\r\n"),
		[]byte("GET / HTTP/1.1\r\nHost: 127.0.0.1\r\nAuthorization: \xff\xfe\x00bad\r\n\r\n"),
	}
	for i, g := range rawGarbage {
		conn, err := net.Dial("tcp", strings.TrimPrefix(st.baseURL, "http://"))
		if err != nil {
			t.Fatalf("dial for garbage %d: %v", i, err)
		}
		_, _ = conn.Write(g)
		time.Sleep(50 * time.Millisecond)
		_ = conn.Close()
	}
	st.assertAlive(t, "C01 raw garbage")

	postPaths := []string{
		"/api/auth/challenge", "/api/auth/hub-login", "/api/quotes",
		"/api/quotes/deadbeef-0000-0000-0000-000000000000/rate",
		"/api/support/tickets", "/api/support/tickets/deadbeef/messages",
		"/api/webhooks/cryptorefills", "/api/presence", "/api/test/buy",
		"/api/admin/auth/login", "/api/admin/auth/bootstrap",
	}
	badBodies := []string{
		"{", "}", "[1,2", `"str"`, "123", "null", "true",
		`{"product_id":}`, `{"product_id":123}`, `{"product_id":null}`,
		`{"product_id":{"a":1}}`, string([]byte{0xC3, 0x28}),
		strings.Repeat("a", 1<<20+5),
		`{"product_id":"test-airbnb","country":"US","denomination":"range","product_value":-5}`,
		`{"product_id":"test-airbnb","country":"US","denomination":"range","product_value":1e308}`,
	}
	authHeaders := []map[string]string{
		nil,
		{"Authorization": "Bearer garbage.token.here"},
		{"Authorization": "Bearer {"},
		{"Authorization": "Basic " + strings.Repeat("x", 5000)},
	}
	count := 0
	for _, p := range postPaths {
		for _, b := range badBodies {
			for _, h := range authHeaders {
				hh := map[string]string{"Content-Type": "application/json"}
				for k, v := range h {
					hh[k] = v
				}
				_, _ = st.apiRaw("POST", p, hh, []byte(b), 5*time.Second)
				count++
			}
		}
	}
	getHostile := []string{
		"/api/health/../../etc/passwd",
		"/api/%00",
		"/api/catalog/products/..%2f..%2f..%2fsecret?country=US",
		"/api/track/" + strings.Repeat("A", 8000),
		"/api/catalog/search?q=" + strings.Repeat("x", 5000),
		"/api/catalog/price?brand=" + strings.Repeat("b", 2000) + "&country=US&coin=USDT&face_value=10",
		"/api/orders?limit=" + strings.Repeat("9", 30),
		"/api/catalog/brands?country=XXXX",
	}
	for _, p := range getHostile {
		_, _ = st.apiRaw("GET", p, nil, nil, 5*time.Second)
		count++
	}
	for _, m := range []string{"DELETE", "PATCH", "PUT", "TRACE", "OPTIONS"} {
		_, _ = st.apiRaw(m, "/api/health", nil, nil, 3*time.Second)
		_, _ = st.apiRaw(m, "/api/quotes", map[string]string{"Authorization": "Bearer " + st.token(st.userIDs[0])}, []byte(`{`), 3*time.Second)
		count++
	}
	t.Logf("C01: sent %d malformed requests", count)
	st.assertAlive(t, "C01 storm")
	st.assertNoPanicInLog(t, "C01")
}

/* --------------------------- C02 concurrent firehose ---------------------- */

func TestC02_ConcurrentFirehose(t *testing.T) {
	st := once()
	st.crReset()
	st.crFault(`{}`)

	var wg sync.WaitGroup
	var okCount, errCount int64
	worker := func(wid int) {
		defer wg.Done()
		uid := st.userIDs[wid%len(st.userIDs)]
		for i := 0; i < 12; i++ {
			switch (wid + i) % 10 {
			case 0:
				st.apiCode("GET", "/api/catalog/brands?country=US", "", "")
			case 1:
				st.apiCode("GET", "/api/catalog/products?country=US&family=test-airbnb&coin=USDT", "", "")
			case 2:
				st.apiCode("GET", "/api/catalog/price?brand=test-airbnb&country=US&coin=USDT&face_value=20", "", "")
			case 3:
				st.apiCode("GET", "/api/catalog/payment-vias", "", "")
			case 4:
				st.apiCode("GET", "/api/geo", "", "")
			case 5:
				st.apiCode("GET", "/api/market/nim-rate", "", "")
			case 6:
				st.apiCode("GET", "/api/activity", "", "")
				st.apiCode("GET", "/api/ratings/summary", "", "")
			case 7:
				st.apiCode("GET", "/api/track/deadbeef-0000-0000-0000-000000000000", "", "")
				st.apiCode("GET", "/api/orders/deadbeef-0000-0000-0000-000000000000", "", "")
			case 8:
				st.apiRaw("POST", "/api/webhooks/cryptorefills", nil,
					[]byte(fmt.Sprintf(`{"order_id":"ord_%d%06d","status":"Done"}`, wid, i)), 5*time.Second)
			case 9:
				code, _ := st.quoteRequest(uid,
					fmt.Sprintf(`{"product_id":"test-airbnb","country":"US","denomination":"range","product_value":2,"quantity":1,"email":%q,"coin":"USDT"}`, testEmail),
					fmt.Sprintf("firehose-%d-%d-0123456789ab", wid, i), 20*time.Second)
				if code == 200 || code == 201 {
					atomic.AddInt64(&okCount, 1)
				}
			}
		}
		resp, err := http.Get(st.baseURL + "/api/health")
		if err == nil {
			resp.Body.Close()
		} else {
			atomic.AddInt64(&errCount, 1)
		}
	}
	for w := 0; w < 100; w++ {
		wg.Add(1)
		go worker(w)
	}
	wg.Wait()
	t.Logf("C02: quotes created under firehose=%d, connection errors=%d", okCount, errCount)
	if errCount > 5 {
		t.Fatalf("too many connection errors under firehose: %d", errCount)
	}
	st.assertAlive(t, "C02 firehose")
	st.assertNoPanicInLog(t, "C02")
	st.waitFor("C02 queue drain", 90*time.Second, func() bool {
		return st.queueStats() == 0
	})
}

/* --------------------------- C03 queue exhaustion -------------------------- */

func TestC03_QueueExhaustion(t *testing.T) {
	st := once()
	st.crReset()
	st.crFault(`{"delay_order_response_ms":20}`)
	defer st.crFault(`{}`)

	var wg sync.WaitGroup
	var busy429, ok, other int64
	for u := range st.userIDs {
		for q := 0; q < 15; q++ {
			wg.Add(1)
			go func(u, q int) {
				defer wg.Done()
				uid := st.userIDs[u]
				code, _ := st.quoteRequest(uid,
					fmt.Sprintf(`{"product_id":"test-airbnb","country":"US","denomination":"range","product_value":1,"quantity":1,"email":%q,"coin":"USDT"}`, testEmail),
					fmt.Sprintf("exhaust-%d-%d-0123456789ab", u, q), 35*time.Second)
				switch {
				case code == 429:
					atomic.AddInt64(&busy429, 1)
				case code == 200 || code == 201:
					atomic.AddInt64(&ok, 1)
				default:
					atomic.AddInt64(&other, 1)
				}
			}(u, q)
		}
	}
	wg.Wait()
	t.Logf("C03: ok=%d queue-busy 429=%d other=%d", ok, busy429, other)
	if busy429 == 0 {
		t.Fatal("expected some queue-busy 429s; queue cap not enforced?")
	}
	// This is an exhaustion test, not a throughput benchmark. Under the
	// deliberately tight per-actor supplier budget it is correct for all
	// admitted requests to time out or be rejected; the safety properties are
	// bounded admission (429) and a live, panic-free server.
	st.assertAlive(t, "C03 exhaustion")
	st.assertNoPanicInLog(t, "C03")

	st.waitFor("C03 queue drain", 30*time.Second, func() bool {
		return st.queueStats() == 0
	})
	// Queue recovery is verified by the empty gauge and health endpoint. Do
	// not require a fresh supplier order here: the supplier's rolling-window
	// budget may legitimately still be exhausted after this local queue has
	// drained.
	st.assertAlive(t, "C03 after drain")
}

/* --------------------------- C04 SIGKILL mid-order ------------------------- */

func TestC04_KillMidOrder(t *testing.T) {
	st := once()
	// Restart FIRST: C02/C03 legitimately exhaust the local 10-minute
	// POST /v5/orders + validations rolling budgets in the shared server
	// process (see C03's own comment about budget bleed). A fresh process
	// resets those budgets exactly like a real redeploy, and C04 kills and
	// restarts the server mid-test anyway.
	st.restartServer()
	st.crReset()
	st.crFault(`{"delay_order_response_ms":2000}`)

	uid := st.userIDs[5]
	before := st.orderCount()

	// Begin the quote asynchronously. Wait until the mock has accepted the
	// supplier order, then kill while its deliberately delayed response is
	// still in flight. This arms the intended crash window without assuming
	// that validation/catalog queueing completes within an arbitrary 400 ms.
	result := make(chan string, 1)
	go func() {
		_, raw := st.quoteRequest(uid,
			fmt.Sprintf(`{"product_id":"test-airbnb","country":"US","denomination":"range","product_value":7,"quantity":1,"email":%q,"coin":"USDT"}`, testEmail),
			"killmid-0123456789abcdef", 5*time.Second)
		result <- raw
	}()
	st.waitFor("C04 supplier order accepted", 8*time.Second, func() bool {
		return st.orderCount() > before
	})
	st.killServer()
	select {
	case raw := <-result:
		t.Logf("C04: interrupted client result: %s", truncate(raw, 120))
	case <-time.After(6 * time.Second):
		t.Fatal("C04 quote client did not return after server kill")
	}
	time.Sleep(200 * time.Millisecond)
	if st.serverAlive() {
		t.Fatal("server survived SIGKILL?")
	}

	st.startServer()
	st.waitHTTP(st.baseURL+"/api/health", 60*time.Second)
	st.assertAlive(t, "C04 after restart")

	// The crash happened with the durable request-start marker SET (the mock
	// had already accepted the order, so the request definitely left), so
	// the tracker must NOT re-dispatch (that could create a duplicate
	// supplier order) but flag manual_review — visible, never silent. The
	// unpaid supplier order simply expires; the customer never got the
	// wallet address, so no money was at risk.
	st.waitFor("C04 stale intent -> manual_review", 60*time.Second, func() bool {
		_, raw := st.api("GET", "/api/quotes", uid, "")
		return strings.Contains(raw, `"status":"manual_review"`)
	})
	_, rawQ := st.api("GET", "/api/quotes", uid, "")
	if !strings.Contains(rawQ, `"status":"manual_review"`) {
		st.dumpLogs("C04 stale intent not resolved")
		t.Fatal("stuck order_creating quote was not flagged manual_review")
	}
	// The durable request-start marker must have been persisted: it is what
	// makes the manual_review decision — and what would have made a blind
	// re-dispatch unsafe here.
	if !strings.Contains(rawQ, `"supplier_request_at"`) {
		t.Fatal("quote is missing the durable supplier_request_at marker")
	}
	// No wallet address may have leaked for the crashed quote.
	if strings.Contains(rawQ, `"wallet_address":"mocksol1"`) && strings.Contains(rawQ, `"status":"awaiting_payment"`) {
		t.Fatal("crashed quote exposed a wallet address without a durable link")
	}
	// No duplicate orders from the crash itself.
	time.Sleep(1 * time.Second)
	if got := st.orderCount(); got > before+1 {
		t.Fatalf("crash caused duplicate supplier orders: before=%d now=%d", before, got)
	}
	// Service keeps working: a fresh quote succeeds.
	qid, wallet, code := st.createQuote(t, st.userIDs[5], "post-kill-0123456789abcdef", 30*time.Second)
	if code != 201 || wallet == "" || qid == "" {
		t.Fatalf("quote after crash failed: %d wallet=%q", code, wallet)
	}
	st.assertNoPanicInLog(t, "C04")
	st.crFault(`{}`)
}

/* ---------------------------- C05 webhook flood ---------------------------- */

func TestC05_WebhookFlood(t *testing.T) {
	st := once()
	st.crReset()
	st.enableWebhooks()
	defer st.crFault(`{}`)

	uid := st.userIDs[6]
	quoteID, wallet, code := st.createQuote(t, uid, "flood-0123456789abcdef", 30*time.Second)
	if code != 201 || wallet == "" {
		t.Fatalf("setup quote failed: %d", code)
	}
	_ = wallet

	// Find the mock order id behind this quote.
	var orderID string
	st.waitFor("C05 order id", 10*time.Second, func() bool {
		_, raw := st.api("GET", "/api/quotes/"+quoteID, uid, "")
		var out struct {
			Quote struct {
				SupplierOrderID string `json:"supplier_order_id"`
			} `json:"quote"`
		}
		if json.Unmarshal([]byte(raw), &out) == nil {
			orderID = out.Quote.SupplierOrderID
			return orderID != ""
		}
		return false
	})
	if orderID == "" {
		t.Fatal("quote has no supplier order id")
	}

	// Drive the order to Done via the mock controls (customer pays).
	st.postControl(st.crBase, "/mock/orders/"+orderID+"/pay", "{}")
	st.postControl(st.crBase, "/mock/orders/"+orderID+"/advance", "{}")
	st.waitFor("C05 natural fulfillment", 60*time.Second, func() bool {
		_, raw := st.api("GET", "/api/quotes/"+quoteID, uid, "")
		return strings.Contains(raw, `"status":"fulfilled"`)
	})
	_, before := st.api("GET", "/api/quotes/"+quoteID, uid, "")
	deliverySnippet := firstDeliverySnippet(before)
	if !strings.Contains(deliverySnippet, "TEST-CODE-") {
		t.Fatalf("fulfillment missing delivery code:\n%s", deliverySnippet)
	}

	// Garbage flood: no key / bad key / bad bodies.
	garbageBodies := [][]byte{
		nil, []byte{}, []byte("not json"), []byte("{"), []byte(`{"order_id":""}`),
		[]byte(`{"order_id":123}`), bytes.Repeat([]byte("x"), 2*1024*1024),
	}
	for i := 0; i < 60; i++ {
		var path string
		switch i % 4 {
		case 0:
			path = "/api/webhooks/cryptorefills"
		case 1:
			path = "/api/webhooks/cryptorefills?key=wrong-key"
		case 2:
			path = "/api/webhooks/cryptorefills?key=" + st.webhookKey
		case 3:
			path = "/api/webhooks/cryptorefills?key=" + st.webhookKey
		}
		var body []byte
		if i%4 >= 2 {
			body = []byte(fmt.Sprintf(`{"order_id":%q,"status":"Done"}`, orderID))
		} else {
			body = garbageBodies[i%len(garbageBodies)]
		}
		_, _ = st.apiRaw("POST", path, nil, body, 10*time.Second)
	}
	// 20 more duplicates of the REAL final webhook (idempotent no-ops).
	for i := 0; i < 20; i++ {
		_, _ = st.apiRaw("POST", "/api/webhooks/cryptorefills?key="+st.webhookKey, nil,
			[]byte(fmt.Sprintf(`{"order_id":%q,"status":"Done"}`, orderID)), 10*time.Second)
	}

	st.assertAlive(t, "C05 flood")
	st.assertNoPanicInLog(t, "C05")

	// The delivery must be exactly what it was before the flood.
	time.Sleep(1 * time.Second)
	_, after := st.api("GET", "/api/quotes/"+quoteID, uid, "")
	if got := firstDeliverySnippet(after); got != deliverySnippet {
		t.Fatalf("webhook flood mutated the delivery:\nbefore=%s\nafter=%s", deliverySnippet, got)
	}
	st.waitFor("C05 queue drain", 90*time.Second, func() bool {
		return st.queueStats() == 0
	})
}

func firstDeliverySnippet(jsonBody string) string {
	var out map[string]interface{}
	_ = json.Unmarshal([]byte(jsonBody), &out)
	b, _ := json.Marshal(out)
	s := string(b)
	if i := strings.Index(s, `"fulfillment"`); i >= 0 {
		return s[i:min(i+500, len(s))]
	}
	return s
}

/* ---------------------------- C06 corrupted DB ---------------------------- */

func TestC06_DBCorrupt(t *testing.T) {
	st := once()
	if st.orderCount() == 0 {
		st.createQuote(t, st.userIDs[7], "corrupt-seed-0123456789ab", 30*time.Second)
	}

	corruptDir := filepath.Join(st.workDir, "badger-corrupt")
	_ = os.RemoveAll(corruptDir)
	if err := copyDir(st.badgerDir, corruptDir); err != nil {
		t.Fatalf("copy db: %v", err)
	}
	files := dirFilesBySize(corruptDir)
	if len(files) < 1 {
		t.Fatal("no data files to corrupt")
	}
	for _, f := range files[:min(2, len(files))] {
		b, err := os.ReadFile(f)
		if err != nil || len(b) < 256 {
			continue
		}
		// Corrupt inside the REAL data region (the first committed entries
		// live at the start of the vlog/memtable; the rest of a fresh test
		// file is pre-allocated padding). Simulates on-disk bit rot of
		// actual entries: replay/compaction must meet a checksum failure,
		// and the server must self-heal or refuse cleanly — never panic.
		off := 16
		if len(b)-16 < off {
			off = len(b) / 2
		}
		for i := 0; i < 16; i++ {
			b[off+i] ^= 0xFF
		}
		if err := os.WriteFile(f, b, 0o600); err != nil {
			t.Fatalf("corrupt write: %v", err)
		}
	}

	freeL, _ := net.Listen("tcp", "127.0.0.1:0")
	cPort := freeL.Addr().(*net.TCPAddr).Port
	freeL.Close()
	cBase := fmt.Sprintf("http://127.0.0.1:%d", cPort)

	logFile, _ := os.Create(filepath.Join(st.workDir, "corrupt-server.log"))
	cmd := exec.Command(st.serverBin)
	cmd.Dir = st.workDir
	env := []string{"PATH=" + os.Getenv("PATH"),
		fmt.Sprintf("LISTEN_ADDR=127.0.0.1:%d", cPort),
		"BADGER_DIR=" + corruptDir,
		"JWT_SECRET=" + st.jwtSecret,
		"ALLOW_HTTP_LOCAL=true",
		fmt.Sprintf("CRYPTOREFILLS_BASE_URL=%s", st.crBase),
		"CRYPTOREFILLS_PARTNER_ID=crash",
		"CRYPTOREFILLS_WEBHOOK_KEY=" + st.webhookKey,
		"PUBLIC_WEBHOOK_BASE_URL=" + cBase,
		"WORKER_ORDER_POLL_SECS=60",
		"TRUST_PROXY=false",
	}
	cmd.Env = env
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	if err := cmd.Start(); err != nil {
		t.Fatalf("start corrupt server: %v", err)
	}
	// Reap in the background: only Cmd.Wait populates cmd.ProcessState,
	// and the checks below rely on it to distinguish "exited cleanly" from
	// "still running" (a Process.Wait in the deferred cleanup would be too
	// late — the loop would see ProcessState==nil for the full deadline).
	waitDone := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(waitDone)
	}()
	defer func() {
		_ = cmd.Process.Kill()
		<-waitDone
	}()

	started := false
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if resp, err := http.Get(cBase + "/api/health"); err == nil {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode == 200 && bytes.Contains(body, []byte(`"ok":true`)) {
				started = true
				break
			}
		}
		if cmd.ProcessState != nil {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}

	logBytes, _ := os.ReadFile(filepath.Join(st.workDir, "corrupt-server.log"))
	if bytes.Contains(logBytes, []byte("panic:")) {
		t.Fatalf("corrupted DB caused a Go panic:\n%s", logBytes)
	}
	if started {
		t.Log("C06: corrupted DB opened and self-healed (WAL replay) — health OK")
	} else if cmd.ProcessState != nil {
		if !bytes.Contains(logBytes, []byte("db init")) && !bytes.Contains(logBytes, []byte("badger")) {
			t.Fatalf("server exited on corrupted DB for an unexplained reason:\n%s", logBytes)
		}
		t.Log("C06: corrupted DB refused cleanly with a startup error (no panic)")
	} else {
		t.Fatal("corrupted-DB server neither started nor exited cleanly (hang?)")
	}
	st.assertAlive(t, "C06 live server unaffected")
}

// copyDirMaxBytes caps a per-file copy: Badger pre-allocates its value log
// as a SPARSE multi-GB file (apparent size ~2GB, real data in the first
// kilobytes). os.ReadFile would allocate the full apparent size (OOM in CI
// sandboxes), and an uncapped materializing copy would burn the sandbox's
// tmpfs. Streaming with a cap copies all real data of a fresh test DB while
// bounding both memory and disk.
const copyDirMaxBytes = 64 << 20

func copyDir(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
		if err != nil {
			return err
		}
		defer out.Close()
		_, err = io.Copy(out, io.LimitReader(in, copyDirMaxBytes))
		return err
	})
}

// dirFilesBySize lists the directory's files, largest apparent size first
// (the vlog and memtable are the data files worth corrupting).
func dirFilesBySize(dir string) []string {
	var out []struct {
		path string
		size int64
	}
	_ = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			out = append(out, struct {
				path string
				size int64
			}{path, info.Size()})
		}
		return nil
	})
	for i := 0; i < len(out); i++ {
		for j := i + 1; j < len(out); j++ {
			if out[j].size > out[i].size {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	paths := make([]string, 0, len(out))
	for _, f := range out {
		paths = append(paths, f.path)
	}
	return paths
}

/* ---------------------------- C07 restart storm ---------------------------- */

func TestC07_RestartStorm(t *testing.T) {
	st := once()
	st.crReset()
	st.enableWebhooks()

	uid := st.userIDs[8]
	seeded := 0
	for i := 0; i < 3; i++ {
		qid, _, code := st.createQuote(t, uid, fmt.Sprintf("storm-%d-0123456789abcdef", i), 30*time.Second)
		if code == 201 {
			seeded++
			// Pay + deliver each seed order through the mock.
			var orderID string
			st.waitFor("C07 order id", 10*time.Second, func() bool {
				_, raw := st.api("GET", "/api/quotes/"+qid, uid, "")
				var out struct {
					Quote struct {
						SupplierOrderID string `json:"supplier_order_id"`
					} `json:"quote"`
				}
				if json.Unmarshal([]byte(raw), &out) == nil {
					orderID = out.Quote.SupplierOrderID
					return orderID != ""
				}
				return false
			})
			if orderID != "" {
				st.postControl(st.crBase, "/mock/orders/"+orderID+"/pay", "{}")
				st.postControl(st.crBase, "/mock/orders/"+orderID+"/advance", "{}")
			}
			st.waitFor("C07 seed fulfillment "+qid, 60*time.Second, func() bool {
				_, raw := st.api("GET", "/api/quotes/"+qid, uid, "")
				return strings.Contains(raw, `"status":"fulfilled"`)
			})
		}
	}
	if seeded == 0 {
		t.Fatal("could not seed dataset")
	}
	countQuotes := func() int {
		_, raw := st.api("GET", "/api/quotes", uid, "")
		var list []map[string]interface{}
		if err := json.Unmarshal([]byte(raw), &list); err != nil {
			return strings.Count(raw, `"id":"`)
		}
		return len(list)
	}
	baseline := countQuotes()
	t.Logf("C07: seeded, user quote baseline=%d", baseline)

	for cycle := 1; cycle <= 5; cycle++ {
		st.killServer()
		time.Sleep(200 * time.Millisecond)
		st.startServer()
		st.waitHTTP(st.baseURL+"/api/health", 60*time.Second)
		st.assertAlive(t, fmt.Sprintf("C07 cycle %d", cycle))
		if got := countQuotes(); got < baseline {
			st.dumpLogs(fmt.Sprintf("C07 cycle %d: data loss", cycle))
			t.Fatalf("cycle %d: quote count dropped %d -> %d (persistence failure)", cycle, baseline, got)
		}
	}
	st.assertNoPanicInLog(t, "C07")
	t.Logf("C07: 5 hard-kill restarts survived, dataset intact (quotes=%d)", countQuotes())
}

/* ---------------------------- C08 happy path ------------------------------- */

func TestC08_HappyPath(t *testing.T) {
	st := once()
	st.crReset()
	st.enableWebhooks()
	defer st.crFault(`{}`)

	uid := st.userIDs[9]
	quoteID, wallet, code := st.createQuote(t, uid, "happy-0123456789abcdef", 30*time.Second)
	if code != 201 || wallet == "" || quoteID == "" {
		t.Fatalf("quote failed: %d wallet=%q", code, wallet)
	}
	// The shop exposes exactly one payment rail: BTC Lightning
	// (quote_handlers PaymentCoin/PaymentNetwork), so the mock answers with
	// a structurally valid pseudo-BOLT11 invoice, not a raw address.
	if !strings.HasPrefix(wallet, "lnbc") {
		t.Fatalf("unexpected wallet address: %s", wallet)
	}
	// The payment payload must be complete.
	_, raw := st.api("GET", "/api/quotes/"+quoteID, uid, "")
	var out struct {
		Quote struct {
			Status     string `json:"status"`
			Coin       string `json:"coin"`
			CoinAmount string `json:"coin_amount"`
			Network    string `json:"network"`
		} `json:"quote"`
	}
	if json.Unmarshal([]byte(raw), &out) != nil {
		t.Fatalf("bad quote response: %s", raw)
	}
	if out.Quote.Status != "awaiting_payment" || out.Quote.CoinAmount == "" {
		t.Fatalf("quote not awaiting payment: %+v", out.Quote)
	}

	// Idempotent retry with the SAME key must return the SAME quote (no
	// second supplier order).
	before := st.orderCount()
	_, _, code2 := st.createQuote(t, uid, "happy-0123456789abcdef", 30*time.Second)
	if code2 != 200 && code2 != 201 {
		t.Fatalf("idempotent retry failed: %d", code2)
	}
	if got := st.orderCount(); got != before {
		t.Fatalf("idempotent retry created a second supplier order: %d -> %d", before, got)
	}

	// Customer pays; tracker + webhook converge to fulfilled with the code.
	var orderID string
	st.waitFor("C08 order id", 10*time.Second, func() bool {
		_, raw := st.api("GET", "/api/quotes/"+quoteID, uid, "")
		var o struct {
			Quote struct {
				SupplierOrderID string `json:"supplier_order_id"`
			} `json:"quote"`
		}
		if json.Unmarshal([]byte(raw), &o) == nil {
			orderID = o.Quote.SupplierOrderID
			return orderID != ""
		}
		return false
	})
	st.postControl(st.crBase, "/mock/orders/"+orderID+"/pay", "{}")
	st.postControl(st.crBase, "/mock/orders/"+orderID+"/advance", "{}")
	st.waitFor("C08 fulfillment", 90*time.Second, func() bool {
		_, raw := st.api("GET", "/api/quotes/"+quoteID, uid, "")
		return strings.Contains(raw, `"status":"fulfilled"`)
	})
	_, rawF := st.api("GET", "/api/quotes/"+quoteID, uid, "")
	if !strings.Contains(rawF, "TEST-CODE-") {
		t.Fatalf("fulfilled quote missing delivery code:\n%s", truncate(rawF, 800))
	}
	st.assertAlive(t, "C08 happy path")
	st.assertNoPanicInLog(t, "C08")

	// Public tracking shows the lifecycle without leaking the code.
	trackCode, trackBody := st.api("GET", "/api/track/"+quoteID, "", "")
	if trackCode != 200 {
		t.Fatalf("track failed: %d", trackCode)
	}
	if strings.Contains(trackBody, "TEST-CODE-") {
		t.Fatalf("public track leaked the delivery code:\n%s", trackBody)
	}
	// Activity feed lists the purchase.
	if c, _ := st.api("GET", "/api/activity", "", ""); c != 200 {
		t.Fatalf("activity failed: %d", c)
	}
}

/* --------------- C09 beneficiary_account rules (E.164/email) ---------------
 *
 * The supplier delivers each product to the order's beneficiary_account:
 * the E.164 phone for mobile topups, the end-user's email for gift cards
 * and eSIMs. This scenario drives the REAL server + mock supplier end to
 * end and verifies: normalization of customer input, strict E.164
 * enforcement BEFORE any supplier order is created, and that the right
 * beneficiary reaches the supplier and the fulfillment.
 */

func TestC09_BeneficiaryRules(t *testing.T) {
	st := once()
	st.crReset()
	st.enableWebhooks()
	defer st.crFault(`{}`)

	uid := st.userIDs[10]

	// 1) check-phone normalizes customer formats server-side.
	code, raw := st.api("GET", "/api/catalog/check-phone?phone_number=0014155551234&country=US", "", "")
	if code != 200 || !strings.Contains(raw, `"phone_number":"+14155551234"`) {
		t.Fatalf("check-phone 00-prefix: code=%d body=%s", code, truncate(raw, 300))
	}
	code, raw = st.api("GET", "/api/catalog/check-phone?phone_number=%2B90%20555%20123%2045%2067&country=TR", "", "")
	if code != 200 || !strings.Contains(raw, `"phone_number":"+905551234567"`) {
		t.Fatalf("check-phone separators: code=%d body=%s", code, truncate(raw, 300))
	}
	code, raw = st.api("GET", "/api/catalog/check-phone?phone_number=user%40example.com", "", "")
	if code != 400 {
		t.Fatalf("check-phone must reject a non-number, got %d: %s", code, truncate(raw, 200))
	}

	// 2) Top-up: customer-typed national/access-prefixed number is
	//    normalized and the E.164 number is what gets delivered.
	before := st.orderCount()
	code, raw = st.quoteRequest(uid,
		fmt.Sprintf(`{"product_id":"test-topup","country":"US","denomination":"range","product_value":10,"quantity":1,"email":%q,"phone_number":"0014155551234","coin":"BTC"}`, testEmail),
		"benef-c09-topup-00000000", 30*time.Second)
	if code != 201 {
		t.Fatalf("topup quote (00-prefixed) failed: %d %s", code, truncate(raw, 300))
	}
	if got := st.orderCount(); got != before+1 {
		t.Fatalf("topup quote order count %d -> %d", before, got)
	}
	quoteID, orderID := st.quoteAndOrderID(t, raw, uid)
	st.postControl(st.crBase, "/mock/orders/"+orderID+"/pay", "{}")
	st.postControl(st.crBase, "/mock/orders/"+orderID+"/advance", "{}")
	st.waitFor("C09 topup fulfillment", 90*time.Second, func() bool {
		_, raw := st.api("GET", "/api/quotes/"+quoteID, uid, "")
		return strings.Contains(raw, `"status":"fulfilled"`)
	})
	_, rawF := st.api("GET", "/api/quotes/"+quoteID, uid, "")
	if !strings.Contains(rawF, "Top-up completed for +14155551234") {
		t.Fatalf("topup not delivered to the normalized E.164 number:\n%s", truncate(rawF, 800))
	}

	// 3) Gift card that ALSO carries a phone number (optional on
	//    non-topup products): delivery must go to the EMAIL, never the
	//    phone.
	code, raw = st.quoteRequest(uid,
		fmt.Sprintf(`{"product_id":"test-airbnb","country":"US","denomination":"range","product_value":10,"quantity":1,"email":%q,"phone_number":"+14155551234","coin":"BTC"}`, testEmail),
		"benef-c09-giftcard-0000000", 30*time.Second)
	if code != 201 {
		t.Fatalf("giftcard quote with optional phone failed: %d %s", code, truncate(raw, 300))
	}
	quoteID, orderID = st.quoteAndOrderID(t, raw, uid)
	st.postControl(st.crBase, "/mock/orders/"+orderID+"/pay", "{}")
	st.postControl(st.crBase, "/mock/orders/"+orderID+"/advance", "{}")
	st.waitFor("C09 giftcard fulfillment", 90*time.Second, func() bool {
		_, raw := st.api("GET", "/api/quotes/"+quoteID, uid, "")
		return strings.Contains(raw, `"status":"fulfilled"`)
	})
	_, rawF = st.api("GET", "/api/quotes/"+quoteID, uid, "")
	if !strings.Contains(rawF, "sent to "+testEmail) {
		t.Fatalf("giftcard not delivered to the email:\n%s", truncate(rawF, 800))
	}
	if strings.Contains(rawF, "Top-up completed") {
		t.Fatalf("giftcard fulfillment looks like a top-up:\n%s", truncate(rawF, 800))
	}

	// 4) Rejections: none of these may create a supplier order.
	before = st.orderCount()
	badCases := []struct {
		name    string
		phone   string
		wantSub string
	}{
		{"email-as-phone", testEmail, "invalid characters"},
		{"short-number", "+1415", "E.164"},
		{"seventeen-digits", "+12345678901234567", "E.164"},
		{"bare-national-no-plus", "14155551234", "country code"},
		{"missing", "", "phone_number is required"},
	}
	for _, c := range badCases {
		code, raw = st.quoteRequest(uid,
			fmt.Sprintf(`{"product_id":"test-topup","country":"US","denomination":"range","product_value":10,"quantity":1,"email":%q,"phone_number":%q,"coin":"BTC"}`, testEmail, c.phone),
			"benef-c09-bad-"+c.name, 30*time.Second)
		if code != 400 {
			t.Fatalf("%s: expected 400, got %d %s", c.name, code, truncate(raw, 300))
		}
		if !strings.Contains(raw, c.wantSub) {
			t.Fatalf("%s: error message %q missing %q", c.name, truncate(raw, 300), c.wantSub)
		}
	}
	if got := st.orderCount(); got != before {
		t.Fatalf("rejected topup attempts created supplier orders: %d -> %d", before, got)
	}

	// 5) A top-up quote without any phone is rejected before supplier calls.
	code, raw = st.quoteRequest(uid,
		fmt.Sprintf(`{"product_id":"test-topup","country":"US","denomination":"range","product_value":10,"quantity":1,"email":%q,"coin":"BTC"}`, testEmail),
		"benef-c09-nophone-0000000", 30*time.Second)
	if code != 400 || !strings.Contains(raw, "phone_number is required") {
		t.Fatalf("topup without phone: code=%d body=%s", code, truncate(raw, 300))
	}

	st.assertAlive(t, "C09 beneficiary rules")
	st.assertNoPanicInLog(t, "C09")
}

// quoteAndOrderID extracts the quote id from a 201 quote response and then
// the supplier order id from the persisted quote (the tracker/webhook
// attach happens synchronously in the quote path).
func (st *crashStack) quoteAndOrderID(t *testing.T, quoteBody, uid string) (string, string) {
	t.Helper()
	var q struct {
		QuoteID string `json:"quote_id"`
	}
	if err := json.Unmarshal([]byte(quoteBody), &q); err != nil || q.QuoteID == "" {
		t.Fatalf("quote id missing: %s", truncate(quoteBody, 300))
	}
	var orderID string
	st.waitFor("C09 supplier order id", 10*time.Second, func() bool {
		_, raw := st.api("GET", "/api/quotes/"+q.QuoteID, uid, "")
		var o struct {
			Quote struct {
				SupplierOrderID string `json:"supplier_order_id"`
			} `json:"quote"`
		}
		if json.Unmarshal([]byte(raw), &o) == nil {
			orderID = o.Quote.SupplierOrderID
			return orderID != ""
		}
		return false
	})
	if orderID == "" {
		t.Fatalf("supplier order id never attached to quote %s", q.QuoteID)
	}
	return q.QuoteID, orderID
}

/* --------------------------------- utils ---------------------------------- */

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
