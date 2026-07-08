package executor

import "testing"

// TestCorrelatedIn_OperandMismatchNotCorrelationColumn is an
// end-to-end regression test for a planner bug where a correlated
// `operand IN (subquery)` unnested to a hash join keyed on the
// subquery's WHERE-clause correlation pair alone, completely
// ignoring both the IN operand and the subquery's actual SELECT
// column. That silently replaced "is x among the y values of
// z=w-correlated rows" with "does a z=w-correlated row exist at
// all" — wrong whenever the correlation column (w/z) differs from
// the operand/select column (x/y).
//
// Data: ci_outer(x, w) has rows (1,5) and (2,99); ci_inner(y, z) has
// one row (5, 777). Real PostgreSQL semantics: for x=1/w=5, the
// subquery is `SELECT y FROM ci_inner WHERE z = 5` — z is 777, no
// match, so the subquery is EMPTY for this row and `x IN (empty)` is
// false; same for x=2/w=99. The correct answer is therefore the
// EMPTY set. The pre-fix bug instead built a join keyed on
// (w = y) alone — with the correlation predicate `z = ci_outer.w`
// folded away into a self-tautology (`z = z`) inside the cloned
// inner plan — so it wrongly matched x=1 (w=5 equals ci_inner.y=5)
// regardless of z or x itself.
func TestCorrelatedIn_OperandMismatchNotCorrelationColumn(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE ci_outer (x int, w int)"); err != nil {
		t.Fatal(err)
	}
	if err := runDDL(t, ctx, "CREATE TABLE ci_inner (y int, z int)"); err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{
		"INSERT INTO ci_outer VALUES (1, 5), (2, 99)",
		"INSERT INTO ci_inner VALUES (5, 777)",
	} {
		if err := runDDL(t, ctx, s); err != nil {
			t.Fatal(err)
		}
	}

	rows := runQueryRows(t, ctx, "SELECT x FROM ci_outer WHERE x IN (SELECT y FROM ci_inner WHERE z = ci_outer.w) ORDER BY x")
	if len(rows) != 0 {
		t.Fatalf("got %d rows, want 0 (no ci_inner row has z matching any ci_outer.w); rows=%v", len(rows), rows)
	}
}

// TestCorrelatedIn_SelfReferencingStillUnnests is the companion
// positive case: when the subquery correlates on and selects the
// SAME column as the IN operand, the fast unnest path remains
// available (and correct) — this is the one shape
// correlatedInOperandSafeToUnnest (internal/planner/unnest.go)
// admits.
func TestCorrelatedIn_SelfReferencingStillUnnests(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE cis_outer (x int)"); err != nil {
		t.Fatal(err)
	}
	if err := runDDL(t, ctx, "CREATE TABLE cis_inner (y int)"); err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{
		"INSERT INTO cis_outer VALUES (1), (2), (3)",
		"INSERT INTO cis_inner VALUES (2)",
	} {
		if err := runDDL(t, ctx, s); err != nil {
			t.Fatal(err)
		}
	}

	rows := runQueryRows(t, ctx, "SELECT x FROM cis_outer WHERE x IN (SELECT y FROM cis_inner WHERE y = cis_outer.x) ORDER BY x")
	want := []int64{2}
	if len(rows) != len(want) {
		t.Fatalf("got %d rows, want %d; rows=%v", len(rows), len(want), rows)
	}
	for i, r := range rows {
		if r[0].Int != want[i] {
			t.Errorf("row %d: got x=%d, want %d", i, r[0].Int, want[i])
		}
	}
}
