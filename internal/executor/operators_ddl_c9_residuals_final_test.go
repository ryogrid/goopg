package executor

// operators_ddl_c9_residuals_final_test.go — M0134-0002 C9 residuals (final
// slice, S1-S4): the three remaining partitioned-parent-block guards in the
// alter_table regress run plus the cyclic-ATTACH rejection.
//
//	S1  ALTER TABLE <partition> ADD COLUMN c text
//	    → 42809 cannot add column to a partition, for BOTH a CREATE TABLE …
//	      PARTITION OF child and a LIKE-then-ATTACH child
//	    (ATExecAddColumn relispartition && !recursing, tablecmds.c:7247-7250)
//	S2  After ATTACH, the child's same-named columns are Inherited
//	    (attislocal=false), so the inherited-DDL refusals fire on the ATTACHed
//	    child: DROP/RENAME/ALTER TYPE → 42P16 cannot (drop|rename|alter)
//	    inherited column "b"
//	    (MergeAttributesIntoExisting, tablecmds.c:17500; ATExecDropColumn
//	    :9343-9351, renameatt_internal :3960-3964, ATExecAlterColumnType
//	    :14436-14440)
//	S4  Self-attach and back-edge attach → 42P07 circular inheritance not
//	    allowed + errdetail "<parent>" is already a child of "<child>".
//	    (ATExecAttachPartition find_all_inheritors membership,
//	    tablecmds.c:20338-20362 — 42P07 DUPLICATE_TABLE, NOT 42P17)
//
// All hierarchies are built for real through the DDL executor; the ATTACH
// child shapes (PartitionParentOID set vs map-only registration) come from the
// parser/catalog, never faked in the fixture.

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// TestC9S1AddColumnToPartitionRefused covers S1 for BOTH partition-child
// shapes: a CREATE TABLE … PARTITION OF child (Table.PartitionParentOID set)
// and a LIKE-then-ATTACH child (PartitionParentOID 0 — the regress part_2
// shape). Both must raise 42809 before the system-column / IF NOT EXISTS
// checks, with no errdetail, no errhint, Pos 0.
func TestC9S1AddColumnToPartitionRefused(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	for _, s := range []string{
		"CREATE TABLE parent_of (a int) PARTITION BY LIST (a)",
		"CREATE TABLE part_of PARTITION OF parent_of FOR VALUES IN (1)",
		"CREATE TABLE parent_att (a int) PARTITION BY LIST (a)",
		"CREATE TABLE part_att (LIKE parent_att)",
		"ALTER TABLE parent_att ATTACH PARTITION part_att FOR VALUES IN (2)",
	} {
		if err := runDDL(t, ctx, s); err != nil {
			t.Fatalf("setup %q: %v", s, err)
		}
	}

	for _, tc := range []struct {
		name string
		sql  string
	}{
		{"partition-of child", "ALTER TABLE part_of ADD COLUMN c text"},
		{"attached child", "ALTER TABLE part_att ADD COLUMN c text"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := runDDL(t, ctx, tc.sql)
			wantExecError(t, err, "42809", "cannot add column to a partition")
			ee := err.(*ExecError)
			if ee.Pos != 0 {
				t.Errorf("Pos = %d, want 0", ee.Pos)
			}
			if ee.Detail != "" {
				t.Errorf("Detail = %q, want empty", ee.Detail)
			}
			if ee.Hint != "" {
				t.Errorf("Hint = %q, want empty", ee.Hint)
			}
		})
	}
}

