package testport

import (
	"testing"

	"github.com/goopg/goopg/internal/testutil/tapnode"
)

// TestPort_Recovery034CreateDatabase ports
// postgres/src/test/recovery/t/034_create_database.pl (M0149-0001): WAL replay
// of CREATE DATABASE … STRATEGY WAL_LOG, checking that a table created in the
// template AFTER the copy persists in the template across crash recovery and
// is absent from the new database.
//
// Variants (M0149 contract C3, single-node):
//   - all-goopg: upstream's own steps on one goopg node — stop('immediate'),
//     start, then the two assertions;
//   - pg18-cold-start: a PostgreSQL 18.3 server started on a copy of the
//     crashed goopg directory runs crash recovery over goopg's WAL and answers
//     the same two assertions.
func TestPort_Recovery034CreateDatabase(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping recovery TAP port in short mode")
	}
	repo := repoRoot(t)
	tapnode.Available(t, repo)

	node := tapnode.NewGoopg(t, repo, "node")
	node.Start()

	// This checks that any DDLs run on the template database that modify
	// pg_class are persisted after creating a database from it using the
	// WAL_LOG strategy, as a direct copy of the template database's pg_class
	// is used in this case.
	const dbTemplate, dbNew = "template1", "test_db_1"

	// Create table. It should persist on the template database.
	node.SafePSQL("postgres", "CREATE DATABASE "+dbNew+" STRATEGY WAL_LOG TEMPLATE "+dbTemplate+";")
	node.SafePSQL(dbTemplate, "CREATE TABLE tab_db_after_create_1 (a INT);")

	// Flush the changes affecting the template database, then replay them.
	node.SafePSQL("postgres", "CHECKPOINT;")

	node.Stop(tapnode.StopImmediate)

	const countTabs = "SELECT count(*) FROM pg_class WHERE relname LIKE 'tab_db_%';"
	assertions := func(t *testing.T, n *tapnode.Node) {
		t.Helper()
		if got := n.SafePSQL(dbTemplate, countTabs); got != "1" {
			t.Errorf("%s: check that table exists on template after crash, with checkpoint: got %q, want \"1\"", n.Flavor(), got)
		}
		// The new database should have no tables.
		if got := n.SafePSQL(dbNew, countTabs); got != "0" {
			t.Errorf("%s: check that there are no tables from template on new database after crash: got %q, want \"0\"", n.Flavor(), got)
		}
	}

	// The PG variant copies the crashed directory before goopg's own restart
	// replays the WAL tail, so both servers recover the same crash.
	t.Run("pg18-cold-start", func(t *testing.T) {
		pg := tapnode.ColdStartPG(t, node, "pg-on-goopg-copy")
		assertions(t, pg)
	})
	t.Run("all-goopg", func(t *testing.T) {
		node.Start()
		assertions(t, node)
	})
}
