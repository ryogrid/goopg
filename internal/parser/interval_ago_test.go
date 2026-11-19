package parser

import "testing"

// TestIntervalAgo pins DecodeInterval's "ago": it negates every field and is
// accepted only as the last field after a field with a unit (PG 18.3).
func TestIntervalAgo(t *testing.T) {
	cases := []struct {
		in           string
		months, days int32
		micros       int64
	}{
		{"3 mons ago", -3, 0, 0},
		{"1 year 2 mons 3 days 04:05:06 ago", -14, -3, -(4*3600 + 5*60 + 6) * 1_000_000},
		{"-1 day ago", 0, 1, 0},
		{"1-2 ago", -14, 0, 0},
		{"2 h AGO", 0, 0, -2 * 3600 * 1_000_000},
	}
	for _, c := range cases {
		m, d, us, ok := ParseIntervalBody(c.in)
		if !ok || m != c.months || d != c.days || us != c.micros {
			t.Errorf("%q = %d %d %d %v", c.in, m, d, us, ok)
		}
	}
	for _, bad := range []string{"ago", "ago 1 day", "1 day ago ago", "1 ago day", "1 ago"} {
		if _, _, _, ok := ParseIntervalBody(bad); ok {
			t.Errorf("%q accepted", bad)
		}
	}
}
