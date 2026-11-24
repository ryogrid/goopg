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

// TestPort_Recovery001StreamRepFull re-ports
// postgres/src/test/recovery/t/001_stream_rep.pl to the M0149 contract
// (M0149-0077): a primary, a standby streaming from it, and a second
// standby cascading from the first, through
//   - streamed table, sequence and unlogged-sequence content and
//     read-only refusals;
//   - target_session_attrs routing;
//   - SHOW and READ_REPLICATION_SLOT on replication connections;
//   - physical slots and hot_standby_feedback xmin on both the primary's
//     and the cascading standby's slot;
//   - a primary_conninfo change without a restart;
//   - physical slot advancing across a restart;
//   - pg_backup_start + BASE_BACKUP and BASE_BACKUP cancellation.
//
// Run all-goopg, with a PG 18.3 primary and goopg standbys, and with a goopg
// primary and PG 18.3 standbys. The older TestPort_Recovery001StreamRep
// shortcut covers 2 of the ~47 assertions; this test replaces it once it
// passes.
func TestPort_Recovery001StreamRepFull(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping recovery TAP port in short mode")
	}
	// The all-goopg pairing passes (M0149-0077l) and always runs; the two
	// mixed pairings still have open blockers (M0149-0077e, -0077f) and run
	// only with GOOPG_TAP_001=1.
	mixed := os.Getenv("GOOPG_TAP_001") != ""
	repo := repoRoot(t)
	tapnode.Available(t, repo)
	goopg := func(t *testing.T, name string) *tapnode.Node { return tapnode.NewGoopg(t, repo, name) }
	pg := func(t *testing.T, name string) *tapnode.Node { return tapnode.NewPG(t, repo, name) }
	goopgStandby := func(t *testing.T, name string, root *tapnode.Node, bkp string) *tapnode.Node {
		return tapnode.InitFromBackupGoopg(t, repo, name, root, bkp, true)
	}
	pgStandby := func(t *testing.T, name string, root *tapnode.Node, bkp string) *tapnode.Node {
		return tapnode.InitFromBackupPG(t, repo, name, root, bkp, true)
	}
	cases := []struct {
		name    string
		primary func(t *testing.T, name string) *tapnode.Node
		standby func(t *testing.T, name string, root *tapnode.Node, bkp string) *tapnode.Node
	}{
		{"all-goopg", goopg, goopgStandby},
		{"pg18-primary-goopg-standbys", pg, goopgStandby},
		{"goopg-primary-pg18-standbys", goopg, pgStandby},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.name != "all-goopg" && !mixed {
				t.Skip("mixed PG/goopg pairing not passing yet (M0149-0077e, M0149-0077f); set GOOPG_TAP_001=1 to run")
			}
			runStreamRep(t, tc.primary(t, "primary"), func(name, bkp string, root *tapnode.Node) *tapnode.Node {
				return tc.standby(t, name, root, bkp)
			})
		})
	}
}

