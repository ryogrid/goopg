package executor

// operators_dml_subpartition_test.go — M0134-0005aq: all four DML
// descendant-expansion sites (updateOp.Next, deleteOp.Next,
// updateOp.updateWithFrom, deleteOp.deleteWithUsing) called the FLAT
// im.PartitionChildren(tbl.OID), so a two-level partition hierarchy (a
// PARTITION OF that is itself PARTITION BY, holding leaf partitions of its
// own) silently modified zero rows: the intermediate sub-partitioned node has
// no storage of its own, and its leaf children were never reached. Fixed by
// routing all four sites through collectDMLPartitionLeaves, a recursive
// partitions-only/leaves-only BFS mirroring the SELECT-side
// collectAllPartitionLeaves (internal/optimizer/planner.go, M0097-0105).
//
// PG oracle: postgres/src/backend/optimizer/util/inherit.c:expand_inherited_rtentry
// (inherit.c:86) — a single recursive expansion site for every rte->inh RTE,
// target or not; PG cannot express this bug because there is no separate flat
// vs. recursive code path.

import "testing"

// subpartFixture builds a three-level partition hierarchy shared by the
// UPDATE/DELETE cases:
//
//	root_p (RANGE id)
//	  mid_p PARTITION OF root_p FOR VALUES FROM (0) TO (100), itself
//	        PARTITION BY RANGE (id)
//	    leaf_p PARTITION OF mid_p FOR VALUES FROM (0) TO (50)
//	other_t (id int, v int) — plain table for the FROM/USING cases
//
// One row lands in leaf_p via INSERT routing (already recursive — not under
// test here), and one matching row in other_t.
func subpartFixture(t *testing.T) (*Context, func()) {
	t.Helper()
	ctx, _, cleanup := newDDLFixture(t)
	for _, s := range []string{
		"CREATE TABLE root_p (id int, v int) PARTITION BY RANGE (id)",
		"CREATE TABLE mid_p PARTITION OF root_p FOR VALUES FROM (0) TO (100) PARTITION BY RANGE (id)",
		"CREATE TABLE leaf_p PARTITION OF mid_p FOR VALUES FROM (0) TO (50)",
		"CREATE TABLE other_t (id int, v int)",
		"INSERT INTO root_p VALUES (10, 1)",
		"INSERT INTO other_t VALUES (10, 99)",
	} {
		if err := runDDL(t, ctx, s); err != nil {
			cleanup()
			t.Fatalf("setup %q: %v", s, err)
		}
	}
	return ctx, cleanup
}

// TestDMLSubpartitionUpdate: bare UPDATE on the root must reach the leaf row
// two levels down.
func TestDMLSubpartitionUpdate(t *testing.T) {
	ctx, cleanup := subpartFixture(t)
	defer cleanup()

	sql := "UPDATE root_p SET v = 100 WHERE id = 10"
	plan := planDMLStmt(t, ctx, sql)
	op := execDMLPlan(t, ctx, sql, plan)
	if u := op.(*updateOp); u.RowsAffected() != 1 {
		t.Errorf("RowsAffected=%d want 1 (leaf_p row two levels down)", u.RowsAffected())
	}
	if err := op.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	rows := runQueryRows(t, ctx, "SELECT v FROM leaf_p")
	if len(rows) != 1 || rows[0][0].Kind != KindInt || rows[0][0].Int != 100 {
		t.Fatalf("leaf_p after UPDATE: %v, want single row v=100", rows)
	}
}

// TestDMLSubpartitionDelete: bare DELETE on the root must remove the leaf row
// two levels down.
func TestDMLSubpartitionDelete(t *testing.T) {
	ctx, cleanup := subpartFixture(t)
	defer cleanup()

	sql := "DELETE FROM root_p"
	plan := planDMLStmt(t, ctx, sql)
	op := execDMLPlan(t, ctx, sql, plan)
	if d := op.(*deleteOp); d.RowsAffected() != 1 {
		t.Errorf("RowsAffected=%d want 1 (leaf_p row two levels down)", d.RowsAffected())
	}
	if err := op.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	rows := runQueryRows(t, ctx, "SELECT v FROM leaf_p")
	if len(rows) != 0 {
		t.Fatalf("leaf_p after DELETE: %v, want empty", rows)
	}
}

