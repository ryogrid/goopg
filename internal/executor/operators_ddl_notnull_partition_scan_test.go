package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// M0134-0005ac: two closely related NOT-NULL/PRIMARY-KEY verification-scan
// gaps in operators_ddl.go, both regression-tested here against the exact
// constraints.sql shapes in the PG oracle (postgres/src/test/regress/sql/
// constraints.sql).
//
// Bucket B — VALIDATE CONSTRAINT never scanned for existing NULLs
// (ATExecValidateConstraint's notnull branch, tablecmds.c:13291-13295, DOES
// trigger a full scan — the old in-code comment claiming otherwise conflated
// it with tablecmds.c:9956's exclusion from the generic ADD-constraint
// Phase-3 queue).
//
// Bucket C — forEachLiveRow scanned only tbl's OWN heap, so a partitioned
// parent (zero own storage) never saw a partition's NULLs
// (ATExecSetNotNull / ATRewriteTable queue work per relation across the
// whole partition tree, ATSimpleRecursion / find_all_inheritors,
// tablecmds.c).

// TestAlterTableValidateConstraintNotNullScansExistingRows verifies acceptance
// criterion 1: constraints.sql:822-827's `ALTER TABLE notnull_tbl1 VALIDATE
// CONSTRAINT nn` on a plain (non-partitioned) table holding a NULL now errors
// with PG's verbatim 23502 wording instead of silently flipping
// convalidated 'f'->'t'.
func TestAlterTableValidateConstraintNotNullScansExistingRows(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	for _, stmt := range []string{
		`CREATE TABLE notnull_tbl1 (a int, b int)`,
		`INSERT INTO notnull_tbl1 VALUES (NULL, 1), (300, 3)`,
		`ALTER TABLE notnull_tbl1 ADD CONSTRAINT nn NOT NULL a NOT VALID`,
	} {
		if err := runDDL(t, ctx, stmt); err != nil {
			t.Fatalf("setup %q: %v", stmt, err)
		}
	}

	err := runDDL(t, ctx, `ALTER TABLE notnull_tbl1 VALIDATE CONSTRAINT nn`)
	requireExecError(t, err, "23502",
		`column "a" of relation "notnull_tbl1" contains null values`)

	// The failed VALIDATE must not flip convalidated.
	tbl, ok := cat.LookupTable(parser.ObjectName{Name: "notnull_tbl1"})
	if !ok {
		t.Fatal("notnull_tbl1 not found")
	}
	found := false
	for _, nc := range tbl.NotNullConstraints {
		if nc.Name == "nn" {
			found = true
			if !nc.NotValid {
				t.Errorf("NotNullConstraints[nn].NotValid = false after failed VALIDATE, want still true")
			}
		}
	}
	if !found {
		t.Fatalf("no NOT NULL constraint nn recorded: %+v", tbl.NotNullConstraints)
	}

	// Clearing the NULL then lets VALIDATE succeed and flip convalidated
	// (constraints.sql:864-866: UPDATE then VALIDATE now ok).
	if err := runDDL(t, ctx, `UPDATE notnull_tbl1 SET a = 100 WHERE b = 1`); err != nil {
		t.Fatalf("UPDATE: %v", err)
	}
	if err := runDDL(t, ctx, `ALTER TABLE notnull_tbl1 VALIDATE CONSTRAINT nn`); err != nil {
		t.Fatalf("VALIDATE CONSTRAINT after cleanup should succeed: %v", err)
	}
	tbl, _ = cat.LookupTable(parser.ObjectName{Name: "notnull_tbl1"})
	for _, nc := range tbl.NotNullConstraints {
		if nc.Name == "nn" && nc.NotValid {
			t.Errorf("NotNullConstraints[nn].NotValid still true after successful VALIDATE")
		}
	}
}

