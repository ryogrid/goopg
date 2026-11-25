package testport

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/goopg/goopg/internal/testutil/tapnode"
)

// TestPort_Recovery004TimelineSwitchFull ports postgres/src/test/recovery/t/004_timeline_switch.pl
// to the M0149 contract: a cascading standby follows a newly promoted
// standby onto its new timeline without its walreceiver restarting, and a
// standby streaming from a primary that was restarted as a standby and
// promoted (with WAL archiving on) follows it onto the newer timeline. It
// runs in three pairings: all-goopg, a PG 18.3 primary with goopg standbys,
// and a goopg primary with PG 18.3 standbys (M0149-0025).
func TestPort_Recovery004TimelineSwitchFull(t *testing.T) {
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
		{"pg18-primary-goopg-standbys", pg, goopgFromBackup},
		{"goopg-primary-pg18-standbys", goopg, pgFromBackup},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runTimelineSwitch(t, func(name string) *tapnode.Node { return tc.primary(t, name) },
				func(name, bkp string, root *tapnode.Node) *tapnode.Node { return tc.standby(t, name, root, bkp) })
		})
	}
}

func runTimelineSwitch(t *testing.T, newPrimary func(name string) *tapnode.Node, fromBackup func(name, bkp string, root *tapnode.Node) *tapnode.Node) {
	is := func(got, want, name string) {
		t.Helper()
		if got != want {
			t.Errorf("not ok: %s: got %q, want %q", name, got, want)
		}
	}
	ok := func(cond bool, name string) {
		t.Helper()
		if !cond {
			t.Errorf("not ok: %s", name)
		}
	}
	// wait_for_catchup(standby): replay mode, the target is the upstream's
	// current write LSN, the standby matched by application_name (its
	// cluster_name).
	waitForCatchupOf := func(upstream, standby *tapnode.Node) {
		t.Helper()
		target := upstream.SafePSQL("postgres", "SELECT pg_current_wal_lsn()")
		q := fmt.Sprintf("SELECT '%s' <= replay_lsn AND state = 'streaming' FROM pg_catalog.pg_stat_replication WHERE application_name IN ('%s', 'walreceiver')",
			target, standby.Name())
		if !upstream.PollQueryUntil("postgres", q, "t") {
			st, _, _ := upstream.PSQLExit("postgres", "SELECT application_name, state, sent_lsn, replay_lsn FROM pg_catalog.pg_stat_replication")
			t.Fatalf("timed out waiting for %s to replay %s on %s; pg_stat_replication: %q", standby.Name(), target, upstream.Name(), st)
		}
	}

	// Ensure that a cascading standby is able to follow a newly-promoted
	// standby on a new timeline.

	// Initialize primary node
	nodePrimary := newPrimary("primary")
	for _, l := range []string{"wal_level = replica", "max_wal_senders = 10"} {
		nodePrimary.AppendConf(l)
	}
	nodePrimary.Start()

	// Take backup
	backupName := "my_backup"
	bkp := nodePrimary.Backup(backupName)

	// Create two standbys linking to it
	nodeStandby1 := fromBackup("standby_1", bkp, nodePrimary)
	nodeStandby1.Start()
	nodeStandby2 := fromBackup("standby_2", bkp, nodePrimary)
	nodeStandby2.Start()

	// Create some content on primary
	nodePrimary.SafePSQL("postgres", "CREATE TABLE tab_int AS SELECT generate_series(1,1000) AS a")

	// Wait until standby has replayed enough data on standby 1
	waitForCatchupOf(nodePrimary, nodeStandby1)

	// Stop and remove primary
	nodePrimary.Stop(tapnode.StopImmediate)

	// promote standby 1 using "pg_promote", switching it to a new timeline
	psqlOut, _ := nodeStandby1.PSQL("postgres", "SELECT pg_promote(wait_seconds => 300)")
	is(psqlOut, "t", "promotion of standby with pg_promote")

	// Switch standby 2 to replay from standby 1
	nodeStandby2.EnableStreaming(nodeStandby1)

	// Rotate logfile before restarting, for the log checks done below.
	logOffset := nodeStandby2.LogSize()
	nodeStandby2.Restart()

	// Wait for walreceiver to reconnect after the restart. We want to
	// verify that after reconnection, the walreceiver stays alive during the
	// timeline switch.
	nodeStandby2.PollQueryUntil("postgres", "SELECT EXISTS(SELECT 1 FROM pg_stat_wal_receiver)", "t")
	wrPidBeforeSwitch := nodeStandby2.SafePSQL("postgres", "SELECT pid FROM pg_stat_wal_receiver")

	// Insert some data in standby 1 and check its presence in standby 2 to
	// ensure that the timeline switch has been done.
	nodeStandby1.SafePSQL("postgres", "INSERT INTO tab_int VALUES (generate_series(1001,2000))")
	waitForCatchupOf(nodeStandby1, nodeStandby2)

	result := nodeStandby2.SafePSQL("postgres", "SELECT count(*) FROM tab_int")
	is(result, "2000", "check content of standby 2")

	// Check the logs, WAL receiver should not have been stopped while
	// transitioning to its new timeline.
	ok(!regexp.MustCompile(`FATAL: .* terminating walreceiver process due to administrator command`).MatchString(nodeStandby2.LogFrom(logOffset)),
		"WAL receiver should not be stopped across timeline jumps")

	// Verify that the walreceiver process stayed alive across the timeline
	// switch, check its PID.
	wrPidAfterSwitch := nodeStandby2.SafePSQL("postgres", "SELECT pid FROM pg_stat_wal_receiver")
	is(wrPidBeforeSwitch, wrPidAfterSwitch, "WAL receiver PID matches across timeline jumps")

	// Ensure that a standby is able to follow a primary on a newer timeline
	// when WAL archiving is enabled.

	// Initialize primary node
	nodePrimary2 := newPrimary("primary_2")
	nodePrimary2.EnableArchiving()
	for _, l := range []string{"wal_level = replica", "max_wal_senders = 10", "wal_keep_size = 512MB"} {
		nodePrimary2.AppendConf(l)
	}
	nodePrimary2.Start()

	// Take backup
	bkp2 := nodePrimary2.Backup(backupName)

	// Create standby node
	nodeStandby3 := fromBackup("standby_3", bkp2, nodePrimary2)

	// Restart primary node in standby mode and promote it, switching it to a
	// new timeline.
	if err := os.WriteFile(filepath.Join(nodePrimary2.DataDir(), "standby.signal"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	nodePrimary2.Restart()
	nodePrimary2.Promote()

	// Start standby node, create some content on primary and check its
	// presence in standby, to ensure that the timeline switch has been done.
	nodeStandby3.Start()
	nodePrimary2.SafePSQL("postgres", "CREATE TABLE tab_int AS SELECT 1 AS a")
	waitForCatchupOf(nodePrimary2, nodeStandby3)

	result2 := nodeStandby3.SafePSQL("postgres", "SELECT count(*) FROM tab_int")
	is(result2, "1", "check content of standby 3")
}
