package testport

import (
	"testing"

	"github.com/goopg/goopg/internal/testutil/tapnode"
)

// TestPort_Recovery030StatsCleanupReplica ports postgres/src/test/recovery/t/030_stats_cleanup_replica.pl
// to the M0149 contract: a standby creates table and function stats when
// queried, drops them when it replays the DROP TABLE / DROP FUNCTION / DROP
// SCHEMA CASCADE / DROP DATABASE that removed the object, keeps them across a
// clean restart and discards them after an immediate one. It runs in three
// pairings: all-goopg, a PG 18.3 primary with a goopg standby, and a goopg
// primary with a PG 18.3 standby.
func TestPort_Recovery030StatsCleanupReplica(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping recovery TAP port in short mode")
	}
	repo := repoRoot(t)
	tapnode.Available(t, repo)
	goopg := func(t *testing.T, name string) *tapnode.Node { return tapnode.NewGoopg(t, repo, name) }
	pg := func(t *testing.T, name string) *tapnode.Node { return tapnode.NewPG(t, repo, name) }
	goopgFromBackup := func(t *testing.T, name string, root *tapnode.Node, bkp string) *tapnode.Node {
		return tapnode.InitFromBackupGoopg(t, repo, name, root, bkp, true)
	}
	pgFromBackup := func(t *testing.T, name string, root *tapnode.Node, bkp string) *tapnode.Node {
		return tapnode.InitFromBackupPG(t, repo, name, root, bkp, true)
	}
	cases := []struct {
		name    string
		primary func(t *testing.T, name string) *tapnode.Node
		standby func(t *testing.T, name string, root *tapnode.Node, bkp string) *tapnode.Node
	}{
		{"all-goopg", goopg, goopgFromBackup},
		{"pg18-primary-goopg-standby", pg, goopgFromBackup},
		{"goopg-primary-pg18-standby", goopg, pgFromBackup},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runStatsCleanupReplica(t, tc.primary(t, "primary"), func(name, bkp string, root *tapnode.Node) *tapnode.Node {
				return tc.standby(t, name, root, bkp)
			})
		})
	}
}

