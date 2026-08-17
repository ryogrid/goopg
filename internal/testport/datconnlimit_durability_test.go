package testport

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/testutil/cluster"
)

// TestPort_DatConnLimitSurvivesRestart verifies M0122-0006: an UPDATE
// pg_database SET datconnlimit survives a clean restart via the persistent
// pg_database heap row (global/1262). Before the fix, SetDatabaseConnLimit was
// purely in-memory — the value was lost on every restart.
//
// Uses a user database (CREATE DATABASE) rather than a bootstrap database
// because the heap reload (reloadDatabasesFromHeap) intentionally skips
// bootstrap databases (OID < FirstUserOID).
func TestPort_DatConnLimitSurvivesRestart(t *testing.T) {
	c, err := cluster.New("datconnlimit-durability", cluster.Options{
		RepoRoot:     repoRoot(t),
		DataDir:      filepath.Join(t.TempDir(), "data"),
		StartupWait:  20 * time.Second,
		ShutdownWait: 20 * time.Second,
		SyncInit:     true,
		SyncRuntime:  true,
	})
	if err != nil {
		t.Fatal(err)
	}
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	// Create a user database to hold the connlimit — user DBs ARE restored
	// from the heap on restart.
	const dbName = "testdb"
	if err := runSQLSimple(t, c, "CREATE DATABASE "+dbName); err != nil {
		t.Fatalf("CREATE DATABASE %s: %v", dbName, err)
	}

	// Pre-update sanity: the default is -1 (unlimited).
	dbRef := "pg_database WHERE datname = '" + dbName + "'"
	if got := queryScalar(t, c, "SELECT datconnlimit FROM "+dbRef); got != "-1" {
		t.Fatalf("pre-update datconnlimit = %q, want -1", got)
	}

	// Set a non-default connlimit.
	const newLimit = "5"
	if err := runSQLSimple(t, c,
		"UPDATE pg_database SET datconnlimit = "+newLimit+" WHERE datname = '"+dbName+"'"); err != nil {
		t.Fatalf("UPDATE pg_database SET datconnlimit: %v", err)
	}

	// Verify the in-memory value updated.
	if got := queryScalar(t, c, "SELECT datconnlimit FROM "+dbRef); got != newLimit {
		t.Fatalf("post-update datconnlimit = %q, want %s", got, newLimit)
	}

	// Clean stop → restart. The connlimit must survive because it is now
	// persisted to the pg_database heap row.
	if err := c.Stop(cluster.ShutdownFast); err != nil {
		t.Fatalf("stop cluster: %v", err)
	}
	if err := c.Start(); err != nil {
		t.Fatalf("restart cluster: %v", err)
	}

	if got := queryScalar(t, c, "SELECT datconnlimit FROM "+dbRef); got != newLimit {
		t.Fatalf("post-restart datconnlimit = %q, want %s (M0122-0006: "+
			"datconnlimit did not survive restart — heap row not written or not reloaded)", got, newLimit)
	}

	// Reset to the default and verify that also survives restart.
	const resetLimit = "-1"
	if err := runSQLSimple(t, c,
		"UPDATE pg_database SET datconnlimit = "+resetLimit+" WHERE datname = '"+dbName+"'"); err != nil {
		t.Fatalf("UPDATE pg_database SET datconnlimit (reset): %v", err)
	}

	if err := c.Stop(cluster.ShutdownFast); err != nil {
		t.Fatalf("stop cluster after reset: %v", err)
	}
	if err := c.Start(); err != nil {
		t.Fatalf("restart cluster after reset: %v", err)
	}

	if got := queryScalar(t, c, "SELECT datconnlimit FROM "+dbRef); got != resetLimit {
		t.Fatalf("post-restart-after-reset datconnlimit = %q, want %s", got, resetLimit)
	}
}
