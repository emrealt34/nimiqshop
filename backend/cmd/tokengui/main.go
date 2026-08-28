package main

import (
	"fmt"
	"os"

	"nimiqshop/internal/auth"
)

// tokengui issues a customer JWT the same way a completed Nimiq Hub login
// would, so the live API can be exercised end-to-end from a script.
func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: tokengui <jwt-secret> <nimiq-address>")
		os.Exit(2)
	}
	tok, err := auth.IssueToken(os.Args[1], os.Args[2], 60)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Print(tok)
}