// TestC9S2AttachMarksColumnsInherited covers S2: after ATTACH, the child's
// same-named columns are Inherited while a column with no parent counterpart
// stays local, and the inherited-DDL refusals fire on the ATTACHed child
// (which S3 makes live-hierarchy-visible despite PartitionParentOID == 0).
func TestC9S2AttachMarksColumnsInherited(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	// part_2 is created LIKE parent (local columns) with an extra local-only
	// column x, then ATTACHed — the regress part_2 shape.
	for _, s := range []string{
		"CREATE TABLE list_parted2 (a int, b int) PARTITION BY LIST (a)",
		"CREATE TABLE part_2 (LIKE list_parted2, x int)",
		"ALTER TABLE list_parted2 ATTACH PARTITION part_2 FOR VALUES IN (2)",
	} {
		if err := runDDL(t, ctx, s); err != nil {
			t.Fatalf("setup %q: %v", s, err)
		}
	}

	child, ok := ctx.Catalog.LookupTable(parser.ObjectName{Name: "part_2"})
	if !ok {
		t.Fatal("part_2 not found in catalog")
	}
	inherited := make(map[string]bool)
	for _, c := range child.Columns {
		inherited[c.Name] = c.Inherited
	}
	if !inherited["a"] || !inherited["b"] {
		t.Errorf("after ATTACH, part_2 columns a/b Inherited = %v/%v, want true/true", inherited["a"], inherited["b"])
	}
	if inherited["x"] {
		t.Errorf("part_2 column x (no same-named parent column) Inherited = true, want false (stays local)")
	}

	// The inherited-DDL refusals must now fire on the ATTACHed child.
	for _, tc := range []struct {
		name    string
		sql     string
		code    string
		message string
		pos     int // -1 asserts Pos != 0 (column-name errposition)
	}{
		{
			name:    "drop inherited column",
			sql:     "ALTER TABLE part_2 DROP COLUMN b",
			code:    "42P16",
			message: `cannot drop inherited column "b"`,
		},
		{
			name:    "rename inherited column",
			sql:     "ALTER TABLE part_2 RENAME COLUMN b to c",
			code:    "42P16",
			message: `cannot rename inherited column "b"`,
		},
		{
			name:    "alter type of inherited column",
			sql:     "ALTER TABLE part_2 ALTER COLUMN b TYPE text",
			code:    "42P16",
			message: `cannot alter inherited column "b"`,
			pos:     -1,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := runDDL(t, ctx, tc.sql)
			wantExecError(t, err, tc.code, tc.message)
			ee := err.(*ExecError)
			if tc.pos == -1 && ee.Pos == 0 {
				t.Errorf("Pos = 0, want nonzero (column-name errposition)")
			}
		})
	}

	// A purely local column drops fine — no false positive from the merge.
	if err := runDDL(t, ctx, "ALTER TABLE part_2 DROP COLUMN x"); err != nil {
		t.Fatalf("DROP COLUMN x on ATTACHed child: %v", err)
	}

	// S2 sibling: DETACH PARTITION clears the Inherited flags again
	// (RemoveInheritance decrements attinhcount and sets attislocal=true,
	// tablecmds.c:18009-18014) so a detached partition's pg_attribute reports
	// attislocal=true — the alter_table regress asserts the reset at
	// alter_table.out:4416-4422.
	if err := runDDL(t, ctx, "ALTER TABLE list_parted2 DETACH PARTITION part_2"); err != nil {
		t.Fatalf("DETACH PARTITION part_2: %v", err)
	}
	child, ok = ctx.Catalog.LookupTable(parser.ObjectName{Name: "part_2"})
	if !ok {
		t.Fatal("part_2 not found after detach")
	}
	for _, c := range child.Columns {
		if c.Inherited {
			t.Errorf("after DETACH, part_2 column %q Inherited = true, want false", c.Name)
		}
	}
}

// TestC9S4CircularAttachRejected covers S4: self-attach and back-edge attach
// raise 42P07 with the byte-exact PG errdetail (parent first, child second),
// no errhint, Pos 0. The back-edge fixture is the ledger-row-1422 shape:
// part_5 is a partition of list_parted2, so attaching list_parted2 (an
// ancestor) under part_5 would create a cycle — walking the CHILD's
// descendants finds the PARENT.
func TestC9S4CircularAttachRejected(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	for _, s := range []string{
		"CREATE TABLE list_parted2 (a int, b int, c int) PARTITION BY LIST (a)",
		"CREATE TABLE part_5 (LIKE list_parted2) PARTITION BY LIST (b)",
		"ALTER TABLE list_parted2 ATTACH PARTITION part_5 FOR VALUES IN (5)",
	} {
		if err := runDDL(t, ctx, s); err != nil {
			t.Fatalf("setup %q: %v", s, err)
		}
	}

	tests := []struct {
		name    string
		sql     string
		detail  string
	}{
		{
			name:   "back-edge attach",
			sql:    "ALTER TABLE part_5 ATTACH PARTITION list_parted2 FOR VALUES IN ('b')",
			detail: `"part_5" is already a child of "list_parted2".`,
		},
		{
			name:   "self attach",
			sql:    "ALTER TABLE list_parted2 ATTACH PARTITION list_parted2 FOR VALUES IN (0)",
			detail: `"list_parted2" is already a child of "list_parted2".`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := runDDL(t, ctx, tc.sql)
			wantExecError(t, err, "42P07", "circular inheritance not allowed")
			ee := err.(*ExecError)
			if ee.Detail != tc.detail {
				t.Errorf("Detail = %q, want %q", ee.Detail, tc.detail)
			}
			if ee.Hint != "" {
				t.Errorf("Hint = %q, want empty", ee.Hint)
			}
			if ee.Pos != 0 {
				t.Errorf("Pos = %d, want 0", ee.Pos)
			}
		})
	}
}

