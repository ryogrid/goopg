package executor

// operators_ddl_partition_recursion_test.go — M0134-0002 C9 residuals: the
// ONLY-on-partitioned DROP COLUMN guard and the descendant-partition recursion
// for the partition-key guards (DROP COLUMN + ALTER TYPE), plus the ALTER TYPE
// inherited-column guard. All three close the partitioned-parent block of the
// alter_table regress run (alter_table.sql:2850-2858, 2902-2903).
//
//	ALTER TABLE ONLY list_parted2 DROP COLUMN b
//	    → 42P16 cannot drop column from only the partitioned table when
//	      partitions exist + HINT "Do not specify the ONLY keyword."
//	    (ATExecDropColumn, tablecmds.c:9385-9389)
//	ALTER TABLE list_parted2 DROP COLUMN b        (part_5 partitioned on b)
//	    → 42P16 cannot drop column "b" because it is part of the partition key
//	      of relation "part_5"   (recursion names the DESCENDANT)
//	    (ATExecDropColumn one-level child recursion, tablecmds.c:9373/9422-9424)
//	ALTER TABLE list_parted2 ALTER COLUMN b TYPE text
//	    → 42P16 cannot alter column "b" because it is part of the partition key
//	      of relation "part_5", Pos = column-name errposition
//	    (ATPrepAlterColumnType find_all_inheritors recursion, tablecmds.c:14576)
//	ALTER TABLE part_2 ALTER COLUMN b TYPE bigint (inherited from parent)
//	    → 42P16 cannot alter inherited column "b", Pos = column-name errposition
//	    (ATExecAlterColumnType, tablecmds.c:14436-14440)
//
// The hierarchy is built for real through the DDL executor (CREATE TABLE
// PARTITION OF ... PARTITION BY ...), so PartitionMethod/PartitionKey and the
// partition edges come from the parser/catalog — never faked in the fixture.

import (
	"testing"
)

func TestAlterTablePartitionDescendantRecursion(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	// list_parted2 is partitioned on a; part_5 is a sub-partition of it that is
	// itself partitioned on b (mirroring alter_table.sql:2538-2628). part_2 is
	// a plain leaf partition. All three columns exist on the parent so the
	// guards, not column existence, decide each outcome.
	for _, s := range []string{
		"CREATE TABLE list_parted2 (a int, b int, c int) PARTITION BY LIST (a)",
		"CREATE TABLE part_2 PARTITION OF list_parted2 FOR VALUES IN (2)",
		"CREATE TABLE part_5 PARTITION OF list_parted2 FOR VALUES IN (5) PARTITION BY LIST (b)",
	} {
		if err := runDDL(t, ctx, s); err != nil {
			t.Fatalf("setup %q: %v", s, err)
		}
	}

	tests := []struct {
		name    string
		sql     string
		wantErr bool
		code    string
		message string
		hint    string
		pos     int // nonzero asserts Pos == wantPos; -1 asserts Pos != 0
	}{
		{
			// (a) ONLY on a partitioned parent with partitions is refused,
			// before any key check.
			name:    "only drop on partitioned parent with partitions refused",
			sql:     "ALTER TABLE ONLY list_parted2 DROP COLUMN b",
			wantErr: true,
			code:    "42P16",
			message: "cannot drop column from only the partitioned table when partitions exist",
			hint:    "Do not specify the ONLY keyword.",
		},
		{
			// (b) non-ONLY DROP of a descendant's key column names the
			// DESCENDANT (part_5), Pos 0.
			name:    "non-only drop of descendant-key column names descendant",
			sql:     "ALTER TABLE list_parted2 DROP COLUMN b",
			wantErr: true,
			code:    "42P16",
			message: `cannot drop column "b" because it is part of the partition key of relation "part_5"`,
		},
		{
			// (c) non-ONLY ALTER TYPE of a descendant's key column: same
			// descendant message, but Pos carries the column-name errposition
			// (tablecmds.c:14450).
			name:    "non-only alter type of descendant-key column names descendant",
			sql:     "ALTER TABLE list_parted2 ALTER COLUMN b TYPE text",
			wantErr: true,
			code:    "42P16",
			message: `cannot alter column "b" because it is part of the partition key of relation "part_5"`,
			pos:     -1,
		},
		{
			// (d) ALTER TYPE of an inherited column on a leaf partition.
			name:    "alter type of inherited column refused",
			sql:     "ALTER TABLE part_2 ALTER COLUMN b TYPE bigint",
			wantErr: true,
			code:    "42P16",
			message: `cannot alter inherited column "b"`,
			pos:     -1,
		},
		{
			// (e) a column in no key drops fine — no false positive.
			name: "non-only drop of non-key column ok",
			sql:  "ALTER TABLE list_parted2 DROP COLUMN c",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := runDDL(t, ctx, tc.sql)
			if !tc.wantErr {
				if err != nil {
					t.Fatalf("%s: %v", tc.sql, err)
				}
				return
			}
			wantExecError(t, err, tc.code, tc.message)
			ee := err.(*ExecError)
			if tc.hint != "" && ee.Hint != tc.hint {
				t.Errorf("Hint = %q, want %q", ee.Hint, tc.hint)
			}
			switch tc.pos {
			case -1:
				if ee.Pos == 0 {
					t.Errorf("Pos = 0, want nonzero (column-name errposition) for %s", tc.sql)
				}
			case 0:
				if ee.Pos != 0 {
					t.Errorf("Pos = %d, want 0 (no errposition) for %s", ee.Pos, tc.sql)
				}
			default:
				if ee.Pos != tc.pos {
					t.Errorf("Pos = %d, want %d for %s", ee.Pos, tc.pos, tc.sql)
				}
			}
		})
	}
}

