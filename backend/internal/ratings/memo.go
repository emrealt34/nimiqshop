// Package ratings holds the pure rules of the on-chain star rating: the memo
// line the buyer signs, the comment charset, and the limits. It has no I/O so
// the storefront, the verifier and the tests all read one definition.
//
// The memo is the whole public record of a rating and it has no field name:
//
//	"5"                      stars only
//	"5 fast delivery"        stars, a space, then the comment
//
// The first byte is the star count (1-5). The comment is ASCII letters and
// digits separated by single spaces, so a memo cannot carry a link, markup or
// a second rating.
package ratings

import (
	"errors"
	"strings"
)

const (
	// MemoMaxBytes is the Nimiq recipient-data ceiling for a basic transaction.
	MemoMaxBytes = 64
	// MaxComment is the longest comment that still fits: "<stars> " takes two
	// bytes, leaving 62 for the comment itself.
	MaxComment = MemoMaxBytes - 2
	// MaxRatingsPerOrder bounds how many on-chain ratings one order may carry,
	// the first rating included. A changed rating costs the buyer another 1 Luna
	// plus the fee, so the cap also limits how often a single order is re-rated.
	MaxRatingsPerOrder = 5
	// MinStars and MaxStars are the inclusive star range.
	MinStars = 1
	MaxStars = 5
)

var (
	ErrStars        = errors.New("rating must be an integer from 1 to 5")
	ErrCommentChars = errors.New("comment may contain only letters a-z, digits 0-9 and spaces")
	ErrCommentLong  = errors.New("comment is too long")
)

// NormalizeComment trims the comment, collapses runs of spaces to one and
// rejects anything outside [A-Za-z0-9 ]. It never silently drops characters:
// a comment the buyer typed is either kept exactly (modulo spacing) or refused.
func NormalizeComment(raw string) (string, error) {
	s := strings.Join(strings.Fields(raw), " ")
	if s == "" {
		return "", nil
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == ' ':
		default:
			return "", ErrCommentChars
		}
	}
	if len(s) > MaxComment {
		return "", ErrCommentLong
	}
	return s, nil
}

// Memo builds the memo for stars plus an already-normalised comment. It
// validates both, so a caller cannot produce an over-long or off-charset memo.
func Memo(stars int, comment string) (string, error) {
	if stars < MinStars || stars > MaxStars {
		return "", ErrStars
	}
	c, err := NormalizeComment(comment)
	if err != nil {
		return "", err
	}
	if c == "" {
		return string(rune('0' + stars)), nil
	}
	return string(rune('0'+stars)) + " " + c, nil
}

// ParseMemo is the inverse of Memo. ok is false for anything Memo could not
// have produced, which lets a verifier reject a memo it does not recognise.
func ParseMemo(memo string) (stars int, comment string, ok bool) {
	if len(memo) == 0 || memo[0] < '1' || memo[0] > '5' {
		return 0, "", false
	}
	stars = int(memo[0] - '0')
	rest := memo[1:]
	if rest == "" {
		return stars, "", true
	}
	if rest[0] != ' ' {
		return 0, "", false
	}
	c, err := NormalizeComment(rest[1:])
	if err != nil || c != rest[1:] || c == "" {
		return 0, "", false
	}
	return stars, c, true
}
