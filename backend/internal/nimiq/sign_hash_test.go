package nimiq

import (
	"strconv"
	"testing"
)

// TestBuildBasicTransactionReturnsCanonicalHash pins the transaction hash that
// BuildBasicTransaction returns. The value is Blake2b-256 of SerializeContent —
// verified live against a Nimiq node (rpc.testnet.nimiqwatch.com), where the
// hash the node reports from sendRawTransaction equals this exact value.
//
// The cashback payout worker persists this hash BEFORE broadcasting, so that a
// crash after "sent" but before the node's reply is recorded can still confirm
// the payout by hash (GetTransactionByHash) instead of re-sending blind. If the
// serialization ever drifts, that recovery would silently break — this golden
// vector fails loudly first.
func TestBuildBasicTransactionReturnsCanonicalHash(t *testing.T) {
	v := vectors(t)
	seed, to, wantTx := v["tx.seed"], v["tx.to"], v["tx.hash"]
	wantLen, err := strconv.Atoi(v["tx.wire_len"])
	if err != nil {
		t.Fatal(err)
	}
	hexTx, hash, err := BuildBasicTransaction(seed, to, 2083000, 1, 1000000, byte(NetworkTestnet), []byte("golden"))
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if hash != wantTx {
		t.Errorf("canonical hash drifted:\n got  %s\n want %s", hash, wantTx)
	}
	if len(hexTx) != wantLen {
		t.Errorf("wire length = %d, want %d", len(hexTx), wantLen)
	}

	// The hash must be deterministic for identical inputs (crash recovery relies
	// on re-deriving the very same hash).
	_, hash2, _ := BuildBasicTransaction(seed, to, 2083000, 1, 1000000, byte(NetworkTestnet), []byte("golden"))
	if hash2 != hash {
		t.Errorf("hash not deterministic: %s vs %s", hash, hash2)
	}
}
