package chainstake

import (
	"context"
	"errors"
	"nimiqshop/internal/nimiq"
	"sync/atomic"
	"testing"
)

func TestPositionPreservesAllBalancesForOtherValidator(t *testing.T) {
	var calls int64
	v := verifierFor(t, map[string]any{"balance": 20000000, "inactiveBalance": 3000000, "retiredBalance": 4000000, "inactiveFrom": 123, "delegation": otherVal}, "", &calls)
	got, err := v.Verify(context.Background(), buyerAddr)
	if err != nil || !got.Exists || got.Staked || !got.Elsewhere || got.ActiveLuna != 20000000 || got.InactiveLuna != 3000000 || got.RetiredLuna != 4000000 || got.InactiveFrom == nil || *got.InactiveFrom != 123 {
		t.Fatalf("position lost: %+v error=%v", got, err)
	}
	if _, err := v.Verify(context.Background(), buyerAddr); err != nil {
		t.Fatal(err)
	}
	if atomic.LoadInt64(&calls) != 1 {
		t.Fatal("position did not use bounded cache")
	}
}
func TestRetiredOnlyPositionIsNotANewStaker(t *testing.T) {
	var calls int64
	v := verifierFor(t, map[string]any{"balance": 0, "retiredBalance": 4000000, "delegation": otherVal}, "", &calls)
	got, err := v.Verify(context.Background(), buyerAddr)
	if err != nil || !got.Exists || got.Staked || got.RetiredLuna != 4000000 {
		t.Fatalf("retired position lost: %+v %v", got, err)
	}
}
func TestOnlyNoStakerRPCErrorMeansNoRecord(t *testing.T) {
	if isNoStaker(errors.New("HTTP resource not found")) {
		t.Fatal("HTTP outage treated as no staker")
	}
	if isNoStaker(&nimiq.RPCError{Code: -32601, Message: "method not found"}) {
		t.Fatal("unsupported method treated as no staker")
	}
	if !isNoStaker(&nimiq.RPCError{Code: -32004, Message: "No staker found for the given address"}) {
		t.Fatal("missing staker not recognized")
	}
}
