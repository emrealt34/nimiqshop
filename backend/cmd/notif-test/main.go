// Command notif-test validates the 1-Luna notification send against YOUR Nimiq
// node before enabling it in production.
//
// Run it with a funded key + your RPC:
//
//	go run ./cmd/notif-test \
//	    -to   "NQ07 2FBV ..." \
//	    -key  <32-byte-hex-private-key-seed> \
//	    -rpc  https://your-nimiq-rpc \
//	    -network mainnet \
//	    -memo "nim.shop notification test"
//
// If the network ACCEPTS the tx and 1 Luna + the memo lands in the destination
// wallet, notifications are safe to enable (NOTIFICATION_ENABLED=true). If the
// RPC rejects the tx, the exact transaction serialization in
// internal/nimiq/sign.go is what to adjust — the rest of the pipeline is correct.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"time"

	"nimiqshop/internal/nimiq"
)

func main() {
	to := flag.String("to", "", "recipient user-friendly address (NQ...)")
	key := flag.String("key", "", "32-byte hex Ed25519 private-key seed")
	rpc := flag.String("rpc", "https://rpc.nimiqwatch.com", "Nimiq JSON-RPC URL")
	network := flag.String("network", "mainnet", "mainnet | testnet")
	memo := flag.String("memo", "nim.shop notification test", "memo text (max 64 bytes)")
	fee := flag.Int("fee", 1, "transaction fee in Luna")
	flag.Parse()

	if *to == "" || *key == "" {
		log.Fatal("-to and -key are required")
	}

	netID := byte(nimiq.NetworkMainnet)
	if *network == "testnet" {
		netID = byte(nimiq.NetworkTestnet)
	}

	c := nimiq.NewClient(*rpc)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	height, err := c.GetBlockNumber(ctx)
	if err != nil {
		log.Fatalf("block number: %v", err)
	}
	fmt.Println("head height:", height)

	txHex, err := nimiq.BuildBasicTransaction(*key, *to, 1, int64(*fee), uint32(height), netID, []byte(*memo))
	if err != nil {
		log.Fatalf("build/sign: %v", err)
	}
	fmt.Println("signed tx hex:", txHex)

	hash, err := c.BroadcastRawTransaction(ctx, txHex)
	if err != nil {
		log.Fatalf("broadcast REJECTED: %v\n\n"+
			"The signing/serialization in internal/nimiq/sign.go likely needs an\n"+
			"adjustment (field order, network-id placement, proof layout). The rest\n"+
			"of the pipeline (idempotency, worker hook, RPC) is independent of it.", err)
	}
	fmt.Println("broadcast OK — tx hash:", hash)
	fmt.Println("Check the destination wallet for 1 Luna + the memo. If it lands,")
	fmt.Println("enable notifications with NOTIFICATION_ENABLED=true.")
}
