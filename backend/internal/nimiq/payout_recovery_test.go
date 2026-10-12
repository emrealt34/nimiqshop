package nimiq

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestExpiryProofRequiresFinalizedCheckpointAndUnpaidReceipt(t *testing.T) {
	v := vectors(t)
	raw, hash, err := BuildBasicTransaction(v["tx.seed"], v["tx.to"], 100000, 1, 1000, NetworkTestnet, []byte("expiry"))
	if err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []string{"absent", "failed", "success", "mempool", "outage", "malformed", "pruned", "wrong_hash", "wrong_network", "no_finality", "new_policy", "unexpired"} {
		t.Run(scenario, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req struct {
					Method string `json:"method"`
				}
				_ = json.NewDecoder(r.Body).Decode(&req)
				var result interface{}
				var problem string
				switch req.Method {
				case "getBlockNumber":
					result = 10000
					if scenario == "unexpired" {
						result = 2000
					}
				case "getPolicyConstants":
					window := 7200
					if scenario == "new_policy" {
						window = 8000
					}
					result = map[string]interface{}{"transactionValidityWindow": window}
				case "getLastMacroBlock":
					result = 9990
				case "getBlockByNumber":
					network := "TestAlbatross"
					if scenario == "wrong_network" {
						network = "MainAlbatross"
					}
					justification := interface{}(map[string]interface{}{"proof": "final"})
					if scenario == "no_finality" {
						justification = nil
					}
					result = map[string]interface{}{"hash": strings.Repeat("a", 64), "number": 9990, "type": "macro", "network": network, "justification": justification}
				case "getTransactionByHash":
					switch scenario {
					case "absent", "wrong_network", "no_finality", "new_policy", "unexpired":
						problem = "Transaction not found: " + hash
					case "failed":
						result = map[string]interface{}{"hash": hash, "blockNumber": 1010, "executionResult": false}
					case "success":
						result = map[string]interface{}{"hash": hash, "blockNumber": 1010, "executionResult": true}
					case "mempool":
						result = map[string]interface{}{"hash": hash}
					case "outage":
						http.Error(w, "unavailable", http.StatusServiceUnavailable)
						return
					case "malformed":
						result = map[string]interface{}{}
					case "pruned":
						problem = "history not available"
					case "wrong_hash":
						result = map[string]interface{}{"hash": strings.Repeat("b", 64), "blockNumber": 1010, "executionResult": false}
					}
				}
				response := map[string]interface{}{"jsonrpc": "2.0", "id": 1, "result": result}
				if problem != "" {
					delete(response, "result")
					response["error"] = map[string]interface{}{"code": -32603, "message": problem}
				}
				_ = json.NewEncoder(w).Encode(response)
			}))
			defer server.Close()
			proof, allowed, _ := NewClient(server.URL).ProvePayoutExpired(context.Background(), raw)
			want := scenario == "absent" || scenario == "failed"
			if allowed != want {
				t.Fatalf("scenario %s: replacement allowed=%v, want %v", scenario, allowed, want)
			}
			if allowed && (proof.Hash != hash || proof.ValidityStart != 1005 || proof.FinalizedHeight != 9990) {
				t.Fatal("wrong expiry proof")
			}
			if scenario == "failed" && proof.FailedBlock != 1010 {
				t.Fatal("failed receipt not retained")
			}
		})
	}
}
