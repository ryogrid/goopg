package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/parser"
)

// M0122-0012 slice 53: a non-correlated sublink (IsNonCorrelated, no lowered
// params) reads nothing of the row it is evaluated against — evalSubquery
// keys it by the sublink alone and its plan reads no outer value — so the
// deform walks no longer decline on it. A correlated or unlowered one still
// declines.
func TestDeformNonCorrelatedSublinkReadsNothing(t *testing.T) {
	cmp := func(sub *optimizer.SubqueryExpr) optimizer.Expr {
		return &optimizer.BinaryOp{Op: parser.OpEq, Left: deformCol(3), Right: sub}
	}
	if got := deformFoldRefs(deformBoundNone, cmp(&optimizer.SubqueryExpr{IsNonCorrelated: true})); got != 3 {
		t.Fatalf("non-correlated sublink: bound %d, want 3 (column 3 alone)", got)
	}
	if got := deformFoldRefs(deformBoundNone, cmp(&optimizer.SubqueryExpr{})); got != deformBoundFull {
		t.Fatalf("correlated sublink: bound %d, want full", got)
	}

	// The lateral probe fold: a non-correlated sublink in the probe's Cond
	// leaves the outer bound at the key's column; a correlated one fails it
	// to full (TestDeformJoinBoundsFoldsLateralProbeKeys' second case).
	outer := &optimizer.SeqScan{Table: deformTable(6)}
	key := &optimizer.OuterColumnRef{Level: 1, Index: 2, Name: "c2", Type: catalog.Type{Name: "int4"}}
	probe := (&optimizer.IndexScan{Table: deformTable(4), Key: key}).WithSchemaForTest(make(optimizer.Schema, 4))
	join := &optimizer.Join{Type: optimizer.JoinTypeInner, Algo: optimizer.JoinAlgoNestedLoop, Lateral: true, Left: outer, Right: probe}
	probe.Cond = &optimizer.BinaryOp{Op: parser.OpEq, Left: deformCol(1), Right: &optimizer.SubqueryExpr{IsNonCorrelated: true}}
	if left, _ := deformJoinBounds(join, 0); left != 2 {
		t.Fatalf("lateral left bound with a non-correlated sublink in the probe Cond = %d, want 2", left)
	}
	probe.Cond = &optimizer.BinaryOp{Op: parser.OpEq, Left: deformCol(1), Right: &optimizer.SubqueryExpr{}}
	if left, _ := deformJoinBounds(join, 0); left != deformBoundFull {
		t.Fatalf("lateral left bound with a correlated sublink = %d, want full", left)
	}
}

// TestLateralProbeWithInitPlanFilterValues runs the Q14b shape end to end: a
// nested-loop index probe whose filter compares to a non-correlated sublink.
// With the deform poison armed, a column the narrowed bound wrongly dropped
// fails loudly; the probe's result must equal the hash join's.
func TestLateralProbeWithInitPlanFilterValues(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	for _, q := range []string{
		"CREATE TABLE ipo (a int NOT NULL, pad1 int, k int NOT NULL, pad2 text, pad3 int)",
		"INSERT INTO ipo SELECT g, g * 3, g % 400, 'p' || g, g FROM generate_series(1, 4000) g",
		"CREATE TABLE ipi (k int NOT NULL, w int NOT NULL, v int NOT NULL, tail text)",
		"INSERT INTO ipi SELECT g % 4000, g % 7, g, 't' || g FROM generate_series(1, 40000) g",
		"CREATE INDEX ipi_k ON ipi (k)",
		"ANALYZE ipo", "ANALYZE ipi",
	} {
		runSQL(t, ctx, q)
	}
	old := seqScanDeformPoison
	seqScanDeformPoison = true
	t.Cleanup(func() { seqScanDeformPoison = old })

	const sql = "SELECT sum(o.a), count(i.v), sum(i.v) FROM ipo o, ipi i WHERE i.k = o.k AND i.w = (SELECT max(w) - 2 FROM ipi)"
	hash := optimizer.DefaultPlannerSettings()
	hash.MaxParallelWorkersPerGather = 0
	hash.EnableNestLoop, hash.EnableMergeJoin = false, false
	probe := optimizer.DefaultPlannerSettings()
	probe.MaxParallelWorkersPerGather = 0
	probe.EnableHashJoin, probe.EnableMergeJoin = false, false
	probe.EnableBitmapScan, probe.EnableMaterial, probe.EnableMemoize = false, false, false

	plan := strings.Join(renderRows(runSQLWith(t, ctx, "EXPLAIN (COSTS OFF) "+sql, probe)), "\n")
	if !strings.Contains(plan, "Nested Loop") || !strings.Contains(plan, "using ipi_k") {
		t.Fatalf("want a nested loop probing ipi_k:\n%s", plan)
	}
	want := strings.Join(renderRows(runSQLWith(t, ctx, sql, hash)), ";")
	got := strings.Join(renderRows(runSQLWith(t, ctx, sql, probe)), ";")
	if got != want {
		t.Fatalf("nested-loop probe = %q, hash join = %q\nplan:\n%s", got, want, plan)
	}
	if got == "" || strings.HasPrefix(got, "NULL") {
		t.Fatalf("empty result %q: the fixture matches nothing", got)
	}
	t.Logf("plan:\n%s\nresult %s", plan, got)
}
