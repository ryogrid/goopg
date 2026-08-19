package executor

import (
	"strings"
	"testing"
)

// TestDefaultPartitionRecreateAfterDrop is the FAIL-pre reproducer for
// M0134-0013b: once a DEFAULT partition is dropped, its parent must be able
// to accept a new DEFAULT partition, matching PostgreSQL's
// check_new_partition_bound() (postgres/src/backend/partitioning/partbounds.c
// :2895-2923), which recomputes the partition descriptor from
// RelationGetPartitionDesc on every check rather than consulting a stale
// aggregate cache.
//
// Before the fix, validateDefaultPartition (internal/executor/operators_ddl.go)
// scanned the never-pruned parent.PartitionBounds aggregate, so the dropped
// child's bound entry kept rejecting new DEFAULT partitions forever, raising
// 42P16 on the third CREATE TABLE below.
func TestDefaultPartitionRecreateAfterDrop(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	for _, s := range []string{
		"CREATE TABLE t_dp (a text) PARTITION BY LIST (a)",
		"CREATE TABLE t_dp_def PARTITION OF t_dp DEFAULT",
		"DROP TABLE t_dp_def",
		"CREATE TABLE t_dp_def2 PARTITION OF t_dp DEFAULT",
	} {
		if err := runDDL(t, ctx, s); err != nil {
			t.Fatalf("runDDL(%q): %v", s, err)
		}
	}

	if err := runDDL(t, ctx, "INSERT INTO t_dp VALUES ('x')"); err != nil {
		t.Fatalf("insert into t_dp: %v", err)
	}

	rows := runQuery(t, ctx, "SELECT a FROM t_dp_def2")
	if len(rows) != 1 {
		t.Fatalf("expected 1 row routed into t_dp_def2, got %d", len(rows))
	}
}

// TestDefaultPartitionRecreateAfterDropRange is the RANGE-partitioned twin of
// TestDefaultPartitionRecreateAfterDrop (brief acceptance criterion 4).
func TestDefaultPartitionRecreateAfterDropRange(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	for _, s := range []string{
		"CREATE TABLE t_dpr (a int) PARTITION BY RANGE (a)",
		"CREATE TABLE t_dpr_p1 PARTITION OF t_dpr FOR VALUES FROM (0) TO (100)",
		"CREATE TABLE t_dpr_def PARTITION OF t_dpr DEFAULT",
		"DROP TABLE t_dpr_def",
		"CREATE TABLE t_dpr_def2 PARTITION OF t_dpr DEFAULT",
	} {
		if err := runDDL(t, ctx, s); err != nil {
			t.Fatalf("runDDL(%q): %v", s, err)
		}
	}

	if err := runDDL(t, ctx, "INSERT INTO t_dpr VALUES (500)"); err != nil {
		t.Fatalf("insert into t_dpr: %v", err)
	}

	rows := runQuery(t, ctx, "SELECT a FROM t_dpr_def2")
	if len(rows) != 1 {
		t.Fatalf("expected 1 row routed into t_dpr_def2, got %d", len(rows))
	}
}

// TestDefaultPartitionSiblingSubpartitionDefaultNotMistaken is the direct
// regression guard for the wrong-tree-level bug caught by the regress gate
// (docs/design/m0134-0013-default-partition-stale-bounds-cache.md §4/§5): a
// non-default sibling child that is itself PARTITION BY and owns its OWN
// DEFAULT sub-partition must not be mistaken for the parent's default. This
// is the insert.sql `part_xx_yy` shape: `part_xx_yy` is an ordinary
// (non-default) child of `list_parted`, itself partitioned, with its own
// default child `part_xx_yy_defpart`. The first-attempt fix scanned each live
// child's OWN PartitionBounds for IsDefault — matching the parent's
// GRANDchild — and mis-reported "conflicts with existing default partition
// "part_xx_yy"".
func TestDefaultPartitionSiblingSubpartitionDefaultNotMistaken(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	for _, s := range []string{
		"CREATE TABLE p (a text) PARTITION BY LIST (a)",
		"CREATE TABLE p_sub PARTITION OF p FOR VALUES IN ('sub') PARTITION BY LIST (a)",
		"CREATE TABLE p_sub_def PARTITION OF p_sub DEFAULT",
	} {
		if err := runDDL(t, ctx, s); err != nil {
			t.Fatalf("setup %q: %v", s, err)
		}
	}

	if err := runDDL(t, ctx, "CREATE TABLE p_def PARTITION OF p DEFAULT"); err != nil {
		t.Fatalf("CREATE TABLE p_def PARTITION OF p DEFAULT should succeed (p has no default of its own yet): %v", err)
	}
}

// TestDefaultPartitionSecondLiveConflictStillRejected is the regression guard
// (brief acceptance criterion 2): creating a SECOND *live* DEFAULT partition,
// with the first NOT dropped, must still raise 42P16 naming the existing
// default child. This is what stops the fix-verifying test above from being
// vacuous — it proves validateDefaultPartition still rejects a genuine
// conflict, not just that it stopped rejecting everything.
func TestDefaultPartitionSecondLiveConflictStillRejected(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	for _, s := range []string{
		"CREATE TABLE t_dpc (a text) PARTITION BY LIST (a)",
		"CREATE TABLE t_dpc_def PARTITION OF t_dpc DEFAULT",
	} {
		if err := runDDL(t, ctx, s); err != nil {
			t.Fatalf("setup %q: %v", s, err)
		}
	}

	err := runDDL(t, ctx, "CREATE TABLE t_dpc_def2 PARTITION OF t_dpc DEFAULT")
	if err == nil {
		t.Fatalf("second live DEFAULT partition should be rejected, got nil")
	}
	ee, ok := err.(*ExecError)
	if !ok {
		t.Fatalf("err type = %T, want *ExecError: %v", err, err)
	}
	if ee.Code != "42P16" {
		t.Fatalf("SQLSTATE = %q, want 42P16: %v", ee.Code, ee)
	}
	if !strings.Contains(ee.Message, `existing default partition "t_dpc_def"`) {
		t.Fatalf("message = %q, want it to name existing default partition t_dpc_def", ee.Message)
	}
}