// TestDMLSubpartitionUpdateFrom: UPDATE ... FROM on the root must reach the
// leaf row two levels down.
func TestDMLSubpartitionUpdateFrom(t *testing.T) {
	ctx, cleanup := subpartFixture(t)
	defer cleanup()

	sql := "UPDATE root_p SET v = other_t.v FROM other_t WHERE root_p.id = other_t.id"
	plan := planDMLStmt(t, ctx, sql)
	op := execDMLPlan(t, ctx, sql, plan)
	if u := op.(*updateOp); u.RowsAffected() != 1 {
		t.Errorf("RowsAffected=%d want 1 (leaf_p row two levels down)", u.RowsAffected())
	}
	if err := op.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	rows := runQueryRows(t, ctx, "SELECT v FROM leaf_p")
	if len(rows) != 1 || rows[0][0].Kind != KindInt || rows[0][0].Int != 99 {
		t.Fatalf("leaf_p after UPDATE...FROM: %v, want single row v=99", rows)
	}
}

// TestDMLSubpartitionDeleteUsing: DELETE ... USING on the root must remove the
// leaf row two levels down.
func TestDMLSubpartitionDeleteUsing(t *testing.T) {
	ctx, cleanup := subpartFixture(t)
	defer cleanup()

	sql := "DELETE FROM root_p USING other_t WHERE root_p.id = other_t.id"
	plan := planDMLStmt(t, ctx, sql)
	op := execDMLPlan(t, ctx, sql, plan)
	if d := op.(*deleteOp); d.RowsAffected() != 1 {
		t.Errorf("RowsAffected=%d want 1 (leaf_p row two levels down)", d.RowsAffected())
	}
	if err := op.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	rows := runQueryRows(t, ctx, "SELECT v FROM leaf_p")
	if len(rows) != 0 {
		t.Fatalf("leaf_p after DELETE...USING: %v, want empty", rows)
	}
}

// TestDMLSubpartitionOneLevelRegression: the pre-existing one-level partition
// case (root -> leaf, no intermediate sub-partitioned node) must keep working
// exactly as before — regression guard for collectDMLPartitionLeaves against
// the shape that was already passing.
func TestDMLSubpartitionOneLevelRegression(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	for _, s := range []string{
		"CREATE TABLE flat_p (id int, v int) PARTITION BY RANGE (id)",
		"CREATE TABLE flat_leaf PARTITION OF flat_p FOR VALUES FROM (0) TO (100)",
		"INSERT INTO flat_p VALUES (10, 1)",
	} {
		if err := runDDL(t, ctx, s); err != nil {
			t.Fatalf("setup %q: %v", s, err)
		}
	}

	sql := "UPDATE flat_p SET v = 100 WHERE id = 10"
	plan := planDMLStmt(t, ctx, sql)
	op := execDMLPlan(t, ctx, sql, plan)
	if u := op.(*updateOp); u.RowsAffected() != 1 {
		t.Errorf("RowsAffected=%d want 1 (one-level partition, no regression)", u.RowsAffected())
	}
	if err := op.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	rows := runQueryRows(t, ctx, "SELECT v FROM flat_leaf")
	if len(rows) != 1 || rows[0][0].Kind != KindInt || rows[0][0].Int != 100 {
		t.Fatalf("flat_leaf after UPDATE: %v, want single row v=100", rows)
	}
}

// TestDMLSubpartitionOnlySuppressesFanout: UPDATE ONLY on a sub-partitioned
// root must keep suppressing the fan-out entirely — collectDMLPartitionLeaves
// must sit inside the existing `!o.plan.Only` gate, not replace it.
func TestDMLSubpartitionOnlySuppressesFanout(t *testing.T) {
	ctx, cleanup := subpartFixture(t)
	defer cleanup()

	sql := "UPDATE ONLY root_p SET v = 100 WHERE id = 10"
	plan := planDMLStmt(t, ctx, sql)
	op := execDMLPlan(t, ctx, sql, plan)
	if u := op.(*updateOp); u.RowsAffected() != 0 {
		t.Errorf("RowsAffected=%d want 0 (ONLY must keep suppressing partition fan-out)", u.RowsAffected())
	}
	if err := op.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	rows := runQueryRows(t, ctx, "SELECT v FROM leaf_p")
	if len(rows) != 1 || rows[0][0].Kind != KindInt || rows[0][0].Int != 1 {
		t.Fatalf("leaf_p after UPDATE ONLY: %v, want row untouched (v=1)", rows)
	}
}
