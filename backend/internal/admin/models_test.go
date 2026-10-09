package admin

import (
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"nimiqshop/internal/stakeledger"
)

func TestSettingsSafeRateBoundsAndLegacyLadderIsolation(t *testing.T) {
	for _, tc := range []struct {
		input *int
		want  int
	}{{nil, 0}, {ptr(-1), 0}, {ptr(75), 75}, {ptr(9999), MaxCashbackBps}} {
		if got := (Settings{CashbackBps: tc.input}).EffectiveCashbackBps(); got != tc.want {
			t.Fatalf("base rate=%d want=%d", got, tc.want)
		}
	}
	for _, tc := range []struct {
		input *int
		want  int
	}{{nil, DefaultStakerCashbackBps}, {ptr(-1), 0}, {ptr(125), 125}, {ptr(9999), MaxCashbackBps}} {
		if got := (Settings{StakerBaseBps: tc.input}).EffectiveStakerCashbackBps(); got != tc.want {
			t.Fatalf("staker base=%d want=%d", got, tc.want)
		}
	}
	if got := (Settings{}).EffectiveStakeCashback(); !reflect.DeepEqual(got, stakeledger.Normalize(stakeledger.Defaults)) {
		t.Fatalf("default ledger params=%+v", got)
	}
	params := stakeledger.Defaults
	if got := (Settings{StakeCashback: &params}).EffectiveStakeCashback(); !reflect.DeepEqual(got, stakeledger.Normalize(params)) {
		t.Fatalf("configured ledger params=%+v", got)
	}
	settings := Settings{CashbackBps: ptr(50), StakerCashbackTiers: []StakerTier{{0, 100}, {100, 300}, {200, 2000}}, StakerLoyalty: []StakerLoyaltyLock{{30, 1000}}}
	for _, tc := range []struct {
		stake  int64
		staked bool
		rate   int
		boost  bool
	}{{0, false, 50, false}, {0, true, 50, false}, {1, true, 100, true}, {100, true, 300, true}} {
		rate, _, boost := settings.CashbackBpsForBuyer(tc.stake, tc.staked)
		if rate != tc.rate || boost != tc.boost {
			t.Fatalf("legacy resolution %+v: %d %t", tc, rate, boost)
		}
	}
	highBase := Settings{CashbackBps: ptr(200), StakerCashbackTiers: []StakerTier{{0, 100}}}
	if rate, _, boost := highBase.CashbackBpsForBuyer(1, true); rate != 200 || boost {
		t.Fatalf("staker reduced base: %d %t", rate, boost)
	}
	if rate, _, bonus, boost := settings.CashbackBpsForBuyerEffective(1000000, true, 365); rate != 50 || bonus != 0 || boost {
		t.Fatalf("legacy bonuses leaked into v2: %d %d %t", rate, bonus, boost)
	}
	raw := Settings{StakerCashbackTiers: []StakerTier{{-1, 100}, {0, -1}, {100, 5000}}}
	if rate, _, ok := raw.StakerCashbackBps(1); ok || rate != 0 {
		t.Fatalf("unmet/invalid rung accepted: %d %t", rate, ok)
	}
	if rate, _, ok := raw.StakerCashbackBps(100); !ok || rate != MaxCashbackBps {
		t.Fatalf("uncapped rung: %d %t", rate, ok)
	}
}
func ptr(v int) *int { return &v }

func TestAdminTierAndLoyaltyNormalization(t *testing.T) {
	tiers := []StakerTier{{-1, 20}, {0, 0}, {10, 100}, {10, 200}, {10, 50}, {20, 9999}}
	got := NormalizeStakerTiers(tiers)
	want := []StakerTier{{10, 200}, {20, MaxCashbackBps}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("tiers=%+v", got)
	}
	for i := 0; i < 20; i++ {
		tiers = append(tiers, StakerTier{int64(i + 100), 100})
	}
	if capped := NormalizeStakerTiers(tiers); len(capped) != MaxStakerTiers || capped[0].MinStakeLuna != 10 {
		t.Fatalf("wrong capped ladder: %+v", capped)
	}
	if len((Settings{}).EffectiveStakerTiers()) != len(DefaultStakerTiers) {
		t.Fatal("default ladder missing")
	}
	if !reflect.DeepEqual((Settings{StakerCashbackTiers: want}).EffectiveStakerTiers(), want) {
		t.Fatal("configured ladder lost")
	}
	locks := []StakerLoyaltyLock{{0, 100}, {10, 0}, {30, 100}, {30, 200}, {30, 50}, {60, 9999}}
	normalized := NormalizeStakerLoyalty(locks)
	if !reflect.DeepEqual(normalized, []StakerLoyaltyLock{{30, 200}, {60, MaxCashbackBps}}) {
		t.Fatalf("locks=%+v", normalized)
	}
	for i := 0; i < 20; i++ {
		locks = append(locks, StakerLoyaltyLock{i + 100, 100})
	}
	if capped := NormalizeStakerLoyalty(locks); len(capped) != MaxStakerLoyalty || capped[0].LockDays != 30 {
		t.Fatalf("wrong capped locks: %+v", capped)
	}
	if len((Settings{}).EffectiveStakerLoyalty()) != len(DefaultStakerLoyalty) {
		t.Fatal("default loyalty missing")
	}
	settings := Settings{StakerLoyalty: normalized}
	if rate, lock, ok := settings.LoyaltyBonusBps(45); !ok || rate != 200 || lock.LockDays != 30 {
		t.Fatalf("loyalty boundary: %d %+v %t", rate, lock, ok)
	}
	if _, _, ok := settings.LoyaltyBonusBps(0); ok {
		t.Fatal("unearned loyalty granted")
	}
	if next, ok := settings.NextLoyaltyLock(30); !ok || next.LockDays != 60 {
		t.Fatalf("next lock: %+v %t", next, ok)
	}
	if _, ok := settings.NextLoyaltyLock(1000); ok {
		t.Fatal("completed ladder still has next rung")
	}
}

