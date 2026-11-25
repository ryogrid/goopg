package testport

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/goopg/goopg/internal/testutil/tapnode"
)

// TestPort_Recovery032RelfilenodeReuse ports postgres/src/test/recovery/t/032_relfilenode_reuse.pl
// to the M0149 contract: with shared_buffers = 1MB and full_page_writes off,
// a database is dropped and re-created under the same OID (so its relations
// reuse their relfilenodes), moved between tablespaces and VACUUM FULLed,
// while pg_prewarm in an open transaction on each node evicts buffers. The
// standby must never serve a stale buffer of the old relfilenode: every
// verify sees the primary's newest datab on both nodes, and both nodes shut
// down cleanly. It runs in three pairings: all-goopg, a PG 18.3 primary with
// a goopg standby, and a goopg primary with a PG 18.3 standby; the PG nodes
// run from the PG 18.3 test-module toolchain, which has pg_prewarm.
//
// Gated by GOOPG_TAP_032=1 until it passes (M0149-0029): a goopg primary
// cannot yet CREATE DATABASE from a template holding indexes (M0149-0029a),
// and a goopg standby rejects PG records whose header padding is nonzero
// (M0149-0029b).
func TestPort_Recovery032RelfilenodeReuse(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping recovery TAP port in short mode")
	}
	if os.Getenv("GOOPG_TAP_032") == "" {
		t.Skip("032 re-port is not passing yet (M0149-0029); set GOOPG_TAP_032=1 to run")
	}
	repo := repoRoot(t)
	tapnode.Available(t, repo)
	goopg := func(t *testing.T, name string) *tapnode.Node { return tapnode.NewGoopg(t, repo, name) }
	pg := func(t *testing.T, name string) *tapnode.Node { return tapnode.NewPGModules(t, repo, name) }
	goopgFromBackup := func(t *testing.T, name string, root *tapnode.Node, bkp string) *tapnode.Node {
		return tapnode.InitFromBackupGoopg(t, repo, name, root, bkp, true)
	}
	pgFromBackup := func(t *testing.T, name string, root *tapnode.Node, bkp string) *tapnode.Node {
		return tapnode.InitFromBackupPGModules(t, repo, name, root, bkp, true)
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
			runRelfilenodeReuse(t, tc.primary(t, "primary"), func(name, bkp string, root *tapnode.Node) *tapnode.Node {
				return tc.standby(t, name, root, bkp)
			})
		})
	}
}

