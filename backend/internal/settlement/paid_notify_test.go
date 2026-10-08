package settlement

import (
	"testing"

	"nimiqshop/internal/cryptorefills"
)

// The payment-confirmed memo fires on the first transition into a paid status,
// including a jump that skips payment_received (straight to delivering or done).
// It never fires on a repeat observation or on a non-paid status.
func TestShouldNotifyPaid(t *testing.T) {
	cases := []struct {
		name    string
		changed bool
		status  string
		want    bool
	}{
		{"received", true, cryptorefills.StatusPaymentReceived, true},
		{"delivering", true, cryptorefills.StatusWaitingForDelivery, true},
		{"done straight from awaiting (skips received)", true, cryptorefills.StatusDone, true},
		{"repeat of received", false, cryptorefills.StatusPaymentReceived, false},
		{"repeat of done", false, cryptorefills.StatusDone, false},
		{"waiting for payment", true, cryptorefills.StatusWaitingForPayment, false},
		{"payment started (not yet confirmed)", true, cryptorefills.StatusPaymentStarted, false},
		{"partial payment started", true, cryptorefills.StatusPartialPaymentStarted, false},
		{"expired", true, cryptorefills.StatusExpired, false},
		{"payment failed", true, cryptorefills.StatusPaymentFailed, false},
		{"refunded", true, cryptorefills.StatusRefunded, false},
		{"manual review", true, cryptorefills.StatusWaitingForManual, false},
	}
	for _, c := range cases {
		if got := shouldNotifyPaid(c.changed, c.status); got != c.want {
			t.Errorf("%s: shouldNotifyPaid(%v, %q) = %v, want %v", c.name, c.changed, c.status, got, c.want)
		}
	}
}