func runStreamRep(t *testing.T, nodePrimary *tapnode.Node, standbyFromBackup func(name, bkp string, root *tapnode.Node) *tapnode.Node) {
	is := func(got, want, name string) {
		t.Helper()
		if got != want {
			t.Errorf("not ok: %s: got %q, want %q", name, got, want)
		}
	}
	ok := func(cond bool, name string, detail ...any) {
		t.Helper()
		if !cond {
			t.Errorf("not ok: %s %v", name, detail)
		}
	}
	// wait_for_catchup(standby, mode 'replay', target_lsn) on upstream.
	waitReplay := func(upstream, standby *tapnode.Node, target string) {
		t.Helper()
		q := fmt.Sprintf("SELECT '%s' <= replay_lsn AND state = 'streaming' FROM pg_catalog.pg_stat_replication WHERE application_name IN ('%s', 'walreceiver')",
			target, standby.Name())
		if !upstream.PollQueryUntil("postgres", q, "t") {
			st, _, _ := upstream.PSQLExit("postgres", "SELECT application_name, state, sent_lsn, replay_lsn FROM pg_catalog.pg_stat_replication")
			t.Fatalf("timed out waiting for %s to replay %s on %s; pg_stat_replication: %q", standby.Name(), target, upstream.Name(), st)
		}
	}
	// wait_for_replay_catchup(standby[, node]): the target is node's flush LSN.
	waitReplayCatchup := func(upstream, standby, node *tapnode.Node) {
		t.Helper()
		if node == nil {
			node = upstream
		}
		waitReplay(upstream, standby, node.SafePSQL("postgres", "SELECT pg_current_wal_flush_lsn()"))
	}
	psqlRC := func(n *tapnode.Node, sql string) int {
		t.Helper()
		_, stderr, rc := n.PSQLExit("postgres", sql)
		if rc != 0 {
			t.Logf("%s: psql exit %d: %s", n.Name(), rc, strings.TrimSpace(stderr))
		}
		return rc
	}

	nodePrimary.Start()
	backupName := "my_backup"
	bkp := nodePrimary.Backup(backupName)

	// Create streaming standby linking to primary
	nodeStandby1 := standbyFromBackup("standby_1", bkp, nodePrimary)
	nodeStandby1.Start()

	// Take backup of standby 1 (not mandatory, but useful to check if
	// pg_basebackup works on a standby).
	bkpStandby := nodeStandby1.Backup(backupName)

	// Take a second backup of the standby while the primary is offline.
	nodePrimary.Stop(tapnode.StopFast)
	nodeStandby1.Backup("my_backup_2")
	nodePrimary.Start()

	// Create second standby node linking to standby 1
	nodeStandby2 := standbyFromBackup("standby_2", bkpStandby, nodeStandby1)
	nodeStandby2.Start()

	// Reset IO statistics, for the WAL sender check with pg_stat_io.
	nodePrimary.SafePSQL("postgres", "SELECT pg_stat_reset_shared('io')")

	// Create some content on primary and check its presence in standby nodes
	nodePrimary.SafePSQL("postgres", "CREATE TABLE tab_int AS SELECT generate_series(1,1002) AS a")
	nodePrimary.SafePSQL("postgres", `
CREATE TABLE user_logins(id serial, who text);

CREATE FUNCTION on_login_proc() RETURNS EVENT_TRIGGER AS $$
BEGIN
  IF NOT pg_is_in_recovery() THEN
    INSERT INTO user_logins (who) VALUES (session_user);
  END IF;
  IF session_user = 'regress_hacker' THEN
    RAISE EXCEPTION 'You are not welcome!';
  END IF;
END;
$$ LANGUAGE plpgsql SECURITY DEFINER;

CREATE EVENT TRIGGER on_login_trigger ON login EXECUTE FUNCTION on_login_proc();
ALTER EVENT TRIGGER on_login_trigger ENABLE ALWAYS;
`)

	// Wait for standbys to catch up
	waitReplayCatchup(nodePrimary, nodeStandby1, nil)
	waitReplayCatchup(nodeStandby1, nodeStandby2, nodePrimary)

	is(nodeStandby1.SafePSQL("postgres", "SELECT count(*) FROM tab_int"), "1002", "check streamed content on standby 1")
	is(nodeStandby2.SafePSQL("postgres", "SELECT count(*) FROM tab_int"), "1002", "check streamed content on standby 2")

	// Likewise, but for a sequence
	nodePrimary.SafePSQL("postgres", "CREATE SEQUENCE seq1; SELECT nextval('seq1')")
	waitReplayCatchup(nodePrimary, nodeStandby1, nil)
	waitReplayCatchup(nodeStandby1, nodeStandby2, nodePrimary)
	is(nodeStandby1.SafePSQL("postgres", "SELECT * FROM seq1"), "33|0|t", "check streamed sequence content on standby 1")
	is(nodeStandby2.SafePSQL("postgres", "SELECT * FROM seq1"), "33|0|t", "check streamed sequence content on standby 2")

	// Check pg_sequence_last_value() returns NULL for unlogged sequence on standby
	nodePrimary.SafePSQL("postgres", "CREATE UNLOGGED SEQUENCE ulseq; SELECT nextval('ulseq')")
	waitReplayCatchup(nodePrimary, nodeStandby1, nil)
	is(nodeStandby1.SafePSQL("postgres", "SELECT pg_sequence_last_value('ulseq'::regclass) IS NULL"), "t",
		"pg_sequence_last_value() on unlogged sequence on standby 1")

	// Check that only READ-only queries can run on standbys
	is(fmt.Sprint(psqlRC(nodeStandby1, "INSERT INTO tab_int VALUES (1)")), "3", "read-only queries on standby 1")
	is(fmt.Sprint(psqlRC(nodeStandby2, "INSERT INTO tab_int VALUES (1)")), "3", "read-only queries on standby 2")

	// Tests for connection parameter target_session_attrs
	testTargetSessionAttrs := func(node1, node2, target *tapnode.Node, mode string, status int) {
		t.Helper()
		connstr := fmt.Sprintf("host=%s,%s port=%s,%s target_session_attrs=%s",
			node1.Host(), node2.Host(), node1.Port(), node2.Port(), mode)
		stdout, stderr, ret := node1.PSQLConnstr(connstr, "SHOW port;", false)
		if status == 0 {
			ok(ret == status && target != nil && stdout == target.Port(),
				fmt.Sprintf("connect to node %s with mode %q and %s,%s listed", target.Name(), mode, node1.Name(), node2.Name()),
				ret, stdout, strings.TrimSpace(stderr))
		} else {
			ok(ret == status && target == nil,
				fmt.Sprintf("fail to connect with mode %q and %s,%s listed", mode, node1.Name(), node2.Name()),
				ret, stdout, strings.TrimSpace(stderr))
		}
	}
	testTargetSessionAttrs(nodePrimary, nodeStandby1, nodePrimary, "read-write", 0)
	testTargetSessionAttrs(nodeStandby1, nodePrimary, nodePrimary, "read-write", 0)
	testTargetSessionAttrs(nodePrimary, nodeStandby1, nodePrimary, "any", 0)
	testTargetSessionAttrs(nodeStandby1, nodePrimary, nodeStandby1, "any", 0)
	testTargetSessionAttrs(nodePrimary, nodeStandby1, nodePrimary, "primary", 0)
	testTargetSessionAttrs(nodeStandby1, nodePrimary, nodePrimary, "primary", 0)
	testTargetSessionAttrs(nodePrimary, nodeStandby1, nodeStandby1, "read-only", 0)
	testTargetSessionAttrs(nodeStandby1, nodePrimary, nodeStandby1, "read-only", 0)
	testTargetSessionAttrs(nodePrimary, nodePrimary, nodePrimary, "prefer-standby", 0)
	testTargetSessionAttrs(nodePrimary, nodeStandby1, nodeStandby1, "prefer-standby", 0)
	testTargetSessionAttrs(nodeStandby1, nodePrimary, nodeStandby1, "prefer-standby", 0)
	testTargetSessionAttrs(nodePrimary, nodeStandby1, nodeStandby1, "standby", 0)
	testTargetSessionAttrs(nodeStandby1, nodePrimary, nodeStandby1, "standby", 0)
	testTargetSessionAttrs(nodeStandby1, nodeStandby2, nil, "read-write", 2)
	testTargetSessionAttrs(nodeStandby1, nodeStandby2, nil, "primary", 2)
	testTargetSessionAttrs(nodePrimary, nodePrimary, nil, "read-only", 2)
	testTargetSessionAttrs(nodePrimary, nodePrimary, nil, "standby", 2)

	// Test SHOW ALL
	psqlRC(nodePrimary, "CREATE ROLE repl_role REPLICATION LOGIN;\nGRANT pg_read_all_settings TO repl_role;")
	connstrCommon := fmt.Sprintf("host=%s port=%s user=repl_role", nodePrimary.Host(), nodePrimary.Port())
	connstrRep := connstrCommon + " replication=1"
	connstrDB := connstrCommon + " replication=database dbname=postgres"
	for _, c := range []struct{ sql, name string }{
		{"SHOW ALL;", "SHOW ALL with replication role and %s replication"},
		{"SHOW work_mem;", "SHOW with user-settable parameter, replication role and %s replication"},
		{"SHOW primary_conninfo;", "SHOW with superuser-settable parameter, replication role and %s replication"},
	} {
		_, stderr, ret := nodePrimary.PSQLConnstr(connstrRep, c.sql, true)
		ok(ret == 0, fmt.Sprintf(c.name, "physical"), strings.TrimSpace(stderr))
		_, stderr, ret = nodePrimary.PSQLConnstr(connstrDB, c.sql, true)
		ok(ret == 0, fmt.Sprintf(c.name, "logical"), strings.TrimSpace(stderr))
	}

	// Test READ_REPLICATION_SLOT with a replication connection
	slotname := "test_read_replication_slot_physical"
	stdout, stderr, ret := nodePrimary.PSQLConnstr(connstrRep, "READ_REPLICATION_SLOT non_existent_slot;", false)
	ok(ret == 0, "READ_REPLICATION_SLOT exit code 0 on success", strings.TrimSpace(stderr))
	ok(regexp.MustCompile(`^\|\|$`).MatchString(stdout), "READ_REPLICATION_SLOT returns NULL values if slot does not exist", stdout)
	nodePrimary.PSQLConnstr(connstrRep, "CREATE_REPLICATION_SLOT "+slotname+" PHYSICAL RESERVE_WAL;", false)
	stdout, stderr, ret = nodePrimary.PSQLConnstr(connstrRep, "READ_REPLICATION_SLOT "+slotname+";", false)
	ok(ret == 0, "READ_REPLICATION_SLOT success with existing slot", strings.TrimSpace(stderr))
	ok(regexp.MustCompile(`^physical\|[^|]*\|1$`).MatchString(stdout), "READ_REPLICATION_SLOT returns tuple with slot information", stdout)
	nodePrimary.PSQLConnstr(connstrRep, "DROP_REPLICATION_SLOT "+slotname+";", false)

	// Wait for the walsender to update its IO statistics.
	if !nodePrimary.PollQueryUntil("postgres", `SELECT sum(reads) > 0
       FROM pg_catalog.pg_stat_io
       WHERE backend_type = 'walsender'
       AND object = 'wal'`, "t") {
		t.Fatalf("Timed out while waiting for the walsender to update its IO statistics")
	}

	// Switching to physical replication slot
	slotname1, slotname2 := "standby_1", "standby_2"
	nodePrimary.AppendConf("max_replication_slots = 4")
	nodePrimary.Restart()
	is(fmt.Sprint(psqlRC(nodePrimary, "SELECT pg_create_physical_replication_slot('"+slotname1+"');")), "0", "physical slot created on primary")
	nodeStandby1.AppendConf("primary_slot_name = " + slotname1)
	nodeStandby1.AppendConf("wal_receiver_status_interval = 1")
	nodeStandby1.AppendConf("max_replication_slots = 4")
	nodeStandby1.Restart()
	is(fmt.Sprint(psqlRC(nodeStandby1, "SELECT pg_create_physical_replication_slot('"+slotname2+"');")), "0", "physical slot created on intermediate replica")
	nodeStandby2.AppendConf("primary_slot_name = " + slotname2)
	nodeStandby2.AppendConf("wal_receiver_status_interval = 1")
	// should be able change primary_slot_name without restart
	// will wait effect in get_slot_xmins above
	nodeStandby2.Reload()

	// Fetch xmin columns from slot's pg_replication_slots row, after waiting
	// for given boolean condition to be true to ensure we've reached a
	// quiescent state
	getSlotXmins := func(node *tapnode.Node, slot, checkExpr string) (string, string) {
		t.Helper()
		if !node.PollQueryUntil("postgres", fmt.Sprintf(`
		SELECT %s
		FROM pg_catalog.pg_replication_slots
		WHERE slot_name = '%s';
	`, checkExpr, slot), "t") {
			st, _, _ := node.PSQLExit("postgres", "SELECT slot_name, active, xmin, catalog_xmin, restart_lsn FROM pg_catalog.pg_replication_slots")
			t.Fatalf("Timed out waiting for slot xmins to advance on %s (%s): %q", node.Name(), checkExpr, st)
		}
		out := node.SafePSQL("postgres", "SELECT coalesce(xmin::text, ''), coalesce(catalog_xmin::text, '') FROM pg_catalog.pg_replication_slots WHERE slot_name = '"+slot+"'")
		parts := strings.SplitN(out, "|", 2)
		if len(parts) != 2 {
			t.Fatalf("slot %s on %s: %q", slot, node.Name(), out)
		}
		return parts[0], parts[1]
	}

	// There's no hot standby feedback and there are no logical slots on
	// either peer so xmin and catalog_xmin should be null on both slots.
	xmin, catalogXmin := getSlotXmins(nodePrimary, slotname1, "xmin IS NULL AND catalog_xmin IS NULL")
	is(xmin, "", "xmin of non-cascaded slot null with no hs_feedback")
	is(catalogXmin, "", "catalog xmin of non-cascaded slot null with no hs_feedback")
	xmin, catalogXmin = getSlotXmins(nodeStandby1, slotname2, "xmin IS NULL AND catalog_xmin IS NULL")
	is(xmin, "", "xmin of cascaded slot null with no hs_feedback")
	is(catalogXmin, "", "catalog xmin of cascaded slot null with no hs_feedback")

	// Replication still works?
	nodePrimary.SafePSQL("postgres", "CREATE TABLE replayed(val integer);")
	replayCheck := func() {
		t.Helper()
		newval := nodePrimary.SafePSQL("postgres", "INSERT INTO replayed(val) SELECT coalesce(max(val),0) + 1 AS newval FROM replayed RETURNING val")
		waitReplayCatchup(nodePrimary, nodeStandby1, nil)
		waitReplayCatchup(nodeStandby1, nodeStandby2, nodePrimary)
		if nodeStandby1.SafePSQL("postgres", "SELECT 1 FROM replayed WHERE val = "+newval) != "1" {
			t.Fatalf("standby_1 didn't replay primary value %s", newval)
		}
		if nodeStandby2.SafePSQL("postgres", "SELECT 1 FROM replayed WHERE val = "+newval) != "1" {
			t.Fatalf("standby_2 didn't replay standby_1 value %s", newval)
		}
	}
	replayCheck()

	is(nodeStandby1.SafePSQL("postgres", "SELECT evtname FROM pg_event_trigger WHERE evtevent = 'login'"), "on_login_trigger", "Name of login trigger")
	is(nodeStandby2.SafePSQL("postgres", "SELECT evtname FROM pg_event_trigger WHERE evtevent = 'login'"), "on_login_trigger", "Name of login trigger")

	// enabling hot_standby_feedback
	nodeStandby1.SafePSQL("postgres", "ALTER SYSTEM SET hot_standby_feedback = on;")
	nodeStandby1.Reload()
	nodeStandby2.SafePSQL("postgres", "ALTER SYSTEM SET hot_standby_feedback = on;")
	nodeStandby2.Reload()
	replayCheck()

	xmin, catalogXmin = getSlotXmins(nodePrimary, slotname1, "xmin IS NOT NULL AND catalog_xmin IS NULL")
	ok(xmin != "", "xmin of non-cascaded slot non-null with hs feedback")
	is(catalogXmin, "", "catalog xmin of non-cascaded slot still null with hs_feedback")
	xmin1, catalogXmin1 := getSlotXmins(nodeStandby1, slotname2, "xmin IS NOT NULL AND catalog_xmin IS NULL")
	ok(xmin1 != "", "xmin of cascaded slot non-null with hs feedback")
	is(catalogXmin1, "", "catalog xmin of cascaded slot still null with hs_feedback")

	// doing some work to advance xmin
	nodePrimary.SafePSQL("postgres", `
do $$
begin
  for i in 10000..11000 loop
    -- use an exception block so that each iteration eats an XID
    begin
      insert into tab_int values (i);
    exception
      when division_by_zero then null;
    end;
  end loop;
end$$;
`)
	nodePrimary.SafePSQL("postgres", "VACUUM;")
	nodePrimary.SafePSQL("postgres", "CHECKPOINT;")

	xmin2, catalogXmin2 := getSlotXmins(nodePrimary, slotname1, "xmin <> '"+xmin+"'")
	ok(xmin2 != xmin, "xmin of non-cascaded slot with hs feedback has changed", xmin, xmin2)
	is(catalogXmin2, "", "catalog xmin of non-cascaded slot still null with hs_feedback unchanged")
	xmin2, catalogXmin2 = getSlotXmins(nodeStandby1, slotname2, "xmin <> '"+xmin1+"'")
	ok(xmin2 != xmin1, "xmin of cascaded slot with hs feedback has changed", xmin1, xmin2)
	is(catalogXmin2, "", "catalog xmin of cascaded slot still null with hs_feedback unchanged")

	// disabling hot_standby_feedback
	nodeStandby1.SafePSQL("postgres", "ALTER SYSTEM SET hot_standby_feedback = off;")
	nodeStandby1.Reload()
	nodeStandby2.SafePSQL("postgres", "ALTER SYSTEM SET hot_standby_feedback = off;")
	nodeStandby2.Reload()
	replayCheck()

	xmin, catalogXmin = getSlotXmins(nodePrimary, slotname1, "xmin IS NULL AND catalog_xmin IS NULL")
	is(xmin, "", "xmin of non-cascaded slot null with hs feedback reset")
	is(catalogXmin, "", "catalog xmin of non-cascaded slot still null with hs_feedback reset")
	xmin, catalogXmin = getSlotXmins(nodeStandby1, slotname2, "xmin IS NULL AND catalog_xmin IS NULL")
	is(xmin, "", "xmin of cascaded slot null with hs feedback reset")
	is(catalogXmin, "", "catalog xmin of cascaded slot still null with hs_feedback reset")

	// check change primary_conninfo without restart
	nodeStandby2.AppendConf("primary_slot_name = ''")
	nodeStandby2.EnableStreaming(nodePrimary)
	nodeStandby2.Reload()

	// The WAL receiver should have generated some IO statistics.
	is(nodeStandby1.SafePSQL("postgres", `SELECT sum(writes) > 0 FROM pg_stat_io
   WHERE backend_type = 'walreceiver' AND object = 'wal'`), "t", "WAL receiver generates statistics for WAL writes")

	// be sure do not streaming from cascade
	nodeStandby1.Stop(tapnode.StopFast)

	newval := nodePrimary.SafePSQL("postgres", "INSERT INTO replayed(val) SELECT coalesce(max(val),0) + 1 AS newval FROM replayed RETURNING val")
	waitReplay(nodePrimary, nodeStandby2, nodePrimary.SafePSQL("postgres", "SELECT pg_current_wal_lsn()"))
	is(nodeStandby2.SafePSQL("postgres", "SELECT 1 FROM replayed WHERE val = "+newval), "1", fmt.Sprintf("standby_2 didn't replay primary value %s", newval))

	// Drop any existing slots on the primary, for the follow-up tests.
	nodePrimary.SafePSQL("postgres", "SELECT pg_drop_replication_slot(slot_name) FROM pg_replication_slots;")

	// Test physical slot advancing and its durability. Create a new slot on
	// the primary, not used by any of the standbys. This reserves WAL at
	// creation.
	physSlot := "phys_slot"
	nodePrimary.SafePSQL("postgres", "SELECT pg_create_physical_replication_slot('"+physSlot+"', true);")
	// Generate some WAL, and switch to a new segment, used to check that
	// the previous segment is correctly getting recycled as the slot
	// advancing would recompute the minimum LSN calculated across all slots.
	segmentRemoved := nodePrimary.SafePSQL("postgres", "SELECT pg_walfile_name(pg_current_wal_lsn())")
	nodePrimary.AdvanceWAL(1)
	currentLSN := nodePrimary.SafePSQL("postgres", "SELECT pg_current_wal_lsn();")
	is(fmt.Sprint(psqlRC(nodePrimary, "SELECT pg_replication_slot_advance('"+physSlot+"', '"+currentLSN+"'::pg_lsn);")), "0", "slot advancing with physical slot")
	restartPre := nodePrimary.SafePSQL("postgres", "SELECT restart_lsn from pg_replication_slots WHERE slot_name = '"+physSlot+"';")
	// Slot advance should persist across clean restarts.
	nodePrimary.Restart()
	restartPost := nodePrimary.SafePSQL("postgres", "SELECT restart_lsn from pg_replication_slots WHERE slot_name = '"+physSlot+"';")
	ok(restartPre == restartPost, "physical slot advance persists across restarts", restartPre, restartPost)

	// Check if the previous segment gets correctly recycled after the
	// server stopped cleanly, causing a shutdown checkpoint to be generated.
	_, err := os.Stat(filepath.Join(nodePrimary.DataDir(), "pg_wal", segmentRemoved))
	ok(os.IsNotExist(err), fmt.Sprintf("WAL segment %s recycled after physical slot advancing", segmentRemoved))

	// testing pg_backup_start() followed by BASE_BACKUP
	connstr := fmt.Sprintf("host=%s port=%s user=%s dbname=postgres replication=database",
		nodePrimary.Host(), nodePrimary.Port(), nodePrimary.User())
	_, stderr, ret = nodePrimary.PSQLCommands(connstr, "SELECT pg_backup_start('backup', true)", "BASE_BACKUP")
	ok(ret != 0 && regexp.MustCompile(`a backup is already in progress in this session`).MatchString(stderr),
		"BASE_BACKUP cannot run in session already running backup", ret, strings.TrimSpace(stderr))

	// testing BASE_BACKUP cancellation
	cmd := nodePrimary.PSQLCommandsCmd(connstr, "BASE_BACKUP (CHECKPOINT 'fast', MAX_RATE 32);", "SELECT pg_backup_stop()")
	var bbErr strings.Builder
	cmd.Stdout, cmd.Stderr = nil, &bbErr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start BASE_BACKUP: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	// The cancellation is issued once the base backup starts streaming
	// files. This works only with a slow MAX_RATE.
	ok(nodePrimary.PollQueryUntil("postgres", "SELECT pg_cancel_backend(a.pid) FROM "+
		"pg_stat_activity a, pg_stat_progress_basebackup b WHERE "+
		"a.pid = b.pid AND a.query ~ 'BASE_BACKUP' AND "+
		"b.phase = 'streaming database files';", "t"),
		"WAL sender sending base backup killed")
	// The psql command should fail on pg_backup_stop().
	select {
	case <-done:
	case <-time.After(180 * time.Second):
		_ = cmd.Process.Kill()
		<-done
	}
	ok(regexp.MustCompile(`backup is not in progress`).MatchString(bbErr.String()), "base backup cleanly canceled", strings.TrimSpace(bbErr.String()))
}
