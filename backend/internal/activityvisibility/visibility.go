// Package activityvisibility keeps simulated purchases out of public activity.
package activityvisibility

import (
	_ "embed"
	"encoding/json"
)

// These two legacy simulations were identified by the operator on 2026-10-11.
// Match purchase IDs only: a real Amazon purchase or the same buyer's real
// Carrefour purchase must remain public. Order data is never deleted.
// The frontend imports this same manifest to protect older cached API rows.
//
//go:embed excluded-purchases.json
var excludedJSON []byte

var excluded = func() map[string]bool {
	var ids []string
	if err := json.Unmarshal(excludedJSON, &ids); err != nil {
		panic("invalid public activity exclusions")
	}
	out := make(map[string]bool, len(ids))
	for _, id := range ids {
		out[id] = true
	}
	return out
}()

// Visible rejects known legacy simulations and all explicitly simulated quotes.
func Visible(id string, testMode bool) bool {
	return !testMode && !excluded[id]
}
