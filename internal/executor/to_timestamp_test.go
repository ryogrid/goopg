package executor

import (
	"testing"
)

// TestPgFormatToGoLayout pins the v0 to_timestamp format-code
// translation. Coverage focuses on the codes HammerDB's TPC-H
// loader actually uses (`YYYY-Mon-DD`) plus a representative
// time-of-day shape.
func TestPgFormatToGoLayout(t *testing.T) {
	cases := []struct {
		pg, want string
	}{
		{"YYYY-Mon-DD", "2006-Jan-02"},
		{"YYYY-MM-DD", "2006-01-02"},
		{"YYYY-MM-DD HH24:MI:SS", "2006-01-02 15:04:05"},
		{"YY/Mon/DD", "06/Jan/02"},
	}
	for _, c := range cases {
		got := pgFormatToGoLayout(c.pg)
		if got != c.want {
			t.Errorf("pgFormatToGoLayout(%q)=%q, want %q", c.pg, got, c.want)
		}
	}
}
