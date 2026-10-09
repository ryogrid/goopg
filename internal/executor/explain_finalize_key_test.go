package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
)

// TestFinalizeGroupKeyOverGatherMergeIsParenthesised pins M0146-0005cr
// against PG 18.3: show_agg_keys deparses a group key against the child's
// targetlist, and a Gather Merge only passes its input's columns through,
// so a computed key reaches the Finalize GroupAggregate as an OUTER_VAR and
// prints in parentheses — PG's own output for this statement (same settings):
//
//	Finalize GroupAggregate
//	  Group Key: (substr((b)::text, 1, 2))
//	  ->  Gather Merge
//	        ->  Partial GroupAggregate
//	              Group Key: (substr((b)::text, 1, 2))
func TestFinalizeGroupKeyOverGatherMergeIsParenthesised(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	runSQL(t, ctx, "CREATE TABLE zpg1 (a int, b varchar(30))")
	runSQL(t, ctx, "INSERT INTO zpg1 SELECT i, 'x' || (i % 50) FROM generate_series(1,20000) i")
	runSQL(t, ctx, "ANALYZE zpg1")
	ps := optimizer.DefaultPlannerSettings()
	ps.MaxParallelWorkersPerGather = 2
	ps.ParallelSetupCost = 0
	ps.ParallelTupleCost = 0
	ps.MinParallelTableScanSize = 0
	ps.EnableHashAgg = false
	const q = "SELECT substr(b, 1, 2), count(*) FROM zpg1 GROUP BY substr(b, 1, 2)"
	var lines []string
	for _, r := range drainPlanRows(t, ctx, planWithSettings(t, ctx, "EXPLAIN (COSTS OFF) "+q, ps)) {
		if len(r) > 0 && r[0].Kind == KindString {
			lines = append(lines, strings.TrimSpace(r[0].StringValue()))
		}
	}
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "Finalize GroupAggregate") {
		t.Skipf("no Finalize GroupAggregate in this plan:\n%s", joined)
	}
	if lines[0] != "Finalize GroupAggregate" || lines[1] != "Group Key: (substr((b)::text, 1, 2))" {
		t.Fatalf("want the Finalize key parenthesised:\n%s", joined)
	}
}

// TestFinalizeGroupKeyOverUnionAllPrintsPartialText pins the M0146-0042
// slice for TPC-DS Q76's shape: a Finalize aggregate's keys index the
// Partial's transport row, and PG deparses them through the Gather into the
// Partial's target list, so both print the same text. PG 18.3 (same
// settings):
//
//	Finalize GroupAggregate
//	  Group Key: ('x'::text), zu1.a
//	  ->  Gather Merge
//	        ->  Partial GroupAggregate
//	              Group Key: ('x'::text), zu1.a
//
// goopg printed the Finalize's output names bare (`ch, a`).
func TestFinalizeGroupKeyOverUnionAllPrintsPartialText(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	runSQL(t, ctx, "CREATE TABLE zu1 (a int, b int)")
	runSQL(t, ctx, "CREATE TABLE zu2 (a int, b int)")
	runSQL(t, ctx, "INSERT INTO zu1 SELECT i % 50, i FROM generate_series(1,20000) i")
	runSQL(t, ctx, "INSERT INTO zu2 SELECT i % 50, i FROM generate_series(1,20000) i")
	runSQL(t, ctx, "ANALYZE zu1")
	runSQL(t, ctx, "ANALYZE zu2")
	ps := optimizer.DefaultPlannerSettings()
	ps.MaxParallelWorkersPerGather = 2
	ps.ParallelSetupCost = 0
	ps.ParallelTupleCost = 0
	ps.MinParallelTableScanSize = 0
	ps.EnableHashAgg = false
	const q = "SELECT ch, a, count(*) FROM (SELECT 'x' AS ch, a FROM zu1 UNION ALL SELECT 'y', a FROM zu2) s GROUP BY ch, a"
	var lines []string
	for _, r := range drainPlanRows(t, ctx, planWithSettings(t, ctx, "EXPLAIN (COSTS OFF) "+q, ps)) {
		if len(r) > 0 && r[0].Kind == KindString {
			lines = append(lines, strings.TrimSpace(r[0].StringValue()))
		}
	}
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "Finalize GroupAggregate") || !strings.Contains(joined, "Partial GroupAggregate") {
		t.Fatalf("the shape under test is gone (want Finalize over Partial GroupAggregate):\n%s", joined)
	}
	var keys []string
	for _, l := range lines {
		if strings.HasPrefix(l, "Group Key: ") {
			keys = append(keys, l)
		}
	}
	if len(keys) != 2 || keys[0] != keys[1] || keys[0] != "Group Key: ('x'::text), zu1.a" {
		t.Fatalf("want PG's `Group Key: ('x'::text), zu1.a` on both aggregates, got %q:\n%s", keys, joined)
	}
}

// TestSortKeyThroughFinalizeGroupKey pins the TPC-DS Q77 shape: a Sort above
// a join reads a grouped, inlined CTE whose aggregate split into Finalize
// over Partial. The Finalize's group position names the Partial's key, and
// PG deparses it through the Gather into the Partial's target list — PG
// 18.3 (same settings) prints `Sort Key: (sum(zu1.b)), zu1.a` and
// `Hash Cond: (zu1.a = zu2.a)`. goopg stopped at the Finalize and printed
// the CTE alias (`ss.a`).
func TestSortKeyThroughFinalizeGroupKey(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	runSQL(t, ctx, "CREATE TABLE zu1 (a int, b int)")
	runSQL(t, ctx, "CREATE TABLE zu2 (a int, b int)")
	runSQL(t, ctx, "INSERT INTO zu1 SELECT i % 50, i FROM generate_series(1,20000) i")
	runSQL(t, ctx, "INSERT INTO zu2 SELECT i, i FROM generate_series(1,50) i")
	runSQL(t, ctx, "ANALYZE zu1")
	runSQL(t, ctx, "ANALYZE zu2")
	ps := optimizer.DefaultPlannerSettings()
	ps.MaxParallelWorkersPerGather = 2
	ps.ParallelSetupCost = 0
	ps.ParallelTupleCost = 0
	ps.MinParallelTableScanSize = 0
	ps.EnableHashAgg = false
	const q = "WITH ss AS (SELECT a, sum(b) s FROM zu1 GROUP BY a) SELECT ss.a, ss.s, zu2.b FROM ss JOIN zu2 ON zu2.a = ss.a ORDER BY ss.s, ss.a"
	var lines []string
	for _, r := range drainPlanRows(t, ctx, planWithSettings(t, ctx, "EXPLAIN (COSTS OFF) "+q, ps)) {
		if len(r) > 0 && r[0].Kind == KindString {
			lines = append(lines, strings.TrimSpace(r[0].StringValue()))
		}
	}
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "Finalize GroupAggregate") {
		t.Fatalf("the shape under test is gone (want a Finalize GroupAggregate):\n%s", joined)
	}
	for _, want := range []string{"Sort Key: (sum(zu1.b)), zu1.a", "Hash Cond: (zu1.a = zu2.a)"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing PG's %q in:\n%s", want, joined)
		}
	}
}
