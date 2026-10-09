package optimizer

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// TestNestedBodySpansScopes pins M0146-0015c's gate: PG's
// convert_EXISTS_sublink_to_join refuses a NESTED sublink whose quals read
// both its parent body (Level 1) and a scope above it (Level >= 2) — neither
// available_rels1 nor child_rels holds both — so it stays a SubPlan. A
// top-level body, or a nested one reading a single scope, is not refused.
func TestNestedBodySpansScopes(t *testing.T) {
	ref := func(level int) Expr { return &OuterColumnRef{Level: level, Name: "c"} }
	eq := func(l, r Expr) Expr { return &BinaryOp{Op: parser.OpEq, Left: l, Right: r} }
	local := &ColumnRef{Index: 0, Name: "x"}
	mixed := []Expr{eq(local, ref(1)), eq(local, ref(2))}
	cases := []struct {
		name  string
		quals []Expr
		depth int
		want  bool
	}{
		{"nested, parent body and grandparent", mixed, 1, true},
		{"nested, one conjunct reading both", []Expr{&BinaryOp{Op: parser.OpAnd, Left: eq(local, ref(1)), Right: eq(local, ref(2))}}, 1, true},
		{"nested, parent body only", []Expr{eq(local, ref(1))}, 1, false},
		{"nested, grandparent only", []Expr{eq(local, ref(2))}, 1, false},
		{"top-level body is never refused here", mixed, 0, false},
	}
	for _, tc := range cases {
		if got := nestedBodySpansScopes(tc.quals, tc.depth); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}
