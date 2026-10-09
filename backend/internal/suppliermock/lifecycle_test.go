package suppliermock

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestMockDeliveryChannelsRemainDistinctAndCodesWaitForDelivery(t *testing.T) {
	m := newState()
	value := 5.0
	o := &order{ID: "order-fixture-12345", Status: stWaiting, Coin: "BTC", Network: "Lightning", CreatedAt: time.Now(), PaymentWindow: time.Minute,
		Deliveries: []delivery{{Family: "test-steam", DeliveryType: "by_email", Beneficiary: "buyer@example.com", ProductValue: &value}, {Family: "test-topup", DeliveryType: "by_phone", Beneficiary: "+905551234567"}}}
	m.orders[o.ID] = o
	before := m.publicOrder(o)
	for _, d := range before["deliveries"].([]map[string]interface{}) {
		if _, ok := d["deliverable"].(map[string]interface{})["pin_code"]; ok {
			t.Fatal("undelivered code exposed")
		}
	}
	m.pay(o)
	if o.Status != stReceived || o.PaidAt.IsZero() {
		t.Fatalf("simulated payment=%+v", o)
	}
	m.advanceLocked(o)
	if o.Status != stDone || !strings.HasPrefix(o.Deliveries[0].Redeem, "TEST-CODE-") || !strings.Contains(o.Deliveries[0].HowToRedeem, "buyer@example.com") {
		t.Fatalf("email delivery=%+v", o)
	}
	if o.Deliveries[1].Redeem != "Top-up completed for +905551234567" || o.Deliveries[1].HowToRedeem != "Credits are active." {
		t.Fatalf("phone delivery became an email code: %+v", o.Deliveries[1])
	}
	after := m.publicOrder(o)
	for _, d := range after["deliveries"].([]map[string]interface{}) {
		if d["delivery_state"] != "Done" || d["deliverable"].(map[string]interface{})["pin_code"] == nil {
			t.Fatalf("completed deliverable missing: %+v", d)
		}
	}
	m.pay(o)
	m.advanceLocked(o)
	if o.Status != stDone {
		t.Fatal("repeated simulation rewound delivered order")
	}
	failed := &order{ID: "failed-order-12345", Status: stWaiting, ForceFail: true}
	m.advanceLocked(failed)
	if failed.Status != stFailed {
		t.Fatal("waiting forced failure did not fail")
	}
}
func TestMockOrderExpiryAndSSEStopBehavior(t *testing.T) {
	m := newState()
	o := &order{ID: "expired-order-12345", Status: stWaiting, CreatedAt: time.Now().Add(-time.Hour), PaymentWindow: time.Second}
	m.orders[o.ID] = o
	response := httptest.NewRecorder()
	m.getOrder(response, o.ID)
	if response.Code != 200 || o.Status != stExpired {
		t.Fatalf("expired order: %d %+v", response.Code, o)
	}
	missing := httptest.NewRecorder()
	m.getOrder(missing, "absent")
	if missing.Code != 404 {
		t.Fatalf("missing order=%d", missing.Code)
	}
	for _, id := range []string{o.ID, "absent"} {
		stream := httptest.NewRecorder()
		m.subscribe(stream, id)
		if stream.Header().Get("Content-Type") != "text/event-stream" || !strings.Contains(stream.Body.String(), "event: stop") {
			t.Fatalf("SSE failed to stop: %s", stream.Body.String())
		}
	}
}
func TestMockControlEndpointsHaveBoundedLocalState(t *testing.T) {
	m := newState()
	o := &order{ID: "controlled-order-12345", Status: stWaiting, CreatedAt: time.Now(), PaymentWindow: time.Minute}
	m.orders[o.ID] = o
	call := func(path, body string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		w := httptest.NewRecorder()
		m.control(w, r)
		return w
	}
	if res := call("/mock/fault", `{"payment_window_ms":5000}`); res.Code != 200 || m.faultInt("payment_window_ms") != 5000 {
		t.Fatal("fault not configured")
	}
	if res := call("/mock/fault", "bad json"); res.Code != 200 || len(m.faults) != 0 {
		t.Fatal("bad fault config kept stale values")
	}
	for _, path := range []string{"/mock/state", "/mock/requests"} {
		res := call(path, "")
		var parsed interface{}
		if res.Code != 200 || json.Unmarshal(res.Body.Bytes(), &parsed) != nil {
			t.Fatalf("control read %s invalid: %s", path, res.Body.String())
		}
	}
	if res := call("/mock/orders/"+o.ID+"/pay", ""); res.Code != 200 || o.Status != stReceived {
		t.Fatal("payment control failed")
	}
	if res := call("/mock/orders/"+o.ID+"/advance", ""); res.Code != 200 || o.Status != stDone {
		t.Fatal("delivery control failed")
	}
	if res := call("/mock/orders/"+o.ID+"/status", `{"status":"WaitingForManualAction"}`); res.Code != 200 || o.Status != stManual {
		t.Fatal("manual state control failed")
	}
	if res := call("/mock/orders/"+o.ID+"/status", `{}`); res.Code != 200 || o.Status != stManual {
		t.Fatal("empty status rewound order")
	}
	for _, action := range []string{"pay", "advance", "status"} {
		if res := call("/mock/orders/missing/"+action, `{"status":"Done"}`); res.Code != 404 {
			t.Fatalf("missing %s=%d", action, res.Code)
		}
	}
	waiting := &order{ID: "waiting-order-12345", Status: stWaiting}
	m.orders[waiting.ID] = waiting
	if res := call("/mock/orders/"+waiting.ID+"/advance", ""); res.Code != 200 || waiting.Status != stDone {
		t.Fatal("advance did not traverse unpaid simulation")
	}
	if res := call("/mock/reset", ""); res.Code != 200 || len(m.orders) != 0 || len(m.faults) != 0 || len(m.requests) != 0 {
		t.Fatal("mock reset did not reset state")
	}
}
