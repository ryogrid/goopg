package executor

import "testing"

// TestMatchSQLLike pins the LIKE pattern semantics implemented for
// TPC-H Q2/Q9/Q13/Q14/Q16/Q20: '%' matches any (possibly empty) run,
// '_' matches exactly one byte, '\' escapes the next pattern byte.
func TestMatchSQLLike(t *testing.T) {
	cases := []struct {
		s, pat string
		want   bool
	}{
		// Plain literal match.
		{"abc", "abc", true},
		{"abc", "abd", false},
		{"abc", "ab", false},
		{"ab", "abc", false},

		// '%' at end / start / both / middle.
		{"abc", "ab%", true},
		{"abc", "%bc", true},
		{"abc", "%b%", true},
		{"axyzc", "a%c", true},
		{"abc", "a%c", true},
		{"ac", "a%c", true},
		{"a", "a%", true},
		{"", "%", true},
		{"", "", true},
		{"abc", "%", true},

		// '_' exact-one.
		{"abc", "a_c", true},
		{"ac", "a_c", false},
		{"abbc", "a_c", false},
		{"abc", "_b_", true},

		// Combination — TPC-H Q14 shape `like 'PROMO%'`.
		{"PROMO BURNISHED COPPER", "PROMO%", true},
		{"BURNISHED PROMO COPPER", "PROMO%", false},

		// TPC-H Q9 shape: `p_name like '%green%'`.
		{"forest green slate", "%green%", true},
		{"FOREST GREEN slate", "%green%", false}, // case-sensitive

		// Escape.
		{"50%", `50\%`, true},
		{"500", `50\%`, false},
		{`abc_d`, `abc\_d`, true},
		{`abcXd`, `abc\_d`, false},

		// Empty pattern only matches empty input.
		{"x", "", false},
	}
	for _, c := range cases {
		got := matchSQLLike(c.s, c.pat)
		if got != c.want {
			t.Errorf("matchSQLLike(%q, %q) = %v, want %v", c.s, c.pat, got, c.want)
		}
	}
}