// TestAlterTableDescendantWalkCycleSafe guards the shared descendant walker
// (allDescendants) against the cyclic inheritance/partition graph. The
// alter_table regress test constructs exactly this cycle (SQL lines 2699-2701):
// PG rejects it with "circular inheritance not allowed". Since the M0134-0002
// C9 residual S4 landed, goopg's ATTACH arm rejects the back-edge and the
// self-attach with 42P07 (tablecmds.c:20338-20362), so the catalog can no
// longer become cyclic through ATTACH; the visited-set protection in
// allDescendants stays as defense-in-depth for any other path that might one
// day leak a cycle. Before that fix, the descendant walk for
// "ALTER TABLE list_parted2 DROP COLUMN b" BFS'd the list_parted2 <-> part_5
// cycle forever, growing memory to 21-22GB until the cgroup OOM-killed the
// server (two observed runs) and the regress output truncated mid-statement.
// This test must terminate promptly; the go test framework's own timeout turns
// any regression of the walker into a test failure, not a suite hang.
func TestAlterTableDescendantWalkCycleSafe(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	// Mirror alter_table.sql:2626-2638 + 2699-2701: part_5 partitioned on b is
	// attached to list_parted2 (the acyclic half of the old fixture).
	for _, s := range []string{
		"CREATE TABLE list_parted2 (a int, b int, c int) PARTITION BY LIST (a)",
		"CREATE TABLE part_5 (LIKE list_parted2) PARTITION BY LIST (b)",
		"ALTER TABLE list_parted2 ATTACH PARTITION part_5 FOR VALUES IN (5)",
	} {
		if err := runDDL(t, ctx, s); err != nil {
			t.Fatalf("setup %q: %v", s, err)
		}
	}

	// S4: the back-edge (part_5 is a descendant of list_parted2, so attaching
	// list_parted2 as part_5's child cycles) and the self-attach are now
	// rejected — errdetail names parent then child, the PG argument order.
	err := runDDL(t, ctx, "ALTER TABLE part_5 ATTACH PARTITION list_parted2 FOR VALUES IN ('b')")
	wantExecError(t, err, "42P07", "circular inheritance not allowed")
	if ee := err.(*ExecError); ee.Detail != `"part_5" is already a child of "list_parted2".` {
		t.Errorf("Detail = %q, want %q", ee.Detail, `"part_5" is already a child of "list_parted2".`)
	}
	err = runDDL(t, ctx, "ALTER TABLE list_parted2 ATTACH PARTITION list_parted2 FOR VALUES IN (0)")
	wantExecError(t, err, "42P07", "circular inheritance not allowed")
	if ee := err.(*ExecError); ee.Detail != `"list_parted2" is already a child of "list_parted2".` {
		t.Errorf("Detail = %q, want %q", ee.Detail, `"list_parted2" is already a child of "list_parted2".`)
	}

	// The walk must terminate (visited set) and name the first key-owning
	// descendant reached. The key assertion is that this returns without
	// hanging — a regression of the walker hangs the whole test binary.
	err = runDDL(t, ctx, "ALTER TABLE list_parted2 DROP COLUMN b")
	wantExecError(t, err, "42P16", `cannot drop column "b" because it is part of the partition key of relation "part_5"`)
}
