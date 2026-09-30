// cashback-test — queue a REAL cashback payout for the worker to send.
//
// The admin test center only produces SIMULATED (TestMode) rows that never
// touch the wallet. To validate the real signing/broadcast path — e.g. on
// the Nimiq testnet — this tool inserts a genuine queued row into the same
// Badger store the server uses. Start the server afterwards and watch the
// cashback worker pay it: sign → persist hex → pushTransaction → confirm.
//
// Usage (server must be STOPPED — Badger takes an exclusive lock):
//
//	cd backend
//	go run ./cmd/cashback-test -to "NQ…" -nim 0.001
//	./bin/nimshop-server            # then watch the cashback: logs
//
// Flags:
//
//	-to    recipient Nimiq address (required)
//	-nim   amount in NIM (default 0.001 = 100 luna)
//	-memo  optional recipient_data memo (default: a cashback-style memo)
//	-data  Badger directory (default ./data/badger, same as the server)
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"nimiqshop/internal/cashback"
	"nimiqshop/internal/db"
)

func main() {
	to := flag.String("to", "", "recipient Nimiq address (NQ… )")
	nim := flag.Float64("nim", 0.001, "amount in NIM (min 0.00001)")
	memo := flag.String("memo", "", "recipient_data memo (default: cashback-style memo)")
	data := flag.String("data", "./data/badger", "Badger directory (same as BADGER_DIR)")
	flag.Parse()

	if *to == "" {
		fmt.Fprintln(os.Stderr, "usage: cashback-test -to \"NQ… \" [-nim 0.001] [-data ./data/badger]")
		os.Exit(2)
	}
	luna := int64(*nim * 100_000)
	if luna < 1 {
		log.Fatalf("-nim too small: %v NIM is %d luna (min 1 luna = 0.00001 NIM)", *nim, luna)
	}
	m := *memo
	if m == "" {
		m = cashback.Memo(cashback.NIMFromLuna(luna), "manual-payout")
	}

	store, err := db.New(*data)
	if err != nil {
		log.Fatalf("open store: %v (is the server still running? stop it first — Badger is single-writer)", err)
	}
	defer func() { _ = store.Close() }()

	cb, err := store.QueueManualCashback(*to, m, luna, time.Now())
	if err != nil {
		log.Fatalf("queue cashback: %v", err)
	}
	fmt.Printf("queued REAL cashback payout:\n")
	fmt.Printf("  id        : %s\n", cb.ID)
	fmt.Printf("  recipient : %s\n", cb.Recipient)
	fmt.Printf("  amount    : %d luna (%.5f NIM)\n", cb.AmountLuna, cashback.NIMFromLuna(cb.AmountLuna))
	fmt.Printf("  memo      : %q\n", cb.Memo)
	fmt.Printf("  status    : %s (test_mode=%v)\n", cb.Status, cb.TestMode)
	fmt.Printf("\nNow start the server (./bin/nimshop-server) and watch it pay.\n")
}
