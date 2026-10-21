package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/optimizer"
)

// TestLateralProbeKeepsOuterKeyDeformed pins deformLateralOuterRefs
// (M0122-0012 slice 35): a LATERAL nested loop's inner index probe reads its
// key from the OUTER row, so the outer scan must deform the key column even
// when nothing above the join reads it. The aggregate here reads o.a (column
// 0) and i.v only; o.k (column 2) is read by the probe alone. Without the
// fold the outer scan deforms [0, 1) and the probe keys on a stale k.
//
// Run with the deform poison armed, so a probe that reads an undeformed
// slot fails loudly instead of matching by accident.
func TestLateralProbeKeepsOuterKeyDeformed(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	for _, q := range []string{
		"CREATE TABLE lok_outer (a int NOT NULL, b int NOT NULL, k int NOT NULL)",
		"INSERT INTO lok_outer SELECT g, g * 3, g % 400 FROM generate_series(1, 4000) g",
		"CREATE TABLE lok_inner (k int NOT NULL, v int NOT NULL)",
		"INSERT INTO lok_inner SELECT g % 4000, g FROM generate_series(1, 40000) g",
		"CREATE INDEX lok_inner_k ON lok_inner (k)",
		"ANALYZE lok_outer", "ANALYZE lok_inner",
	} {
		runSQL(t, ctx, q)
	}
	old := seqScanDeformPoison
	seqScanDeformPoison = true
	t.Cleanup(func() { seqScanDeformPoison = old })

	const sql = "SELECT sum(o.a), count(i.v), sum(i.v) FROM lok_outer o, lok_inner i WHERE i.k = o.k"
	hash := optimizer.DefaultPlannerSettings()
	hash.MaxParallelWorkersPerGather = 0
	hash.EnableNestLoop, hash.EnableMergeJoin = false, false
	probe := optimizer.DefaultPlannerSettings()
	probe.MaxParallelWorkersPerGather = 0
	probe.EnableHashJoin, probe.EnableMergeJoin = false, false
	probe.EnableBitmapScan, probe.EnableMaterial, probe.EnableMemoize = false, false, false

	plan := strings.Join(renderRows(runSQLWith(t, ctx, "EXPLAIN (COSTS OFF) "+sql, probe)), "\n")
	if !strings.Contains(plan, "Nested Loop") || !strings.Contains(plan, "using lok_inner_k") {
		t.Fatalf("want a nested loop probing lok_inner_k:\n%s", plan)
	}
	want := strings.Join(renderRows(runSQLWith(t, ctx, sql, hash)), ";")
	got := strings.Join(renderRows(runSQLWith(t, ctx, sql, probe)), ";")
	if got != want {
		t.Fatalf("nested-loop probe = %q, hash join = %q\nplan:\n%s", got, want, plan)
	}
	t.Logf("plan:\n%s\nresult %s", plan, got)
}

// TestDeformJoinBoundsFoldsLateralProbeKeys pins the fold where the probe's
// key is the only reader of the outer column: a lateral join over an index
// probe whose clause the probe absorbed (no join Predicate repeats it), as
// the planner emits for TPC-DS Q14. Above the join only outer column 0 is
// read; the probe keys on outer column 2 (OuterColumnRef level 1), so the
// left bound must reach 2. A sublink in the probe's Cond must fail the left
// side to full.
func TestDeformJoinBoundsFoldsLateralProbeKeys(t *testing.T) {
	outer := &optimizer.SeqScan{Table: deformTable(6)}
	key := &optimizer.OuterColumnRef{Level: 1, Index: 2, Name: "c2", Type: catalog.Type{Name: "int4"}}
	probe := (&optimizer.IndexScan{Table: deformTable(4), Key: key}).WithSchemaForTest(make(optimizer.Schema, 4))
	join := &optimizer.Join{Type: optimizer.JoinTypeInner, Algo: optimizer.JoinAlgoNestedLoop, Lateral: true, Left: outer, Right: probe}

	left, _ := deformJoinBounds(join, 0)
	if left != 2 {
		t.Fatalf("left bound = %d, want 2 (the probe key's outer column)", left)
	}

	probe.Cond = &optimizer.SubqueryExpr{}
	if left, _ := deformJoinBounds(join, 0); left != deformBoundFull {
		t.Fatalf("left bound with a sublink in the probe Cond = %d, want full", left)
	}
}
