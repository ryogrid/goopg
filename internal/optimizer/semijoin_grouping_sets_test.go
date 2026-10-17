package optimizer

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// TestStatementHasGroupingSets pins which GROUP BY forms leave PG 18.3's
// parse->groupingSets non-NIL — the condition pull_up_simple_subquery tests
// before wrapping every pulled output in a PlaceHolderVar (M0146-0179).
// Verified on PG 18.3 through its effect: a semijoin to a distinct subquery
// stays a semijoin under ROLLUP / CUBE / two grouping sets and is reduced to
// an inner join under `GROUPING SETS ((a, b))`, which plans as GROUP BY a, b.
func TestStatementHasGroupingSets(t *testing.T) {
	for _, tc := range []struct {
		groupBy string
		want    bool
	}{
		{"GROUP BY a, b", false},
		{"GROUP BY ROLLUP (a, b)", true},
		{"GROUP BY CUBE (a, b)", true},
		{"GROUP BY a, ROLLUP (b)", true},
		{"GROUP BY GROUPING SETS ((a, b), (a))", true},
		{"GROUP BY GROUPING SETS ((a, b))", false},
	} {
		stmts, err := parser.Parse("SELECT a, b, count(*) FROM t " + tc.groupBy)
		if err != nil {
			t.Fatalf("%s: %v", tc.groupBy, err)
		}
		s, ok := stmts[0].(*parser.SelectStmt)
		if !ok {
			t.Fatalf("%s: parsed %T", tc.groupBy, stmts[0])
		}
		if got := statementHasGroupingSets(s); got != tc.want {
			t.Errorf("%s: statementHasGroupingSets = %v, want %v", tc.groupBy, got, tc.want)
		}
	}
}

// TestUniqueOuterNestLoopUsesUniquePath pins match_unsorted_outer's
// JOIN_UNIQUE_OUTER arm (joinpath.c: `outerpath = create_unique_path(...)`)
// for the plain and materialised nested loops: the outer is the unique-ified
// cheapest path, never the raw one. The raw path is only the same thing when
// unique-ifying is free; a paid unique path removes duplicates and carries no
// pathkeys, and TPC-DS Q70's raw, ordered outer won a cost tie PG's unordered
// one loses (M0146-0179).
func TestUniqueOuterNestLoopUsesUniquePath(t *testing.T) {
	raw := &Path{Kind: PathPrebuilt, Rows: 5, Pathkeys: []PathKey{{Expr: &ColumnRef{Name: "k"}}}}
	unique := &Path{Kind: PathUnique, Rows: 2, Children: []*Path{raw}}
	rel := &RelOptInfo{CheapestTotal: raw, Pathlist: []*Path{raw}, CheapestUnique: unique}
	sj := &SpecialJoinInfo{Jointype: parser.JoinSemi}
	got := nestLoopPlainOuters(rel, uniqueSideOuter, sj, defaultCostParams())
	if len(got) != 1 || got[0] != unique {
		t.Fatalf("JOIN_UNIQUE_OUTER plain nested loop outers = %v, want only the unique-ified path", got)
	}
	if got := nestLoopPlainOuters(rel, uniqueSideNone, nil, defaultCostParams()); len(got) != 1 || got[0] != raw {
		t.Fatalf("plain nested loop outers = %v, want the raw path", got)
	}
}