// TestC9DropConstraintOnlyDoesNotCascade pins the DROP-CONSTRAINT recurse
// semantics (dropconstraint_internal, tablecmds.c:14300-14343): `ALTER TABLE
// ONLY parent DROP CONSTRAINT name` removes the constraint from the parent
// AND still walks each child to decrement coninhcount (flipping conislocal
// to true on reaching zero) — it does NOT delete the child's own row, but it
// is not a no-op on the child either. The plain (recursive) form instead
// cascades an outright DELETE of the child's row. Exposed by S4 — before the
// cyclic-ATTACH rejection, the alter_table regress's ONLY-drop errored with
// "cannot drop inherited constraint" (the cyclic self-loop had made the
// parent's own constraint InhCount=1), which masked the missing recurse=false
// handling. The name is legacy (kept to avoid an unrelated rename) — the
// assertions below were corrected under M0134-0005ah slice B: this test
// previously pinned an INVERTED reading ("ONLY-drop must not cascade" as
// "must not touch children at all"), which is not what PG does — see
// tmp/ralph-handoffs/m0134-0005ah-research/report.md item B.
func TestC9DropConstraintOnlyDoesNotCascade(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	for _, s := range []string{
		"CREATE TABLE p (a int, b int) PARTITION BY LIST (a)",
		"CREATE TABLE c (LIKE p)",
		"ALTER TABLE p ATTACH PARTITION c FOR VALUES IN (2)",
	} {
		if err := runDDL(t, ctx, s); err != nil {
			t.Fatalf("setup %q: %v", s, err)
		}
	}

	checkB := func(tblName string) (count int, inhCount int, isLocal bool) {
		tbl, ok := ctx.Catalog.LookupTable(parser.ObjectName{Name: tblName})
		if !ok {
			t.Fatalf("%s not found in catalog", tblName)
		}
		for _, n := range tbl.NamedChecks {
			if strings.EqualFold(n.Name, "check_b") {
				count++
				if n.InhCount > inhCount {
					inhCount = n.InhCount
				}
				isLocal = n.IsLocal
			}
		}
		return count, inhCount, isLocal
	}

	// Seed: the regress :2863 form — goopg's ONLY-guard for ADD CONSTRAINT is a
	// separate ledgered item, so the ONLY ADD propagates to the child (the child
	// copy is inherited, InhCount=1).
	if err := runDDL(t, ctx, "ALTER TABLE ONLY p ADD CONSTRAINT check_b CHECK (b <> 'zz')"); err != nil {
		t.Fatalf("ONLY ADD CONSTRAINT check_b: %v", err)
	}
	if n, _, _ := checkB("c"); n != 1 {
		t.Fatalf("after ONLY ADD, child c has %d check_b, want 1", n)
	}

	// P1: ONLY (recurse=false) drop removes the parent's copy and decrements
	// the child's coninhcount to 0 (flipping conislocal to true) — but the
	// child's row is NOT deleted (dropconstraint_internal, tablecmds.c:
	// 14300-14343; M0134-0005ah slice B corrected this from the prior
	// "child untouched" reading).
	if err := runDDL(t, ctx, "ALTER TABLE ONLY p DROP CONSTRAINT check_b"); err != nil {
		t.Fatalf("ONLY DROP CONSTRAINT check_b: %v", err)
	}
	if n, _, _ := checkB("p"); n != 0 {
		t.Errorf("after ONLY DROP, parent p has %d check_b, want 0", n)
	}
	if n, ic, local := checkB("c"); n != 1 || ic != 0 || !local {
		t.Errorf("after ONLY DROP, child c has %d check_b (InhCount=%d, IsLocal=%v), want 1 (InhCount=0, IsLocal=true) — ONLY-drop must still decrement/orphan, not skip the child", n, ic, local)
	}

	// P2: the plain (recursive) drop deletes the child's own row outright.
	// Re-add to the parent; the child's now-local copy (from P1) merges back
	// to inherited (InhCount=1, IsLocal=false).
	if err := runDDL(t, ctx, "ALTER TABLE p ADD CONSTRAINT check_b CHECK (b <> 'zz')"); err != nil {
		t.Fatalf("ADD CONSTRAINT check_b: %v", err)
	}
	if n, _, _ := checkB("c"); n != 1 {
		t.Fatalf("after re-ADD, child c has %d check_b, want 1", n)
	}
	if err := runDDL(t, ctx, "ALTER TABLE p DROP CONSTRAINT check_b"); err != nil {
		t.Fatalf("DROP CONSTRAINT check_b: %v", err)
	}
	if n, _, _ := checkB("p"); n != 0 {
		t.Errorf("after plain DROP, parent p has %d check_b, want 0", n)
	}
	if n, _, _ := checkB("c"); n != 0 {
		t.Errorf("after plain DROP, child c has %d check_b, want 0 (recursive drop must cascade)", n)
	}
}
