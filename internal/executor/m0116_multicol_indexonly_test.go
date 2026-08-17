package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/optimizer"
)

// findIndexOnlyScan walks the planner tree through the unary scaffolding
// nodes (Project / Filter / Sort / Limit) and returns the first
// *planner.IndexOnlyScan, or nil if none is present.
func findIndexOnlyScan(n optimizer.Node) *optimizer.IndexOnlyScan {
	for n != nil {
		switch v := n.(type) {
		case *optimizer.IndexOnlyScan:
			return v
		case *optimizer.Project:
			n = v.Child
		case *optimizer.Filter:
			n = v.Child
		case *optimizer.Sort:
			n = v.Child
		case *optimizer.Limit:
			n = v.Child
		default:
			return nil
		}
	}
	return nil
}

// findIndexScan mirrors findIndexOnlyScan for the heap-fetching IndexScan
// node so HeapFallback tests can prove the planner did NOT promote.
func findIndexScan(n optimizer.Node) *optimizer.IndexScan {
	for n != nil {
		switch v := n.(type) {
		case *optimizer.IndexScan:
			return v
		case *optimizer.IndexOnlyScan:
			return nil
		case *optimizer.Project:
			n = v.Child
		case *optimizer.Filter:
			n = v.Child
		case *optimizer.Sort:
			n = v.Child
		case *optimizer.Limit:
			n = v.Child
		default:
			return nil
		}
	}
	return nil
}

// runComposite is a small batch runner: every DDL/DML failure is fatal so the
// test bodies below stay focused on the M0116 assertions.
func runComposite(t *testing.T, ctx *Context, stmts ...string) {
	t.Helper()
	for _, s := range stmts {
		if err := runDDL(t, ctx, s); err != nil {
			t.Fatalf("runDDL(%q): %v", s, err)
		}
	}
}

// vacuumThen commits the seeding txn, starts a fresh one, VACUUMs `tbl`, and
// verifies the heap page is now ALL_VISIBLE — the runtime precondition for
// the IOS fast path on top of M0116-0001/0002.
func vacuumThen(t *testing.T, ctx *Context, tbl string) {
	t.Helper()
	commitTx(t, ctx)
	beginTx(t, ctx)
	if err := runDDL(t, ctx, "VACUUM "+tbl); err != nil {
		t.Fatalf("VACUUM %s: %v", tbl, err)
	}
	c, ok := ctx.Catalog.LookupTable(parser.ObjectName{Name: tbl})
	if !ok {
		t.Fatalf("LookupTable(%q): not found", tbl)
	}
	rel := ctx.Catalog.RelFileNode(c)
	if !ctx.VM.AllVisible(rel, 0) {
		t.Fatalf("%s page 0 not ALL_VISIBLE after VACUUM", tbl)
	}
}

// TestIOS_CompositeInt4Int4 is M0116-0003 §5 row 1: a 2-column (int4, int4)
// composite primary key, projecting both key columns. Asserts the planner
// promotes to IndexOnlyScan and the runtime returns correct rows decoded
// from the multi-column B-tree key bytes (M0116-0001 decodeRowFromKey loop).
func TestIOS_CompositeInt4Int4(t *testing.T) {
	ctx, cleanup := newVMFixture(t)
	defer cleanup()

	runComposite(t, ctx,
		"CREATE TABLE ii (a int, b int)",
		"CREATE UNIQUE INDEX ii_pk ON ii (a, b)",
		"INSERT INTO ii VALUES (1, 10)",
		"INSERT INTO ii VALUES (1, 20)",
		"INSERT INTO ii VALUES (2, 30)",
	)
	vacuumThen(t, ctx, "ii")

	const q = "SELECT a, b FROM ii WHERE a = 1 AND b = 20"
	ios := findIndexOnlyScan(planOne(t, q, ctx.Catalog))
	if ios == nil {
		t.Fatalf("plan for %q has no IndexOnlyScan", q)
	}
	if len(ios.Index.Columns) != 2 {
		t.Fatalf("Index.Columns=%v want 2 cols", ios.Index.Columns)
	}
	if len(ios.Covered) != 2 {
		t.Fatalf("Covered=%v want 2 cols", ios.Covered)
	}

	rows := runQuery(t, ctx, q)
	if len(rows) != 1 {
		t.Fatalf("rows=%d want 1", len(rows))
	}
	if rows[0][0].Int != 1 || rows[0][1].Int != 20 {
		t.Errorf("row=%+v want [1, 20]", rows[0])
	}
}

