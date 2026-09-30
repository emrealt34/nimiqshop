package mailtrap

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	mailtrapsdk "github.com/mailtrap/mailtrap-go"
)

func TestConfigEnvironmentAndValidation(t *testing.T) {
	for _, key := range []string{EnvAPIToken, EnvFromEmail, EnvFromName, EnvReplyTo, EnvCategory, EnvUseSandbox, EnvSandboxID, EnvTimeout, EnvAPIEndpoint} {
		t.Setenv(key, "")
	}
	c := ConfigFromEnv()
	if c.Enabled() || c.FromEmail != DefaultFromEmail || c.FromName != DefaultFromName || c.Category != DefaultCategory || c.Timeout != DefaultTimeout {
		t.Fatalf("defaults: %+v", c)
	}
	for key, value := range map[string]string{EnvAPIToken: " fixture-token ", EnvFromEmail: " sender@example.com ", EnvFromName: " Sender ", EnvReplyTo: " reply@example.com ", EnvCategory: " orders ", EnvUseSandbox: " TRUE ", EnvSandboxID: " 42 ", EnvTimeout: " 2s ", EnvAPIEndpoint: " https://mail.invalid/// "} {
		t.Setenv(key, value)
	}
	c = ConfigFromEnv()
	if !c.Enabled() || c.APIToken != "fixture-token" || c.FromEmail != "sender@example.com" || c.FromName != "Sender" || c.ReplyTo != "reply@example.com" || c.Category != "orders" || !c.UseSandbox || c.SandboxID != 42 || c.timeout() != 2*time.Second || c.APIEndpoint != "https://mail.invalid" {
		t.Fatal("environment normalization failed")
	}
	for _, value := range []string{"bad", "0s", "-1s"} {
		t.Setenv(EnvTimeout, value)
		t.Setenv(EnvSandboxID, "bad")
		got := ConfigFromEnv()
		if got.Timeout != DefaultTimeout || got.SandboxID != 0 {
			t.Fatal("malformed settings not defaulted")
		}
	}
	if (Config{}).timeout() != DefaultTimeout {
		t.Fatal("zero timeout")
	}
	for _, tc := range []struct {
		name    string
		cfg     Config
		want    error
		invalid bool
	}{
		{"disabled", Config{}, nil, false},
		{"sandbox missing id", Config{UseSandbox: true}, ErrSandboxID, true},
		{"invalid sender", Config{APIToken: "fixture-token", FromEmail: "bad"}, nil, true},
		{"invalid endpoint", Config{APIToken: "fixture-token", FromEmail: "a@example.com", APIEndpoint: "javascript:alert(1)"}, ErrEndpoint, true},
		{"malformed endpoint", Config{APIToken: "fixture-token", FromEmail: "a@example.com", APIEndpoint: "https://%"}, ErrEndpoint, true},
		{"sandbox", Config{APIToken: "fixture-token", FromEmail: " a@example.com ", UseSandbox: true, SandboxID: 42, APIEndpoint: "https://mail.invalid"}, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, err := New(tc.cfg)
			if (err != nil) != tc.invalid || (tc.want != nil && !errors.Is(err, tc.want)) {
				t.Fatalf("New: %v", err)
			}
			if err == nil {
				if client.Enabled() != tc.cfg.Enabled() || client.Sandbox() != tc.cfg.UseSandbox {
					t.Fatal("state mismatch")
				}
				masked := client.Config()
				if tc.cfg.APIToken != "" && masked.APIToken != "***" {
					t.Fatal("token not masked")
				}
			}
		})
	}
	var absent *Client
	if absent.Enabled() || absent.Sandbox() {
		t.Fatal("nil client enabled")
	}
	if _, err := absent.Send(context.Background(), Message{}); !errors.Is(err, ErrDisabled) {
		t.Fatalf("nil send: %v", err)
	}
}