func runStatsCleanupReplica(t *testing.T, nodePrimary *tapnode.Node, fromBackup func(name, bkp string, root *tapnode.Node) *tapnode.Node) {
	// Initialize primary node
	for _, l := range []string{"wal_level = replica", "max_wal_senders = 10", "track_functions = 'all'"} {
		nodePrimary.AppendConf(l)
	}
	nodePrimary.Start()

	backupName := "my_backup"

	// Take backup
	bkp := nodePrimary.Backup(backupName)

	// Set up standby linking to primary
	nodeStandby := fromBackup("standby", bkp, nodePrimary)
	nodeStandby.Start()

	sect := "initial"

	populateStandbyStats := func(connectDB, schema string) (dboid, tableoid, funcoid string) {
		t.Helper()
		// create objects on primary
		nodePrimary.SafePSQL(connectDB, "CREATE TABLE "+schema+".drop_tab_test1 AS SELECT generate_series(1,100) AS a")
		nodePrimary.SafePSQL(connectDB, "CREATE FUNCTION "+schema+".drop_func_test1() RETURNS VOID AS 'select 2;' LANGUAGE SQL IMMUTABLE")
		waitForReplayOf(t, nodePrimary, nodeStandby)

		// collect object oids
		dboid = nodeStandby.SafePSQL(connectDB, "SELECT oid FROM pg_database WHERE datname = '"+connectDB+"'")
		tableoid = nodeStandby.SafePSQL(connectDB, "SELECT '"+schema+".drop_tab_test1'::regclass::oid")
		funcoid = nodeStandby.SafePSQL(connectDB, "SELECT '"+schema+".drop_func_test1()'::regprocedure::oid")

		// generate stats on standby
		nodeStandby.SafePSQL(connectDB, "SELECT * FROM "+schema+".drop_tab_test1")
		nodeStandby.SafePSQL(connectDB, "SELECT "+schema+".drop_func_test1()")
		return dboid, tableoid, funcoid
	}
	dropFunctionByOID := func(connectDB, funcoid string) {
		t.Helper()
		// Get function name from returned oid
		funcName := nodePrimary.SafePSQL(connectDB, "SELECT '"+funcoid+"'::regprocedure")
		nodePrimary.SafePSQL(connectDB, "DROP FUNCTION "+funcName)
	}
	dropTableByOID := func(connectDB, tableoid string) {
		t.Helper()
		// Get table name from returned oid
		tableName := nodePrimary.SafePSQL(connectDB, "SELECT '"+tableoid+"'::regclass")
		nodePrimary.SafePSQL(connectDB, "DROP TABLE "+tableName)
	}
	testStandbyFuncTabStatsStatus := func(connectDB, dboid, tableoid, funcoid, present string) {
		t.Helper()
		rel := nodeStandby.SafePSQL(connectDB, "SELECT pg_stat_have_stats('relation', "+dboid+", "+tableoid+")")
		fn := nodeStandby.SafePSQL(connectDB, "SELECT pg_stat_have_stats('function', "+dboid+", "+funcoid+")")
		if rel != present || fn != present {
			t.Errorf("not ok: %s: standby stats as expected: rel=%q func=%q, want %q", sect, rel, fn, present)
		}
	}
	testStandbyDBStatsStatus := func(connectDB, dboid, present string) {
		t.Helper()
		if got := nodeStandby.SafePSQL(connectDB, "SELECT pg_stat_have_stats('database', "+dboid+", 0)"); got != present {
			t.Errorf("not ok: %s: standby db stats as expected: got %q, want %q", sect, got, present)
		}
	}

	// Test that stats are cleaned up on standby after dropping table or
	// function
	dboid, tableoid, funcoid := populateStandbyStats("postgres", "public")
	testStandbyFuncTabStatsStatus("postgres", dboid, tableoid, funcoid, "t")

	dropTableByOID("postgres", tableoid)
	dropFunctionByOID("postgres", funcoid)

	sect = "post drop"
	waitForReplayOf(t, nodePrimary, nodeStandby)
	testStandbyFuncTabStatsStatus("postgres", dboid, tableoid, funcoid, "f")

	// Test that stats are cleaned up on standby after dropping indirectly
	sect = "schema creation"

	nodePrimary.SafePSQL("postgres", "CREATE SCHEMA drop_schema_test1")
	waitForReplayOf(t, nodePrimary, nodeStandby)

	dboid, tableoid, funcoid = populateStandbyStats("postgres", "drop_schema_test1")
	testStandbyFuncTabStatsStatus("postgres", dboid, tableoid, funcoid, "t")
	nodePrimary.SafePSQL("postgres", "DROP SCHEMA drop_schema_test1 CASCADE")

	sect = "post schema drop"
	waitForReplayOf(t, nodePrimary, nodeStandby)

	// verify table and function stats removed from standby
	testStandbyFuncTabStatsStatus("postgres", dboid, tableoid, funcoid, "f")

	// Test that stats are cleaned up on standby after dropping database
	sect = "createdb"

	nodePrimary.SafePSQL("postgres", "CREATE DATABASE test")
	waitForReplayOf(t, nodePrimary, nodeStandby)

	dboid, tableoid, funcoid = populateStandbyStats("test", "public")

	// verify stats are present
	testStandbyFuncTabStatsStatus("test", dboid, tableoid, funcoid, "t")
	testStandbyDBStatsStatus("test", dboid, "t")

	nodePrimary.SafePSQL("postgres", "DROP DATABASE test")
	sect = "post dropdb"
	waitForReplayOf(t, nodePrimary, nodeStandby)

	// Test that the stats were cleaned up on standby. Have to use different
	// databases due to the database having been dropped.
	testStandbyFuncTabStatsStatus("postgres", dboid, tableoid, funcoid, "f")
	testStandbyDBStatsStatus("postgres", dboid, "f")

	// Test that stats are retained across a non-immediate restart, but
	// discarded after an immediate restart
	sect = "pre restart"
	dboid, tableoid, funcoid = populateStandbyStats("postgres", "public")
	testStandbyFuncTabStatsStatus("postgres", dboid, tableoid, funcoid, "t")

	nodeStandby.Restart()

	sect = "post non-immediate"

	testStandbyFuncTabStatsStatus("postgres", dboid, tableoid, funcoid, "t")

	// but gone after an immediate restart
	nodeStandby.Stop(tapnode.StopImmediate)
	nodeStandby.Start()

	sect = "post immediate restart"

	testStandbyFuncTabStatsStatus("postgres", dboid, tableoid, funcoid, "f")
}
