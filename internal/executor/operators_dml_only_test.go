package executor

// operators_dml_only_test.go — M0134-0005ao: `DELETE FROM ONLY <t>` /
// `UPDATE ONLY <t> SET …` must stop touching inheritance and partition
// descendants, exactly as PG does. `RangeVar.Only` was already parsed
// correctly but silently dropped between the planner and the four
// execution-time expansion sites in operators_storage.go (updateOp.Next,
// updateOp.updateWithFrom, deleteOp.Next, deleteOp.deleteWithUsing).
// Precedent: the SELECT side already honors ONLY via the `!rv.Only` gate
// at internal/optimizer/planner.go:3047 (planScanRangeVar); this test
// covers the DML twin.

import "testing"

// TestDeleteFromOnlyInherit: DELETE FROM ONLY p leaves the INHERITS
// child's row untouched and reports only the parent's own row deleted.
func TestDeleteFromOnlyInherit(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	for _, s := range []string{
		"CREATE TABLE donly_p (id int primary key, val int)",
		"CREATE TABLE donly_c (extra text) INHERITS (donly_p)",
		"INSERT INTO donly_p (id, val) VALUES (1, 10)",
		"INSERT INTO donly_c (id, val, extra) VALUES (2, 20, 'x')",
	} {
		if err := runDDL(t, ctx, s); err != nil {
			t.Fatalf("setup %q: %v", s, err)
		}
	}

	plan := planDMLStmt(t, ctx, "DELETE FROM ONLY donly_p")
	op := execDMLPlan(t, ctx, "DELETE FROM ONLY donly_p", plan)
	if d := op.(*deleteOp); d.RowsAffected() != 1 {
		t.Errorf("RowsAffected=%d want 1 (only the parent's own row)", d.RowsAffected())
	}
	if err := op.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	rows := runQueryRows(t, ctx, "SELECT val FROM ONLY donly_p")
	if len(rows) != 0 {
		t.Fatalf("parent's own row must be gone: %v", rows)
	}
	rows = runQueryRows(t, ctx, "SELECT val FROM ONLY donly_c")
	if len(rows) != 1 || rows[0][0].Kind != KindInt || rows[0][0].Int != 20 {
		t.Fatalf("child row must survive DELETE FROM ONLY: %v", rows)
	}
}

// TestDeleteFromInheritNoOnlyRegression: DELETE FROM p (no ONLY) against
// the same fixture must still fan out to the child — the no-regression
// half of this campaign's convention. This MUST pass both before and
// after the fix.
func TestDeleteFromInheritNoOnlyRegression(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	for _, s := range []string{
		"CREATE TABLE dnoonly_p (id int primary key, val int)",
		"CREATE TABLE dnoonly_c (extra text) INHERITS (dnoonly_p)",
		"INSERT INTO dnoonly_p (id, val) VALUES (1, 10)",
		"INSERT INTO dnoonly_c (id, val, extra) VALUES (2, 20, 'x')",
	} {
		if err := runDDL(t, ctx, s); err != nil {
			t.Fatalf("setup %q: %v", s, err)
		}
	}

	plan := planDMLStmt(t, ctx, "DELETE FROM dnoonly_p")
	op := execDMLPlan(t, ctx, "DELETE FROM dnoonly_p", plan)
	if d := op.(*deleteOp); d.RowsAffected() != 2 {
		t.Errorf("RowsAffected=%d want 2 (parent + child)", d.RowsAffected())
	}
	if err := op.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	rows := runQueryRows(t, ctx, "SELECT val FROM dnoonly_p")
	if len(rows) != 0 {
		t.Fatalf("no-ONLY DELETE must fan out to the child too: %v", rows)
	}
}

