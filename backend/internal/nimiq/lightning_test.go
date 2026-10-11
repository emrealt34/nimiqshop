package nimiq

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func lightningFixture() (string, lightningTx, lightningTx) {
	secret := make([]byte, 32)
	sum := sha256.Sum256(secret)
	hash := hex.EncodeToString(sum[:])
	data := make([]byte, 82)
	data[40] = 3
	copy(data[41:73], sum[:])
	data[73] = 1
	proof := append([]byte{0, 1, 3}, sum[:]...)
	proof = append(proof, 32)
	proof = append(proof, secret...)
	proof = append(proof, 0)
	funding := lightningTx{Hash: strings.Repeat("a", 64), From: "buyer", To: "contract", ToType: 2, NetworkID: 24, BlockNumber: 100, Success: true, Data: hex.EncodeToString(data)}
	redeemed := lightningTx{Hash: strings.Repeat("b", 64), From: "contract", FromType: 2, NetworkID: 24, BlockNumber: 101, Success: true, Proof: hex.EncodeToString(proof)}
	return hash, funding, redeemed
}

func TestLightningProofRequiresExecutedMainnetInvoiceBoundHTLC(t *testing.T) {
	hash, funding, redeem := lightningFixture()
	if !fundingMatches(funding, hash) || redemptionPreimage(redeem, hash, funding.To) != strings.Repeat("0", 64) {
		t.Fatal("valid atomic swap proof rejected")
	}
	for _, mutate := range []func(*lightningTx){
		func(x *lightningTx) { x.Success = false }, func(x *lightningTx) { x.NetworkID = 5 },
		func(x *lightningTx) { x.BlockNumber = 0 }, func(x *lightningTx) { x.ToType = 0 },
		func(x *lightningTx) { x.Data = x.Data[:80] + "01" + x.Data[82:] },
		func(x *lightningTx) { x.Data = "00" },
	} {
		tx := funding
		mutate(&tx)
		if fundingMatches(tx, hash) {
			t.Fatal("invalid funding accepted")
		}
	}
	if fundingMatches(funding, strings.Repeat("c", 64)) {
		t.Fatal("other invoice accepted")
	}
	for _, mutate := range []func(*lightningTx){
		func(x *lightningTx) { x.Success = false }, func(x *lightningTx) { x.NetworkID = 5 },
		func(x *lightningTx) { x.BlockNumber = 0 }, func(x *lightningTx) { x.From = "unrelated" },
		func(x *lightningTx) { x.FromType = 0 }, func(x *lightningTx) { x.Proof = "01" + x.Proof[2:] },
		func(x *lightningTx) { x.Proof = x.Proof[:72] + strings.Repeat("1", 64) + x.Proof[136:] },
		func(x *lightningTx) { x.Proof = x.Proof[:10] },
	} {
		tx := redeem
		mutate(&tx)
		if redemptionPreimage(tx, hash, funding.To) != "" {
			t.Fatal("refund, wrong chain or invalid secret accepted")
		}
	}
}

func proofRPC(t *testing.T, funding, redeem lightningTx) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Method string            `json:"method"`
			Params []json.RawMessage `json:"params"`
		}
		if json.NewDecoder(r.Body).Decode(&in) != nil {
			t.Error("invalid request")
			return
		}
		var result any
		var key string
		_ = json.Unmarshal(in.Params[0], &key)
		if in.Method == "getTransactionByHash" {
			if key == funding.Hash {
				result = funding
			} else {
				result = redeem
			}
		} else {
			if len(in.Params) != 3 || string(in.Params[2]) != "null" {
				t.Error("first-page cursor must be null")
			}
			if key == funding.To {
				result = []lightningTx{redeem}
			} else {
				result = []lightningTx{funding}
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{"data": result, "metadata": map[string]any{}}})
	}))
}
func TestFindLightningProofCrossChecksAndSeparatesFundingFromSettlement(t *testing.T) {
	hash, funding, redeem := lightningFixture()
	a := proofRPC(t, funding, redeem)
	defer a.Close()
	b := proofRPC(t, funding, redeem)
	defer b.Close()
	c := NewClient(a.URL, b.URL)
	p, err := c.FindLightningProof(context.Background(), hash, "buyer", "")
	if err != nil || p == nil || p.FundingTx != funding.Hash || p.SettlementTx != redeem.Hash || p.Preimage != strings.Repeat("0", 64) {
		t.Fatalf("proof: %#v %v", p, err)
	}
	if _, err = c.FindLightningProof(context.Background(), strings.Repeat("c", 64), "buyer", funding.Hash); err == nil {
		t.Fatal("unrelated client hint accepted")
	}
	redeem.Success = false
	d := proofRPC(t, funding, redeem)
	defer d.Close()
	p, err = NewClient(d.URL).FindLightningProof(context.Background(), hash, "buyer", funding.Hash)
	if err != nil || p == nil || p.Preimage != "" || p.SettlementTx != "" {
		t.Fatal("funding mistaken for settlement")
	}
	funding.Success = false
	e := proofRPC(t, funding, redeem)
	defer e.Close()
	if _, err = NewClient(a.URL, e.URL).FindLightningProof(context.Background(), hash, "buyer", strings.Repeat("a", 64)); err == nil {
		t.Fatal("disagreeing RPCs accepted")
	}
}