// TestAlterTableValidateConstraintNotNullOnPartitionScans verifies acceptance
// criterion 2: constraints.sql:927-934's VALIDATE CONSTRAINT issued directly
// on a partition (notnull_tbl1_3, attached to a LIST-partitioned parent)
// scans that partition's own rows and errors on an existing NULL.
func TestAlterTableValidateConstraintNotNullOnPartitionScans(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	for _, stmt := range []string{
		`CREATE TABLE notnull_tbl1 (a int, b int) PARTITION BY LIST (a)`,
		`ALTER TABLE notnull_tbl1 ADD CONSTRAINT notnull_con NOT NULL a NOT VALID`,
		`CREATE TABLE notnull_tbl1_1 PARTITION OF notnull_tbl1 FOR VALUES IN (1,2)`,
		`CREATE TABLE notnull_tbl1_3(a int, b int)`,
		`INSERT INTO notnull_tbl1_3 values(NULL,1)`,
		`ALTER TABLE notnull_tbl1_3 add CONSTRAINT nn3 NOT NULL a NOT VALID`,
		`ALTER TABLE notnull_tbl1 ATTACH PARTITION notnull_tbl1_3 FOR VALUES IN (NULL,5)`,
	} {
		if err := runDDL(t, ctx, stmt); err != nil {
			t.Fatalf("setup %q: %v", stmt, err)
		}
	}

	err := runDDL(t, ctx, `ALTER TABLE notnull_tbl1_3 VALIDATE CONSTRAINT nn3`)
	requireExecError(t, err, "23502",
		`column "a" of relation "notnull_tbl1_3" contains null values`)
}

// TestAlterTableSetNotNullOnPartitionedParentScansPartitions verifies
// acceptance criterion 3: constraints.sql:933's `ALTER TABLE notnull_tbl1
// ALTER COLUMN a SET NOT NULL` on a partitioned parent whose partition
// notnull_tbl1_3 holds a NULL now errors (previously forEachLiveRow scanned
// only the parent's own — empty — storage and wrongly succeeded).
func TestAlterTableSetNotNullOnPartitionedParentScansPartitions(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	for _, stmt := range []string{
		`CREATE TABLE notnull_tbl1 (a int, b int) PARTITION BY LIST (a)`,
		`ALTER TABLE notnull_tbl1 ADD CONSTRAINT notnull_con NOT NULL a NOT VALID`,
		`CREATE TABLE notnull_tbl1_1 PARTITION OF notnull_tbl1 FOR VALUES IN (1,2)`,
		`CREATE TABLE notnull_tbl1_2(a int, CONSTRAINT nn2 NOT NULL a, b int)`,
		`ALTER TABLE notnull_tbl1 ATTACH PARTITION notnull_tbl1_2 FOR VALUES IN (3,4)`,
		`CREATE TABLE notnull_tbl1_3(a int, b int)`,
		`INSERT INTO notnull_tbl1_3 values(NULL,1)`,
		`ALTER TABLE notnull_tbl1_3 add CONSTRAINT nn3 NOT NULL a NOT VALID`,
		`ALTER TABLE notnull_tbl1 ATTACH PARTITION notnull_tbl1_3 FOR VALUES IN (NULL,5)`,
	} {
		if err := runDDL(t, ctx, stmt); err != nil {
			t.Fatalf("setup %q: %v", stmt, err)
		}
	}

	// PG's phase-3 verify scan reports the relation ACTUALLY holding the
	// violating row — the partition, not the ALTER TABLE target — because
	// ATRewriteTable is queued and errors per relation
	// (constraints.out:1561: "column "a" of relation "notnull_tbl1_3"
	// contains null values", not "notnull_tbl1").
	err := runDDL(t, ctx, `ALTER TABLE notnull_tbl1 ALTER COLUMN a SET NOT NULL`)
	requireExecError(t, err, "23502",
		`column "a" of relation "notnull_tbl1_3" contains null values`)

	// The failed SET NOT NULL leaves notnull_con's convalidated flag alone —
	// column a already carries attnotnull=true from the earlier NOT VALID ADD
	// (constraints.sql:822's "even an invalid not-null forbids new nulls"), so
	// this only re-confirms that the not-null constraint stays NotValid.
	tbl, ok := cat.LookupTable(parser.ObjectName{Name: "notnull_tbl1"})
	if !ok {
		t.Fatal("notnull_tbl1 not found")
	}
	for _, nc := range tbl.NotNullConstraints {
		if nc.Name == "notnull_con" && !nc.NotValid {
			t.Errorf("NotNullConstraints[notnull_con].NotValid = false after failed SET NOT NULL, want still true")
		}
	}

	// TRUNCATE clears the NULL-bearing partition, then SET NOT NULL succeeds
	// (constraints.sql:935-936).
	if err := runDDL(t, ctx, `TRUNCATE notnull_tbl1`); err != nil {
		t.Fatalf("TRUNCATE: %v", err)
	}
	if err := runDDL(t, ctx, `ALTER TABLE notnull_tbl1 ALTER COLUMN a SET NOT NULL`); err != nil {
		t.Fatalf("SET NOT NULL after TRUNCATE should succeed: %v", err)
	}
}

