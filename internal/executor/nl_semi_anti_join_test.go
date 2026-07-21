package executor

// S4a (D3.2, matrix M14/M17) — execution-level pins for the nested-loop
// semi/anti join mode added to joinOp for zero-equijoin decorrelated
// sublinks, and for the ordinal-tagged aggregate-above-join scalar
// rewrite. The planner-side shapes are pinned in
// internal/planner/exists_unnest_test.go / unnest_test.go; the tests
// here pin the RUNTIME contracts the rewrite relies on:
//
//   - SEMI emits the outer row exactly ONCE no matter how many inner
//     rows qualify (emit-once), and emits the LEFT columns only.
//   - ANTI emits the outer row iff NO inner row qualifies.
//   - A NULL-valued join predicate is a non-match (three-valued logic:
//     UNKNOWN never satisfies a join qual), for both semi and anti.
//   - The OrdinalityWrap tag preserves duplicate-outer multiplicity
//     end-to-end on a table (not just the M17 UNION ALL derived table).

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/planner"
)

// newNLJoinFixture builds outr/innr so that one outer row has TWO
// qualifying inner rows (emit-once probe), one has none, and one drives
// the predicate to NULL.
//
//	outr(a, b) = (1,10) (2,20) (3,NULL)
//	innr(c)    = (5) (15) (15)
//
// Under `innr.c > outr.b`: row 1 matches twice (15,15), row 2 never,
// row 3 evaluates `15 > NULL` = UNKNOWN = no match.
func newNLJoinFixture(t *testing.T) (*Context, func()) {
	t.Helper()
	ctx, _, cleanup := newDDLFixture(t)
	for _, stmt := range []string{
		"CREATE TABLE outr (a int, b int)",
		"CREATE TABLE innr (c int)",
		"INSERT INTO outr VALUES (1, 10)",
		"INSERT INTO outr VALUES (2, 20)",
		"INSERT INTO outr VALUES (3, NULL)",
		"INSERT INTO innr VALUES (5)",
		"INSERT INTO innr VALUES (15)",
		"INSERT INTO innr VALUES (15)",
	} {
		if err := runDDL(t, ctx, stmt); err != nil {
			cleanup()
			t.Fatalf("fixture %q: %v", stmt, err)
		}
	}
	return ctx, cleanup
}

// mustUseNLSemiAnti asserts the query decorrelated to a nested-loop
// join rather than falling back to a SubPlan — without this the result
// assertions would vacuously pass on the SubPlan path.
func mustUseNLSemiAnti(t *testing.T, ctx *Context, sql string) {
	t.Helper()
	plan := explainText(t, ctx, sql)
	if !strings.Contains(plan, "Nested Loop") {
		t.Fatalf("expected a Nested Loop semi/anti join, got:\n%s", plan)
	}
	if strings.Contains(plan, "SubPlan") {
		t.Fatalf("sublink fell back to SubPlan:\n%s", plan)
	}
}

// TestNLSemiJoinEmitOnce: outer row 1 has two qualifying inner rows and
// must appear exactly once; NULL predicate (row 3) is a non-match.
func TestNLSemiJoinEmitOnce(t *testing.T) {
	ctx, cleanup := newNLJoinFixture(t)
	defer cleanup()
	planner.SetSubqueryUnnestEnabled(true)
	defer planner.SetSubqueryUnnestEnabled(true)

	const sql = "SELECT a FROM outr WHERE EXISTS (SELECT 1 FROM innr WHERE innr.c > outr.b) ORDER BY a"
	mustUseNLSemiAnti(t, ctx, sql)
	rows, err := runQueryWithErr(ctx, sql)
	if err != nil {
		t.Fatal(err)
	}
	got := renderRows(rows)
	want := []string{"1"}
	if !equalStrings(got, want) {
		t.Fatalf("NL semi join: got %v want %v (duplicate = emit-once broken; extra 3 = NULL treated as match)", got, want)
	}
}

// TestNLAntiJoinNullPredicate: anti emits rows with NO qualifying inner
// row. Row 3's predicate is UNKNOWN for every inner row — never a
// match — so the anti join emits it, matching PG's NOT EXISTS (whose
// inner scan yields zero rows for row 3).
func TestNLAntiJoinNullPredicate(t *testing.T) {
	ctx, cleanup := newNLJoinFixture(t)
	defer cleanup()
	planner.SetSubqueryUnnestEnabled(true)
	defer planner.SetSubqueryUnnestEnabled(true)

	const sql = "SELECT a FROM outr WHERE NOT EXISTS (SELECT 1 FROM innr WHERE innr.c > outr.b) ORDER BY a"
	mustUseNLSemiAnti(t, ctx, sql)
	rows, err := runQueryWithErr(ctx, sql)
	if err != nil {
		t.Fatal(err)
	}
	got := renderRows(rows)
	want := []string{"2", "3"}
	if !equalStrings(got, want) {
		t.Fatalf("NL anti join: got %v want %v", got, want)
	}
}

// TestScalarResidualDuplicateOuterOrdinality is the base-table twin of
// matrix row M17: two IDENTICAL outer rows must both survive the
// aggregate-above-join rewrite, and the plan must show the Ordinality
// tag actually fired (the rewrite, not the SubPlan path, produced the
// result).
func TestScalarResidualDuplicateOuterOrdinality(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	for _, stmt := range []string{
		"CREATE TABLE d1 (a int, b int)",
		"CREATE TABLE d2 (a int, b int)",
		"INSERT INTO d1 VALUES (1, 10)",
		"INSERT INTO d1 VALUES (1, 10)",
		"INSERT INTO d1 VALUES (2, 20)",
		"INSERT INTO d2 VALUES (1, 5)",
	} {
		if err := runDDL(t, ctx, stmt); err != nil {
			t.Fatalf("fixture %q: %v", stmt, err)
		}
	}
	planner.SetSubqueryUnnestEnabled(true)
	defer planner.SetSubqueryUnnestEnabled(true)

	const sql = "SELECT d1.a FROM d1 WHERE d1.b >= (" +
		"SELECT min(y.b) FROM d2 y WHERE y.a = d1.a AND y.b <= d1.b) ORDER BY a"
	plan := explainText(t, ctx, sql)
	if !strings.Contains(plan, "Ordinality") {
		t.Fatalf("aggregate-above-join rewrite did not fire (no Ordinality node):\n%s", plan)
	}
	rows, err := runQueryWithErr(ctx, sql)
	if err != nil {
		t.Fatal(err)
	}
	got := renderRows(rows)
	want := []string{"1", "1"}
	if !equalStrings(got, want) {
		t.Fatalf("duplicate-outer multiplicity: got %v want %v (single 1 = ordinal tag collapsed duplicates)", got, want)
	}
}
