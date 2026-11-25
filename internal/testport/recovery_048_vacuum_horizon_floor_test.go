package testport

import (
	"os"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/testutil/tapnode"
)

// TestPort_Recovery048VacuumHorizonFloor ports postgres/src/test/recovery/t/048_vacuum_horizon_floor.pl
// to the M0149 contract: a VACUUM (FREEZE) on the primary blocks on a buffer
// pin held by an open cursor while the standby (hot_standby_feedback on) is
// disconnected; the standby reconnects mid-VACUUM, sending an older xmin, and
// the VACUUM must still remove every tuple dead before its OldestXmin over
// several index-vacuum passes (maintenance_work_mem = 64) without looping or
// erroring. It runs in three pairings: all-goopg, a PG 18.3 primary with a
// goopg standby, and a goopg primary with a PG 18.3 standby.
//
// Gated by GOOPG_TAP_048=1 until it passes (M0149-0033).
func TestPort_Recovery048VacuumHorizonFloor(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping recovery TAP port in short mode")
	}
	if os.Getenv("GOOPG_TAP_048") == "" {
		t.Skip("048 re-port is not passing yet (M0149-0033); set GOOPG_TAP_048=1 to run")
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
			runVacuumHorizonFloor(t, tc.primary(t, "primary"), func(name, bkp string, root *tapnode.Node) *tapnode.Node {
				return tc.standby(t, name, root, bkp)
			})
		})
	}
}

