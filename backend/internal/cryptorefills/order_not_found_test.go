package cryptorefills

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

/* order_not_found_test.go — the supplier's "no such order".
 *
 * The v6 PARTNER API answers an unknown order id with a BARE 404: empty body,
 * no code, nothing to classify. Every caller therefore read it as "the
 * supplier is unavailable" and reported a 502 — which is how the admin
 * console's "sync with supplier" ended up showing a 502 for every order the
 * partner API cannot see (live bug, 2026-10-19, on every order created before
 * the shop moved onto the partner API).
 *
 * A 404 is an ANSWER: the order is not there, and asking again will not put
 * it there. These tests pin that it is recognizable as one.
 */

func TestAnUnknownOrderIsReportedAsAnAnswerNotAnOutage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Exactly what the partner API returns for an order it cannot see:
		// 404 with an empty body.
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	c := NewClient(ClientOptions{BaseURL: srv.URL, PartnerKey: "cr_pk_test"})

	order, err := c.GetOrderFresh(context.Background(), "never-existed")
	if err == nil {
		t.Fatalf("a 404 must be an error, got order %+v", order)
	}
	if !IsOrderNotFound(err) {
		t.Fatalf("IsOrderNotFound(%v) = false, want true", err)
	}
	// The raw upstream answer is still reachable: callers that read the
	// status through errors.As(*SupplierError) must keep working.
	var se *SupplierError
	if !errorAsSupplier(err, &se) || se.Status != http.StatusNotFound {
		t.Fatalf("the underlying supplier error with its 404 must survive: %v", err)
	}
	// Same answer through the polling path.
	if _, perr := c.GetOrder(context.Background(), "never-existed"); !IsOrderNotFound(perr) {
		t.Fatalf("GetOrder: IsOrderNotFound(%v) = false, want true", perr)
	}
}

func TestARealOutageIsStillTransient(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()
	c := NewClient(ClientOptions{BaseURL: srv.URL, PartnerKey: "cr_pk_test"})

	_, err := c.GetOrderFresh(context.Background(), "some-order")
	if err == nil {
		t.Fatal("a 502 must be an error")
	}
	if IsOrderNotFound(err) {
		t.Fatalf("a supplier outage must NOT be reported as an unknown order: %v", err)
	}
	var se *SupplierError
	if !errorAsSupplier(err, &se) || se.Status != http.StatusBadGateway {
		t.Fatalf("the upstream 502 must be reported as-is: %v", err)
	}
}

// errorAsSupplier is errors.As for *SupplierError, kept local so the test
// file does not need the errors import for one line.
func errorAsSupplier(err error, target **SupplierError) bool {
	type unwrappy interface{ Unwrap() error }
	for e := err; e != nil; {
		if se, ok := e.(*SupplierError); ok {
			*target = se
			return true
		}
		u, ok := e.(unwrappy)
		if !ok {
			return false
		}
		e = u.Unwrap()
	}
	return false
}
