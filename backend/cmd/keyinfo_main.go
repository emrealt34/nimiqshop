package main

import (
	"crypto/ed25519"
	"encoding/hex"
	"fmt"
	"os"

	"nimiqshop/internal/nimiq"
)

// keyinfo prints what a wallet secret derives to. The argument is either a
// 32-byte hex seed, a 64-byte hex private key, or a 24-word BIP39 recovery
// phrase (quote it). Optional second argument: the Hub account index for
// phrases (default 0 → path m/44'/242'/0').
//
//	go run ./cmd/keyinfo 'word word … word'
//	go run ./cmd/keyinfo 112b19f361f8d25033c1337546ea1ff588713beb0dbd9afd89986f5436efa096
func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: keyinfo <hex-seed | 64-byte-hex-key | '24-word recovery phrase'> [account-index]")
		os.Exit(2)
	}
	value := os.Args[1]
	account := uint32(0)
	if len(os.Args) > 2 {
		var idx int
		if _, err := fmt.Sscanf(os.Args[2], "%d", &idx); err != nil || idx < 0 {
			fmt.Fprintln(os.Stderr, "invalid account index:", os.Args[2])
			os.Exit(2)
		}
		account = uint32(idx)
	}

	seedHex, derived, err := nimiq.ResolveWalletSecret(value, account)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	addr, err := nimiq.AddressFromKeyHex(seedHex)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	if derived {
		fmt.Printf("PHRASE=ok (BIP39, path m/44'/242'/%d')\n", account)
	}
	seed, _ := hex.DecodeString(seedHex)
	priv := ed25519.NewKeyFromSeed(seed)
	fmt.Println("SEED32=" + seedHex)
	fmt.Println("KEY64=" + hex.EncodeToString(priv))
	fmt.Println("PUB=" + hex.EncodeToString(priv.Public().(ed25519.PublicKey)))
	fmt.Println("ADDR=" + addr)
	if !derived {
		fmt.Println("(input was a hex seed/key — paste it into CASHBACK_WALLET_SEED as-is)")
	} else {
		fmt.Println("(you can paste the PHRASE itself into CASHBACK_WALLET_SEED — or SEED32 above)")
	}
}
