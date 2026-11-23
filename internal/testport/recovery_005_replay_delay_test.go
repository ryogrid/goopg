package testport

import (
	"testing"
	"time"

	"github.com/goopg/goopg/internal/testutil/tapnode"
)

// TestPort_Recovery005ReplayDelay ports
// postgres/src/test/recovery/t/005_replay_delay.pl (M0149-0013): a standby
// with recovery_min_apply_delay applies the primary's WAL only after the
// delay, and a second standby's replay can be paused (no WAL replayed while
// paused, even as more arrives), resumed, and promoted while paused. Run
// all-goopg and with PG 18.3 on either side.
func TestPort_Recovery005ReplayDelay(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping recovery TAP port in short mode")
	}
	repo := repoRoot(t)
	tapnode.Available(t, repo)
	type mk func(t *testing.T, name string, root *tapnode.Node, bkp string) *tapnode.Node
	goopgStandby := func(t *testing.T, name string, root *tapnode.Node, bkp string) *tapnode.Node {
		return tapnode.InitFromBackupGoopg(t, repo, name, root, bkp, true)
	}
	pgStandby := func(t *testing.T, name string, root *tapnode.Node, bkp string) *tapnode.Node {
		return tapnode.InitFromBackupPG(t, repo, name, root, bkp, true)
	}
	cases := []struct {
		name    string
		primary func(t *testing.T) *tapnode.Node
		standby mk
	}{
		{"all-goopg", func(t *testing.T) *tapnode.Node { return tapnode.NewGoopg(t, repo, "primary") }, goopgStandby},
		{"pg18-primary-goopg-standby", func(t *testing.T) *tapnode.Node { return tapnode.NewPG(t, repo, "primary") }, goopgStandby},
		{"goopg-primary-pg18-standby", func(t *testing.T) *tapnode.Node { return tapnode.NewGoopg(t, repo, "primary") }, pgStandby},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runReplayDelay(t, tc.primary(t), func(name, bkp string, root *tapnode.Node) *tapnode.Node {
				return tc.standby(t, name, root, bkp)
			})
		})
	}
}

func runReplayDelay(t *testing.T, primary *tapnode.Node, standbyFromBackup func(name, bkp string, root *tapnode.Node) *tapnode.Node) {
	is := func(got, want, name string) {
		t.Helper()
		if got != want {
			t.Errorf("not ok: %s: got %q, want %q", name, got, want)
		}
	}
	poll := func(n *tapnode.Node, q, msg string) {
		t.Helper()
		if !n.PollQueryUntil("postgres", q, "t") {
			t.Fatal(msg)
		}
	}
	// Initialize primary node
	primary.Start()

	// And some content
	primary.SafePSQL("postgres", "CREATE TABLE tab_int AS SELECT generate_series(1, 10) AS a")

	// Take backup
	bkp := primary.Backup("my_backup")

	// Create streaming standby from backup
	const delay = 3
	standby := standbyFromBackup("standby", bkp, primary)
	standby.AppendConf("recovery_min_apply_delay = '3s'")
	standby.Start()

	// Make new content on primary and check its presence in standby depending
	// on the delay applied above. Before doing the insertion, get the
	// current timestamp that will be used as a comparison base. Even on slow
	// machines, this allows to have a predictable behavior when comparing the
	// delay between data insertion moment on primary and replay time on standby.
	primaryInsertTime := time.Now()
	primary.SafePSQL("postgres", "INSERT INTO tab_int VALUES (generate_series(11, 20))")

	// Now wait for replay to complete on standby. We're done waiting when the
	// standby has replayed up to the previously saved primary LSN.
	untilLSN := primary.SafePSQL("postgres", "SELECT pg_current_wal_lsn()")
	poll(standby, "SELECT (pg_last_wal_replay_lsn() - '"+untilLSN+"'::pg_lsn) >= 0", "standby never caught up")

	// This test is successful if and only if the LSN has been applied with at
	// least the configured apply delay.
	if time.Since(primaryInsertTime) < delay*time.Second {
		t.Errorf("not ok: standby applies WAL only after replication delay (%v)", time.Since(primaryInsertTime))
	}

	// Check that recovery can be paused or resumed expectedly.
	standby2 := standbyFromBackup("standby2", bkp, primary)
	standby2.Start()

	// Recovery is not yet paused.
	is(standby2.SafePSQL("postgres", "SELECT pg_get_wal_replay_pause_state()"), "not paused",
		"pg_get_wal_replay_pause_state() reports not paused")

	// Request to pause recovery and wait until it's actually paused.
	standby2.SafePSQL("postgres", "SELECT pg_wal_replay_pause()")
	primary.SafePSQL("postgres", "INSERT INTO tab_int VALUES (generate_series(21,30))")
	poll(standby2, "SELECT pg_get_wal_replay_pause_state() = 'paused'", "Timed out while waiting for recovery to be paused")

	// Even if new WAL records are streamed from the primary,
	// recovery in the paused state doesn't replay them.
	receiveLSN := standby2.SafePSQL("postgres", "SELECT pg_last_wal_receive_lsn()")
	replayLSN := standby2.SafePSQL("postgres", "SELECT pg_last_wal_replay_lsn()")
	primary.SafePSQL("postgres", "INSERT INTO tab_int VALUES (generate_series(31,40))")
	poll(standby2, "SELECT '"+receiveLSN+"'::pg_lsn < pg_last_wal_receive_lsn()", "Timed out while waiting for new WAL to be streamed")
	is(standby2.SafePSQL("postgres", "SELECT pg_last_wal_replay_lsn()"), replayLSN, "no WAL is replayed in the paused state")

	// Request to resume recovery and wait until it's actually resumed.
	standby2.SafePSQL("postgres", "SELECT pg_wal_replay_resume()")
	poll(standby2, "SELECT pg_get_wal_replay_pause_state() = 'not paused' AND pg_last_wal_replay_lsn() > '"+replayLSN+"'::pg_lsn",
		"Timed out while waiting for recovery to be resumed")

	// Check that the paused state ends and promotion continues if a promotion
	// is triggered while recovery is paused.
	standby2.SafePSQL("postgres", "SELECT pg_wal_replay_pause()")
	primary.SafePSQL("postgres", "INSERT INTO tab_int VALUES (generate_series(41,50))")
	poll(standby2, "SELECT pg_get_wal_replay_pause_state() = 'paused'", "Timed out while waiting for recovery to be paused")

	standby2.Promote()
	poll(standby2, "SELECT NOT pg_is_in_recovery()", "Timed out while waiting for promotion to finish")

	standby2.Stop(tapnode.StopFast)
	standby.Stop(tapnode.StopFast)
	primary.Stop(tapnode.StopFast)
}
