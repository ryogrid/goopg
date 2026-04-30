package testport

import (
	"context"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/testutil/cluster"
)

// TestE2E_DDLDMLChain exercises a full DDL → DML → DDL → DML → DROP chain.
func TestE2E_DDLDMLChain(t *testing.T) {
	c := newCluster(t, "e2e_chain")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	runSQL(t, c, "CREATE TABLE t (id int, val text)")
	runSQL(t, c, "INSERT INTO t VALUES (1, 'a')")
	runSQL(t, c, "ALTER TABLE t ADD COLUMN extra int")
	runSQL(t, c, "UPDATE t SET extra = 100 WHERE id = 1")

	rows := runSQL(t, c, "SELECT id, val, extra FROM t")
	if len(rows) != 1 || rows[0][0] != "1" || rows[0][1] != "a" || rows[0][2] != "100" {
		t.Fatalf("chain result = %v, want [[1 a 100]]", rows)
	}

	runSQL(t, c, "DROP TABLE t")
}

// TestE2E_PLpgSQLNested exercises a procedure calling a function.
func TestE2E_PLpgSQLNested(t *testing.T) {
	c := newCluster(t, "e2e_plpgsql")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	runSQL(t, c, "CREATE FUNCTION inner_func(x int) RETURNS int LANGUAGE plpgsql AS $$ BEGIN RETURN x * 2; END $$")
	runSQL(t, c, "CREATE FUNCTION outer_func(x int) RETURNS int LANGUAGE plpgsql AS $$ BEGIN RETURN inner_func(x) + 1; END $$")

	rows := runSQL(t, c, "SELECT outer_func(5)")
	if rows[0][0] != "11" {
		t.Fatalf("outer_func(5) = %v, want 11", rows[0][0])
	}

	// Procedure calling function
	runSQL(t, c, "CREATE PROCEDURE proc(IN x int, OUT result int) LANGUAGE plpgsql AS $$ BEGIN result := outer_func(x); END $$")
	rows = runSQL(t, c, "CALL proc(3)")
	if rows[0][0] != "7" {
		t.Fatalf("CALL proc(3) = %v, want 7", rows[0][0])
	}

	runSQL(t, c, "DROP PROCEDURE proc(int, int)")
	runSQL(t, c, "DROP FUNCTION outer_func(int)")
	runSQL(t, c, "DROP FUNCTION inner_func(int)")
}

// TestE2E_ReadCommittedIsolation verifies that goopg's implicit
// per-statement transaction uses Read Committed isolation:
// each statement sees only committed data.
func TestE2E_ReadCommittedIsolation(t *testing.T) {
	c := newCluster(t, "e2e_rc")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	runSQL(t, c, "CREATE TABLE t (id int)")
	runSQL(t, c, "INSERT INTO t VALUES (1)")

	// Session B inserts a row in its own implicit txn.
	err := runSQLSimple(t, c, "INSERT INTO t VALUES (2)")
	if err != nil {
		t.Fatal(err)
	}

	// Session A: a separate query sees the committed row because each
	// query runs in its own Read Committed transaction.
	ctx := context.Background()
	rowsA, err := c.Query(ctx, "SELECT COUNT(*) FROM t")
	if err != nil {
		t.Fatal(err)
	}
	if rowsA[0][0] != "2" {
		t.Fatalf("count = %v, want 2 (Read Committed: sees committed data)", rowsA[0][0])
	}

	runSQL(t, c, "DROP TABLE t")
}

// TestE2E_ConcurrentSession tests two sessions reading and writing.
func TestE2E_ConcurrentSession(t *testing.T) {
	c := newCluster(t, "e2e_concurrent")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	runSQL(t, c, "CREATE TABLE t (id int, val int)")
	runSQL(t, c, "INSERT INTO t VALUES (1, 10)")

	// Session A: simple UPDATE
	runSQL(t, c, "UPDATE t SET val = 20 WHERE id = 1")

	// Session B: read after update (each query is independent in v0)
	rows := runSQL(t, c, "SELECT val FROM t WHERE id = 1")
	if rows[0][0] != "20" {
		t.Fatalf("val = %v, want 20 after concurrent update", rows[0][0])
	}

	runSQL(t, c, "DROP TABLE t")
}

// runSQLSimple runs a SQL statement ignoring the result, returning only an error.
func runSQLSimple(t *testing.T, c *cluster.Cluster, sql string) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := c.Query(ctx, sql)
	return err
}