func runVacuumHorizonFloor(t *testing.T, nodePrimary *tapnode.Node, fromBackup func(name, bkp string, root *tapnode.Node) *tapnode.Node) {
	poll := func(n *tapnode.Node, db, q, want, what string) {
		t.Helper()
		if !n.PollQueryUntil(db, q, want) {
			t.Fatalf("timed out: %s (%s)", what, q)
		}
	}
	// Set up nodes
	for _, l := range []string{
		"wal_level = replica", "max_wal_senders = 10",
		"hot_standby_feedback = on", "autovacuum = off", "log_min_messages = INFO",
		"maintenance_work_mem = 64", "io_combine_limit = 1",
	} {
		nodePrimary.AppendConf(l)
	}
	nodePrimary.Start()

	bkp := nodePrimary.Backup("my_backup")
	nodeReplica := fromBackup("standby", bkp, nodePrimary)
	nodeReplica.Start()

	testDB := "test_db"
	nodePrimary.SafePSQL("postgres", "CREATE DATABASE "+testDB)

	table1 := "vac_horizon_floor_table"

	// Long-running Primary Session A
	psqlPrimaryA := nodePrimary.BackgroundPSQL(testDB, true)
	// Long-running Primary Session B
	psqlPrimaryB := nodePrimary.BackgroundPSQL(testDB, true)

	// Because vacuum's first pass, pruning, is where we use the
	// GlobalVisState to check tuple visibility, GlobalVisState->maybe_needed
	// must move backwards during pruning before checking the visibility for
	// a tuple which would have been considered HEAPTUPLE_DEAD prior to
	// maybe_needed moving backwards but HEAPTUPLE_RECENTLY_DEAD compared to
	// the new, older value of maybe_needed.
	nrows := "2000"
	nodePrimary.SafePSQL(testDB, `
	CREATE TABLE `+table1+`(col1 int)
		WITH (autovacuum_enabled=false, fillfactor=10);
	INSERT INTO `+table1+` VALUES(7);
	INSERT INTO `+table1+` SELECT generate_series(1, `+nrows+`) % 3;
	CREATE INDEX on `+table1+`(col1);
	DELETE FROM `+table1+` WHERE col1 = 0;
	INSERT INTO `+table1+` VALUES(7);
`)

	// We will later move the primary forward while the standby is
	// disconnected. For now, however, there is no reason not to wait for the
	// standby to catch up.
	waitForReplayOf(t, nodePrimary, nodeReplica)

	// Test that the WAL receiver is up and running.
	poll(nodeReplica, testDB, "SELECT EXISTS (SELECT * FROM pg_stat_wal_receiver);", "t", "walreceiver up")

	// Set primary_conninfo to something invalid on the replica and reload
	// the config. Once the config is reloaded, the startup process will
	// force the WAL receiver to restart and it will be unable to reconnect
	// because of the invalid connection information.
	nodeReplica.SafePSQL(testDB, `
		ALTER SYSTEM SET primary_conninfo = '';
		SELECT pg_reload_conf();
	`)

	// Wait until the WAL receiver has shut down and been unable to start up
	// again.
	poll(nodeReplica, testDB, "SELECT EXISTS (SELECT * FROM pg_stat_wal_receiver);", "f", "walreceiver down")

	// Now insert and update a tuple which will be visible to the vacuum on
	// the primary but which will have xmax newer than the oldest xmin on the
	// standby that was recently disconnected.
	res := psqlPrimaryA.QuerySafe(`
		INSERT INTO ` + table1 + ` VALUES (99);
		UPDATE ` + table1 + ` SET col1 = 100 WHERE col1 = 99;
		SELECT 'after_update';
`)
	if !strings.Contains(res, "after_update") {
		t.Errorf("not ok: UPDATE occurred on primary session A: %q", res)
	}

	// Open a cursor on the primary whose pin will keep VACUUM from getting a
	// cleanup lock on the first page of the relation. We want VACUUM to be
	// able to start, calculate initial values for OldestXmin and
	// GlobalVisState and then be unable to proceed with pruning our dead
	// tuples. This will allow us to reconnect the standby and push the
	// horizon back before we start actual pruning and vacuuming.
	primaryCursor1 := "vac_horizon_floor_cursor1"

	// The first value inserted into the table was a 7, so FETCH FORWARD
	// should return a 7. That's how we know the cursor has a pin.
	// Disable index scans so the cursor pins heap pages and not index pages.
	res = psqlPrimaryB.QuerySafe(`
	BEGIN;
	SET enable_bitmapscan = off;
	SET enable_indexscan = off;
	SET enable_indexonlyscan = off;
	DECLARE ` + primaryCursor1 + ` CURSOR FOR SELECT * FROM ` + table1 + ` WHERE col1 = 7;
	FETCH ` + primaryCursor1 + `;
`)
	if strings.TrimSpace(lastLine(res)) != "7" {
		t.Errorf("not ok: Cursor query returned %q. Expected value 7.", res)
	}

	// Get the PID of the session which will run the VACUUM FREEZE so that we
	// can use it to filter pg_stat_activity later.
	vacuumPID := psqlPrimaryA.QuerySafe("SELECT pg_backend_pid();")

	// Now start a VACUUM FREEZE on the primary. It will call
	// vacuum_get_cutoffs() and establish values of OldestXmin and
	// GlobalVisState which are newer than all of our dead tuples. Then it
	// will be unable to get a cleanup lock to start pruning, so it will
	// hang.
	//
	// We use VACUUM FREEZE because it will wait for a cleanup lock instead
	// of skipping the page pinned by the cursor. Note that works because the
	// target tuple's xmax is visible to all, so lazy_scan_noprune() cannot
	// use the page.
	psqlPrimaryA.Send(`
		SET maintenance_io_concurrency = 0;
		VACUUM (VERBOSE, FREEZE, PARALLEL 0) ` + table1 + `;
		\echo VACUUM
`)

	// Make sure that the VACUUM has already called vacuum_get_cutoffs() and
	// is just waiting on the lock to start vacuuming. We don't want the
	// standby to re-establish a connection to the primary and push the
	// horizon back until we've saved initial values in GlobalVisState and
	// calculated OldestXmin.
	poll(nodePrimary, testDB, `
	SELECT count(*) >= 1 FROM pg_stat_activity
		WHERE pid = `+vacuumPID+`
		AND wait_event = 'BufferPin';`, "t", "VACUUM waits on BufferPin")

	// Ensure the WAL receiver is still not active on the replica.
	poll(nodeReplica, testDB, "SELECT EXISTS (SELECT * FROM pg_stat_wal_receiver);", "f", "walreceiver still down")

	// Allow the WAL receiver connection to re-establish.
	nodeReplica.SafePSQL(testDB, `
		ALTER SYSTEM SET primary_conninfo = '`+nodePrimary.ConnStr()+`';
		SELECT pg_reload_conf();
`)

	// Ensure the new WAL receiver has connected.
	poll(nodeReplica, testDB, "SELECT EXISTS (SELECT * FROM pg_stat_wal_receiver);", "t", "walreceiver reconnected")

	// Once the WAL sender is shown on the primary, the replica should have
	// connected with the primary and pushed the horizon backward. Primary
	// Session A won't see that until the VACUUM FREEZE proceeds and does its
	// first round of index vacuuming.
	poll(nodePrimary, testDB, "SELECT EXISTS (SELECT * FROM pg_stat_replication);", "t", "walsender shown")

	// Move the cursor forward to the next 7. We inserted the 7 much later,
	// so advancing the cursor should allow vacuum to proceed vacuuming most
	// pages of the relation. Because we set maintanence_work_mem
	// sufficiently low, we expect that a round of index vacuuming has
	// happened and that the vacuum is now waiting for the cursor to release
	// its pin on the last page of the relation.
	res = psqlPrimaryB.QuerySafe("FETCH " + primaryCursor1)
	if strings.TrimSpace(lastLine(res)) != "7" {
		t.Errorf("not ok: Cursor query returned %q from second fetch. Expected value 7.", res)
	}

	// Prevent the test from incorrectly passing by confirming that we did
	// indeed do a pass of index vacuuming.
	poll(nodePrimary, testDB, `
	SELECT index_vacuum_count > 0
	FROM pg_stat_progress_vacuum
	WHERE datname='`+testDB+`' AND relid::regclass = '`+table1+`'::regclass;`, "t", "an index vacuum pass happened")

	// Commit the transaction with the open cursor so that the VACUUM can
	// finish.
	psqlPrimaryB.QuerySafe("COMMIT;")

	// VACUUM proceeds with pruning and does a visibility check on each
	// tuple. In older versions of Postgres, pruning found our final dead
	// tuple non-removable (HEAPTUPLE_RECENTLY_DEAD) since its xmax is after
	// the new value of maybe_needed. Then heap_prepare_freeze_tuple() would
	// decide the tuple xmax should be frozen because it precedes
	// OldestXmin. Vacuum would then error out in
	// heap_pre_freeze_checks() with "cannot freeze committed xmax". This was
	// fixed by changing pruning to find all
	// HEAPTUPLE_RECENTLY_DEAD tuples with xmaxes preceding OldestXmin
	// HEAPTUPLE_DEAD and removing them.

	// With the fix, VACUUM should finish successfully, incrementing the
	// table vacuum_count.
	poll(nodePrimary, testDB, `
	SELECT vacuum_count > 0
	FROM pg_stat_all_tables WHERE relname = '`+table1+`';`, "t", "vacuum_count increments")

	nodePrimary.SafePSQL(testDB, "INSERT INTO "+table1+" VALUES (1);")

	// Do a basic sanity check that the replica is still alive and kicking.
	waitForReplayOf(t, nodePrimary, nodeReplica)

	if err := psqlPrimaryA.Quit(); err != nil {
		t.Logf("session A quit: %v", err)
	}
	if err := psqlPrimaryB.Quit(); err != nil {
		t.Logf("session B quit: %v", err)
	}
	nodeReplica.Stop(tapnode.StopFast)
	nodePrimary.Stop(tapnode.StopFast)
}

func lastLine(s string) string {
	s = strings.TrimRight(s, "\n")
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		return s[i+1:]
	}
	return s
}