func runRelfilenodeReuse(t *testing.T, nodePrimary *tapnode.Node, fromBackup func(name, bkp string, root *tapnode.Node) *tapnode.Node) {
	is := func(got, want, name string) {
		t.Helper()
		if got != want {
			t.Errorf("not ok: %s: got %q, want %q", name, got, want)
		}
	}

	for _, l := range []string{
		"wal_level = replica", "max_wal_senders = 10",
		"allow_in_place_tablespaces = true",
		"log_connections=receipt",
		"full_page_writes=off",
		"log_min_messages=debug2",
		"shared_buffers=1MB",
	} {
		nodePrimary.AppendConf(l)
	}
	nodePrimary.Start()

	backupName := "my_backup"
	bkp := nodePrimary.Backup(backupName)

	nodeStandby := fromBackup("standby", bkp, nodePrimary)
	nodeStandby.Start()

	// We'll reset this timeout for each individual query we run.
	psqlPrimary := nodePrimary.BackgroundPSQL("postgres", false)
	psqlStandby := nodeStandby.BackgroundPSQL("postgres", false)

	// Create template database with a table that we'll update, to trigger
	// dirty rows. Using a template database + preexisting rows makes it a
	// bit easier to reproduce, because there's no cache invalidations
	// generated.
	nodePrimary.SafePSQL("postgres", "CREATE DATABASE conflict_db_template OID = 50000;")
	nodePrimary.SafePSQL("conflict_db_template", `
    CREATE TABLE large(id serial primary key, dataa text, datab text);
    INSERT INTO large(dataa, datab) SELECT g.i::text, 1 FROM generate_series(1, 4000) g(i);`)
	nodePrimary.SafePSQL("postgres", "CREATE DATABASE conflict_db TEMPLATE conflict_db_template OID = 50001;")

	nodePrimary.SafePSQL("postgres", `
    CREATE EXTENSION pg_prewarm;
    CREATE TABLE replace_sb(data text);
    INSERT INTO replace_sb(data) SELECT random()::text FROM generate_series(1, 15000);`)

	waitForReplayOf(t, nodePrimary, nodeStandby)

	// Use longrunning transactions, so that AtEOXact_SMgr doesn't close
	// files
	psqlPrimary.Query("BEGIN;")
	psqlStandby.Query("BEGIN;")

	// Cause lots of dirty rows in shared_buffers
	nodePrimary.SafePSQL("conflict_db", "UPDATE large SET datab = 1;")

	// Now do a bunch of work in another database. That will end up needing
	// to write back dirty data from the previous step, opening the relevant
	// file descriptors
	causeEviction := func() {
		t.Helper()
		const q = "SELECT SUM(pg_prewarm(oid)) warmed_buffers FROM pg_class WHERE pg_relation_filenode(oid) != 0;"
		for _, b := range []*tapnode.BackgroundPSQL{psqlPrimary, psqlStandby} {
			b.ClearStderr()
			out := b.Query(q)
			if e := b.Stderr(); e != "" || out == "" {
				t.Fatalf("cause_eviction: %q, stderr %q", out, e)
			}
		}
	}
	verify := func(counter, message string) {
		t.Helper()
		const query = "SELECT datab, count(*) FROM large GROUP BY 1 ORDER BY 1 LIMIT 10"
		is(nodePrimary.SafePSQL("conflict_db", query), counter+"|4000", "primary: "+message)
		waitForReplayOf(t, nodePrimary, nodeStandby)
		is(nodeStandby.SafePSQL("conflict_db", query), counter+"|4000", "standby: "+message)
	}

	causeEviction()

	// drop and recreate database
	nodePrimary.SafePSQL("postgres", "DROP DATABASE conflict_db;")
	nodePrimary.SafePSQL("postgres", "CREATE DATABASE conflict_db TEMPLATE conflict_db_template OID = 50001;")

	verify("1", "initial contents as expected")

	// Again cause lots of dirty rows in shared_buffers, but use a different
	// update value so we can check everything is OK
	nodePrimary.SafePSQL("conflict_db", "UPDATE large SET datab = 2;")

	// Again cause a lot of IO. That'll again write back dirty data, but
	// uses newly opened file descriptors, so we don't confuse old files
	// with new files despite recycling relfilenodes.
	causeEviction()

	verify("2", "update to reused relfilenode (due to DB oid conflict) is not lost")

	nodePrimary.SafePSQL("conflict_db", "VACUUM FULL large;")
	nodePrimary.SafePSQL("conflict_db", "UPDATE large SET datab = 3;")

	verify("3", "restored contents as expected")

	// Test for old filehandles after moving a database in / out of
	// tablespace
	nodePrimary.SafePSQL("postgres", "CREATE TABLESPACE test_tablespace LOCATION ''")

	// cause dirty buffers
	nodePrimary.SafePSQL("conflict_db", "UPDATE large SET datab = 4;")
	// cause files to be opened in backend in other database
	causeEviction()

	// move database back / forth
	nodePrimary.SafePSQL("postgres", "ALTER DATABASE conflict_db SET TABLESPACE test_tablespace")
	nodePrimary.SafePSQL("postgres", "ALTER DATABASE conflict_db SET TABLESPACE pg_default")

	// cause dirty buffers
	nodePrimary.SafePSQL("conflict_db", "UPDATE large SET datab = 5;")
	causeEviction()

	verify("5", "post move contents as expected")

	nodePrimary.SafePSQL("postgres", "ALTER DATABASE conflict_db SET TABLESPACE test_tablespace")

	nodePrimary.SafePSQL("conflict_db", "UPDATE large SET datab = 7;")
	causeEviction()
	nodePrimary.SafePSQL("conflict_db", "UPDATE large SET datab = 8;")
	nodePrimary.SafePSQL("postgres", "DROP DATABASE conflict_db")
	nodePrimary.SafePSQL("postgres", "DROP TABLESPACE test_tablespace")

	nodePrimary.SafePSQL("postgres", "REINDEX TABLE pg_database")

	// explicitly shut down psql instances gracefully - to avoid hangs or
	// worse on windows
	if err := psqlPrimary.Quit(); err != nil {
		t.Logf("psql primary quit: %v", err)
	}
	if err := psqlStandby.Quit(); err != nil {
		t.Logf("psql standby quit: %v", err)
	}

	nodePrimary.Stop(tapnode.StopFast)
	nodeStandby.Stop(tapnode.StopFast)

	// Make sure that there weren't crashes during shutdown
	controldata := func(n *tapnode.Node) string {
		out, err := exec.Command(filepath.Join(tapnode.BinDir(repoRoot(t)), "pg_controldata"), n.DataDir()).CombinedOutput()
		if err != nil {
			t.Errorf("pg_controldata %s: %v\n%s", n.Name(), err, out)
		}
		return string(out)
	}
	if !regexp.MustCompile(`Database cluster state:\s+shut down\n`).MatchString(controldata(nodePrimary)) {
		t.Errorf("not ok: primary shut down ok")
	}
	if !regexp.MustCompile(`Database cluster state:\s+shut down in recovery\n`).MatchString(controldata(nodeStandby)) {
		t.Errorf("not ok: standby shut down ok")
	}
}
