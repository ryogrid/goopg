package executor

// S6 (D6.2) executor verification: an index-driven NLI semi/anti join
// with a residual predicate that references INNER columns must return
// PostgreSQL's answer — including the NULL cases, where a NULL residual
// comparison is "no match" (semi: row not emitted; anti: row emitted
// only when NO inner row qualifies).
//
// Why this is trustworthy despite semi/anti emitting the outer schema
// only: the operator evaluates plan.Predicate through virtualOut, whose
// column mapping spans outer ++ inner regardless of the emit schema
// (operators_nljoin.go builds cols over both source slots, and
// evalExprSlot bounds-checks against Width(), not the schema). These
// tests pin that property end-to-end: the EXPLAIN assertion proves the
// NLI path actually served the query, and the row assertions prove the
// residual was evaluated with the right values.
//
// The fixture mirrors TPC-H Q4's shape at doll-house scale:
//
//	ord(o_key, o_val)                — outer, with a local conjunct
//	line(l_key, l_c, l_r) + btree on l_key — inner; residual l_c < l_r
//
// Expected classification per o_key:
//	1 → (1,1,2)                       residual passes        → EXISTS
//	2 → (2,9,3), (2,NULL,5)           fail + NULL            → NOT EXISTS
//	3 → (3,1,NULL), (3,0,9)           NULL + pass            → EXISTS
//	4 → no rows                       probe miss             → NOT EXISTS

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/planner"
)

func newNLIResidualFixture(t *testing.T) (*Context, func()) {
	t.Helper()
	ctx, _, cleanup := newDDLFixture(t)
	for _, stmt := range []string{
		"CREATE TABLE ord (o_key int, o_val int)",
		"CREATE TABLE line (l_key int, l_c int, l_r int)",
		"CREATE INDEX line_key_idx ON line (l_key)",
		"INSERT INTO ord VALUES (1, 1)",
		"INSERT INTO ord VALUES (2, 4)",
		"INSERT INTO ord VALUES (3, 5)",
		"INSERT INTO ord VALUES (4, 7)",
		"INSERT INTO line VALUES (1, 1, 2)",
		"INSERT INTO line VALUES (2, 9, 3)",
		"INSERT INTO line VALUES (2, NULL, 5)",
		"INSERT INTO line VALUES (3, 1, NULL)",
		"INSERT INTO line VALUES (3, 0, 9)",
	} {
		if err := runDDL(t, ctx, stmt); err != nil {
			cleanup()
			t.Fatalf("fixture %q: %v", stmt, err)
		}
	}
	// D6.3a: the semi/anti NLI cost gate is stats-aware and keeps hash
	// without ANALYZE data. The in-process fixture's ANALYZE is a no-op
	// (rows=0), so set the stats directly: 4 outer rows probing a 4-row
	// inner with 3 distinct keys (match set 1) — the selective shape
	// these tests exist for.
	if tbl, ok := ctx.Catalog.LookupTable(parser.ObjectName{Name: "ord"}); ok {
		tbl.Stats = &catalog.TableStats{RowCount: 4, Columns: []catalog.ColumnStats{
			{NDistinct: 4}, {NDistinct: 4},
		}}
	}
	if tbl, ok := ctx.Catalog.LookupTable(parser.ObjectName{Name: "line"}); ok {
		tbl.Stats = &catalog.TableStats{RowCount: 4, Columns: []catalog.ColumnStats{
			{NDistinct: 3}, {NDistinct: 3}, {NDistinct: 3},
		}}
	}
	return ctx, cleanup
}

func nliResidualRows(t *testing.T, ctx *Context, sql string) []string {
	t.Helper()
	rows, err := runQueryWithErr(ctx, sql)
	if err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, datumTestString(r[0]))
	}
	return out
}

func nliResidualExplain(t *testing.T, ctx *Context, sql string) string {
	t.Helper()
	rows, err := runQueryWithErr(ctx, "EXPLAIN "+sql)
	if err != nil {
		t.Fatalf("EXPLAIN %s: %v", sql, err)
	}
	var b strings.Builder
	for _, r := range rows {
		b.WriteString(datumTestString(r[0]))
		b.WriteString("\n")
	}
	return b.String()
}

