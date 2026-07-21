package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/planner"
)

// Stage S0-2 (design bundle correlated-subquery-planning, gate V6):
// every sublink evaluation is counted, so the question "did the
// executor re-instantiate the inner plan once per outer row?" is
// answered by a measurement instead of a Fermi estimate. The
// counters are deliberately allowed to record today's pathology —
// a correlated EXISTS shows Calls == Rebuilds — because the S2
// rescan work is graded on turning exactly that ratio around.
//
// Probe shapes follow the same constraint documented in
// explain_subplan_test.go: a top-level correlated sublink over
// these index-less fixtures gets decorrelated by the planner and
// leaves no SubPlan to count, so the probes park the sublink under
// an always-false OR arm. `IN` under an OR is still unusable at
// this stage (live planner non-termination, design bundle F1,
// fixed in S1a).

// statsFixture builds the two-table fixture and seeds t1 with rows
// that all fail the left OR arm, so the sublink is evaluated once
// per outer row rather than short-circuited away.
func statsFixture(t *testing.T, extraDDL ...string) (*Context, func()) {
	t.Helper()
	ctx, _, cleanup := newDDLFixture(t)
	ddls := []string{
		"CREATE TABLE t1 (a int, b int)",
		"CREATE TABLE t2 (a int, b int)",
	}
	ddls = append(ddls, extraDDL...)
	ddls = append(ddls,
		"INSERT INTO t1 VALUES (1, 10), (2, 20), (3, 30)",
		"INSERT INTO t2 VALUES (1, 100), (1, 101), (3, 300)",
	)
	for _, ddl := range ddls {
		if err := runDDL(t, ctx, ddl); err != nil {
			cleanup()
			t.Fatalf("%s: %v", ddl, err)
		}
	}
	return ctx, cleanup
}

// onlyStat returns the single recorded sublink's counters, failing
// when the query recorded none (which means the planner unnested
// the sublink and the probe no longer tests what it claims to).
func onlyStat(t *testing.T, ctx *Context) *SubPlanSiteStats {
	t.Helper()
	if len(ctx.SubPlanStats) != 1 {
		t.Fatalf("want exactly 1 instrumented sublink, got %d — did the planner unnest the probe?", len(ctx.SubPlanStats))
	}
	for _, s := range ctx.SubPlanStats {
		return s
	}
	return nil
}

// TestSubPlanStatsCorrelatedExistsRescans pins the Stage-9 (S2c) fix
// of the pathology this test originally documented: correlated EXISTS
// used to Build+Open+Close its inner plan per outer row; the SubPlan
// handle now builds once and rescans, which is what upstream always
// does (nodeSubplan.c ExecScanSubPlan → ExecReScan). The legacy
// counter shape (Rebuilds == Calls) is pinned separately by the
// kill-switch test in subplan_handle_test.go.
func TestSubPlanStatsCorrelatedExistsRescans(t *testing.T) {
	ctx, cleanup := statsFixture(t)
	defer cleanup()

	runQuery(t, ctx,
		"SELECT * FROM t1 WHERE t1.a = -999 OR EXISTS (SELECT 1 FROM t2 WHERE t2.a = t1.a)")

	s := onlyStat(t, ctx)
	if s.Calls != 3 {
		t.Errorf("Calls = %d, want 3 (one per t1 row)", s.Calls)
	}
	if s.Rebuilds != 1 {
		t.Errorf("Rebuilds = %d, want 1 (built once, then rescanned)", s.Rebuilds)
	}
	if s.Rescans != s.Calls-1 {
		t.Errorf("Rescans = %d, want Calls-1 (%d)", s.Rescans, s.Calls-1)
	}
}

// TestSubPlanStatsCorrelatedScalarRescans covers the one path that
// already reuses an operator: a correlated scalar subquery whose
// inner plan is index-scan-based is built once and re-Opened per
// outer row (CorrSubqOps). The first call is the build, the rest
// are rescans.
func TestSubPlanStatsCorrelatedScalarRescans(t *testing.T) {
	ctx, cleanup := statsFixture(t, "CREATE INDEX t2_a_idx ON t2 (a)")
	defer cleanup()

	runQuery(t, ctx,
		"SELECT * FROM t1 WHERE t1.a = -999 OR t1.b > (SELECT max(t2.b) FROM t2 WHERE t2.a = t1.a)")

	s := onlyStat(t, ctx)
	if s.Calls != 3 {
		t.Fatalf("Calls = %d, want 3", s.Calls)
	}
	if s.Rescans == 0 {
		t.Errorf("Rescans = 0, want > 0: the CorrSubqOps path should reuse the built operator (Rebuilds=%d)", s.Rebuilds)
	}
	if s.Rebuilds+s.Rescans != s.Calls {
		t.Errorf("Rebuilds(%d)+Rescans(%d) = %d, want == Calls(%d): every call must be accounted for",
			s.Rebuilds, s.Rescans, s.Rebuilds+s.Rescans, s.Calls)
	}
}

