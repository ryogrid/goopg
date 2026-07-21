package executor

// R3-4: composite (multi-equijoin) EXISTS decorrelates, and the resulting
// semi/anti join must produce PG's answer whether or not a covering
// composite index exists.
//
// The pre-S1c planner keyed the semi join on params[0] and silently
// dropped the remaining pairs (over-matching); S1c replaced that with a
// bail to the per-row SubPlan path. R3-4 lifts the bail by putting
// params[1:] on the join predicate, where the executor's lazy hash
// semi/anti already re-evaluates the full predicate per bucket match.
//
// The bail's stated fear was that the downstream NLI rewrite might extract
// one of those predicate conjuncts as a competing probe key and lose the
// original pair. These tests exercise exactly that risk from both sides:
//
//   - WITH a covering composite index, collectCrossSideEquiKeys harvests
//     LeftKey/RightKey together with the predicate conjuncts and the probe
//     consumes every pair;
//   - WITHOUT one, the uncovered pair stays on the predicate and the
//     executor re-checks it per candidate.
//
// Both must return the same rows, and both must agree with the SubPlan
// path. The fixture's load-bearing row is the one matching on the FIRST
// key only: a first-key-only implementation emits it, a correct one does
// not.

import (
	"testing"

	"github.com/goopg/goopg/internal/planner"
)

func newCompositeExistsFixture(t *testing.T, withCompositeIndex bool) (*Context, func()) {
	t.Helper()
	ctx, _, cleanup := newDDLFixture(t)
	stmts := []string{
		"CREATE TABLE ce_outer (k1 int, k2 int, tag text)",
		"CREATE TABLE ce_inner (j1 int, j2 int)",
	}
	if withCompositeIndex {
		stmts = append(stmts, "CREATE INDEX ce_inner_composite ON ce_inner (j1, j2)")
	}
	stmts = append(stmts,
		"INSERT INTO ce_outer VALUES (1, 10, 'both')",       // matches on both keys
		"INSERT INTO ce_outer VALUES (2, 20, 'firstonly')",  // matches j1 only -> must NOT qualify
		"INSERT INTO ce_outer VALUES (3, 30, 'none')",       // no inner row at all
		"INSERT INTO ce_outer VALUES (4, 40, 'secondonly')", // matches j2 only -> must NOT qualify
		"INSERT INTO ce_inner VALUES (1, 10)",
		"INSERT INTO ce_inner VALUES (2, 99)",
		"INSERT INTO ce_inner VALUES (77, 40)",
		"INSERT INTO ce_inner VALUES (5, 50)",
	)
	for _, stmt := range stmts {
		if err := runDDL(t, ctx, stmt); err != nil {
			cleanup()
			t.Fatalf("fixture %q: %v", stmt, err)
		}
	}
	return ctx, cleanup
}

const compositeExistsSQL = "SELECT tag FROM ce_outer WHERE EXISTS (" +
	"SELECT 1 FROM ce_inner WHERE j1 = k1 AND j2 = k2) ORDER BY tag"

const compositeNotExistsSQL = "SELECT tag FROM ce_outer WHERE NOT EXISTS (" +
	"SELECT 1 FROM ce_inner WHERE j1 = k1 AND j2 = k2) ORDER BY tag"

func compositeRows(t *testing.T, ctx *Context, sql string) []string {
	t.Helper()
	rows, err := runQueryWithErr(ctx, sql)
	if err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return renderRows(rows)
}

func assertEqualRows(t *testing.T, what string, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: got %v, want %v", what, got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("%s row %d: got %q want %q (all: %v)", what, i, got[i], want[i], got)
		}
	}
}

// TestCompositeExistsBothIndexShapes runs EXISTS and NOT EXISTS with and
// without a covering composite index; all four combinations must match PG.
func TestCompositeExistsBothIndexShapes(t *testing.T) {
	for _, indexed := range []bool{false, true} {
		name := "unindexed"
		if indexed {
			name = "composite-index"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cleanup := newCompositeExistsFixture(t, indexed)
			defer cleanup()
			assertEqualRows(t, "EXISTS", compositeRows(t, ctx, compositeExistsSQL), []string{"both"})
			assertEqualRows(t, "NOT EXISTS", compositeRows(t, ctx, compositeNotExistsSQL),
				[]string{"firstonly", "none", "secondonly"})
		})
	}
}

// TestCompositeExistsMatchesSubPlanPath pins the decorrelated result
// against the always-correct per-row SubPlan path — the same dual-path
// discipline the semantics matrix uses, applied to the indexed shape where
// the probe-key harvest is most likely to go wrong.
func TestCompositeExistsMatchesSubPlanPath(t *testing.T) {
	for _, indexed := range []bool{false, true} {
		name := "unindexed"
		if indexed {
			name = "composite-index"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cleanup := newCompositeExistsFixture(t, indexed)
			defer cleanup()
			for _, sql := range []string{compositeExistsSQL, compositeNotExistsSQL} {
				planner.SetSubqueryUnnestEnabled(true)
				unnested := compositeRows(t, ctx, sql)
				planner.SetSubqueryUnnestEnabled(false)
				subplan := compositeRows(t, ctx, sql)
				planner.SetSubqueryUnnestEnabled(true)
				assertEqualRows(t, "unnested vs SubPlan ("+sql+")", unnested, subplan)
			}
		})
	}
}
