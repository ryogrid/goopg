package testport

import (
	"testing"

	"github.com/goopg/goopg/internal/testutil/cluster"
)

// TestSyntax_UPSERT_DoNothing exercises INSERT ... ON CONFLICT DO NOTHING.
func TestSyntax_UPSERT_DoNothing(t *testing.T) {
	c := newCluster(t, "syntax_up_nothing")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	// Syntax check: ON CONFLICT DO NOTHING parses and executes without error.
	runSQL(t, c, "CREATE TABLE t (id int, val int)")
	runSQL(t, c, "INSERT INTO t VALUES (1, 10)")

	// ON CONFLICT DO NOTHING should not error
	runSQL(t, c, "INSERT INTO t VALUES (2, 20) ON CONFLICT DO NOTHING")

	// Verify the original rows still exist
	rows := runSQL(t, c, "SELECT COUNT(*) FROM t")
	if rows[0][0] == "0" {
		t.Fatal("table is empty after INSERT ON CONFLICT DO NOTHING")
	}

	runSQL(t, c, "DROP TABLE t")
}

// TestSyntax_UPSERT_DoUpdate exercises INSERT ... ON CONFLICT DO UPDATE.
func TestSyntax_UPSERT_DoUpdate(t *testing.T) {
	c := newCluster(t, "syntax_up_update")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	runSQL(t, c, "CREATE TABLE t (id int, val int)")
	runSQL(t, c, "CREATE UNIQUE INDEX i ON t (id)")
	runSQL(t, c, "INSERT INTO t VALUES (1, 10)")

	// ON CONFLICT DO UPDATE SET — requires a matching unique index
	runSQL(t, c, "INSERT INTO t VALUES (2, 20) ON CONFLICT (id) DO UPDATE SET val = EXCLUDED.val")

	rows := runSQL(t, c, "SELECT COUNT(*) FROM t")
	if rows[0][0] == "0" {
		t.Fatal("table is empty after UPSERT")
	}

	runSQL(t, c, "DROP TABLE t")
}

func TestSyntax_UPSERT_DoNothing_ExpressionUniqueIndex(t *testing.T) {
	c := newCluster(t, "syntax_up_nothing_expr")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	runSQL(t, c, "CREATE TABLE t (key text, val text)")
	runSQL(t, c, "CREATE FUNCTION idx_key(text) RETURNS text IMMUTABLE LANGUAGE plpgsql AS $$ BEGIN RETURN $1; END $$")
	runSQL(t, c, "CREATE UNIQUE INDEX t_key_expr_idx ON t ((idx_key(key)))")
	runSQL(t, c, "INSERT INTO t VALUES ('k1', 'v1') ON CONFLICT DO NOTHING")
	runSQL(t, c, "INSERT INTO t VALUES ('k1', 'v2') ON CONFLICT DO NOTHING")

	rows := runSQL(t, c, "SELECT COUNT(*), MIN(val), MAX(val) FROM t")
	if len(rows) != 1 || len(rows[0]) != 3 || rows[0][0] != "1" || rows[0][1] != "v1" || rows[0][2] != "v1" {
		t.Fatalf("expression-index DO NOTHING result = %v, want [[1 v1 v1]]", rows)
	}

	runSQL(t, c, "DROP TABLE t")
	runSQL(t, c, "DROP FUNCTION idx_key")
}
