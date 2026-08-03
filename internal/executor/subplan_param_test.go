package executor

import (
	"strings"
	"testing"
)

// Stage S2b (design bundle correlated-subquery-planning, D4.1): the
// executor side of the PARAM_EXEC lowering. A lowered sublink's eval
// site binds Args into ParamExec slots and runs the inner plan without
// pushing ctx.OuterRows; caches key on the bound values (the projected
// key), so distinct outer rows agreeing on the correlation column share
// an entry.
//
// Probe shapes park sublinks under an always-false OR arm so the S1a
// guards keep them as SubPlans (the same convention as
// subplan_stats_test.go, whose fixture these tests reuse).

// loweredRows runs sql and renders the single output column.
func loweredRows(t *testing.T, ctx *Context, sql string) []string {
	t.Helper()
	rows := runQuery(t, ctx, sql)
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, datumTestString(r[0]))
	}
	return out
}

// TestLoweredExistsCorrectAcrossOuterRows: the lowered path must
// deliver per-row correlation values exactly as the stack path did.
func TestLoweredExistsCorrectAcrossOuterRows(t *testing.T) {
	ctx, cleanup := statsFixture(t)
	defer cleanup()

	got := loweredRows(t, ctx,
		"SELECT a FROM t1 WHERE a = -999 OR EXISTS (SELECT 1 FROM t2 WHERE t2.a = t1.a) ORDER BY a")
	want := []string{"1", "3"} // t2 has a ∈ {1, 3}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("lowered correlated EXISTS: got %v want %v", got, want)
	}
}

// TestLoweredScalarProjectedKeyHits: two outer rows share the
// correlation value a=1, so the second evaluation must hit the cache
// under the projected (param-value) key — the full-outer-row key could
// not, because the rows differ in b.
func TestLoweredScalarProjectedKeyHits(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	for _, ddl := range []string{
		"CREATE TABLE t1 (a int, b int)",
		"CREATE TABLE t2 (a int, b int)",
		"INSERT INTO t1 VALUES (1, 10), (1, 99), (2, 20)",
		"INSERT INTO t2 VALUES (1, 100), (1, 101), (2, 300)",
	} {
		if err := runDDL(t, ctx, ddl); err != nil {
			t.Fatalf("%s: %v", ddl, err)
		}
	}

	got := loweredRows(t, ctx,
		"SELECT b FROM t1 WHERE a = -999 OR b < (SELECT min(t2.b) FROM t2 WHERE t2.a = t1.a) ORDER BY b")
	// min(t2.b | a=1) = 100 → rows b=10 and b=99 qualify; min(a=2)=300 → b=20 qualifies.
	want := []string{"10", "20", "99"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("lowered correlated scalar: got %v want %v", got, want)
	}

	s := onlyStat(t, ctx)
	if s.Calls != 3 {
		t.Fatalf("Calls = %d, want 3 (one per outer row)", s.Calls)
	}
	if s.CacheHits < 1 {
		t.Errorf("CacheHits = %d, want ≥1 — the projected key must dedup the two a=1 outer rows (full-row keying cannot: their b differs)", s.CacheHits)
	}
}

// TestLoweredExplainRendersParam: the SubPlan subtree's correlation
// now prints as a PG-style $N exec param instead of a bare column name.
func TestLoweredExplainRendersParam(t *testing.T) {
	// M0125-0036: this probe's `= -999 OR EXISTS (…)` shape is what the
	// EXISTS→ANY conversion consumes; the correlated-SubPlan path it
	// grades is still live for every EXISTS the conversion declines.
	pinCorrelatedSubPlanPath(t)
	ctx, cleanup := statsFixture(t)
	defer cleanup()

	rows := runQuery(t, ctx,
		"EXPLAIN SELECT a FROM t1 WHERE a = -999 OR EXISTS (SELECT 1 FROM t2 WHERE t2.a = t1.a)")
	var text strings.Builder
	for _, r := range rows {
		text.WriteString(datumTestString(r[0]))
		text.WriteByte('\n')
	}
	out := text.String()
	if !strings.Contains(out, "SubPlan 1") {
		t.Fatalf("no SubPlan subtree in EXPLAIN:\n%s", out)
	}
	if !strings.Contains(out, "$0") {
		t.Errorf("lowered correlation does not render as $0:\n%s", out)
	}
}
