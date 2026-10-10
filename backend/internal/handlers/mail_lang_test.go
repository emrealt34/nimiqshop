package handlers

import "testing"

func TestCommonItemCountry(t *testing.T) {
	cases := []struct {
		in   []string
		want string
	}{
		{[]string{"TR"}, "TR"},
		{[]string{"TR", "TR"}, "TR"},
		{[]string{"TR", "US"}, ""},
		{[]string{"TR", ""}, ""},
		{nil, ""},
	}
	for _, c := range cases {
		if got := commonItemCountry(c.in...); got != c.want {
			t.Errorf("commonItemCountry(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}
