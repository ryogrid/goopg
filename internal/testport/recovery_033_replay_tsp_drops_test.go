package testport

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/testutil/tapnode"
)

// TestPort_Recovery033ReplayTspDrops ports postgres/src/test/recovery/t/033_replay_tsp_drops.pl
// to the M0149 contract: for both CREATE DATABASE strategies a standby
// replays creating databases in in-place tablespaces, moving one between
// tablespaces, creating one from a template and then dropping databases and
// tablespaces, and must restart from an immediate stop afterwards (the
// restart replays the drops again). A standby whose tablespace directory was
// removed must log "creating missing directory: pg_tblspc/…" when replaying
// a FILE_COPY CREATE DATABASE into it. It runs in three pairings:
// all-goopg, a PG 18.3 primary with a goopg standby, and a goopg primary
// with a PG 18.3 standby.
//
// Gated by GOOPG_TAP_033=1 until it passes (M0149-0030).
func TestPort_Recovery033ReplayTspDrops(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping recovery TAP port in short mode")
	}
	if os.Getenv("GOOPG_TAP_033") == "" {
		t.Skip("033 re-port is not passing yet (M0149-0030); set GOOPG_TAP_033=1 to run")
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
			newPrimary := func(name string) *tapnode.Node { return tc.primary(t, name) }
			fromBackup := func(name, bkp string, root *tapnode.Node) *tapnode.Node { return tc.standby(t, name, root, bkp) }
			for _, strategy := range []string{"FILE_COPY", "WAL_LOG"} {
				t.Run(strategy, func(t *testing.T) { testTablespace(t, strategy, newPrimary, fromBackup) })
			}
			t.Run("missing-directory", func(t *testing.T) { testMissingTablespaceDir(t, newPrimary, fromBackup) })
		})
	}
}

// waitForWriteOf is wait_for_catchup(standby, 'write'): the upstream's
// current write LSN reached the standby's write_lsn.
func waitForWriteOf(t *testing.T, upstream, standby *tapnode.Node) {
	t.Helper()
	target := upstream.SafePSQL("postgres", "SELECT pg_current_wal_lsn()")
	q := "SELECT '" + target + "' <= write_lsn AND state = 'streaming' FROM pg_catalog.pg_stat_replication WHERE application_name IN ('" + standby.Name() + "', 'walreceiver')"
	if !upstream.PollQueryUntil("postgres", q, "t") {
		st, _, _ := upstream.PSQLExit("postgres", "SELECT application_name, state, sent_lsn, write_lsn FROM pg_catalog.pg_stat_replication")
		t.Fatalf("timed out waiting for %s to write %s on %s; pg_stat_replication: %q", standby.Name(), target, upstream.Name(), st)
	}
}

func testTablespace(t *testing.T, strategy string, newPrimary func(string) *tapnode.Node, fromBackup func(name, bkp string, root *tapnode.Node) *tapnode.Node) {
	nodePrimary := newPrimary("primary1_" + strategy)
	nodePrimary.AppendConf("wal_level = replica")
	nodePrimary.AppendConf("max_wal_senders = 10")
	nodePrimary.AppendConf("max_replication_slots = 10")
	nodePrimary.Start()
	if out, err := nodePrimary.PSQL("postgres", `
			SET allow_in_place_tablespaces=on;
			CREATE TABLESPACE dropme_ts1 LOCATION '';
			CREATE TABLESPACE dropme_ts2 LOCATION '';
			CREATE TABLESPACE source_ts  LOCATION '';
			CREATE TABLESPACE target_ts  LOCATION '';
			CREATE DATABASE template_db IS_TEMPLATE = true;
			SELECT pg_create_physical_replication_slot('slot', true);
		`); err != nil {
		t.Logf("primary setup: %v\n%s", err, out)
	}
	backupName := "my_backup"
	bkp := nodePrimary.Backup(backupName)

	nodeStandby := fromBackup("standby2_"+strategy, bkp, nodePrimary)
	nodeStandby.AppendConf("allow_in_place_tablespaces = on")
	nodeStandby.AppendConf("primary_slot_name = slot")
	nodeStandby.Start()

	// Make sure the connection is made
	waitForWriteOf(t, nodePrimary, nodeStandby)

	// Do immediate shutdown just after a sequence of CREATE DATABASE / DROP
	// DATABASE / DROP TABLESPACE. This causes CREATE DATABASE WAL records
	// to be applied to already-removed directories.
	query := strings.ReplaceAll(`
		CREATE DATABASE dropme_db1 WITH TABLESPACE dropme_ts1 STRATEGY=<STRATEGY>;
		CREATE TABLE t (a int) TABLESPACE dropme_ts2;
		CREATE DATABASE dropme_db2 WITH TABLESPACE dropme_ts2 STRATEGY=<STRATEGY>;
		CREATE DATABASE moveme_db TABLESPACE source_ts STRATEGY=<STRATEGY>;
		ALTER DATABASE moveme_db SET TABLESPACE target_ts;
		CREATE DATABASE newdb TEMPLATE template_db STRATEGY=<STRATEGY>;
		ALTER DATABASE template_db IS_TEMPLATE = false;
		DROP DATABASE dropme_db1;
		DROP TABLE t;
		DROP DATABASE dropme_db2; DROP TABLESPACE dropme_ts2;
		DROP TABLESPACE source_ts;
		DROP DATABASE template_db;
	`, "<STRATEGY>", strategy)
	nodePrimary.SafePSQL("postgres", query)
	waitForWriteOf(t, nodePrimary, nodeStandby)

	// show "create missing directory" log message
	nodeStandby.SafePSQL("postgres", "ALTER SYSTEM SET log_min_messages TO debug1;")
	nodeStandby.Stop(tapnode.StopImmediate)
	// Should restart ok.
	nodeStandby.Start() // is(start(fail_ok => 1), 1, "standby node started for $strategy")
	nodeStandby.Stop(tapnode.StopImmediate)
}

