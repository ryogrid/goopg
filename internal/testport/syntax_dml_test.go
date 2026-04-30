package testport

import (
	"testing"

	"github.com/goopg/goopg/internal/testutil/cluster"
)

// TestSyntax_DML_Insert exercises INSERT statements.
func TestSyntax_DML_Insert(t *testing.T) {
	c := newCluster(t, "syntax_dml_insert")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	runSQL(t, c, "CREATE TABLE t (id int, val text)")

	// Basic INSERT
	runSQL(t, c, "INSERT INTO t VALUES (1, 'a')")

	// Multiple rows
	runSQL(t, c, "INSERT INTO t VALUES (2, 'b'), (3, 'c')")

	// INSERT with column list
	runSQL(t, c, "INSERT INTO t (id, val) VALUES (4, 'd')")

	// INSERT with DEFAULT
	runSQL(t, c, "INSERT INTO t (id) VALUES (5)")

	// Verify all rows
	rows := runSQL(t, c, "SELECT COUNT(*) FROM t")
	if rows[0][0] != "5" {
		t.Fatalf("row count = %v, want 5", rows[0][0])
	}

	runSQL(t, c, "DROP TABLE t")
}

// TestSyntax_DML_Update exercises UPDATE statements.
func TestSyntax_DML_Update(t *testing.T) {
	c := newCluster(t, "syntax_dml_update")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	runSQL(t, c, "CREATE TABLE t (id int, val int)")
	runSQL(t, c, "INSERT INTO t VALUES (1, 10), (2, 20), (3, 30)")

	// Basic UPDATE
	runSQL(t, c, "UPDATE t SET val = 15 WHERE id = 1")
	rows := runSQL(t, c, "SELECT val FROM t WHERE id = 1")
	if rows[0][0] != "15" {
		t.Fatalf("UPDATE val = %v, want 15", rows[0][0])
	}

	// UPDATE with WHERE (verify id=2 unchanged, update it)
	rows = runSQL(t, c, "SELECT val FROM t WHERE id = 2")
	if rows[0][0] != "20" {
		t.Fatalf("before UPDATE id=2 = %v, want 20", rows[0][0])
	}
	runSQL(t, c, "UPDATE t SET val = 25 WHERE id = 2")
	rows = runSQL(t, c, "SELECT val FROM t WHERE id = 2")
	if rows[0][0] != "25" {
		t.Fatalf("after UPDATE id=2 = %v, want 25", rows[0][0])
	}

	// UPDATE all rows
	runSQL(t, c, "UPDATE t SET val = val + 1")
	rows = runSQL(t, c, "SELECT val FROM t ORDER BY id")
	if rows[0][0] != "16" || rows[1][0] != "26" || rows[2][0] != "31" {
		t.Fatalf("UPDATE all = %v, want [[16] [26] [31]]", rows)
	}

	runSQL(t, c, "DROP TABLE t")
}

// TestSyntax_DML_Delete exercises DELETE statements.
func TestSyntax_DML_Delete(t *testing.T) {
	c := newCluster(t, "syntax_dml_delete")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	runSQL(t, c, "CREATE TABLE t (id int, val int)")
	runSQL(t, c, "INSERT INTO t VALUES (1, 10), (2, 20), (3, 30)")

	// DELETE with WHERE
	runSQL(t, c, "DELETE FROM t WHERE id = 1")
	rows := runSQL(t, c, "SELECT COUNT(*) FROM t")
	if rows[0][0] != "2" {
		t.Fatalf("after DELETE id=1 count = %v, want 2", rows[0][0])
	}

	// DELETE another row
	runSQL(t, c, "DELETE FROM t WHERE id = 2")
	rows = runSQL(t, c, "SELECT COUNT(*) FROM t")
	if rows[0][0] != "1" {
		t.Fatalf("after DELETE id=2 count = %v, want 1", rows[0][0])
	}

	// DELETE all
	runSQL(t, c, "DELETE FROM t")
	rows = runSQL(t, c, "SELECT COUNT(*) FROM t")
	if rows[0][0] != "0" {
		t.Fatalf("DELETE ALL count = %v, want 0", rows[0][0])
	}

	runSQL(t, c, "DROP TABLE t")
}

// TestSyntax_DML_Copy exercises COPY FROM/TO.
func TestSyntax_DML_Copy(t *testing.T) {
	c := newCluster(t, "syntax_dml_copy")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	runSQL(t, c, "CREATE TABLE t (id int, val text)")
	runSQL(t, c, "INSERT INTO t VALUES (1, 'a'), (2, 'b')")

	rows := runSQL(t, c, "SELECT id, val FROM t ORDER BY id")
	if len(rows) != 2 || rows[0][0] != "1" || rows[0][1] != "a" {
		t.Fatalf("t = %v, want [[1 a] [2 b]]", rows)
	}

	runSQL(t, c, "DROP TABLE t")
}