func TestMessageValidationAndMetadata(t *testing.T) {
	valid := Message{To: []Address{{Email: "to@example.com"}}, Subject: "subject", Text: "body"}
	for _, tc := range []struct {
		name   string
		mutate func(*Message)
		valid  bool
	}{
		{"text", func(*Message) {}, true},
		{"html", func(m *Message) { m.Text = ""; m.HTML = "<b>body</b>" }, true},
		{"missing recipient", func(m *Message) { m.To = nil }, false},
		{"bad recipient", func(m *Message) { m.To[0].Email = "bad" }, false},
		{"empty subject", func(m *Message) { m.Subject = " " }, false},
		{"empty body", func(m *Message) { m.Text = " " }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := valid
			m.To = append([]Address(nil), valid.To...)
			tc.mutate(&m)
			if err := m.validate(); (err == nil) != tc.valid {
				t.Fatalf("validate: %v", err)
			}
		})
	}
	for _, email := range []string{"", "@example.com", "a@", "a@@example.com", "a b@example.com", "a@localhost", "a@.com", "a@example."} {
		if looksLikeAddress(email) {
			t.Errorf("accepted %q", email)
		}
	}
	if !looksLikeAddress(" a@example.com ") {
		t.Fatal("valid address rejected")
	}
	if (Address{Email: "a@example.com"}).String() != "a@example.com" || (Address{Email: "a@example.com", Name: "A"}).String() != "A <a@example.com>" {
		t.Fatal("address display")
	}
	m := Message{}
	if m.category("") != DefaultCategory || m.category("cfg") != "cfg" || m.replyTo(" cfg@example.com ") != "cfg@example.com" || len(m.headers()) != 0 || m.variables() != nil || toSDK(nil) != nil {
		t.Fatal("empty metadata")
	}
	m = Message{Category: " special ", ReplyTo: Address{Email: " reply@example.com "}, OrderID: "order-1", MessageID: "id@example.com", CustomVariables: map[string]any{"order_id": "spoof", "extra": 42}}
	if m.category("cfg") != "special" || m.replyTo("cfg") != "reply@example.com" || m.headers()["Message-ID"] != "<id@example.com>" || m.headers()["X-Order-Id"] != "order-1" || m.variables()["order_id"] != "order-1" || m.variables()["extra"] != 42 {
		t.Fatal("metadata overrides")
	}
	if m.CustomVariables["order_id"] != "spoof" {
		t.Fatal("variables mutated caller map")
	}
	m.MessageID = "<id@example.com>"
	if m.headers()["Message-ID"] != m.MessageID {
		t.Fatal("double message-id brackets")
	}
	sdk := toSDK([]Address{{Email: " a@example.com ", Name: "A"}})
	if len(sdk) != 1 || sdk[0].Email != "a@example.com" || sdk[0].Name != "A" {
		t.Fatal("SDK conversion")
	}
}

func TestErrorClassification(t *testing.T) {
	for _, tc := range []struct {
		err  error
		kind Kind
	}{
		{nil, KindUnknown}, {errors.New("dial failed"), KindUnknown}, {ErrDisabled, KindPermanent}, {ErrNoRecipient, KindPermanent}, {ErrSandboxID, KindPermanent},
		{&mailtrapsdk.RateLimitError{Err: &mailtrapsdk.Error{StatusCode: 429}, RetryAfter: time.Second}, KindTemporary},
		{&mailtrapsdk.ValidationError{Err: &mailtrapsdk.Error{StatusCode: 422}}, KindPermanent}, {&mailtrapsdk.UnauthorizedError{Err: &mailtrapsdk.Error{StatusCode: 401}}, KindPermanent}, {&mailtrapsdk.ForbiddenError{Err: &mailtrapsdk.Error{StatusCode: 403}}, KindPermanent},
		{&mailtrapsdk.Error{StatusCode: 500}, KindTemporary}, {&mailtrapsdk.Error{StatusCode: 400}, KindPermanent},
	} {
		if got := Classify(tc.err); got != tc.kind {
			t.Errorf("classify %T: %v != %v", tc.err, got, tc.kind)
		}
		if tc.err != nil && Classify(fmt.Errorf("wrapped: %w", tc.err)) != tc.kind {
			t.Errorf("wrapped %T", tc.err)
		}
	}
	if RateLimitWait(errors.New("other")) != 0 || RateLimitWait(&mailtrapsdk.RateLimitError{Err: &mailtrapsdk.Error{StatusCode: 429}, RetryAfter: time.Second}) != time.Second {
		t.Fatal("retry delay")
	}
	for kind, want := range map[Kind]string{KindUnknown: "unknown", KindTemporary: "temporary", KindPermanent: "permanent", KindDisabled: "disabled", Kind(99): "unknown"} {
		if kind.String() != want || kind.Retryable() != (kind == KindTemporary) {
			t.Errorf("kind %d", kind)
		}
	}
}

