package testport

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/testutil/cluster"
	"github.com/lib/pq"
)

// isFKErr reports whether err is a foreign-key-violation (23503).
func isFKErr(err error) bool {
	if err == nil {
		return false
	}
	if pe, ok := err.(*pq.Error); ok {
		return pe.Code == "23503"
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "foreign key constraint")
}

// TestPort_PartitionedDeferredFKCatchesViolationAtCommit is the M0134-0005h
// positive guard: a DEFERRABLE INITIALLY DEFERRED foreign key on a
// PARTITIONED child table must raise 23503 at COMMIT when a violating row
// was routed into a leaf partition — mirroring PG 18.3's leaf-scoped RI
// trigger deferral (postgres/src/backend/commands/tablecmds.c
// addFkRecurseReferencing / postgres/src/backend/utils/adt/ri_triggers.c).
//
// Root cause (tmp/ralph-handoffs/m0134-0005h-probe/report.md): the deferred
// FK queue always records the partitioned ROOT's table name (FK definitions
// live only on the root — checkFKInsertForConstraints,
// internal/executor/operators_fk.go), and the COMMIT-tier scan
// (fullTableFKCheck, operators_fk.go) scanned ONLY that exact table object.
// A partitioned root has zero physical blocks, so the scan found nothing and
// the transaction committed a violated FK. Fixed by expanding the scan with
// allDescendants, the same idiom already used by scanTableForMatch and
// scanTableForMatchFKWait in the same file.
//
// PG names the LEAF partition in the error message (verified live against
// PG 18.3, probe report Q5 Test 1): `... violates foreign key constraint
// "fk_c" ...` naming table "fk_child_p1", not the root "fk_child" — assert
// that shape here, not just "any 23503".
func TestPort_PartitionedDeferredFKCatchesViolationAtCommit(t *testing.T) {
	c := newCluster(t, "partdeferfk")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	if err := runSQLSimple(t, c, "CREATE TABLE fk_parent (id int PRIMARY KEY)"); err != nil {
		t.Fatalf("create parent: %v", err)
	}
	if err := runSQLSimple(t, c, "INSERT INTO fk_parent VALUES (1)"); err != nil {
		t.Fatalf("seed parent: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE TABLE fk_child (id int PRIMARY KEY, pid int, "+
		"CONSTRAINT fk_c FOREIGN KEY (pid) REFERENCES fk_parent(id) DEFERRABLE INITIALLY DEFERRED) "+
		"PARTITION BY RANGE (id)"); err != nil {
		t.Fatalf("create partitioned child: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE TABLE fk_child_p1 PARTITION OF fk_child "+
		"FOR VALUES FROM (0) TO (1000)"); err != nil {
		t.Fatalf("create leaf partition: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	db, conn := scConn(t, c, ctx)
	defer db.Close()
	defer conn.Close()

	ex := func(q string) error {
		_, err := conn.ExecContext(ctx, q)
		return err
	}

	if err := ex("BEGIN"); err != nil {
		t.Fatalf("begin: %v", err)
	}
	// pid=999 does not exist in fk_parent. With INITIALLY DEFERRED the
	// row-routed INSERT into leaf fk_child_p1 must be accepted here...
	if err := ex("INSERT INTO fk_child VALUES (1, 999)"); err != nil {
		t.Fatalf("deferred-violating insert should not fail at INSERT: %v", err)
	}
	// ...and only raise 23503 at COMMIT.
	err := ex("COMMIT")
	if !isFKErr(err) {
		t.Fatalf("expected 23503 at COMMIT, got %v", err)
	}
	if !strings.Contains(err.Error(), `"fk_child_p1"`) {
		t.Fatalf("expected error naming the LEAF partition fk_child_p1, got %v", err)
	}
	if !strings.Contains(err.Error(), `"fk_c"`) {
		t.Fatalf("expected error naming constraint fk_c, got %v", err)
	}
	if pe, ok := err.(*pq.Error); ok {
		if !strings.Contains(pe.Detail, "Key (pid)=(999) is not present in table \"fk_parent\"") {
			t.Fatalf("expected DETAIL naming the missing key, got %q", pe.Detail)
		}
	} else {
		t.Fatalf("expected *pq.Error, got %T: %v", err, err)
	}
	_ = ex("ROLLBACK")

	// The failed COMMIT rolled back: no row should have landed.
	rows := runSQL(t, c, "SELECT id FROM fk_child WHERE id = 1")
	if len(rows) != 0 {
		t.Fatalf("violating row should have rolled back, got %v", rows)
	}

	if err := runSQLSimple(t, c, "DROP TABLE fk_child"); err != nil {
		t.Fatalf("drop child: %v", err)
	}
	if err := runSQLSimple(t, c, "DROP TABLE fk_parent"); err != nil {
		t.Fatalf("drop parent: %v", err)
	}
}

// TestPort_PartitionedDeferredFKMultiLevelCatchesViolationAtCommit proves
// allDescendants' recursion actually matters: a violating row routed into a
// partition-of-a-partition (two levels deep) must still be caught at COMMIT,
// not just direct children of the FK-owning root.
func TestPort_PartitionedDeferredFKMultiLevelCatchesViolationAtCommit(t *testing.T) {
	c := newCluster(t, "partdeferfkmulti")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	if err := runSQLSimple(t, c, "CREATE TABLE fk_parent (id int PRIMARY KEY)"); err != nil {
		t.Fatalf("create parent: %v", err)
	}
	if err := runSQLSimple(t, c, "INSERT INTO fk_parent VALUES (1)"); err != nil {
		t.Fatalf("seed parent: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE TABLE fk_child (id int PRIMARY KEY, pid int, "+
		"CONSTRAINT fk_c FOREIGN KEY (pid) REFERENCES fk_parent(id) DEFERRABLE INITIALLY DEFERRED) "+
		"PARTITION BY RANGE (id)"); err != nil {
		t.Fatalf("create partitioned child: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE TABLE fk_child_p1 PARTITION OF fk_child "+
		"FOR VALUES FROM (0) TO (2000) PARTITION BY RANGE (id)"); err != nil {
		t.Fatalf("create mid-level partition: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE TABLE fk_child_p1_p1 PARTITION OF fk_child_p1 "+
		"FOR VALUES FROM (0) TO (1000)"); err != nil {
		t.Fatalf("create leaf partition: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	db, conn := scConn(t, c, ctx)
	defer db.Close()
	defer conn.Close()

	ex := func(q string) error {
		_, err := conn.ExecContext(ctx, q)
		return err
	}

	if err := ex("BEGIN"); err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err := ex("INSERT INTO fk_child VALUES (1, 999)"); err != nil {
		t.Fatalf("deferred-violating insert should not fail at INSERT: %v", err)
	}
	err := ex("COMMIT")
	if !isFKErr(err) {
		t.Fatalf("expected 23503 at COMMIT for a two-level-deep leaf, got %v", err)
	}
	if !strings.Contains(err.Error(), `"fk_child_p1_p1"`) {
		t.Fatalf("expected error naming the second-level leaf fk_child_p1_p1, got %v", err)
	}
	_ = ex("ROLLBACK")

	if err := runSQLSimple(t, c, "DROP TABLE fk_child"); err != nil {
		t.Fatalf("drop child: %v", err)
	}
	if err := runSQLSimple(t, c, "DROP TABLE fk_parent"); err != nil {
		t.Fatalf("drop parent: %v", err)
	}
}

// TestPort_PartitionedDeferredFKSatisfiedCommitsCleanly is the first
// negative guard: a partitioned child holding a row whose deferred FK is
// SATISFIED must still commit cleanly. A one-directional fix (always raising
// 23503 for any partitioned deferred FK) would pass the positive guard above
// but wrongly break this case.
func TestPort_PartitionedDeferredFKSatisfiedCommitsCleanly(t *testing.T) {
	c := newCluster(t, "partdeferfkok")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	if err := runSQLSimple(t, c, "CREATE TABLE fk_parent (id int PRIMARY KEY)"); err != nil {
		t.Fatalf("create parent: %v", err)
	}
	if err := runSQLSimple(t, c, "INSERT INTO fk_parent VALUES (1)"); err != nil {
		t.Fatalf("seed parent: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE TABLE fk_child (id int PRIMARY KEY, pid int, "+
		"CONSTRAINT fk_c FOREIGN KEY (pid) REFERENCES fk_parent(id) DEFERRABLE INITIALLY DEFERRED) "+
		"PARTITION BY RANGE (id)"); err != nil {
		t.Fatalf("create partitioned child: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE TABLE fk_child_p1 PARTITION OF fk_child "+
		"FOR VALUES FROM (0) TO (1000)"); err != nil {
		t.Fatalf("create leaf partition: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	db, conn := scConn(t, c, ctx)
	defer db.Close()
	defer conn.Close()

	ex := func(q string) error {
		_, err := conn.ExecContext(ctx, q)
		return err
	}

	if err := ex("BEGIN"); err != nil {
		t.Fatalf("begin: %v", err)
	}
	// pid=1 DOES exist in fk_parent — the deferred check must find it and
	// COMMIT must succeed.
	if err := ex("INSERT INTO fk_child VALUES (2, 1)"); err != nil {
		t.Fatalf("satisfied insert should not fail at INSERT: %v", err)
	}
	if err := ex("COMMIT"); err != nil {
		t.Fatalf("expected clean commit for a satisfied deferred FK, got %v", err)
	}

	rows := runSQL(t, c, "SELECT id, pid FROM fk_child WHERE id = 2")
	if len(rows) != 1 {
		t.Fatalf("expected the satisfied row to have landed, got %v", rows)
	}

	if err := runSQLSimple(t, c, "DROP TABLE fk_child"); err != nil {
		t.Fatalf("drop child: %v", err)
	}
	if err := runSQLSimple(t, c, "DROP TABLE fk_parent"); err != nil {
		t.Fatalf("drop parent: %v", err)
	}
}

// TestPort_NonPartitionedDeferredFKStillCatchesViolationAtCommit is the
// second negative guard: the pre-existing, already-correct control case
// (deferred FK on a NON-partitioned child) must not regress. This shape is
// probe report Q5 Test 2.
func TestPort_NonPartitionedDeferredFKStillCatchesViolationAtCommit(t *testing.T) {
	c := newCluster(t, "nonpartdeferfk")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	if err := runSQLSimple(t, c, "CREATE TABLE fk_parent (id int PRIMARY KEY)"); err != nil {
		t.Fatalf("create parent: %v", err)
	}
	if err := runSQLSimple(t, c, "INSERT INTO fk_parent VALUES (1)"); err != nil {
		t.Fatalf("seed parent: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE TABLE fk_child_nonpart (id int PRIMARY KEY, pid int, "+
		"CONSTRAINT fk_c2 FOREIGN KEY (pid) REFERENCES fk_parent(id) DEFERRABLE INITIALLY DEFERRED)"); err != nil {
		t.Fatalf("create non-partitioned child: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	db, conn := scConn(t, c, ctx)
	defer db.Close()
	defer conn.Close()

	ex := func(q string) error {
		_, err := conn.ExecContext(ctx, q)
		return err
	}

	if err := ex("BEGIN"); err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err := ex("INSERT INTO fk_child_nonpart VALUES (1, 999)"); err != nil {
		t.Fatalf("deferred-violating insert should not fail at INSERT: %v", err)
	}
	err := ex("COMMIT")
	if !isFKErr(err) {
		t.Fatalf("expected 23503 at COMMIT for the non-partitioned control, got %v", err)
	}
	if !strings.Contains(err.Error(), `"fk_child_nonpart"`) {
		t.Fatalf("expected error naming fk_child_nonpart, got %v", err)
	}
	_ = ex("ROLLBACK")

	if err := runSQLSimple(t, c, "DROP TABLE fk_child_nonpart"); err != nil {
		t.Fatalf("drop child: %v", err)
	}
	if err := runSQLSimple(t, c, "DROP TABLE fk_parent"); err != nil {
		t.Fatalf("drop parent: %v", err)
	}
}

// TestPort_PartitionedAddForeignKeyCatchesExistingLeafViolation is the DDL
// twin (M0134-0005h scope item 2): ALTER TABLE <partitioned root> ADD
// FOREIGN KEY must scan every leaf partition's existing rows, not just the
// storage-less root. Verified live against PG 18.3 first (probe follow-up):
// PG rejects with 23503 naming the leaf, goopg silently accepted before this
// fix — see internal/executor/operators_ddl.go validateFKConstraintExistingRows.
func TestPort_PartitionedAddForeignKeyCatchesExistingLeafViolation(t *testing.T) {
	c := newCluster(t, "partaddfk")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	if err := runSQLSimple(t, c, "CREATE TABLE fk_parent (id int PRIMARY KEY)"); err != nil {
		t.Fatalf("create parent: %v", err)
	}
	if err := runSQLSimple(t, c, "INSERT INTO fk_parent VALUES (1)"); err != nil {
		t.Fatalf("seed parent: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE TABLE fk_child (id int PRIMARY KEY, pid int) "+
		"PARTITION BY RANGE (id)"); err != nil {
		t.Fatalf("create partitioned child: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE TABLE fk_child_p1 PARTITION OF fk_child "+
		"FOR VALUES FROM (0) TO (1000)"); err != nil {
		t.Fatalf("create leaf partition: %v", err)
	}
	// The violating row is inserted BEFORE the FK exists, so it can only be
	// caught by ADD FOREIGN KEY's existing-row validation scan.
	if err := runSQLSimple(t, c, "INSERT INTO fk_child VALUES (1, 999)"); err != nil {
		t.Fatalf("seed violating row: %v", err)
	}

	err := runSQLSimple(t, c, "ALTER TABLE fk_child ADD CONSTRAINT fk_c FOREIGN KEY (pid) "+
		"REFERENCES fk_parent(id)")
	if !isFKErr(err) {
		t.Fatalf("expected 23503 from ADD FOREIGN KEY validation, got %v", err)
	}
	if !strings.Contains(err.Error(), `"fk_child_p1"`) {
		t.Fatalf("expected error naming the LEAF partition fk_child_p1, got %v", err)
	}

	if err := runSQLSimple(t, c, "DROP TABLE fk_child"); err != nil {
		t.Fatalf("drop child: %v", err)
	}
	if err := runSQLSimple(t, c, "DROP TABLE fk_parent"); err != nil {
		t.Fatalf("drop parent: %v", err)
	}
}

// TestPort_PartitionedAddForeignKeySatisfiedSucceeds is the negative guard
// for the DDL twin: ADD FOREIGN KEY over a partitioned child whose existing
// leaf rows all satisfy the constraint must succeed.
func TestPort_PartitionedAddForeignKeySatisfiedSucceeds(t *testing.T) {
	c := newCluster(t, "partaddfkok")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	if err := runSQLSimple(t, c, "CREATE TABLE fk_parent (id int PRIMARY KEY)"); err != nil {
		t.Fatalf("create parent: %v", err)
	}
	if err := runSQLSimple(t, c, "INSERT INTO fk_parent VALUES (1)"); err != nil {
		t.Fatalf("seed parent: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE TABLE fk_child (id int PRIMARY KEY, pid int) "+
		"PARTITION BY RANGE (id)"); err != nil {
		t.Fatalf("create partitioned child: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE TABLE fk_child_p1 PARTITION OF fk_child "+
		"FOR VALUES FROM (0) TO (1000)"); err != nil {
		t.Fatalf("create leaf partition: %v", err)
	}
	if err := runSQLSimple(t, c, "INSERT INTO fk_child VALUES (1, 1)"); err != nil {
		t.Fatalf("seed satisfying row: %v", err)
	}

	if err := runSQLSimple(t, c, "ALTER TABLE fk_child ADD CONSTRAINT fk_c FOREIGN KEY (pid) "+
		"REFERENCES fk_parent(id)"); err != nil {
		t.Fatalf("ADD FOREIGN KEY over satisfied leaf rows should succeed: %v", err)
	}

	if err := runSQLSimple(t, c, "DROP TABLE fk_child"); err != nil {
		t.Fatalf("drop child: %v", err)
	}
	if err := runSQLSimple(t, c, "DROP TABLE fk_parent"); err != nil {
		t.Fatalf("drop parent: %v", err)
	}
}