func testMissingTablespaceDir(t *testing.T, newPrimary func(string) *tapnode.Node, fromBackup func(name, bkp string, root *tapnode.Node) *tapnode.Node) {
	// Ensure that a missing tablespace directory during create database
	// replay immediately causes panic if the standby has already reached
	// consistent state (archive recovery is in progress). This is
	// effective only for CREATE DATABASE WITH STRATEGY=FILE_COPY.
	nodePrimary := newPrimary("primary2")
	nodePrimary.AppendConf("wal_level = replica")
	nodePrimary.AppendConf("max_wal_senders = 10")
	nodePrimary.Start()

	// Create tablespace
	nodePrimary.SafePSQL("postgres", `
		SET allow_in_place_tablespaces=on;
		CREATE TABLESPACE ts1 LOCATION ''`)
	nodePrimary.SafePSQL("postgres", "CREATE DATABASE db1 WITH TABLESPACE ts1 STRATEGY=FILE_COPY")

	// Take backup
	backupName := "my_backup"
	bkp := nodePrimary.Backup(backupName)
	nodeStandby := fromBackup("standby3", bkp, nodePrimary)
	nodeStandby.AppendConf("allow_in_place_tablespaces = on")
	nodeStandby.Start()

	// Make sure standby reached consistency and starts accepting connections
	nodeStandby.PollQueryUntil("postgres", "SELECT 1", "1")

	// Remove standby tablespace directory so it will be missing when replay
	// resumes.
	tspoid := nodeStandby.SafePSQL("postgres", "SELECT oid FROM pg_tablespace WHERE spcname = 'ts1';")
	tspdir := filepath.Join(nodeStandby.DataDir(), "pg_tblspc", tspoid)
	if err := os.RemoveAll(tspdir); err != nil {
		t.Fatal(err)
	}

	logstart := nodeStandby.LogSize()

	// Create a database in the tablespace and a table in default tablespace
	nodePrimary.SafePSQL("postgres", `
		CREATE TABLE should_not_replay_insertion(a int);
		CREATE DATABASE db2 WITH TABLESPACE ts1 STRATEGY=FILE_COPY;
		INSERT INTO should_not_replay_insertion VALUES (1);
	`)

	// Standby should fail and should not silently skip replaying the wal
	// In this test, PANIC turns into WARNING by allow_in_place_tablespaces.
	// Check the log messages instead of confirming standby failure.
	re := regexp.MustCompile(`WARNING: ( [A-Z0-9]+:)? creating missing directory: pg_tblspc/`)
	deadline := time.Now().Add(180 * time.Second)
	for time.Now().Before(deadline) {
		if re.MatchString(nodeStandby.LogFrom(logstart)) {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Errorf("not ok: invalid directory creation is detected\n%s", fmt.Sprint(fileTail(nodeStandby.LogPath(), 30)))
}
