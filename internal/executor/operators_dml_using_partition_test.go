package executor

// operators_dml_using_partition_test.go — M0134-0005ap: deleteOp.deleteWithUsing
// was the only one of goopg's four DML descendant-expansion sites that never
// appended PartitionChildren to its scan targets, so `DELETE FROM
// <partitioned_parent> USING <other> WHERE ...` silently deleted zero rows from
// every partition. Fixed by mirroring updateOp.updateWithFrom's partition loop
// (operators_storage.go) inside the existing `!o.plan.Only` gate.
//
// PG oracle: postgres/src/backend/optimizer/util/inherit.c:expand_inherited_rtentry
// expands any RTE with rte->inh set uniformly, whether it is the DML target, a
// FROM item, or (for DELETE) a USING item — there is no USING-specific branch in
// nodeModifyTable.c's partition handling.

import "testing"

// TestDeleteUsingPartitionedTarget: DELETE ... USING against a range-partitioned
// parent with matching rows in more than one partition must remove them from
// each partition's own storage and report the correct affected-row count.
func TestDeleteUsingPartitionedTarget(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	for _, s := range []string{
		"CREATE TABLE dupart_p (id int, val int) PARTITION BY RANGE (id)",
		"CREATE TABLE dupart_lo PARTITION OF dupart_p FOR VALUES FROM (0) TO (100)",
		"CREATE TABLE dupart_hi PARTITION OF dupart_p FOR VALUES FROM (100) TO (200)",
		"CREATE TABLE dupart_src (id int)",
		"INSERT INTO dupart_p (id, val) VALUES (1, 10), (2, 20), (101, 110), (102, 120)",
		"INSERT INTO dupart_src (id) VALUES (1), (101)",
	} {
		if err := runDDL(t, ctx, s); err != nil {
			t.Fatalf("setup %q: %v", s, err)
		}
	}

	sql := "DELETE FROM dupart_p USING dupart_src WHERE dupart_p.id = dupart_src.id"
	plan := planDMLStmt(t, ctx, sql)
	op := execDMLPlan(t, ctx, sql, plan)
	if d := op.(*deleteOp); d.RowsAffected() != 2 {
		t.Errorf("RowsAffected=%d want 2 (one row from each partition)", d.RowsAffected())
	}
	if err := op.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Query each partition directly by name, not just via the parent, so a
	// no-op that merely "hides" rows through the parent view can't pass.
	rows := runQueryRows(t, ctx, "SELECT val FROM dupart_lo ORDER BY val")
	if len(rows) != 1 || rows[0][0].Kind != KindInt || rows[0][0].Int != 20 {
		t.Fatalf("dupart_lo after DELETE USING: %v, want only val=20 surviving", rows)
	}
	rows = runQueryRows(t, ctx, "SELECT val FROM dupart_hi ORDER BY val")
	if len(rows) != 1 || rows[0][0].Kind != KindInt || rows[0][0].Int != 120 {
		t.Fatalf("dupart_hi after DELETE USING: %v, want only val=120 surviving", rows)
	}
}

// TestDeleteFromOnlyUsingPartitionedNoop: DELETE FROM ONLY ... USING must keep
// skipping partitions even after the fix — the new PartitionChildren call sits
// inside the existing `!o.plan.Only` gate, not a new unconditional one.
func TestDeleteFromOnlyUsingPartitionedNoop(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	for _, s := range []string{
		"CREATE TABLE donlyupart_p (id int, val int) PARTITION BY RANGE (id)",
		"CREATE TABLE donlyupart_c PARTITION OF donlyupart_p FOR VALUES FROM (0) TO (100)",
		"CREATE TABLE donlyupart_src (id int)",
		"INSERT INTO donlyupart_p (id, val) VALUES (1, 10)",
		"INSERT INTO donlyupart_src (id) VALUES (1)",
	} {
		if err := runDDL(t, ctx, s); err != nil {
			t.Fatalf("setup %q: %v", s, err)
		}
	}

	sql := "DELETE FROM ONLY donlyupart_p USING donlyupart_src WHERE donlyupart_p.id = donlyupart_src.id"
	plan := planDMLStmt(t, ctx, sql)
	op := execDMLPlan(t, ctx, sql, plan)
	if d := op.(*deleteOp); d.RowsAffected() != 0 {
		t.Errorf("RowsAffected=%d want 0 (ONLY must keep skipping partitions)", d.RowsAffected())
	}
	if err := op.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	rows := runQueryRows(t, ctx, "SELECT val FROM donlyupart_c")
	if len(rows) != 1 || rows[0][0].Kind != KindInt || rows[0][0].Int != 10 {
		t.Fatalf("partition row must survive DELETE FROM ONLY ... USING: %v", rows)
	}
}

