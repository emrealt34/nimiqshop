package handlers

import (
	"encoding/json"
	"errors"
	"math/big"
	"strconv"
	"strings"
)

// ratingRequest is shared by intent and submission. A form's numeric string
// is accepted only when it selects a whole star; never round a buyer's rating.
type ratingRequest struct {
	Stars   int    `json:"stars"`
	Comment string `json:"comment"`
	TxHash  string `json:"tx_hash,omitempty"`
}

var errRatingStars = errors.New("rating must be an integer from 1 to 5")

func decodeRatingStars(raw json.RawMessage) (int, error) {
	if len(raw) == 0 || len(raw) > 64 {
		return 0, errRatingStars
	}
	if raw[0] == '"' {
		var text string
		if json.Unmarshal(raw, &text) != nil {
			return 0, errRatingStars
		}
		text = strings.TrimSpace(text)
		if len(text) != 1 || text[0] < '1' || text[0] > '5' {
			return 0, errRatingStars
		}
		return int(text[0] - '0'), nil
	}
	var number json.Number
	if json.Unmarshal(raw, &number) != nil {
		return 0, errRatingStars
	}
	text := number.String()
	// Bound exact decimal parsing, including exponent allocation. We need no
	// unbounded numbers to distinguish the five possible star selections.
	if i := strings.IndexAny(text, "eE"); i >= 0 {
		exponent, err := strconv.ParseInt(text[i+1:], 10, 32)
		if err != nil || exponent < -64 || exponent > 64 {
			return 0, errRatingStars
		}
	}
	value, ok := new(big.Rat).SetString(text)
	if !ok || !value.IsInt() || !value.Num().IsInt64() {
		return 0, errRatingStars
	}
	stars := value.Num().Int64()
	if stars < 1 || stars > 5 {
		return 0, errRatingStars
	}
	return int(stars), nil
}

func decodeRatingRequest(raw []byte) (ratingRequest, error) {
	var req ratingRequest
	var fields struct {
		Stars   json.RawMessage `json:"stars"`
		Comment json.RawMessage `json:"comment"`
		TxHash  json.RawMessage `json:"tx_hash"`
	}
	if err := json.Unmarshal(raw, &fields); err != nil {
		return req, errors.New("send a valid JSON object for the rating")
	}
	stars, err := decodeRatingStars(fields.Stars)
	if err != nil {
		return req, err
	}
	req.Stars = stars
	if len(fields.Comment) > 0 && json.Unmarshal(fields.Comment, &req.Comment) != nil {
		return req, errors.New("comment must be a string")
	}
	if len(fields.TxHash) > 0 && json.Unmarshal(fields.TxHash, &req.TxHash) != nil {
		return req, errors.New("tx_hash must be a string")
	}
	return req, nil
}
