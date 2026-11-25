package testport

import (
	"testing"

	"github.com/goopg/goopg/internal/testutil/tapnode"
)

// TestPort_Recovery028PitrTimelinesFull ports postgres/src/test/recovery/t/028_pitr_timelines.pl
// to the M0149 contract: a standby with archive_mode = always is promoted to
// timeline 2 (archiving into the primary's archive, whose archive_command it
// inherits with the base backup); a PITR node restored from the primary's
// backup to restore point 'rp' promotes onto timeline 3 and archives there
// too; a second PITR node with no target then recovers along the newest
// timeline and sees the row written on timeline 3. It runs in three
// pairings: all-goopg, a PG 18.3 primary with goopg standby/PITR nodes, and
// a goopg primary with PG 18.3 standby/PITR nodes (M0149-0027).
func TestPort_Recovery028PitrTimelinesFull(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping recovery TAP port in short mode")
	}
	repo := repoRoot(t)
	tapnode.Available(t, repo)
	goopg := func(t *testing.T, name string) *tapnode.Node { return tapnode.NewGoopg(t, repo, name) }
	pg := func(t *testing.T, name string) *tapnode.Node { return tapnode.NewPG(t, repo, name) }
	goopgFromBackup := func(t *testing.T, name string, root *tapnode.Node, bkp string, streaming bool) *tapnode.Node {
		return tapnode.InitFromBackupGoopg(t, repo, name, root, bkp, streaming)
	}
	pgFromBackup := func(t *testing.T, name string, root *tapnode.Node, bkp string, streaming bool) *tapnode.Node {
		return tapnode.InitFromBackupPG(t, repo, name, root, bkp, streaming)
	}
	cases := []struct {
		name    string
		primary func(t *testing.T, name string) *tapnode.Node
		other   func(t *testing.T, name string, root *tapnode.Node, bkp string, streaming bool) *tapnode.Node
	}{
		{"all-goopg", goopg, goopgFromBackup},
		{"pg18-primary-goopg-nodes", pg, goopgFromBackup},
		{"goopg-primary-pg18-nodes", goopg, pgFromBackup},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runPitrTimelines(t, tc.primary(t, "primary"), func(name, bkp string, root *tapnode.Node, streaming bool) *tapnode.Node {
				return tc.other(t, name, root, bkp, streaming)
			})
		})
	}
}

