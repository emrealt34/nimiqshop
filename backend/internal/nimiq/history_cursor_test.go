package nimiq

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHistoryCursorUsesNullForFirstPageAndPreservesNextPage(t *testing.T) {
	var cursors []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Params []json.RawMessage `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		cursors = append(cursors, string(request.Params[2]))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"result":{"data":[],"metadata":{}}}`))
	}))
	defer server.Close()
	client := NewClient(server.URL)
	for _, cursor := range []string{"", strings.Repeat("ab", 32)} {
		if _, err := client.GetTransactionsByAddress(context.Background(), "buyer", 20, cursor); err != nil {
			t.Fatal(err)
		}
	}
	if len(cursors) != 2 || cursors[0] != "null" || cursors[1] != `"`+strings.Repeat("ab", 32)+`"` {
		t.Fatalf("invalid history cursor: %#v", cursors)
	}
}
