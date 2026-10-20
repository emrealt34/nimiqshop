package cryptorefills

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"
)

// This customer-safe catalogue is shared with the frontend. Never translate
// by blindly rendering moreDetails.reason or arbitrary upstream response text.
// problem_catalog.json is embedded as-is: the frontend imports the same
// file (src/lib/supplierProblems.ts), so one edit reaches both sides and
// the two copies can never drift apart again (a hand-maintained base64
// copy once carried four codes the storefront had never heard of).
//
//go:embed problem_catalog.json
var problemCatalogJSON []byte

type ProblemInfo struct {
	Scope   string `json:"scope"`
	Message string `json:"message"`
}

var problemCatalog = func() map[string]ProblemInfo {
	if len(problemCatalogJSON) == 0 {
		return map[string]ProblemInfo{}
	}
	var out map[string]ProblemInfo
	if err := json.Unmarshal(problemCatalogJSON, &out); err != nil {
		return map[string]ProblemInfo{}
	}
	return out
}()

func LookupProblem(code string) (ProblemInfo, bool) {
	code = strings.ToUpper(strings.TrimSpace(code))
	if code == "PRODUCT_NOT_AVAILABLE" {
		code = "NOT_AVAILABLE_PRODUCT"
	}
	info, ok := problemCatalog[code]
	return info, ok
}

func ProblemMessage(code string) string {
	if info, ok := LookupProblem(code); ok {
		return info.Message
	}
	switch code {
	case "INVALID_DENOMINATION", "INVALID_PRODUCT_VALUE":
		return "The selected amount is not available for this item. Choose another amount."
	case "AMOUNT_TOO_LOW":
		return "The amount is below this item's minimum. Increase the amount."
	case "AMOUNT_TOO_HIGH":
		return "The amount is above this item's maximum. Reduce the amount."
	case "COUNTRY_NOT_SUPPORTED":
		return "This item is not sold in the selected country. Check the intended country."
	}
	return "Cryptorefills could not approve this request. Review the order and contact support before trying again."
}

// responseProblems handles the same envelope on success and HTTP-error paths.
// Invalid problem arrays are protocol failures, never permission to order.
func responseProblems(raw []byte, status int) error {
	// The v6 PARTNER API answers a rejected order with a BARE JSON array
	// ([{"problem":"NOT_AVAILABLE_PRODUCT","moreDetails":{...}}]) where v5
	// wrapped the same list in an object ({"problems":[…]}). Both must be
	// understood: an unparsed rejection becomes a generic failure and the
	// buyer loses the actual reason (out of stock, KYC, limits…). Verified
	// against the live partner endpoint (422 on POST /v6/partner/orders).
	//
	// Detection is strict on purpose: the supplier returns ARRAYS for plenty
	// of healthy responses too (a product/brand catalogue is a bare array of
	// objects that carry no "problem" key). A bare array is only a problem
	// list when EVERY element carries a well-formed problem code — anything
	// else is ordinary data and falls through to the v5 shape below.
	var top []Problem
	if err := json.Unmarshal(bytes.TrimSpace(raw), &top); err == nil && len(top) > 0 {
		problems := top
		for _, problem := range top {
			if !isProblemCode(problem.Code) {
				problems = nil
				break
			}
		}
		if problems != nil {
			return &ProblemError{Problems: problems, HTTPStatus: status}
		}
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) != nil || obj == nil {
		return nil
	}
	p, present := obj["problems"]
	if !present {
		return nil
	}
	p = bytes.TrimSpace(p)
	// Observed live success fixtures use null for an empty list. The validation
	// decoder still requires a valid positive quoted amount before success.
	if bytes.Equal(p, []byte("null")) {
		return nil
	}
	if len(p) == 0 || p[0] != '[' {
		return fmt.Errorf("supplier response has invalid problems array")
	}
	var problems []Problem
	if err := json.Unmarshal(p, &problems); err != nil {
		return fmt.Errorf("supplier response has malformed problems: %w", err)
	}
	for _, problem := range problems {
		if !isProblemCode(problem.Code) {
			return fmt.Errorf("supplier response has invalid problem code")
		}
	}
	if len(problems) == 0 {
		return nil
	}
	// A success-looking order identity AND a rejection are contradictory. Do
	// not classify a possibly created order as safely rejected and resend it.
	for _, key := range []string{"order_id", "id"} {
		if v := bytes.TrimSpace(obj[key]); len(v) > 0 && string(v) != "null" && string(v) != `""` {
			return fmt.Errorf("supplier returned both an order identity and problems; outcome unknown")
		}
	}
	// Preserve a top-level suspension as well if a supplier combines envelopes.
	var detail string
	if json.Unmarshal(obj["detail"], &detail) == nil && isProblemCode(detail) {
		found := false
		for _, p := range problems {
			if p.Code == detail {
				found = true
			}
		}
		if !found {
			var more any
			_ = json.Unmarshal(obj["moreDetails"], &more)
			problems = append(problems, Problem{Code: detail, Details: more})
		}
	}
	return &ProblemError{Problems: problems, HTTPStatus: status}
}