func TestAdminPromoNormalizationPreservesCapsAndRestrictions(t *testing.T) {
	expires := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	rules := []CashbackCodeRule{
		{Code: " z-code ", CashbackBps: 100, MaxOrderUSD: 50, OnlyCategories: []string{"games"}, OnlyKinds: []string{"giftcard"}, OnlyCountries: []string{"TR"}, MaxUsesTotal: 10, MaxUsesPerUser: 1, MaxTotalCashbackUSD: 25, ExpiresAt: expires},
		{Code: "B-CODE", CashbackBps: 200},
		{Code: "A_CODE", CashbackBps: 200},
	}
	got, err := NormalizeCashbackCodeRules(rules)
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Code != "A_CODE" || got[1].Code != "B-CODE" || got[2].Code != "Z-CODE" {
		t.Fatalf("ordering=%+v", got)
	}
	expected := rules[0]
	expected.Code = "Z-CODE"
	if !reflect.DeepEqual(got[2], expected) {
		t.Fatalf("operator restrictions were lost: %+v", got[2])
	}
	if (Settings{}).EffectiveCashbackCodeRules() != nil {
		t.Fatal("legacy nil became configured")
	}
	if effective := (Settings{CashbackCodeRules: &rules}).EffectiveCashbackCodeRules(); len(effective) != 3 {
		t.Fatal("valid rules missing")
	}
	empty := []CashbackCodeRule{}
	if effective := (Settings{CashbackCodeRules: &empty}).EffectiveCashbackCodeRules(); effective == nil || len(effective) != 0 {
		t.Fatal("intentional disable became legacy fallback")
	}
	invalid := []CashbackCodeRule{{Code: "bad!", CashbackBps: 100}}
	if effective := (Settings{CashbackCodeRules: &invalid}).EffectiveCashbackCodeRules(); effective != nil {
		t.Fatal("invalid persisted rules accepted")
	}
}

func TestAdminPromoInvalidValuesAreRejectedNotSilentlyClamped(t *testing.T) {
	baseline := CashbackCodeRule{Code: "VALID", CashbackBps: 100}
	changes := []func(*CashbackCodeRule){
		func(r *CashbackCodeRule) { r.Code = "" }, func(r *CashbackCodeRule) { r.Code = "A" }, func(r *CashbackCodeRule) { r.Code = strings.Repeat("A", 41) }, func(r *CashbackCodeRule) { r.Code = "bad!" },
		func(r *CashbackCodeRule) { r.CashbackBps = 0 }, func(r *CashbackCodeRule) { r.CashbackBps = MaxCashbackBps + 1 },
		func(r *CashbackCodeRule) { r.MaxOrderUSD = -1 }, func(r *CashbackCodeRule) { r.MaxOrderUSD = math.NaN() }, func(r *CashbackCodeRule) { r.MaxOrderUSD = math.Inf(1) }, func(r *CashbackCodeRule) { r.MaxOrderUSD = 1000001 },
		func(r *CashbackCodeRule) { r.MaxUsesTotal = -1 }, func(r *CashbackCodeRule) { r.MaxUsesTotal = 100000001 }, func(r *CashbackCodeRule) { r.MaxUsesPerUser = -1 }, func(r *CashbackCodeRule) { r.MaxUsesPerUser = 1000001 },
		func(r *CashbackCodeRule) { r.MaxTotalCashbackUSD = math.NaN() }, func(r *CashbackCodeRule) { r.MaxTotalCashbackUSD = math.Inf(1) }, func(r *CashbackCodeRule) { r.MaxTotalCashbackUSD = -1 }, func(r *CashbackCodeRule) { r.MaxTotalCashbackUSD = 100000001 },
		func(r *CashbackCodeRule) { r.ExpiresAt = time.Date(1999, 1, 1, 0, 0, 0, 0, time.UTC) },
	}
	for i, change := range changes {
		r := baseline
		change(&r)
		if _, err := NormalizeCashbackCodeRules([]CashbackCodeRule{r}); err == nil {
			t.Fatalf("invalid rule %d accepted: %+v", i, r)
		}
	}
	if _, err := NormalizeCashbackCodeRules([]CashbackCodeRule{baseline, baseline}); err == nil {
		t.Fatal("duplicate code accepted")
	}
	if _, err := NormalizeCashbackCodeRules(make([]CashbackCodeRule, MaxCashbackCodeRules+1)); err == nil {
		t.Fatal("unbounded promo table accepted")
	}
}
