package handlers

import (
	"strings"
	"testing"

	"nimiqshop/internal/sampleassets"
)

func TestBuyerIdenticonAcceptedForPurchaseAndGift(t *testing.T) {
	avatar := sampleassets.IdenticonDataURI()
	for _, channel := range []string{"", "email"} {
		if got := normalizeGifterIdenticon(avatar, false, channel); got != avatar {
			t.Fatalf("channel %q: normal buyer avatar was discarded", channel)
		}
		if got := normalizeGifterIdenticon(avatar, true, channel); got != "" {
			t.Fatalf("channel %q: anonymous order disclosed its avatar", channel)
		}
	}
	for _, invalid := range []string{"https://tracker.invalid/avatar", "data:image/svg+xml;base64,PHN2Zz4=", "data:image/png;base64,bad", "data:image/png;base64," + strings.Repeat("A", 70_000)} {
		if normalizeGifterIdenticon(invalid, false, "") != "" {
			t.Fatal("invalid avatar admitted")
		}
	}
}
