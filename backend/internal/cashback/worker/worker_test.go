package worker

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"nimiqshop/internal/db"
	"nimiqshop/internal/nimiq"
)

type faultChain struct {
	mu          sync.Mutex
	height      int64
	balance     int64
	accepted    map[string]int64
	wires       map[string]string
	pushes      int
	lostReply   bool
	unavailable bool
}

func (c *faultChain) serve(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var req struct {
		Method string            `json:"method"`
		Params []json.RawMessage `json:"params"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	if c.unavailable {
		http.Error(w, "RPC unavailable", http.StatusServiceUnavailable)
		return
	}
	var result interface{}
	var problem string
	switch req.Method {
	case "getAccountByAddress":
		result = map[string]interface{}{"balance": c.balance}
	case "getBlockNumber":
		result = c.height
	case "getPolicyConstants":
		result = map[string]interface{}{"transactionValidityWindow": 7200}
	case "getLastMacroBlock":
		result = c.height - 10
	case "getBlockByNumber":
		var number uint32
		_ = json.Unmarshal(req.Params[0], &number)
		result = map[string]interface{}{"hash": strings.Repeat("a", 64), "number": number, "type": "macro", "network": "TestAlbatross", "justification": map[string]interface{}{"proof": "final"}}
	case "getTransactionByHash":
		var hash string
		_ = json.Unmarshal(req.Params[0], &hash)
		if block, ok := c.accepted[hash]; ok {
			result = map[string]interface{}{"hash": hash, "blockNumber": block, "confirmations": 60, "executionResult": true}
		} else {
			problem = "Transaction not found: " + hash
		}
	case "pushTransaction":
		var raw string
		_ = json.Unmarshal(req.Params[0], &raw)
		hash, start, network, err := nimiq.SignedTxInfo(raw)
		if err != nil || network != nimiq.NetworkTestnet {
			problem = "invalid transaction"
			break
		}
		c.pushes++
		if _, ok := c.accepted[hash]; ok {
			problem = "already known"
			break
		}
		if c.height > int64(start)+7200 {
			problem = "expired"
			break
		}
		if c.balance < 100001 {
			problem = "insufficient funds"
			break
		}
		c.balance -= 100001
		c.accepted[hash] = c.height
		c.wires[hash] = raw
		c.height += 70
		if c.lostReply {
			c.lostReply = false
			problem = "accepted but response lost"
		} else {
			result = hash
		}
	default:
		problem = "unsupported method: " + req.Method
	}
	w.Header().Set("Content-Type", "application/json")
	if problem != "" {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"jsonrpc": "2.0", "id": 1, "error": map[string]interface{}{"code": -32603, "message": problem}})
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"jsonrpc": "2.0", "id": 1, "result": map[string]interface{}{"data": result, "metadata": nil}})
}
func (c *faultChain) set(balance, height int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.balance = balance
	c.height = height
}
func (c *faultChain) count() (int, int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.accepted), c.pushes
}

type faultStore struct {
	PayoutStore
	attachAfter, broadcastBefore, paidBefore bool
}

func (s *faultStore) AttachCashbackSignedTx(id, raw, hash string, height uint32) error {
	if err := s.PayoutStore.AttachCashbackSignedTx(id, raw, hash, height); err != nil {
		return err
	}
	if s.attachAfter {
		s.attachAfter = false
		return errors.New("crash after durable signature")
	}
	return nil
}
func (s *faultStore) MarkCashbackBroadcast(id, hash string) error {
	if s.broadcastBefore {
		s.broadcastBefore = false
		return errors.New("crash before receipt commit")
	}
	return s.PayoutStore.MarkCashbackBroadcast(id, hash)
}
func (s *faultStore) MarkCashbackPaid(id, hash string) error {
	if s.paidBefore {
		s.paidBefore = false
		return errors.New("crash before paid commit")
	}
	return s.PayoutStore.MarkCashbackPaid(id, hash)
}

func fixture(t *testing.T) (*db.Store, string, db.Cashback, *faultChain, *nimiq.Client) {
	t.Helper()
	dir := t.TempDir()
	store, err := db.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	to, err := nimiq.AddressFromKeyHex(strings.Repeat("02", 32))
	if err != nil {
		t.Fatal(err)
	}
	row, err := store.QueueManualCashback(to, "nimshop.io recovery test", 100000, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	chain := &faultChain{height: 10000, balance: 1000000, accepted: map[string]int64{}, wires: map[string]string{}}
	a := httptest.NewServer(http.HandlerFunc(chain.serve))
	b := httptest.NewServer(http.HandlerFunc(chain.serve))
	t.Cleanup(a.Close)
	t.Cleanup(b.Close)
	return store, dir, row, chain, nimiq.NewClient(a.URL, b.URL)
}
func newWorker(store PayoutStore, rpc *nimiq.Client) *Worker {
	return &Worker{Store: store, RPC: rpc, Enabled: true, SeedHex: strings.Repeat("01", 32), NetworkID: nimiq.NetworkTestnet, FeeLuna: 1}
}
func runTicks(w *Worker, n int) {
	for i := 0; i < n; i++ {
		w.tick(context.Background())
	}
}
func assertPaidOnce(t *testing.T, s *db.Store, id string, c *faultChain) {
	t.Helper()
	rows, err := s.ListCashbacksByStatus(db.CashbackPaid, 0)
	if err != nil || len(rows) != 1 || rows[0].ID != id {
		t.Fatalf("payment not settled exactly once: %v %v", rows, err)
	}
	accepted, _ := c.count()
	if accepted != 1 {
		t.Fatalf("recipient credited %d times", accepted)
	}
}
func TestPayoutCrashRecoveryAndLostAcknowledgement(t *testing.T) {
	for _, stage := range []string{"signature_commit", "broadcast_reply", "broadcast_commit", "paid_commit"} {
		t.Run(stage, func(t *testing.T) {
			store, _, row, chain, rpc := fixture(t)
			faults := &faultStore{PayoutStore: WithQuizPayouts(store)}
			switch stage {
			case "signature_commit":
				faults.attachAfter = true
			case "broadcast_reply":
				chain.lostReply = true
			case "broadcast_commit":
				faults.broadcastBefore = true
			case "paid_commit":
				faults.paidBefore = true
			}
			w := newWorker(faults, rpc)
			if stage == "paid_commit" {
				runTicks(w, 2)
			} else {
				runTicks(w, 1)
			}
			if faults.attachAfter || faults.broadcastBefore || faults.paidBefore {
				t.Fatal("configured crash point was not exercised")
			}
			w = newWorker(WithQuizPayouts(store), rpc)
			runTicks(w, 8)
			assertPaidOnce(t, store, row.ID, chain)
		})
	}
}
func TestPayoutWaitsForFundingAndSurvivesStoreReopen(t *testing.T) {
	store, dir, row, chain, rpc := fixture(t)
	chain.set(0, 10000)
	w := newWorker(WithQuizPayouts(store), rpc)
	runTicks(w, 8)
	rows, err := store.ListCashbacksByStatus(db.CashbackQueued, 0)
	if err != nil || len(rows) != 1 || rows[0].SignedTxHex != "" {
		t.Fatalf("unfunded payment lost or signed: %v %v", rows, err)
	}
	if accepted, pushes := chain.count(); accepted != 0 || pushes != 0 {
		t.Fatal("sent while unfunded")
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := db.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	chain.set(1000000, 90000)
	w = newWorker(WithQuizPayouts(reopened), rpc)
	runTicks(w, 8)
	assertPaidOnce(t, reopened, row.ID, chain)
}
func TestExpiredUnpaidSignatureIsRetiredBeforeFreshAttempt(t *testing.T) {
	store, _, row, chain, rpc := fixture(t)
	faults := &faultStore{PayoutStore: WithQuizPayouts(store), attachAfter: true}
	runTicks(newWorker(faults, rpc), 1)
	old, err := store.ListCashbacksByStatus(db.CashbackSending, 0)
	if err != nil || len(old) != 1 || old[0].SignedTxHex == "" {
		t.Fatal("signature not durable", err)
	}
	chain.set(1000000, 90000)
	w := newWorker(WithQuizPayouts(store), rpc)
	runTicks(w, 8)
	assertPaidOnce(t, store, row.ID, chain)
	paid, _ := store.ListCashbacksByStatus(db.CashbackPaid, 0)
	if len(paid[0].ExpiredAttempts) != 1 || paid[0].ExpiredAttempts[0].SignedTxHex != old[0].SignedTxHex || paid[0].TxHash == old[0].TxHash {
		t.Fatal("expiry proof/audit missing")
	}
	if err := store.RetireExpiredPayout(row.ID, old[0].SignedTxHex, paid[0].ExpiredAttempts[0].Proof); !errors.Is(err, db.ErrConflict) {
		t.Fatal("settled payment requeued", err)
	}
}
func TestConcurrentWorkersCannotPayOneObligationTwice(t *testing.T) {
	store, _, row, chain, rpc := fixture(t)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); runTicks(newWorker(WithQuizPayouts(store), rpc), 8) }()
	}
	wg.Wait()
	assertPaidOnce(t, store, row.ID, chain)
}

func TestRPCOutageCannotLoseOrDuplicateDurableSignature(t *testing.T) {
	store, _, row, chain, rpc := fixture(t)
	faults := &faultStore{PayoutStore: WithQuizPayouts(store), attachAfter: true}
	runTicks(newWorker(faults, rpc), 1)
	sending, _ := store.ListCashbacksByStatus(db.CashbackSending, 0)
	original := sending[0].SignedTxHex
	chain.mu.Lock()
	chain.unavailable = true
	chain.height = 90000
	chain.mu.Unlock()
	w := newWorker(WithQuizPayouts(store), rpc)
	runTicks(w, 5)
	sending, _ = store.ListCashbacksByStatus(db.CashbackSending, 0)
	if len(sending) != 1 || sending[0].SignedTxHex != original || len(sending[0].ExpiredAttempts) != 0 {
		t.Fatal("RPC outage re-signed or lost obligation")
	}
	chain.mu.Lock()
	chain.unavailable = false
	chain.mu.Unlock()
	runTicks(w, 8)
	assertPaidOnce(t, store, row.ID, chain)
}
func TestOneRPCSeeingPaymentPreventsExpiryReplacement(t *testing.T) {
	store, _, row, chain, _ := fixture(t)
	a := httptest.NewServer(http.HandlerFunc(chain.serve))
	defer a.Close()
	other := &faultChain{height: 10000, balance: 1000000, accepted: map[string]int64{}, wires: map[string]string{}}
	b := httptest.NewServer(http.HandlerFunc(other.serve))
	defer b.Close()
	faults := &faultStore{PayoutStore: WithQuizPayouts(store), attachAfter: true}
	rpc := nimiq.NewClient(a.URL, b.URL)
	runTicks(newWorker(faults, rpc), 1)
	sending, _ := store.ListCashbacksByStatus(db.CashbackSending, 0)
	old := sending[0]
	chain.mu.Lock()
	chain.accepted[old.TxHash] = 10000
	chain.height = 90000
	chain.mu.Unlock()
	other.set(1000000, 90000)
	runTicks(newWorker(WithQuizPayouts(store), rpc), 4)
	broadcast, _ := store.ListCashbacksByStatus(db.CashbackBroadcast, 0)
	if len(broadcast) != 1 || broadcast[0].TxHash != old.TxHash || len(broadcast[0].ExpiredAttempts) != 0 {
		t.Fatal("partial receipt caused replacement")
	}
	if _, pushes := chain.count(); pushes != 0 {
		t.Fatal("resent despite one provider seeing payment")
	}
	other.mu.Lock()
	other.accepted[old.TxHash] = 10000
	other.mu.Unlock()
	runTicks(newWorker(WithQuizPayouts(store), rpc), 3)
	assertPaidOnce(t, store, row.ID, chain)
}
func TestSeparateSameValueObligationsHaveDistinctSignedTransactions(t *testing.T) {
	store, _, row, chain, rpc := fixture(t)
	next, err := store.QueueManualCashback(row.Recipient, row.Memo, row.AmountLuna, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	w := newWorker(WithQuizPayouts(store), rpc)
	runTicks(w, 8)
	paid, _ := store.ListCashbacksByStatus(db.CashbackPaid, 0)
	if len(paid) != 2 || paid[0].TxHash == paid[1].TxHash || paid[0].Memo == paid[1].Memo {
		t.Fatal("distinct obligations collapsed")
	}
	if paid[0].ID != next.ID && paid[1].ID != next.ID {
		t.Fatal("second obligation missing")
	}
	if accepted, _ := chain.count(); accepted != 2 {
		t.Fatalf("wanted two separate credits, got %d", accepted)
	}
}
func TestQuizPrizesUseSameWorkerAndOwnerCashbackHistory(t *testing.T) {
	store, _, row, chain, rpc := fixture(t)
	now := time.Now().UTC()
	two := 2
	c, err := store.PutQuizCompetition(db.QuizCompetition{Title: "Prize test", Prize: db.QuizPrize{Title: "NIM"}, TieBreak: "submitted_at", WinnerCount: &two, StartsAt: now.Add(-time.Minute), EndsAt: now.Add(time.Hour), Payout: db.QuizPayoutConfig{Enabled: true, Source: "cashback_wallet", PoolNIM: 2, PerWinnerNIM: 1}, Questions: []db.QuizQuestion{{Prompt: "Pick", Options: []string{"A", "B"}, CorrectIndex: 0}}}, true, now)
	if err != nil {
		t.Fatal(err)
	}
	c, err = store.PublishQuiz(c.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"03", "04"} {
		address, e := nimiq.AddressFromKeyHex(strings.Repeat(key, 32))
		if e != nil {
			t.Fatal(e)
		}
		user, e := store.FindOrCreateUserByAddress(address)
		if e != nil {
			t.Fatal(e)
		}
		if _, _, e = store.StartQuiz(c.ID, user.ID, now); e != nil {
			t.Fatal(e)
		}
		if _, e = store.SaveQuizAnswers(c.ID, user.ID, []int{0}, true, now); e != nil {
			t.Fatal(e)
		}
	}
	if err = store.FinalizeDueQuizPayouts(c.EndsAt); err != nil {
		t.Fatal(err)
	}
	runTicks(newWorker(WithQuizPayouts(store), rpc), 8)
	prizes, err := store.ListQuizPayouts(c.ID)
	if err != nil || len(prizes) != 2 {
		t.Fatal("prizes missing", err)
	}
	for _, prize := range prizes {
		if prize.Status != db.CashbackPaid || prize.Memo == "" || prize.TxHash == "" {
			t.Fatal("prize not confirmed")
		}
		history, e := store.ListQuizPayoutsByUser(prize.UserID)
		if e != nil || len(history) != 1 || history[0].ID != prize.ID {
			t.Fatal("owner history missing", e)
		}
		sum, e := store.GetUserCashbackSummary(prize.UserID)
		if e != nil || sum.PaidLuna != 0 || sum.PendingLuna != 0 {
			t.Fatal("purchase cashback accounting changed", e)
		}
	}
	history, e := store.ListQuizPayoutsByUser("another-user")
	if e != nil || len(history) != 0 {
		t.Fatal("another owner's prize leaked")
	}
	paid, _ := store.ListCashbacksByStatus(db.CashbackPaid, 0)
	if len(paid) != 1 || paid[0].ID != row.ID {
		t.Fatal("purchase queue affected")
	}
	if accepted, _ := chain.count(); accepted != 3 {
		t.Fatal("prize duplicated or missed", accepted)
	}
}

func TestAbruptProcessExitAfterSignatureCommit(t *testing.T) {
	if dir := os.Getenv("NIMSHOP_PAYOUT_CRASH_CHILD"); dir != "" {
		store, err := db.New(dir)
		if err != nil {
			t.Fatal(err)
		}
		rows, err := store.ListCashbacksByStatus(db.CashbackQueued, 1)
		if err != nil || len(rows) != 1 {
			t.Fatal("child queue missing", err)
		}
		row, err := store.ClaimCashbackSend(rows[0].ID)
		if err != nil {
			t.Fatal(err)
		}
		raw, hash, err := nimiq.BuildBasicTransaction(strings.Repeat("01", 32), row.Recipient, row.AmountLuna, 1, 10000, nimiq.NetworkTestnet, []byte(row.Memo))
		if err != nil {
			t.Fatal(err)
		}
		if err = store.AttachCashbackSignedTx(row.ID, raw, hash, 10005); err != nil {
			t.Fatal(err)
		}
		// Deliberately skip Close and all deferred cleanup, like a killed server.
		os.Exit(42)
	}
	store, dir, row, chain, rpc := fixture(t)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	child := exec.Command(os.Args[0], "-test.run=^TestAbruptProcessExitAfterSignatureCommit$")
	child.Env = append(os.Environ(), "NIMSHOP_PAYOUT_CRASH_CHILD="+dir)
	output, err := child.CombinedOutput()
	var exited *exec.ExitError
	if !errors.As(err, &exited) || exited.ExitCode() != 42 {
		t.Fatalf("crash simulation failed: %v %s", err, output)
	}
	reopened, err := db.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	sending, err := reopened.ListCashbacksByStatus(db.CashbackSending, 0)
	if err != nil || len(sending) != 1 || sending[0].SignedTxHex == "" {
		t.Fatal("committed signature lost across abrupt exit", err)
	}
	runTicks(newWorker(WithQuizPayouts(reopened), rpc), 8)
	assertPaidOnce(t, reopened, row.ID, chain)
}