func runPitrTimelines(t *testing.T, nodePrimary *tapnode.Node, fromBackup func(name, bkp string, root *tapnode.Node, streaming bool) *tapnode.Node) {
	is := func(got, want, name string) {
		t.Helper()
		if got != want {
			t.Errorf("not ok: %s: got %q, want %q", name, got, want)
		}
	}
	psql := func(n *tapnode.Node, sql string) {
		t.Helper()
		if _, err := n.PSQL("postgres", sql); err != nil {
			t.Logf("%s: psql: %v", n.Name(), err)
		}
	}

	// Initialize and start primary node with WAL archiving
	nodePrimary.EnableArchiving()
	for _, l := range []string{"wal_level = replica", "max_wal_senders = 10"} {
		nodePrimary.AppendConf(l)
	}
	nodePrimary.Start()

	// Take a backup.
	backupName := "my_backup"
	bkp := nodePrimary.Backup(backupName)

	// Workload with some transactions, and the target restore point.
	psql(nodePrimary, `
CREATE TABLE foo(i int);
INSERT INTO foo VALUES(1);
SELECT pg_create_restore_point('rp');
INSERT INTO foo VALUES(2);
`)

	// Contents of the WAL archive at this point:
	//
	// 000000010000000000000001
	// 000000010000000000000002
	// 000000010000000000000002.00000028.backup
	//
	// The operations on the test table and the restore point went into WAL
	// segment 3, but it hasn't been archived yet.

	// Start a standby node, and wait for it to catch up. has_archiving only
	// creates the archive directory: the standby keeps the primary's
	// archive_command from the base backup.
	nodeStandby := fromBackup("standby", bkp, nodePrimary, true)
	nodeStandby.AppendConf("archive_mode = always")
	nodeStandby.Start()
	waitForReplayOf(t, nodePrimary, nodeStandby)

	// Check that it's really caught up.
	result := nodeStandby.SafePSQL("postgres", "SELECT max(i) FROM foo;")
	is(result, "2", "check table contents after archive recovery")

	// Kill the old primary, before it archives the most recent WAL segment
	// that contains all the INSERTs.
	nodePrimary.Stop(tapnode.StopImmediate)

	// Promote the standby, and switch WAL so that it archives a WAL segment
	// that contains all the INSERTs, on a new timeline.
	nodeStandby.Promote()

	// Find next WAL segment to be archived.
	nodeStandby.SafePSQL("postgres", "SELECT pg_walfile_name(pg_current_wal_lsn());")

	// Make WAL segment eligible for archival
	nodeStandby.SafePSQL("postgres", "SELECT pg_switch_wal()")

	// We don't need the standby anymore, request shutdown. The server will
	// finish archiving all the WAL on timeline 2 before it exits.
	nodeStandby.Stop(tapnode.StopFast)

	// Contents of the WAL archive now:
	//
	// 000000010000000000000001
	// 000000010000000000000002
	// 000000010000000000000002.00000028.backup
	// 000000010000000000000003.partial
	// 000000020000000000000003
	// 00000002.history
	//
	// The operations on the test table and the restore point are in segment
	// 3. They are part of timeline 1, but were not archived by the primary
	// yet. However, they were copied into the beginning of segment
	// 000000020000000000000003, before the timeline switching record. (They
	// are also present in the 000000010000000000000003.partial file, but
	// .partial files are not used automatically.)

	// Now test PITR to the recovery target. It should find the WAL in
	// segment 000000020000000000000003, but not follow the timeline switch
	// to timeline 2.
	nodePitr := fromBackup("node_pitr", bkp, nodePrimary, false)
	nodePitr.EnableRestoring(nodePrimary, false) // standby => 0
	nodePitr.AppendConf("recovery_target_name = 'rp'")
	nodePitr.AppendConf("recovery_target_action = 'promote'")
	nodePitr.Start()

	// Wait until recovery finishes.
	if !nodePitr.PollQueryUntil("postgres", "SELECT pg_is_in_recovery() = 'f';", "t") {
		t.Fatalf("Timed out while waiting for PITR promotion")
	}

	// Check that we see the data we expect.
	result = nodePitr.SafePSQL("postgres", "SELECT max(i) FROM foo;")
	is(result, "1", "check table contents after point-in-time recovery")

	// Insert a row so that we can check later that we successfully recover
	// back to this timeline.
	nodePitr.SafePSQL("postgres", "INSERT INTO foo VALUES(3);")

	// Wait for the archiver to be running. The startup process might have
	// yet to exit, in which case the postmaster has not started the archiver.
	// If we stop() without an archiver, the archive will be incomplete.
	if !nodePitr.PollQueryUntil("postgres", "SELECT true FROM pg_stat_activity WHERE backend_type = 'archiver';", "t") {
		t.Fatalf("Timed out while waiting for archiver to start")
	}

	// Stop the node. This archives the last segment.
	nodePitr.Stop(tapnode.StopFast)

	// Test archive recovery on the timeline created by the PITR. This
	// replays the end-of-recovery record that switches from timeline 2 to 3.
	nodePitr2 := fromBackup("node_pitr2", bkp, nodePrimary, false)
	nodePitr2.EnableRestoring(nodePrimary, false) // standby => 0
	nodePitr2.AppendConf("recovery_target_action = 'promote'")
	nodePitr2.Start()

	// Wait until recovery finishes.
	if !nodePitr2.PollQueryUntil("postgres", "SELECT pg_is_in_recovery() = 'f';", "t") {
		t.Fatalf("Timed out while waiting for PITR promotion")
	}

	// Verify that we can see the row inserted after the PITR.
	result = nodePitr2.SafePSQL("postgres", "SELECT max(i) FROM foo;")
	is(result, "3", "check table contents after point-in-time recovery")
}

// waitForReplayOf is Cluster.pm's wait_for_catchup(standby): replay mode,
// the upstream's current write LSN, the standby matched by application_name.
func waitForReplayOf(t *testing.T, upstream, standby *tapnode.Node) {
	t.Helper()
	target := upstream.SafePSQL("postgres", "SELECT pg_current_wal_lsn()")
	q := "SELECT '" + target + "' <= replay_lsn AND state = 'streaming' FROM pg_catalog.pg_stat_replication WHERE application_name IN ('" + standby.Name() + "', 'walreceiver')"
	if !upstream.PollQueryUntil("postgres", q, "t") {
		st, _, _ := upstream.PSQLExit("postgres", "SELECT application_name, state, sent_lsn, replay_lsn FROM pg_catalog.pg_stat_replication")
		t.Fatalf("timed out waiting for %s to replay %s on %s; pg_stat_replication: %q", standby.Name(), target, upstream.Name(), st)
	}
}
