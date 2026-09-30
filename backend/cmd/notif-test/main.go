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
//	    -memo "shop.nimiqbase.com notification test"
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
	key := flag.String("key", "", "32-byte hex Ed25519 private-key seed OR a 24-word BIP39 recovery phrase")
	rpc := flag.String("rpc", "https://rpc.nimiqwatch.com", "Nimiq JSON-RPC URL")
	rpc2 := flag.String("rpc2", "https://rpc-mainnet.nimiqscan.com", "Second Nimiq JSON-RPC URL (cross-check; empty to disable)")
	network := flag.String("network", "mainnet", "mainnet | testnet")
	memo := flag.String("memo", "shop.nimiqbase.com notification test", "memo text (max 64 bytes)")
	fee := flag.Int("fee", 1, "transaction fee in Luna")
	flag.Parse()

	if *to == "" || *key == "" {
		log.Fatal("-to and -key are required")
	}
	// Accept a 24-word recovery phrase in -key as well: derive it the way
	// the Nimiq Hub does before building the transaction.
	keySeed, _, err := nimiq.ResolveWalletSecret(*key, 0)
	if err != nil {
		log.Fatalf("-key: %v", err)
	}

	netID := byte(nimiq.NetworkMainnet)
	if *network == "testnet" {
		netID = byte(nimiq.NetworkTestnet)
	}

	// Both URLs: the second is the cross-check endpoint the flag documents.
	c := nimiq.NewClient(*rpc, *rpc2)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	height, err := c.GetBlockNumber(ctx)
	if err != nil {
		log.Fatalf("block number: %v", err)
	}
	fmt.Println("head height:", height)

	txHex, _, err := nimiq.BuildBasicTransaction(keySeed, *to, 1, int64(*fee), uint32(height), netID, []byte(*memo))
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
