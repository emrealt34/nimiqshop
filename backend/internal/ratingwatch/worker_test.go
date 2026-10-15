package ratingwatch

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"nimiqshop/internal/db"
	"nimiqshop/internal/nimiq"
)

const buyer = "NQ22111111111111111111111111111111111111"
const shop = "NQ07000000000000000000000000000000000000"

var now = time.Date(2026, 10, 15, 8, 0, 0, 0, time.UTC)

type fakeRPC struct {
	history     []nimiq.Transaction
	proofs      map[string]nimiq.TxDetail
	detailCalls int
	fail        bool
	cursors     []string
}

func (f *fakeRPC) GetTransactionsByAddress(_ context.Context, _ string, limit int, cursor string) ([]nimiq.Transaction, error) {
	if f.fail {
		return nil, errors.New("RPC offline")
	}
	f.cursors = append(f.cursors, cursor)
	start := 0
	if cursor != "" {
		start = len(f.history)
		for i, tx := range f.history {
			if tx.Hash == cursor {
				start = i + 1
				break
			}
		}
	}
	return f.history[start:min(len(f.history), start+limit)], nil
}
func (f *fakeRPC) GetTransactionDetail(_ context.Context, hash string) (nimiq.TxDetail, bool, error) {
	f.detailCalls++
	if f.fail {
		return nimiq.TxDetail{}, false, errors.New("RPC offline")
	}
	p, ok := f.proofs[hash]
	return p, ok, nil
}
func history(n int, memo string) nimiq.Transaction {
	data := hex.EncodeToString([]byte(memo))
	return nimiq.Transaction{Hash: fmt.Sprintf("%064x", n), From: buyer, To: shop, Value: 1, RecipientData: &data, Timestamp: now.Add(time.Duration(n) * time.Second).UnixMilli()}
}
func proof(tx nimiq.Transaction, memo string) nimiq.TxDetail {
	return nimiq.TxDetail{Hash: tx.Hash, From: buyer, To: shop, Value: 1, Data: []byte(memo), Mined: true, Success: true}
}
func openStore(t *testing.T, path string) *db.Store {
	t.Helper()
	s, err := db.New(path)
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func prepare(t *testing.T, s *db.Store, id, memo string) {
	t.Helper()
	if _, err := s.GetOrCreateUserByID("buyer", buyer); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateOrder(db.Order{ID: id, UserID: "buyer", Status: "delivered"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveRatingIntent(db.RatingIntent{Key: db.RatingIntentKey(buyer, shop, memo), Kind: "order", ID: id, UserID: "buyer", Buyer: buyer, Recipient: shop, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
}
func assertSaved(t *testing.T, s *db.Store, id, hash string) {
	t.Helper()
	st, err := s.GetRatingState("order", id, "buyer")
	if err != nil || !st.Rated || st.RatingTx != hash || st.Edits != 1 {
		t.Fatalf("rating not saved exactly once: %+v %v", st, err)
	}
}

func TestWalletMonitorRecordsReviewWithoutAnyBrowserSubmission(t *testing.T) {
	s := openStore(t, t.TempDir())
	defer func() { _ = s.Close() }()
	prepare(t, s, "o1", "5 excellent")
	tx := history(1, "5 excellent")
	rpc := &fakeRPC{history: []nimiq.Transaction{tx}, proofs: map[string]nimiq.TxDetail{tx.Hash: proof(tx, "5 excellent")}}
	w := Worker{Store: s, RPC: rpc, Recipient: shop}
	if _, err := w.Tick(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	assertSaved(t, s, "o1", tx.Hash)
	if err := s.SaveRatingChainInbox(db.RatingChainInbox{Hash: tx.Hash, Status: "pending", ChainAt: now}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Tick(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	assertSaved(t, s, "o1", tx.Hash)
	agg, err := s.GetRatingAggregate()
	if err != nil || agg.Count != 1 || agg.Sum != 5 {
		t.Fatalf("duplicate aggregate: %+v %v", agg, err)
	}
	if _, err := s.GetRatingIntent(db.RatingIntentKey(buyer, shop, "5 excellent")); !errors.Is(err, db.ErrNotFound) {
		t.Fatal("completed intent was not consumed atomically")
	}
}
func TestRestartResumesOlderHistoryPageAndFindsPaymentBeyondLatestHundred(t *testing.T) {
	path := t.TempDir()
	s := openStore(t, path)
	prepare(t, s, "o1", "4 reliable")
	rpc := &fakeRPC{proofs: map[string]nimiq.TxDetail{}}
	for i := 120; i >= 1; i-- {
		tx := history(i, "not a rating")
		rpc.history = append(rpc.history, tx)
	}
	tx := history(5, "4 reliable")
	rpc.history[115] = tx
	rpc.proofs[tx.Hash] = proof(tx, "4 reliable")
	w := Worker{Store: s, RPC: rpc, Recipient: shop}
	if more, err := w.Tick(context.Background(), now); err != nil || !more {
		t.Fatalf("first checkpoint: %v %v", more, err)
	}
	// A newer payment arrives while the older-history scan is checkpointed.
	newest := history(121, "5 latest")
	rpc.history = append([]nimiq.Transaction{newest}, rpc.history...)
	rpc.proofs[newest.Hash] = proof(newest, "5 latest")
	if err := s.SaveRatingIntent(db.RatingIntent{Key: db.RatingIntentKey(buyer, shop, "5 latest"), Kind: "order", ID: "o1", UserID: "buyer", Buyer: buyer, Recipient: shop, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s = openStore(t, path)
	defer func() { _ = s.Close() }()
	w.Store = s
	if _, err := w.Tick(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	assertSaved(t, s, "o1", tx.Hash)
	if len(rpc.cursors) != 2 || rpc.cursors[1] != fmt.Sprintf("%064x", 21) {
		t.Fatalf("scan did not resume: %v", rpc.cursors)
	}
	if _, err := w.Tick(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	st, err := s.GetRatingState("order", "o1", "buyer")
	if err != nil || st.Stars != 5 || st.Edits != 2 || st.RatingTx != newest.Hash {
		t.Fatalf("new arrival lost or revisions reordered: %+v %v", st, err)
	}
}
func TestUnmatchedVerifiedPaymentSurvivesRestartAndIsMatchedWithoutAnotherChainLookup(t *testing.T) {
	path := t.TempDir()
	s := openStore(t, path)
	tx := history(1, "5 excellent")
	rpc := &fakeRPC{history: []nimiq.Transaction{tx}, proofs: map[string]nimiq.TxDetail{tx.Hash: proof(tx, "5 excellent")}}
	w := Worker{Store: s, RPC: rpc, Recipient: shop}
	if _, err := w.Tick(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s = openStore(t, path)
	defer func() { _ = s.Close() }()
	w.Store = s
	prepare(t, s, "o1", "5 excellent")
	if _, err := w.Tick(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	assertSaved(t, s, "o1", tx.Hash)
	if rpc.detailCalls != 1 {
		t.Fatalf("verified proof fetched repeatedly: %d", rpc.detailCalls)
	}
}
func TestRPCOutageAndUnconfirmedPaymentRecoverWithoutUser(t *testing.T) {
	s := openStore(t, t.TempDir())
	defer func() { _ = s.Close() }()
	prepare(t, s, "o1", "5 excellent")
	tx := history(1, "5 excellent")
	rpc := &fakeRPC{fail: true, history: []nimiq.Transaction{tx}, proofs: map[string]nimiq.TxDetail{}}
	w := Worker{Store: s, RPC: rpc, Recipient: shop}
	if _, err := w.Tick(context.Background(), now); err == nil {
		t.Fatal("outage hidden")
	}
	rpc.fail = false
	if _, err := w.Tick(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	rpc.proofs[tx.Hash] = proof(tx, "5 excellent")
	if _, err := w.Tick(context.Background(), now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	assertSaved(t, s, "o1", tx.Hash)
}
func TestForgedHistoryOrFailedExecutionCannotRecordRating(t *testing.T) {
	for _, wrong := range []string{"sender", "execution", "memo", "hash"} {
		t.Run(wrong, func(t *testing.T) {
			s := openStore(t, t.TempDir())
			defer func() { _ = s.Close() }()
			prepare(t, s, "o1", "5 excellent")
			tx := history(1, "5 excellent")
			p := proof(tx, "5 excellent")
			switch wrong {
			case "sender":
				p.From = "NQ33222222222222222222222222222222222222"
			case "execution":
				p.Success = false
			case "memo":
				p.Data = []byte("4 different")
			case "hash":
				p.Hash = strings.Repeat("ab", 32)
			}
			rpc := &fakeRPC{history: []nimiq.Transaction{tx}, proofs: map[string]nimiq.TxDetail{tx.Hash: p}}
			w := Worker{Store: s, RPC: rpc, Recipient: shop}
			if _, err := w.Tick(context.Background(), now); err != nil {
				t.Fatal(err)
			}
			st, _ := s.GetRatingState("order", "o1", "buyer")
			if st.Rated {
				t.Fatal("unproven review recorded")
			}
		})
	}
}

func TestLegacyPaymentWithoutIntentRecoversOnlyUnambiguousRealPurchase(t *testing.T) {
	for _, ambiguous := range []bool{false, true} {
		t.Run(fmt.Sprint(ambiguous), func(t *testing.T) {
			s := openStore(t, t.TempDir())
			defer func() { _ = s.Close() }()
			if _, err := s.GetOrCreateUserByID("buyer", buyer); err != nil {
				t.Fatal(err)
			}
			delivered := now.Add(-time.Hour)
			for _, id := range []string{"real", "29760a02-9a43-40a2-8d5c-71277624df94"} {
				if err := s.CreateOrder(db.Order{ID: id, UserID: "buyer", Status: "delivered", CreatedAt: delivered, DeliveredAt: &delivered}); err != nil {
					t.Fatal(err)
				}
			}
			if ambiguous {
				if err := s.CreateOrder(db.Order{ID: "second-real", UserID: "buyer", Status: "delivered", CreatedAt: delivered, DeliveredAt: &delivered}); err != nil {
					t.Fatal(err)
				}
			}
			tx := history(1, "5 excellent")
			rpc := &fakeRPC{history: []nimiq.Transaction{tx}, proofs: map[string]nimiq.TxDetail{tx.Hash: proof(tx, "5 excellent")}}
			w := Worker{Store: s, RPC: rpc, Recipient: shop}
			if _, err := w.Tick(context.Background(), now); err != nil {
				t.Fatal(err)
			}
			st, _ := s.GetRatingState("order", "real", "buyer")
			if st.Rated == ambiguous {
				t.Fatalf("legacy attribution guessed or not recovered: %+v", st)
			}
		})
	}
}

func TestOldPaymentCannotConsumeANewRatingIntent(t *testing.T) {
	s := openStore(t, t.TempDir())
	defer func() { _ = s.Close() }()
	prepare(t, s, "o1", "5 excellent")
	tx := history(1, "5 excellent")
	tx.Timestamp = now.Add(-time.Hour).UnixMilli()
	rpc := &fakeRPC{history: []nimiq.Transaction{tx}, proofs: map[string]nimiq.TxDetail{tx.Hash: proof(tx, "5 excellent")}}
	w := Worker{Store: s, RPC: rpc, Recipient: shop}
	if _, err := w.Tick(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	st, _ := s.GetRatingState("order", "o1", "buyer")
	if st.Rated {
		t.Fatal("historical payment hijacked new intent")
	}
}

func TestBackgroundRunRecordsReviewWithNoHTTPRequest(t *testing.T) {
	s := openStore(t, t.TempDir())
	defer func() { _ = s.Close() }()
	prepare(t, s, "o1", "5 excellent")
	tx := history(1, "5 excellent")
	rpc := &fakeRPC{history: []nimiq.Transaction{tx}, proofs: map[string]nimiq.TxDetail{tx.Hash: proof(tx, "5 excellent")}}
	w := Worker{Store: s, RPC: rpc, Recipient: shop}
	ctx, cancel := context.WithCancel(context.Background())
	done := w.Run(ctx)
	defer func() { cancel(); <-done }()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		state, err := s.GetRatingState("order", "o1", "buyer")
		if err != nil {
			t.Fatal(err)
		}
		if state.Rated {
			assertSaved(t, s, "o1", tx.Hash)
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("background monitor did not record payment without a client")
}
