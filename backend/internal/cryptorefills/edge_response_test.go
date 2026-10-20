package cryptorefills

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

/* edge_response_test.go — an HTML error page is not an API answer.
 *
 * Live, 2026-10-20: right after a deploy the shop polled its twenty open
 * orders in a burst and the infrastructure in front of the supplier's API
 * answered every GET with nginx's "403 Forbidden" page for ten seconds. The
 * client turned that into an ordinary 403 SupplierError, the tracker read a
 * 403 as "the supplier refuses to serve this order", and every payable
 * invoice on the shop was parked under manual review. Had a backup partner
 * key been configured, the client would also have failed over to it.
 *
 * The API itself answers JSON — an object, a problem list, or an empty body.
 * An HTML page therefore comes from a WAF, a rate limiter or a proxy, says
 * nothing about the order or the key, and is transient by definition.
 */

const nginxForbidden = "<html>\r\n<head><title>403 Forbidden</title></head>\r\n<body>\r\n<center><h1>403 Forbidden</h1></center>\r\n</body>\r\n</html>\r\n<!-- a padding to disable MSIE and Chrome friendly error page -->\r\n"

func TestAnHTMLErrorPageIsAnEdgeAnswerNotARefusal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(nginxForbidden))
	}))
	defer srv.Close()

	c := NewClient(ClientOptions{BaseURL: srv.URL, PartnerID: "partner-test", PartnerKey: "cr_pk_test"})
	_, err := c.GetOrder(context.Background(), "sup-1")
	var se *SupplierError
	if !errors.As(err, &se) {
		t.Fatalf("want *SupplierError, got %T: %v", err, err)
	}
	if !se.Edge {
		t.Fatalf("an nginx HTML page must be marked as an edge answer: %+v", se)
	}
	if se.Status != http.StatusForbidden || se.Detail != "403 Forbidden" {
		t.Fatalf("edge answer must keep the status and condense the page to its title, got %d %q", se.Status, se.Detail)
	}
	if se.Code != "" {
		t.Fatalf("an HTML page carries no supplier code, got %q", se.Code)
	}
}

func TestAnHTML404IsNotTheSuppliersOrderNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("<html><head><title>404 Not Found</title></head><body><center><h1>404 Not Found</h1></center></body></html>"))
	}))
	defer srv.Close()

	c := NewClient(ClientOptions{BaseURL: srv.URL, PartnerID: "partner-test", PartnerKey: "cr_pk_test"})
	_, err := c.GetOrder(context.Background(), "sup-2")
	if err == nil {
		t.Fatal("want an error")
	}
	if IsOrderNotFound(err) {
		t.Fatalf("a proxy's 404 page must not be read as the supplier not knowing the order: %v", err)
	}
	var se *SupplierError
	if !errors.As(err, &se) || !se.Edge || se.Status != http.StatusNotFound {
		t.Fatalf("want an edge 404, got %T %v", err, err)
	}
}

func TestAnEdge403DoesNotFailOverToTheBackupKey(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(nginxForbidden))
	}))
	defer srv.Close()

	c := NewClient(ClientOptions{BaseURL: srv.URL, PartnerID: "partner-test", PartnerKey: "cr_pk_primary", PartnerKeyBackup: "cr_pk_backup"})
	if _, err := c.GetOrder(context.Background(), "sup-3"); err == nil {
		t.Fatal("want an error")
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("an edge answer must not be retried on the backup key: %d calls", got)
	}
	if c.usingBackup {
		t.Fatal("the primary key was never evaluated by the API; the client must stay on it")
	}
}

// The API's own JSON 403 keeps its meaning: it is a decision about the key.
func TestAJSON403StillCountsAsTheAPIsAnswer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"message":"forbidden"}`))
	}))
	defer srv.Close()

	c := NewClient(ClientOptions{BaseURL: srv.URL, PartnerID: "partner-test", PartnerKey: "cr_pk_test"})
	_, err := c.GetOrder(context.Background(), "sup-4")
	var se *SupplierError
	if !errors.As(err, &se) {
		t.Fatalf("want *SupplierError, got %T: %v", err, err)
	}
	if se.Edge {
		t.Fatalf("a JSON 403 is the API talking, not the edge: %+v", se)
	}
}

func TestEdgeResponseDetection(t *testing.T) {
	cases := []struct {
		name string
		ct   string
		body string
		want bool
	}{
		{"nginx page with content type", "text/html", nginxForbidden, true},
		{"html page without content type", "", "<html><body>blocked</body></html>", true},
		{"json object", "application/json", `{"message":"forbidden"}`, false},
		{"json problem list", "application/json", `[{"problem":"KYC_MISSING"}]`, false},
		{"empty body", "", "", false},
		{"plain text", "text/plain", "forbidden", false},
	}
	for _, tc := range cases {
		if got := edgeResponse(tc.ct, []byte(tc.body)); got != tc.want {
			t.Errorf("%s: edgeResponse = %v, want %v", tc.name, got, tc.want)
		}
	}
}