// TestUpdateOnlyInherit: UPDATE ONLY p SET … leaves the INHERITS child's
// row unmodified and reports only the parent's own row updated.
func TestUpdateOnlyInherit(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	for _, s := range []string{
		"CREATE TABLE uonly_p (id int primary key, val int)",
		"CREATE TABLE uonly_c (extra text) INHERITS (uonly_p)",
		"INSERT INTO uonly_p (id, val) VALUES (1, 10)",
		"INSERT INTO uonly_c (id, val, extra) VALUES (2, 20, 'x')",
	} {
		if err := runDDL(t, ctx, s); err != nil {
			t.Fatalf("setup %q: %v", s, err)
		}
	}

	plan := planDMLStmt(t, ctx, "UPDATE ONLY uonly_p SET val = val + 100")
	op := execDMLPlan(t, ctx, "UPDATE ONLY uonly_p SET val = val + 100", plan)
	if u := op.(*updateOp); u.RowsAffected() != 1 {
		t.Errorf("RowsAffected=%d want 1 (only the parent's own row)", u.RowsAffected())
	}
	if err := op.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	rows := runQueryRows(t, ctx, "SELECT val FROM ONLY uonly_p")
	if len(rows) != 1 || rows[0][0].Kind != KindInt || rows[0][0].Int != 110 {
		t.Fatalf("parent's own row must be updated: %v", rows)
	}
	rows = runQueryRows(t, ctx, "SELECT val FROM ONLY uonly_c")
	if len(rows) != 1 || rows[0][0].Kind != KindInt || rows[0][0].Int != 20 {
		t.Fatalf("child row must be unmodified by UPDATE ONLY: %v", rows)
	}
}

// TestUpdateInheritNoOnlyRegression: UPDATE p SET … (no ONLY) against the
// same fixture must still fan out to the child.
func TestUpdateInheritNoOnlyRegression(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	for _, s := range []string{
		"CREATE TABLE unoonly_p (id int primary key, val int)",
		"CREATE TABLE unoonly_c (extra text) INHERITS (unoonly_p)",
		"INSERT INTO unoonly_p (id, val) VALUES (1, 10)",
		"INSERT INTO unoonly_c (id, val, extra) VALUES (2, 20, 'x')",
	} {
		if err := runDDL(t, ctx, s); err != nil {
			t.Fatalf("setup %q: %v", s, err)
		}
	}

	plan := planDMLStmt(t, ctx, "UPDATE unoonly_p SET val = val + 100")
	op := execDMLPlan(t, ctx, "UPDATE unoonly_p SET val = val + 100", plan)
	if u := op.(*updateOp); u.RowsAffected() != 2 {
		t.Errorf("RowsAffected=%d want 2 (parent + child)", u.RowsAffected())
	}
	if err := op.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	rows := runQueryRows(t, ctx, "SELECT val FROM unoonly_c")
	if len(rows) != 1 || rows[0][0].Kind != KindInt || rows[0][0].Int != 120 {
		t.Fatalf("no-ONLY UPDATE must fan out to the child too: %v", rows)
	}
}

// TestDeleteFromOnlyUsingInherit covers the cross-product variant
// (deleteOp.deleteWithUsing).
func TestDeleteFromOnlyUsingInherit(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	for _, s := range []string{
		"CREATE TABLE donlyu_p (id int primary key, val int)",
		"CREATE TABLE donlyu_c (extra text) INHERITS (donlyu_p)",
		"CREATE TABLE donlyu_src (id int)",
		"INSERT INTO donlyu_p (id, val) VALUES (1, 10)",
		"INSERT INTO donlyu_c (id, val, extra) VALUES (2, 20, 'x')",
		"INSERT INTO donlyu_src (id) VALUES (1), (2)",
	} {
		if err := runDDL(t, ctx, s); err != nil {
			t.Fatalf("setup %q: %v", s, err)
		}
	}

	sql := "DELETE FROM ONLY donlyu_p USING donlyu_src WHERE donlyu_p.id = donlyu_src.id"
	plan := planDMLStmt(t, ctx, sql)
	op := execDMLPlan(t, ctx, sql, plan)
	if d := op.(*deleteOp); d.RowsAffected() != 1 {
		t.Errorf("RowsAffected=%d want 1 (only the parent's own row)", d.RowsAffected())
	}
	if err := op.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	rows := runQueryRows(t, ctx, "SELECT val FROM ONLY donlyu_c")
	if len(rows) != 1 || rows[0][0].Kind != KindInt || rows[0][0].Int != 20 {
		t.Fatalf("child row must survive DELETE FROM ONLY … USING: %v", rows)
	}
}