// TestSubPlanStatsNonCorrelatedCachesAfterFirst verifies the
// M0058-0001 constant-key cache still does its job and that the
// counters describe it correctly: one miss, the remainder hits.
func TestSubPlanStatsNonCorrelatedCachesAfterFirst(t *testing.T) {
	ctx, cleanup := statsFixture(t)
	defer cleanup()

	runQuery(t, ctx,
		"SELECT * FROM t1 WHERE t1.a = -999 OR t1.b > (SELECT max(t2.b) FROM t2)")

	s := onlyStat(t, ctx)
	if s.Calls != 3 {
		t.Fatalf("Calls = %d, want 3", s.Calls)
	}
	if s.CacheMisses != 1 {
		t.Errorf("CacheMisses = %d, want 1: a non-correlated sublink executes once", s.CacheMisses)
	}
	if s.CacheHits != s.Calls-1 {
		t.Errorf("CacheHits = %d, want %d", s.CacheHits, s.Calls-1)
	}
	if s.Rebuilds != 1 {
		t.Errorf("Rebuilds = %d, want 1", s.Rebuilds)
	}
}

// TestSubPlanStatsExplainAnalyzeSurface checks the counters reach
// the user: EXPLAIN ANALYZE annotates the `SubPlan N` line, plain
// EXPLAIN leaves it bare (nothing has executed, so there is nothing
// to report).
func TestSubPlanStatsExplainAnalyzeSurface(t *testing.T) {
	ctx, cleanup := statsFixture(t)
	defer cleanup()

	const probe = "SELECT * FROM t1 WHERE t1.a = -999 OR EXISTS (SELECT 1 FROM t2 WHERE t2.a = t1.a)"

	plain := strings.Join(runExplainRows(t, ctx, "EXPLAIN "+probe), "\n")
	for _, line := range strings.Split(plain, "\n") {
		if strings.Contains(line, "SubPlan 1") && strings.Contains(line, "calls=") {
			t.Errorf("plain EXPLAIN must not carry counters:\n%s", plain)
		}
	}

	analyzed := strings.Join(runExplainRows(t, ctx, "EXPLAIN (ANALYZE) "+probe), "\n")
	var counterLine string
	for _, line := range strings.Split(analyzed, "\n") {
		if strings.Contains(line, "SubPlan 1") {
			counterLine = strings.TrimSpace(line)
		}
	}
	if counterLine == "" {
		t.Fatalf("no SubPlan line in EXPLAIN ANALYZE output:\n%s", analyzed)
	}
	for _, want := range []string{"calls=", "rebuilds=", "rescans=", "hits=", "misses="} {
		if !strings.Contains(counterLine, want) {
			t.Errorf("SubPlan line %q missing %q", counterLine, want)
		}
	}
	if !strings.Contains(counterLine, "calls=3") {
		t.Errorf("SubPlan line %q: want calls=3 (one per t1 row)", counterLine)
	}
}

// TestSubPlanSubtreeIndentUnderRootNode pins a rendering fix this
// stage's ANALYZE probe surfaced: the subtree depth was derived
// from the owning node's tree depth, which assumes every node
// carries a "->  " prefix. The root node does not, so a sublink
// hanging off the root printed its plan 4 columns too deep. The
// subtree must start exactly 2 columns right of its `SubPlan N`
// line at every depth.
func TestSubPlanSubtreeIndentUnderRootNode(t *testing.T) {
	ctx, cleanup := statsFixture(t)
	defer cleanup()

	lines := runExplainRows(t, ctx,
		"EXPLAIN SELECT * FROM t1 WHERE t1.a = -999 OR EXISTS (SELECT 1 FROM t2 WHERE t2.a = t1.a)")

	var subPlanIndent = -1
	for _, l := range lines {
		trimmed := strings.TrimLeft(l, " ")
		switch {
		case strings.HasPrefix(trimmed, "SubPlan "):
			subPlanIndent = len(l) - len(trimmed)
		case subPlanIndent >= 0 && strings.HasPrefix(trimmed, "->  "):
			if got := len(l) - len(trimmed); got != subPlanIndent+2 {
				t.Errorf("subtree indent = %d, want %d (SubPlan line at %d):\n%s",
					got, subPlanIndent+2, subPlanIndent, strings.Join(lines, "\n"))
			}
			return
		}
	}
	t.Fatalf("no SubPlan subtree found:\n%s", strings.Join(lines, "\n"))
}

// TestSubPlanStatNilContextSafe guards the accessor: expressions are
// evaluated with a bare or absent Context in several test and
// utility paths, and instrumentation must never be the thing that
// panics there.
func TestSubPlanStatNilContextSafe(t *testing.T) {
	var ctx *Context
	e := &planner.ExistsExpr{}
	if s := ctx.subPlanStat(e); s == nil {
		t.Fatal("nil Context: want a throwaway stats block, got nil")
	}

	live := &Context{}
	s1 := live.subPlanStat(e)
	s1.Calls++
	if s2 := live.subPlanStat(e); s2 != s1 || s2.Calls != 1 {
		t.Errorf("repeat lookup must return the same block: got %+v", s2)
	}
}
