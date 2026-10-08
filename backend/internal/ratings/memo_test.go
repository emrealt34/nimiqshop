package ratings

import (
	"strings"
	"testing"
)

func TestNormalizeComment(t *testing.T) {
	cases := []struct {
		in, want string
		err      error
	}{
		{"", "", nil},
		{"   ", "", nil},
		{"fast delivery", "fast delivery", nil},
		{"  fast   delivery  ", "fast delivery", nil},
		{"Ab12 CD34", "Ab12 CD34", nil},
		{"great!", "", ErrCommentChars},
		{"see https://x.io", "", ErrCommentChars},
		{"çok iyi", "", ErrCommentChars}, // strict a-z 0-9 by design
		{"tab\there", "tab here", nil},   // whitespace runs collapse to one space
		{"<b>x</b>", "", ErrCommentChars},
		{strings.Repeat("a", MaxComment), strings.Repeat("a", MaxComment), nil},
		{strings.Repeat("a", MaxComment+1), "", ErrCommentLong},
	}
	for _, c := range cases {
		got, err := NormalizeComment(c.in)
		if err != c.err {
			t.Fatalf("NormalizeComment(%q) err = %v, want %v", c.in, err, c.err)
		}
		if got != c.want {
			t.Fatalf("NormalizeComment(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestMemoHasNoFieldName(t *testing.T) {
	m, err := Memo(5, "fast delivery")
	if err != nil {
		t.Fatal(err)
	}
	if m != "5 fast delivery" {
		t.Fatalf("memo = %q", m)
	}
	m, _ = Memo(3, "")
	if m != "3" {
		t.Fatalf("stars-only memo = %q", m)
	}
}

func TestMemoFitsCeiling(t *testing.T) {
	m, err := Memo(1, strings.Repeat("z", MaxComment))
	if err != nil {
		t.Fatal(err)
	}
	if len(m) > MemoMaxBytes {
		t.Fatalf("memo is %d bytes, ceiling %d", len(m), MemoMaxBytes)
	}
}

func TestMemoRejectsBadStars(t *testing.T) {
	for _, s := range []int{0, 6, -1} {
		if _, err := Memo(s, ""); err != ErrStars {
			t.Fatalf("Memo(%d) err = %v", s, err)
		}
	}
}

func TestParseMemoRoundTrip(t *testing.T) {
	for stars := 1; stars <= 5; stars++ {
		for _, c := range []string{"", "ok", "a1 b2 c3"} {
			m, err := Memo(stars, c)
			if err != nil {
				t.Fatal(err)
			}
			gs, gc, ok := ParseMemo(m)
			if !ok || gs != stars || gc != c {
				t.Fatalf("ParseMemo(%q) = %d %q %v", m, gs, gc, ok)
			}
		}
	}
}

func TestParseMemoRejectsForeignLines(t *testing.T) {
	for _, m := range []string{
		"", "0", "6", "x", "5x", "5  double", "5 bad!", "5 ", "nimiqshop.io rating 5/5 order ab",
		"5 " + strings.Repeat("a", MaxComment+1),
	} {
		if _, _, ok := ParseMemo(m); ok {
			t.Fatalf("ParseMemo(%q) accepted", m)
		}
	}
}
