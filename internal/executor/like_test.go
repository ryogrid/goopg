package executor

import (
	"strings"
	"testing"
)

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

// TestEvalLikeAcceptsKindBytes pins the M0062-followup forward fix
// (Q9 LIKE regression). varchar values that arrive at the LIKE
// evaluator as KindBytes (via any row-reshaping path) must be
// treated as UTF-8 text, not rejected with SQLSTATE 42883.
func TestEvalLikeAcceptsKindBytes(t *testing.T) {
	left := NewBytesDatum([]byte("forest green slate"))
	right := NewStringDatum("%green%")
	got, err := evalBinary("LIKE", left, right, 0)
	if err != nil {
		t.Fatalf("LIKE on KindBytes left: unexpected error %v", err)
	}
	if got.Kind != KindBool || !got.BoolValue() {
		t.Errorf("LIKE bytes vs string: got %#v, want bool true", got)
	}

	// Both-sides-bytes also works (defensive symmetry).
	right2 := NewBytesDatum([]byte("%green%"))
	got2, err := evalBinary("NOT LIKE", left, right2, 0)
	if err != nil {
		t.Fatalf("NOT LIKE on KindBytes both: %v", err)
	}
	if got2.Kind != KindBool || got2.BoolValue() {
		t.Errorf("NOT LIKE bytes vs bytes: got %#v, want bool false", got2)
	}
}

// TestEvalLikeKindReportedInError pins the upgraded diagnostic
// message: when a non-text-like Kind reaches LIKE, the error must
// include the actual kinds so log triage doesn't need a re-run.
func TestEvalLikeKindReportedInError(t *testing.T) {
	left := Datum{Kind: KindInt, Int: 42}
	right := NewStringDatum("%foo%")
	_, err := evalBinary("LIKE", left, right, 0)
	if err == nil {
		t.Fatalf("LIKE on KindInt: expected error, got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "left.Kind=") || !strings.Contains(msg, "right.Kind=") {
		t.Errorf("LIKE error message lacks Kind diagnostic: %q", msg)
	}
}
