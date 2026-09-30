package cashback

import (
	"math"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestAmountLunaBoundaries(t *testing.T) {
	for _, tc := range []struct {
		nim  float64
		bps  int
		want int64
	}{
		{0, 100, 0}, {-1, 100, 0}, {1, 0, 0}, {1, -1, 0}, {math.NaN(), 100, 0},
		{math.Inf(1), 100, 0}, {math.Inf(-1), 100, 0}, {0.00001, 1, 0},
		{1, 100, 1000}, {12.34, 100, 12340}, {100, 50, 50000}, {math.MaxFloat64, 100, 0},
	} {
		if got := AmountLuna(tc.nim, tc.bps); got != tc.want {
			t.Errorf("AmountLuna(%v,%d)=%d want %d", tc.nim, tc.bps, got, tc.want)
		}
	}
	if NIMFromLuna(123456) != 1.23456 {
		t.Fatal("Luna conversion")
	}
	for _, tc := range []struct {
		n    float64
		want string
	}{{0, "0"}, {1, "1"}, {12.34, "12.34"}, {1.5, "1.5"}, {100, "100"}, {123.4, "123"}} {
		if got := formatNIM(tc.n); got != tc.want {
			t.Errorf("format %v: %q", tc.n, got)
		}
	}
}

func TestMemoByteLimitsAndUnicode(t *testing.T) {
	old := ShopName
	t.Cleanup(func() { ShopName = old })
	for length := 0; length < 100; length++ {
		ShopName = strings.Repeat("s", length)
		for _, product := range []string{"", "item", "gift\ncard", strings.Repeat("🌳", 100), strings.Repeat("a", 200)} {
			for _, makeMemo := range []func(float64, string) string{Memo, TreeMemo} {
				got := makeMemo(12.34, product)
				if len(got) > MaxMemoBytes || !utf8.ValidString(got) || strings.Contains(got, "\n") || got == "" {
					t.Errorf("invalid memo for host length %d: %q (%d bytes)", length, got, len(got))
				}
			}
		}
	}
	ShopName = " "
	if shop() != "shop.nimiqbase.com" {
		t.Fatal("default shop")
	}
	ShopName = " shop.example "
	if shop() != "shop.example" || !strings.Contains(Memo(1, "gift"), "shop.example") {
		t.Fatal("configured shop")
	}
	for _, tc := range []struct {
		s    string
		n    int
		want string
	}{{"hello", 0, ""}, {"hello", -1, ""}, {"hello", 10, "hello"}, {"🌳tree", 1, ""}, {"🌳tree", 4, "🌳"}, {"abc", 2, "ab"}} {
		if got := clipBytes(tc.s, tc.n); got != tc.want {
			t.Errorf("clip(%q,%d)=%q want %q", tc.s, tc.n, got, tc.want)
		}
	}
}
