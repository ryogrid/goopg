package optimizer

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// M0145-0004: `tlist_same_datatypes`, the half of is_simple_union_all_recurse
// (prepjointree.c:2258) that had no port.
//
// Upstream refuses to flatten a UNION ALL into an appendrel when a member's
// output types differ from the top-level colTypes
// (tlist_same_datatypes, tlist.c:257). goopg could not ask the question at
// mark time — the parser AST has no resolved types — and after
// setOpUnifyBranches every branch agrees by construction, so the verdict is
// captured at the moment of unification and consulted at the hoist.

func tlistGateSetOpPair(t *testing.T, leftType, rightType string) (*SetOp, *SetOp) {
	t.Helper()
	mk := func(typ string) Node {
		return &Project{
			Targets: []Expr{&ColumnRef{Index: 0, Name: "c", Type: catalog.Type{Name: typ}}},
			schema:  Schema{{Name: "c", Type: catalog.Type{Name: typ}}},
		}
	}
	same := &SetOp{Left: mk(leftType), Right: mk(leftType), All: true}
	diff := &SetOp{Left: mk(leftType), Right: mk(rightType), All: true}
	return same, diff
}

// TestSetOpBranchTypesDifferMatchesUpstreamComparison pins the predicate
// itself: same type names agree, different ones do not, and column-count
// mismatch answers "differ" (upstream's "tlist longer/shorter than colTypes"
// arms). Typmods are deliberately not compared — upstream's own comment is
// "currently no callers care about comparing typmods" — so `numeric(10,2)`
// and `numeric` must NOT be reported as differing by name.
func TestSetOpBranchTypesDifferMatchesUpstreamComparison(t *testing.T) {
	col := func(typ string, args ...int64) Node {
		return &Project{
			Targets: []Expr{&ColumnRef{Index: 0, Name: "c", Type: catalog.Type{Name: typ, Args: args}}},
			schema:  Schema{{Name: "c", Type: catalog.Type{Name: typ, Args: args}}},
		}
	}
	two := &Project{
		Targets: []Expr{&ColumnRef{Index: 0, Name: "a"}, &ColumnRef{Index: 1, Name: "b"}},
		schema:  Schema{{Name: "a", Type: catalog.Type{Name: "int4"}}, {Name: "b", Type: catalog.Type{Name: "int4"}}},
	}
	if setOpBranchTypesDiffer(col("int4"), col("int4")) {
		t.Error("identical type names must not report a difference")
	}
	if !setOpBranchTypesDiffer(col("int4"), col("numeric")) {
		t.Error("int4 vs numeric must report a difference")
	}
	if setOpBranchTypesDiffer(col("numeric", 10, 2), col("numeric")) {
		t.Error("typmods must NOT be compared (tlist.c:257's own note)")
	}
	// ALIAS SPELLINGS ARE THE SAME TYPE. Upstream compares OIDs, where
	// decimal IS numeric (1700) and int IS int4. Comparing raw spellings
	// refused TPC-DS Q5 — whose union mixes a table column with
	// `cast(0 as decimal(7,2))` — and removed the Parallel Append shape
	// M0145-0004 had landed. Measured, not hypothetical.
	if setOpBranchTypesDiffer(col("decimal"), col("numeric")) {
		t.Error("decimal and numeric are one type upstream (OID 1700)")
	}
	if setOpBranchTypesDiffer(col("int"), col("int4")) {
		t.Error("int and int4 are one type upstream")
	}
	if !setOpBranchTypesDiffer(col("int4"), two) {
		t.Error("differing column counts must report a difference")
	}
	// A nested link that already answered "differ" stays differing — this is
	// is_simple_union_all_recurse's && over larg and rarg.
	nested := &SetOp{Left: col("int4"), Right: col("int4"), All: true, TlistTypesDiffer: true}
	if !setOpBranchTypesDiffer(nested, col("int4")) {
		t.Error("a nested link that already differs must propagate")
	}
}

// TestAppendRelHoistRefusesTypeMismatchedUnion is the gate in place. The
// control is load-bearing: a type-MATCHED union must still hoist, or the
// change would be "never hoist", which is a different and wrong change.
func TestAppendRelHoistRefusesTypeMismatchedUnion(t *testing.T) {
	same, diff := tlistGateSetOpPair(t, "int4", "numeric")
	diff.TlistTypesDiffer = true

	for _, tc := range []struct {
		name      string
		leaf      Node
		wantHoist bool
	}{
		{"matched types hoist", same, true},
		{"mismatched types refuse", diff, false},
		{"matched types under a Gather still hoist", &Gather{Child: same}, true},
		{"mismatched types under a Gather refuse", &Gather{Child: diff}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			so := carrierSetOpNode(tc.leaf)
			if so == nil {
				t.Fatalf("carrierSetOpNode found no *SetOp under %T", tc.leaf)
			}
			gotHoist := !so.TlistTypesDiffer
			if gotHoist != tc.wantHoist {
				t.Errorf("hoist admitted = %v, want %v", gotHoist, tc.wantHoist)
			}
		})
	}
}

// TestPlannedSetOpCarriesTlistVerdict closes the wiring gap the two tests
// above leave: they build `*SetOp` values directly, so each END of the
// sibling pair is pinned but not the connection between them. Neutralising
// the stamp in `applySetOp` leaves both of them green.
//
// This one plans real SQL and reads the verdict off the produced node, so the
// capture site and the field it writes must actually agree.
func TestPlannedSetOpCarriesTlistVerdict(t *testing.T) {
	for _, tc := range []struct {
		sql  string
		want bool
	}{
		// Identical member types: upstream's tlist_same_datatypes is true, so
		// flattening stays available.
		{`SELECT 1::int4 AS a UNION ALL SELECT 2::int4`, false},
		// int4 vs numeric: the types differ before coercion, so upstream's
		// is_simple_union_all_recurse refuses. That setOpUnifyBranches can
		// reconcile them afterwards is exactly why the verdict has to be taken
		// before it runs.
		{`SELECT 1::int4 AS a UNION ALL SELECT 2.5::numeric`, true},
		// A three-member chain whose mismatch is in the SECOND link: the
		// verdict must propagate up, which is is_simple_union_all_recurse's
		// && over larg and rarg.
		{`SELECT 1::int4 AS a UNION ALL SELECT 2::int4 UNION ALL SELECT 2.5::numeric`, true},
	} {
		t.Run(tc.sql, func(t *testing.T) {
			so := planSetOpForTlistTest(t, tc.sql)
			if so.TlistTypesDiffer != tc.want {
				t.Errorf("TlistTypesDiffer = %v, want %v", so.TlistTypesDiffer, tc.want)
			}
		})
	}
}

// planSetOpForTlistTest plans `sql` and returns the top *SetOp node.
func planSetOpForTlistTest(t *testing.T, sql string) *SetOp {
	t.Helper()
	stmts, err := parser.Parse(sql)
	if err != nil {
		t.Fatalf("parse %q: %v", sql, err)
	}
	plan, err := Plan(stmts[0], catalog.NewInMemory())
	if err != nil {
		t.Fatalf("plan %q: %v", sql, err)
	}
	so := carrierSetOpNode(plan)
	if so == nil {
		t.Fatalf("plan %q produced no *SetOp carrier (%T)", sql, plan)
	}
	return so
}