func TestNLISemiResidualExecution(t *testing.T) {
	planner.SetIndexKeyHarvestEnabled(true)
	t.Cleanup(func() { planner.SetIndexKeyHarvestEnabled(true) }) // restore the ON default
	ctx, cleanup := newNLIResidualFixture(t)
	defer cleanup()

	sql := "SELECT o_key FROM ord WHERE EXISTS (SELECT 1 FROM line WHERE l_key = o_key AND l_c < l_r) ORDER BY o_key"
	plan := nliResidualExplain(t, ctx, sql)
	if !strings.Contains(plan, "Nested Loop (SEMI)") {
		t.Fatalf("expected the NLI semi path to serve this query; plan:\n%s", plan)
	}
	got := nliResidualRows(t, ctx, sql)
	want := []string{"1", "3"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("semi with inner residual: got %v want %v (NULL residual must not count as a match)", got, want)
	}
}

func TestNLIAntiResidualExecution(t *testing.T) {
	planner.SetIndexKeyHarvestEnabled(true)
	t.Cleanup(func() { planner.SetIndexKeyHarvestEnabled(true) }) // restore the ON default
	ctx, cleanup := newNLIResidualFixture(t)
	defer cleanup()

	sql := "SELECT o_key FROM ord WHERE NOT EXISTS (SELECT 1 FROM line WHERE l_key = o_key AND l_c < l_r) ORDER BY o_key"
	plan := nliResidualExplain(t, ctx, sql)
	if !strings.Contains(plan, "Nested Loop (ANTI)") {
		t.Fatalf("expected the NLI anti path to serve this query; plan:\n%s", plan)
	}
	got := nliResidualRows(t, ctx, sql)
	// o_key=2: inner rows exist but none passes (9<3 false, NULL<5
	// unknown) → anti emits; o_key=4: probe miss → anti emits.
	want := []string{"2", "4"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("anti with inner residual: got %v want %v", got, want)
	}
}

// TestNLISemiResidualMatchesSubPlanPath runs the Q4 shape (outer local
// conjunct + EXISTS residual) on both plan paths and requires identical
// answers — the decorrelated NLI form and the SubPlan form must agree.
func TestNLISemiResidualMatchesSubPlanPath(t *testing.T) {
	ctx, cleanup := newNLIResidualFixture(t)
	defer cleanup()

	sql := "SELECT o_key FROM ord WHERE o_val > 3 AND EXISTS (SELECT 1 FROM line WHERE l_key = o_key AND l_c < l_r) ORDER BY o_key"
	// o_val > 3 keeps {2,3,4}; EXISTS keeps {3}.
	want := []string{"3"}

	defer planner.SetIndexKeyHarvestEnabled(true) // restore the ON default
	planner.SetIndexKeyHarvestEnabled(true)
	nliGot := nliResidualRows(t, ctx, sql)
	planner.SetIndexKeyHarvestEnabled(false)
	subplanGot := nliResidualRows(t, ctx, sql)

	if len(nliGot) != 1 || nliGot[0] != want[0] {
		t.Fatalf("NLI path: got %v want %v", nliGot, want)
	}
	if len(subplanGot) != 1 || subplanGot[0] != want[0] {
		t.Fatalf("SubPlan path: got %v want %v", subplanGot, want)
	}
}

// TestScalarProbeCheapPolicyResultsAgree pins the S6 scalar policy from
// the results side: an index-probe-cheap scalar stays a SubPlan with the
// harvest on (planner test asserts the shape); here we require the
// answer to be identical with the harvest on and off.
func TestScalarProbeCheapPolicyResultsAgree(t *testing.T) {
	ctx, cleanup := newNLIResidualFixture(t)
	defer cleanup()

	sql := "SELECT o_key FROM ord WHERE o_val < (SELECT max(l_r) FROM line WHERE l_key = o_key) ORDER BY o_key"
	// max(l_r) per key: 1→2, 2→5, 3→9, 4→NULL. o_val: 1,4,5,7 →
	// 1<2 ✓, 4<5 ✓, 5<9 ✓, 7<NULL → NULL → excluded.
	want := []string{"1", "2", "3"}

	defer planner.SetIndexKeyHarvestEnabled(true) // restore the ON default
	planner.SetIndexKeyHarvestEnabled(true)
	onGot := nliResidualRows(t, ctx, sql)
	planner.SetIndexKeyHarvestEnabled(false)
	offGot := nliResidualRows(t, ctx, sql)

	for name, got := range map[string][]string{"harvest-on": onGot, "harvest-off": offGot} {
		if len(got) != len(want) {
			t.Fatalf("%s: got %v want %v", name, got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("%s: got %v want %v", name, got, want)
			}
		}
	}
}