// TestDeleteUsingInheritNoPartitionRegression: DELETE ... USING against a
// plain-INHERITS (non-partitioned) parent must keep fanning out to the child
// exactly as before — the no-regression half of this campaign's convention.
func TestDeleteUsingInheritNoPartitionRegression(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	for _, s := range []string{
		"CREATE TABLE duinh_p (id int primary key, val int)",
		"CREATE TABLE duinh_c (extra text) INHERITS (duinh_p)",
		"CREATE TABLE duinh_src (id int)",
		"INSERT INTO duinh_p (id, val) VALUES (1, 10)",
		"INSERT INTO duinh_c (id, val, extra) VALUES (2, 20, 'x')",
		"INSERT INTO duinh_src (id) VALUES (1), (2)",
	} {
		if err := runDDL(t, ctx, s); err != nil {
			t.Fatalf("setup %q: %v", s, err)
		}
	}

	sql := "DELETE FROM duinh_p USING duinh_src WHERE duinh_p.id = duinh_src.id"
	plan := planDMLStmt(t, ctx, sql)
	op := execDMLPlan(t, ctx, sql, plan)
	if d := op.(*deleteOp); d.RowsAffected() != 2 {
		t.Errorf("RowsAffected=%d want 2 (parent + INHERITS child, unchanged)", d.RowsAffected())
	}
	if err := op.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	rows := runQueryRows(t, ctx, "SELECT val FROM duinh_p")
	if len(rows) != 0 {
		t.Fatalf("no-ONLY DELETE USING must still fan out to the INHERITS child: %v", rows)
	}
}

// TestDeleteUsingPartitionedReturning: RETURNING with a partitioned target
// returns the partition-sourced rows with parent-aligned columns — partition
// children carry colMap=nil (same ordinals as the parent), so no remapping is
// needed for RETURNING to line up.
func TestDeleteUsingPartitionedReturning(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	for _, s := range []string{
		"CREATE TABLE duret_p (id int, val int) PARTITION BY RANGE (id)",
		"CREATE TABLE duret_lo PARTITION OF duret_p FOR VALUES FROM (0) TO (100)",
		"CREATE TABLE duret_hi PARTITION OF duret_p FOR VALUES FROM (100) TO (200)",
		"CREATE TABLE duret_src (id int)",
		"INSERT INTO duret_p (id, val) VALUES (1, 10), (101, 110)",
		"INSERT INTO duret_src (id) VALUES (1), (101)",
	} {
		if err := runDDL(t, ctx, s); err != nil {
			t.Fatalf("setup %q: %v", s, err)
		}
	}

	sql := "DELETE FROM duret_p USING duret_src WHERE duret_p.id = duret_src.id RETURNING duret_p.id, duret_p.val"
	rows := runDMLRows(t, ctx, sql)
	if len(rows) != 2 {
		t.Fatalf("RETURNING row count=%d want 2 (one from each partition)", len(rows))
	}
	got := map[int64]int64{}
	for _, r := range rows {
		if r[0].Kind != KindInt || r[1].Kind != KindInt {
			t.Fatalf("RETURNING row has unexpected kind: %v", r)
		}
		got[r[0].Int] = r[1].Int
	}
	want := map[int64]int64{1: 10, 101: 110}
	if len(got) != len(want) || got[1] != 10 || got[101] != 110 {
		t.Fatalf("RETURNING rows=%v want %v", got, want)
	}
}
