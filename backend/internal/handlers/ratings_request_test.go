package handlers

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestDecodeRatingWholeStars(t *testing.T) {
	for star := 1; star <= 5; star++ {
		for _, raw := range []string{fmt.Sprint(star), fmt.Sprintf(`"%d"`, star), fmt.Sprintf(`" %d "`, star), fmt.Sprintf(`%d.0`, star), fmt.Sprintf(`%de0`, star), fmt.Sprintf(`%d0e-1`, star)} {
			got, err := decodeRatingStars(json.RawMessage(raw))
			if err != nil || got != star {
				t.Errorf("stars %s: got %d, %v; want %d", raw, got, err, star)
			}
		}
	}
}

func TestDecodeRatingRefusesFractionalAndMalformedStars(t *testing.T) {
	for _, raw := range []string{"", "null", "true", "[]", "{}", "0", "6", "-1", "2.5", "4.999999999999999999", "1.000000000000000001", "1e1000000000", "1e-1000000000", `""`, `"0"`, `"6"`, `"2.5"`, `"5stars"`, `"1e0"`, strings.Repeat("5", 65)} {
		if _, err := decodeRatingStars(json.RawMessage(raw)); err != errRatingStars {
			t.Errorf("stars %s: got %v, want invalid stars", raw, err)
		}
	}
}

func TestDecodeRatingRequestIdentifiesTheInvalidField(t *testing.T) {
	for _, tc := range []struct{ raw, want string }{
		{`{"stars":5,"comment":7}`, "comment must be a string"},
		{`{"stars":5,"tx_hash":{}}`, "tx_hash must be a string"},
		{`{"stars":2.5}`, errRatingStars.Error()},
		{`{"comment":"ok"}`, errRatingStars.Error()},
		{`{"stars":5`, "send a valid JSON object for the rating"},
		{`[]`, "send a valid JSON object for the rating"},
	} {
		if _, err := decodeRatingRequest([]byte(tc.raw)); err == nil || err.Error() != tc.want {
			t.Errorf("%s: got %v, want %s", tc.raw, err, tc.want)
		}
	}
	req, err := decodeRatingRequest([]byte(`{"stars":"5","comment":"fast delivery","tx_hash":"abc"}`))
	if err != nil || req.Stars != 5 || req.Comment != "fast delivery" || req.TxHash != "abc" {
		t.Fatalf("valid request: %+v %v", req, err)
	}
}