func TestSendHTTPContractAndFailures(t *testing.T) {
	for _, tc := range []struct {
		name string
		code int
		body string
		ok   bool
	}{
		{"delivered", 200, `{"success":true,"message_ids":["message-1"]}`, true},
		{"delivered ids", 200, `{"success":false,"message_ids":["message-1"]}`, true},
		{"nothing delivered", 200, `{"success":false,"message_ids":[]}`, false},
		{"malformed response", 200, `{`, false},
		{"unauthorized", 401, `{"message":"unauthorized"}`, false},
		{"provider failure", 503, `{"message":"unavailable"}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.Method != http.MethodPost || !strings.Contains(r.URL.Path, "send") || r.Header.Get("Authorization") != "Bearer fixture-token" {
					t.Errorf("request contract: %s %s", r.Method, r.URL)
				}
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if body["subject"] != "Subject" || body["category"] != "fixture" || body["reply_to"] == nil || body["custom_variables"] == nil {
					t.Errorf("payload: %v", body)
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.code)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			c, err := New(Config{APIToken: "fixture-token", FromEmail: "sender@example.com", ReplyTo: "reply@example.com", Category: "fixture", APIEndpoint: server.URL})
			if err != nil {
				t.Fatal(err)
			}
			m := Message{To: []Address{{Email: "to@example.com"}}, Subject: "Subject", Text: "Body", OrderID: "order-1"}
			if _, err = c.Send(context.Background(), Message{}); !errors.Is(err, ErrNoRecipient) {
				t.Fatalf("invalid send: %v", err)
			}
			ids, err := c.SendRetried(context.Background(), m, 0)
			if (err == nil) != tc.ok || calls.Load() != 1 {
				t.Fatalf("send: ids=%v err=%v calls=%d", ids, err, calls.Load())
			}
			if tc.ok && (len(ids) != 1 || ids[0] != "message-1") {
				t.Fatalf("ids: %v", ids)
			}
		})
	}
}

func TestRateLimitedRetryAndCancellation(t *testing.T) {
	for _, tc := range []struct {
		name, delay         string
		attempts, wantCalls int
		cancel, ok          bool
	}{
		{"retry", "1", 2, 2, false, true}, {"no retry-after", "", 2, 1, false, false}, {"attempt limit", "1", 1, 1, false, false}, {"cancellation", "1", 2, 1, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Content-Type", "application/json")
				if calls.Load() == 1 {
					w.Header().Set("Retry-After", tc.delay)
					w.WriteHeader(http.StatusTooManyRequests)
					_, _ = w.Write([]byte(`{"message":"rate limited"}`))
					return
				}
				_, _ = w.Write([]byte(`{"success":true,"message_ids":["retried"]}`))
			}))
			defer server.Close()
			c, err := New(Config{APIToken: "fixture-token", FromEmail: "a@example.com", APIEndpoint: server.URL})
			if err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			if tc.cancel {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, 100*time.Millisecond)
				defer cancel()
			}
			_, err = c.SendRetried(ctx, Message{To: []Address{{Email: "to@example.com"}}, Subject: "subject", Text: "body"}, tc.attempts)
			if (err == nil) != tc.ok || calls.Load() != int32(tc.wantCalls) {
				t.Fatalf("retry err=%v calls=%d", err, calls.Load())
			}
			if tc.cancel && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("cancellation: %v", err)
			}
		})
	}
}
