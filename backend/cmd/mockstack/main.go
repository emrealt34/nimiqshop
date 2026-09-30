// Command mockstack runs the deterministic CryptoRefills stand-in from
// internal/suppliermock as a standalone loopback process (MOCK_CR_PORT,
// default 9020) for the local dev stack and the end-to-end tests.
package main

import "nimiqshop/internal/suppliermock"

func main() { suppliermock.Serve() }
