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
