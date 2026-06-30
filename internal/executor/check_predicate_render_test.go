package executor

import "testing"

// TestRenderCheckPredicate pins renderCheckPredicate to PostgreSQL's
// pg_get_constraintdef_worker bytes for table-level CHECK constraints. The
// expected strings were verified byte-identical against real pg_dump 18.3
// (DU-002 slice 362): PG re-deparses the stored node with get_rule_expr, which
// fully parenthesizes every sub-expression, so a single comparison keeps one
// inner paren layer while a boolean combination parenthesizes each operand and
// a function call carries no padding spaces around its parentheses.
func TestRenderCheckPredicate(t *testing.T) {
	cases := []struct {
		raw  string
		want string
	}{
		// Single comparisons (slices 127–129) must be byte-unchanged by the
		// re-deparse path.
		{"a < b", "CHECK ((a < b))"},
		{"qty >= 0", "CHECK ((qty >= 0))"},
		{"x > 0", "CHECK ((x > 0))"},
		// Compound boolean predicates gain PG's per-operand parens.
		{"a > 0 AND b > 0", "CHECK (((a > 0) AND (b > 0)))"},
		{"a < 0 OR b > 10", "CHECK (((a < 0) OR (b > 10)))"},
		// A function-call predicate loses the spaces the token reconstruction
		// inserts around the call's parentheses.
		{"length(name) > 0", "CHECK ((length(name) > 0))"},
		{"length ( name ) > 0", "CHECK ((length(name) > 0))"},
	}
	for _, tc := range cases {
		if got := renderCheckPredicate(tc.raw); got != tc.want {
			t.Errorf("renderCheckPredicate(%q) = %q, want %q", tc.raw, got, tc.want)
		}
	}
}

// TestRenderCheckPredicateFallback verifies the re-parse round-trip guard: an
// input the deparser cannot faithfully render (or that does not parse) keeps the
// legacy double-paren raw-text wrap so the dump never emits non-SQL garbage.
func TestRenderCheckPredicateFallback(t *testing.T) {
	// A deliberately unparseable fragment must fall back to the raw wrap.
	const raw = "a > > b"
	if got := renderCheckPredicate(raw); got != "CHECK (("+raw+"))" {
		t.Errorf("renderCheckPredicate(%q) = %q, want legacy raw wrap", raw, got)
	}
}