// TestAlterTableAddPrimaryKeyOnPartitionedParentScansPartitions verifies
// acceptance criterion 4: constraints.sql:775-778's `ALTER TABLE cnn2_parted
// ADD PRIMARY KEY (a)` on a partitioned parent whose partition cnn_part1
// holds a NULL now errors.
func TestAlterTableAddPrimaryKeyOnPartitionedParentScansPartitions(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	for _, stmt := range []string{
		`create table cnn2_parted(a int) partition by list (a)`,
		`create table cnn_part1 partition of cnn2_parted for values in (1, null)`,
		`insert into cnn_part1 values (null)`,
	} {
		if err := runDDL(t, ctx, stmt); err != nil {
			t.Fatalf("setup %q: %v", stmt, err)
		}
	}

	// Same per-relation reporting as SET NOT NULL: PG names the partition
	// (constraints.out:1254: "column "a" of relation "cnn_part1" contains
	// null values"), not the ALTER TABLE target "cnn2_parted".
	err := runDDL(t, ctx, `alter table cnn2_parted add primary key (a)`)
	requireExecError(t, err, "23502",
		`column "a" of relation "cnn_part1" contains null values`)
}

// TestAlterTableSetNotNullAndValidateStillSucceedWithoutNulls is the
// over-fix guard: a plain, non-partitioned table with no NULLs must still
// pass both SET NOT NULL and VALIDATE CONSTRAINT after M0134-0005ac's
// forEachLiveRow recursion + VALIDATE CONSTRAINT scan are added — recursion
// into a (nonexistent) descendant set, and the added scan itself, must stay
// a no-op when there is nothing to reject.
func TestAlterTableSetNotNullAndValidateStillSucceedWithoutNulls(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	for _, stmt := range []string{
		`CREATE TABLE nn_ok (a int, b int)`,
		`INSERT INTO nn_ok VALUES (1, 2), (3, 4)`,
	} {
		if err := runDDL(t, ctx, stmt); err != nil {
			t.Fatalf("setup %q: %v", stmt, err)
		}
	}

	if err := runDDL(t, ctx, `ALTER TABLE nn_ok ALTER a SET NOT NULL`); err != nil {
		t.Fatalf("SET NOT NULL over a NULL-free table should succeed: %v", err)
	}
	tbl, ok := cat.LookupTable(parser.ObjectName{Name: "nn_ok"})
	if !ok {
		t.Fatal("nn_ok not found")
	}
	if !tbl.Columns[0].NotNull {
		t.Errorf("column a NotNull = false after successful SET NOT NULL, want true")
	}

	if err := runDDL(t, ctx, `ALTER TABLE nn_ok ADD CONSTRAINT nn_b NOT NULL b NOT VALID`); err != nil {
		t.Fatalf("ADD ... NOT NULL ... NOT VALID: %v", err)
	}
	if err := runDDL(t, ctx, `ALTER TABLE nn_ok VALIDATE CONSTRAINT nn_b`); err != nil {
		t.Fatalf("VALIDATE CONSTRAINT over a NULL-free column should succeed: %v", err)
	}
	tbl, _ = cat.LookupTable(parser.ObjectName{Name: "nn_ok"})
	for _, nc := range tbl.NotNullConstraints {
		if nc.Name == "nn_b" && nc.NotValid {
			t.Errorf("NotNullConstraints[nn_b].NotValid still true after successful VALIDATE")
		}
	}
}
