package executor

import (
	"strings"
	"testing"
)

// TestExplainSelfCorrelatedExistsDoesNotAliasCollide guards M0142-0008e: a
// self-correlated EXISTS whose outer table and EXISTS-body table happen to
// share the SAME raw SourceTableIdx (both are "first table in their own
// FROM list" — the outer query's level and the EXISTS body's level each
// restart that counter at 1, per explain_names.go's documented per-level
// scheme) used to print BOTH sides of the decorrelated Semi Join's key and
// residual with the OUTER alias, e.g. `t1.a = t1.a` / `t1.b <> t1.b` instead
// of `t1.a = t2.a` / `t1.b <> t2.b` — a pure EXPLAIN-display defect (M0142-
// 0008d's design doc §9: execution and row counts were unaffected in every
// case measured; only the printed text was wrong).
//
// unnestExistsExpr splices the EXISTS body into the outer tree as an
// ordinary Join.Right child, so explain_names.go's collect() walks both
// scans in ONE tree; whichever scan registers first under the raw
// SourceTableIdx key wins the printed name for every ColumnRef carrying
// that value, including the ones that meant the OTHER table.
// remapSourceTableIdx (unnest.go) fixes this by shifting the entire inner
// plan's SourceTableIdx numbering (schema columns and embedded ColumnRefs
// alike) past anything the outer side uses, so the two scopes can never
// collide once merged into one EXPLAIN tree.
func TestExplainSelfCorrelatedExistsDoesNotAliasCollide(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	if err := runDDL(t, ctx, "CREATE TABLE t (a int, b int)"); err != nil {
		t.Fatal(err)
	}

	lines := runExplain(t, ctx, `SELECT 1 FROM t t1 WHERE EXISTS (
		SELECT 1 FROM t t2 WHERE t2.a = t1.a AND t2.b <> t1.b
	)`)
	joined := strings.Join(lines, "\n")

	if !strings.Contains(joined, "t1.a = t2.a") && !strings.Contains(joined, "t2.a = t1.a") {
		t.Errorf("want the hash/equi key to name BOTH t1 and t2, got:\n%s", joined)
	}
	if !strings.Contains(joined, "t1.b <> t2.b") && !strings.Contains(joined, "t2.b <> t1.b") {
		t.Errorf("want the residual filter to name BOTH t1 and t2, got:\n%s", joined)
	}
	if strings.Contains(joined, "t1.a = t1.a") {
		t.Errorf("self-comparison alias collision on the equi key:\n%s", joined)
	}
	if strings.Contains(joined, "t1.b <> t1.b") {
		t.Errorf("self-comparison alias collision on the residual filter:\n%s", joined)
	}
	if strings.Contains(joined, "t2.a = t2.a") {
		t.Errorf("self-comparison alias collision (t2 side) on the equi key:\n%s", joined)
	}
	if strings.Contains(joined, "t2.b <> t2.b") {
		t.Errorf("self-comparison alias collision (t2 side) on the residual filter:\n%s", joined)
	}
}
