package poolstake

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// Cross-repository contract test: the shop's client against a REAL running
// NimiqBase pool (https://github.com/emrealt34/NimiqBase, pool/). It is
// skipped unless a live pool is pointed at, because a unit fixture can only
// prove what the shop believes the pool says:
//
//	cd ../nimiqbase/pool && GPOOL_FEED_API_KEY=… ./nimiqbase
//	POOL_LIVE_URL=http://127.0.0.1:8080 POOL_LIVE_FEED_KEY=… \
//	  go test ./internal/poolstake/ -run TestLivePoolContract -v
//
// Everything the cashback programme depends on is asserted here: the staker
// base the shop applies verbatim, the X-Feed-Key handshake on
// /api/cashback/profit, the "never delegated here" 404, and the period
// validation that keeps a bad window from being credited.
func TestLivePoolContract(t *testing.T) {
	base := strings.TrimSpace(os.Getenv("POOL_LIVE_URL"))
	if base == "" {
		t.Skip("POOL_LIVE_URL not set: skipping the live NimiqBase contract test")
	}
	key := strings.TrimSpace(os.Getenv("POOL_LIVE_FEED_KEY"))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	client := New(base, 5*time.Second, 0)

	t.Run("terms advertise the published staker base", func(t *testing.T) {
		terms, err := client.Terms(ctx)
		if err != nil {
			t.Fatalf("Terms: %v", err)
		}
		if terms.StakerBaseBps != DefaultStakerBaseBps {
			t.Errorf("StakerBaseBps = %d, want the pool default %d", terms.StakerBaseBps, DefaultStakerBaseBps)
		}
		if want := float64(terms.StakerBaseBps) / 100; terms.StakerBasePercent != want {
			t.Errorf("StakerBasePercent = %v, want %v", terms.StakerBasePercent, want)
		}
		if terms.Rule != "any_positive_stake" {
			t.Errorf("Rule = %q, want any_positive_stake", terms.Rule)
		}
		if terms.PoolFeePercentage < 0 || terms.PoolFeePercentage >= 1 {
			t.Errorf("PoolFeePercentage = %v, want a fraction in [0,1)", terms.PoolFeePercentage)
		}
	})

	t.Run("an address that never delegated is not an error", func(t *testing.T) {
		status, err := client.Stake(ctx, "NQ07 0000 0000 0000 0000 0000 0000 0000 0000")
		if err != nil {
			t.Fatalf("Stake on an unknown address must be a clean 404, got %v", err)
		}
		if status.Staked || status.StakeLuna != 0 || status.BaseBps != 0 {
			t.Fatalf("unknown address must be unstaked with base 0: %+v", status)
		}
	})

	t.Run("profit requires the feed key", func(t *testing.T) {
		if key == "" {
			t.Skip("POOL_LIVE_FEED_KEY not set")
		}
		address := "NQ07 0000 0000 0000 0000 0000 0000 0000 0000"

		locked := New(base, 5*time.Second, 0)
		if _, err := locked.Profit(ctx, address, "this_month"); err == nil {
			t.Fatal("without a feed key the pool must refuse the profit read")
		}

		client.SetFeedKey(key)
		profit, err := client.Profit(ctx, address, "this_month")
		if err != nil {
			t.Fatalf("Profit with the shared key: %v", err)
		}
		if profit.PoolFeeLuna < 0 || profit.StakeLuna < 0 {
			t.Fatalf("negative amounts from the pool: %+v", profit)
		}
		if profit.Period != "this_month" {
			t.Errorf("Period = %q, want this_month", profit.Period)
		}
		if profit.LoyaltyMultiplier < 0 || profit.LoyaltyMultiplier > 1 {
			t.Errorf("LoyaltyMultiplier = %v, want a factor in [0,1]", profit.LoyaltyMultiplier)
		}
		if profit.From == "" || profit.To == "" {
			t.Errorf("missing window bounds: %+v", profit)
		}
		// The store keys ledgers by the canonical address, so the round trip
		// has to survive the pool's spaced form.
		if CanonicalAddress(profit.Address) != CanonicalAddress(address) {
			t.Errorf("address round trip: %q -> %q", address, profit.Address)
		}

		if _, err := client.Profit(ctx, address, "last_century"); err == nil {
			t.Error("an invalid period must be refused, not credited on a guessed window")
		}
	})
}