// TestIOS_CompositeInt4Text is M0116-0003 §5 row 2: a (int4, text) composite
// unique index. Exercises the variable-length DecodeVarcharLen path inside
// the multi-column decoder loop.
func TestIOS_CompositeInt4Text(t *testing.T) {
	ctx, cleanup := newVMFixture(t)
	defer cleanup()

	runComposite(t, ctx,
		"CREATE TABLE it (k int, s text)",
		"CREATE UNIQUE INDEX it_uk ON it (k, s)",
		"INSERT INTO it VALUES (1, 'alpha')",
		"INSERT INTO it VALUES (1, 'beta')",
		"INSERT INTO it VALUES (2, 'gamma')",
	)
	vacuumThen(t, ctx, "it")

	const q = "SELECT k, s FROM it WHERE k = 1 AND s = 'beta'"
	ios := findIndexOnlyScan(planOne(t, q, ctx.Catalog))
	if ios == nil {
		t.Fatalf("plan for %q has no IndexOnlyScan", q)
	}
	if len(ios.Index.Columns) != 2 {
		t.Fatalf("Index.Columns=%v want 2 cols", ios.Index.Columns)
	}

	rows := runQuery(t, ctx, q)
	if len(rows) != 1 {
		t.Fatalf("rows=%d want 1", len(rows))
	}
	if rows[0][0].Int != 1 || rows[0][1].StringValue() != "beta" {
		t.Errorf("row=%+v want [1, beta]", rows[0])
	}
}

// TestIOS_HeapFallback is M0116-0003 §5 row 3: when the query projects a
// column NOT in the composite index, the planner must NOT promote to
// IndexOnlyScan — IndexScan (heap fetch) is the only correct choice.
func TestIOS_HeapFallback(t *testing.T) {
	ctx, cleanup := newVMFixture(t)
	defer cleanup()

	runComposite(t, ctx,
		"CREATE TABLE hf (a int, b int, c text)",
		"CREATE INDEX hf_ab ON hf (a, b)",
		"INSERT INTO hf VALUES (1, 10, 'one-ten')",
		"INSERT INTO hf VALUES (1, 20, 'one-twenty')",
	)
	vacuumThen(t, ctx, "hf")

	// `c` is not in the (a, b) index → IOS promotion must be rejected.
	const q = "SELECT a, b, c FROM hf WHERE a = 1 AND b = 20"
	plan := planOne(t, q, ctx.Catalog)
	if ios := findIndexOnlyScan(plan); ios != nil {
		t.Fatalf("expected IndexScan (heap fetch); planner promoted to IndexOnlyScan: %+v", ios.Index.Columns)
	}
	if is := findIndexScan(plan); is == nil {
		t.Fatalf("expected IndexScan in plan for %q, got root %T", q, plan)
	}

	rows := runQuery(t, ctx, q)
	if len(rows) != 1 {
		t.Fatalf("rows=%d want 1", len(rows))
	}
	if rows[0][0].Int != 1 || rows[0][1].Int != 20 || rows[0][2].StringValue() != "one-twenty" {
		t.Errorf("row=%+v want [1, 20, one-twenty]", rows[0])
	}
}

// TestIOS_3Columns is M0116-0003 §5 row 5: a 3-column composite key whose
// projection fully overlaps the index, exercising the decoder loop's
// `off += n` advancement across three boundaries.
func TestIOS_3Columns(t *testing.T) {
	ctx, cleanup := newVMFixture(t)
	defer cleanup()

	runComposite(t, ctx,
		"CREATE TABLE t3 (a int, b int, c int)",
		"ALTER TABLE t3 ADD CONSTRAINT t3_pk PRIMARY KEY (a, b, c)",
		"INSERT INTO t3 VALUES (1, 10, 100)",
		"INSERT INTO t3 VALUES (1, 20, 200)",
		"INSERT INTO t3 VALUES (2, 10, 300)",
	)
	vacuumThen(t, ctx, "t3")

	const q = "SELECT a, b, c FROM t3 WHERE a = 1 AND b = 20 AND c = 200"
	ios := findIndexOnlyScan(planOne(t, q, ctx.Catalog))
	if ios == nil {
		t.Fatalf("plan for %q has no IndexOnlyScan", q)
	}
	if len(ios.Index.Columns) != 3 {
		t.Fatalf("Index.Columns=%v want 3 cols", ios.Index.Columns)
	}
	if len(ios.Covered) != 3 {
		t.Fatalf("Covered=%v want 3 cols", ios.Covered)
	}

	rows := runQuery(t, ctx, q)
	if len(rows) != 1 {
		t.Fatalf("rows=%d want 1", len(rows))
	}
	if rows[0][0].Int != 1 || rows[0][1].Int != 20 || rows[0][2].Int != 200 {
		t.Errorf("row=%+v want [1, 20, 200]", rows[0])
	}
}