// TestUpdateOnlyFromInherit covers the cross-product variant
// (updateOp.updateWithFrom).
func TestUpdateOnlyFromInherit(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	for _, s := range []string{
		"CREATE TABLE uonlyf_p (id int primary key, val int)",
		"CREATE TABLE uonlyf_c (extra text) INHERITS (uonlyf_p)",
		"CREATE TABLE uonlyf_src (id int, bump int)",
		"INSERT INTO uonlyf_p (id, val) VALUES (1, 10)",
		"INSERT INTO uonlyf_c (id, val, extra) VALUES (2, 20, 'x')",
		"INSERT INTO uonlyf_src (id, bump) VALUES (1, 100), (2, 100)",
	} {
		if err := runDDL(t, ctx, s); err != nil {
			t.Fatalf("setup %q: %v", s, err)
		}
	}

	sql := "UPDATE ONLY uonlyf_p SET val = uonlyf_p.val + uonlyf_src.bump FROM uonlyf_src WHERE uonlyf_p.id = uonlyf_src.id"
	plan := planDMLStmt(t, ctx, sql)
	op := execDMLPlan(t, ctx, sql, plan)
	if u := op.(*updateOp); u.RowsAffected() != 1 {
		t.Errorf("RowsAffected=%d want 1 (only the parent's own row)", u.RowsAffected())
	}
	if err := op.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	rows := runQueryRows(t, ctx, "SELECT val FROM ONLY uonlyf_c")
	if len(rows) != 1 || rows[0][0].Kind != KindInt || rows[0][0].Int != 20 {
		t.Fatalf("child row must be unmodified by UPDATE ONLY … FROM: %v", rows)
	}
}

// TestDeleteFromOnlyPartitionedNoop: DELETE FROM ONLY on a PARTITIONED
// parent affects zero rows and raises no error — the parent itself
// stores no rows (PG semantics; ONLY on a partitioned table is legal,
// it simply matches nothing).
func TestDeleteFromOnlyPartitionedNoop(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	for _, s := range []string{
		"CREATE TABLE donlypart_p (id int, val int) PARTITION BY RANGE (id)",
		"CREATE TABLE donlypart_c PARTITION OF donlypart_p FOR VALUES FROM (0) TO (100)",
		"INSERT INTO donlypart_p (id, val) VALUES (1, 10)",
	} {
		if err := runDDL(t, ctx, s); err != nil {
			t.Fatalf("setup %q: %v", s, err)
		}
	}

	plan := planDMLStmt(t, ctx, "DELETE FROM ONLY donlypart_p")
	op := execDMLPlan(t, ctx, "DELETE FROM ONLY donlypart_p", plan)
	if d := op.(*deleteOp); d.RowsAffected() != 0 {
		t.Errorf("RowsAffected=%d want 0 (partitioned parent stores no rows)", d.RowsAffected())
	}
	if err := op.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	rows := runQueryRows(t, ctx, "SELECT val FROM donlypart_c")
	if len(rows) != 1 || rows[0][0].Kind != KindInt || rows[0][0].Int != 10 {
		t.Fatalf("partition row must survive DELETE FROM ONLY on the parent: %v", rows)
	}
}

// TestUpdateOnlyPartitionedNoop: UPDATE ONLY on a PARTITIONED parent
// affects zero rows and raises no error.
func TestUpdateOnlyPartitionedNoop(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	for _, s := range []string{
		"CREATE TABLE uonlypart_p (id int, val int) PARTITION BY RANGE (id)",
		"CREATE TABLE uonlypart_c PARTITION OF uonlypart_p FOR VALUES FROM (0) TO (100)",
		"INSERT INTO uonlypart_p (id, val) VALUES (1, 10)",
	} {
		if err := runDDL(t, ctx, s); err != nil {
			t.Fatalf("setup %q: %v", s, err)
		}
	}

	plan := planDMLStmt(t, ctx, "UPDATE ONLY uonlypart_p SET val = val + 1")
	op := execDMLPlan(t, ctx, "UPDATE ONLY uonlypart_p SET val = val + 1", plan)
	if u := op.(*updateOp); u.RowsAffected() != 0 {
		t.Errorf("RowsAffected=%d want 0 (partitioned parent stores no rows)", u.RowsAffected())
	}
	if err := op.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	rows := runQueryRows(t, ctx, "SELECT val FROM uonlypart_c")
	if len(rows) != 1 || rows[0][0].Kind != KindInt || rows[0][0].Int != 10 {
		t.Fatalf("partition row must be unmodified by UPDATE ONLY on the parent: %v", rows)
	}
}
