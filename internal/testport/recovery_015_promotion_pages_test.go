package testport

import (
	"testing"

	"github.com/goopg/goopg/internal/testutil/tapnode"
)

// TestPort_Recovery015PromotionPages ports
// postgres/src/test/recovery/t/015_promotion_pages.pl (M0149-0010): WAL
// written by a freshly promoted standby before its first checkpoint must be
// replayable after a crash. The standby replays a TRUNCATE and a VACUUM that
// set visibility bits with wal_log_hints = off; after promotion it
// truncates, vacuums and inserts, crashes, and crash recovery must restore
// exactly the 1000 rows inserted after promotion.
func TestPort_Recovery015PromotionPages(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping recovery TAP port in short mode")
	}
	repo := repoRoot(t)
	tapnode.Available(t, repo)

	// Initialize primary node
	alpha := tapnode.NewGoopg(t, repo, "alpha")
	alpha.AppendConf("wal_log_hints = off")
	alpha.Start()

	// setup/start a standby
	bkp := alpha.Backup("bkp")
	bravo := tapnode.InitFromBackupGoopg(t, repo, "bravo", alpha, bkp, true)
	bravo.AppendConf("checkpoint_timeout=1h")
	bravo.Start()

	// Dummy table for the upcoming tests.
	alpha.SafePSQL("postgres", "create table test1 (a int)")
	alpha.SafePSQL("postgres", "insert into test1 select generate_series(1, 10000)")

	// take a checkpoint
	alpha.SafePSQL("postgres", "checkpoint")

	// The following vacuum will set visibility map bits and create
	// problematic WAL records.
	alpha.SafePSQL("postgres", "vacuum verbose test1")
	// Wait for last record to have been replayed on the standby.
	waitForCatchup(t, alpha, bravo)

	// Now force a checkpoint on the standby. This seems unnecessary but for
	// "some" reason, the previous checkpoint on the primary does not
	// reflect on the standby and without an explicit checkpoint, it may
	// start redo recovery from a much older point, which includes even
	// create table above. We don't want to replay that far back.
	bravo.SafePSQL("postgres", "checkpoint")

	// Now just use a dummy table and run some operations to move minRecoveryPoint
	// beyond the previous vacuum.
	alpha.SafePSQL("postgres", "create table test2 (a int, b bytea)")
	alpha.SafePSQL("postgres", "insert into test2 select generate_series(1,10000), sha256(random()::text::bytea)")
	alpha.SafePSQL("postgres", "truncate test2")

	// Wait again for all records to be replayed.
	waitForCatchup(t, alpha, bravo)

	// Do the promotion, which reinitializes minRecoveryPoint in the control
	// file so as WAL is replayed up to the end.
	bravo.Promote()

	// Truncate the table on the promoted standby, vacuum and extend it
	// again to create new page references. The first post-recovery checkpoint
	// has not happened yet.
	bravo.SafePSQL("postgres", "truncate test1")
	bravo.SafePSQL("postgres", "vacuum verbose test1")
	bravo.SafePSQL("postgres", "insert into test1 select generate_series(1,1000)")

	// Now crash-stop the promoted standby and restart. This makes sure that
	// replay does not see invalid page references because of an invalid
	// minimum consistent recovery point.
	bravo.Stop(tapnode.StopImmediate)
	bravo.Start()

	// Check state of the table after full crash recovery. All its data should
	// be here.
	if got := bravo.SafePSQL("postgres", "SELECT count(*) FROM test1"); got != "1000" {
		t.Errorf("not ok: Check that table state is correct: got %q, want %q", got, "1000")
	}
	bravo.Stop(tapnode.StopFast)
	alpha.Stop(tapnode.StopFast)
}

// waitForCatchup is Cluster.pm's wait_for_catchup(standby) in its default
// 'replay' mode: it polls the primary's pg_stat_replication until the
// standby's replay_lsn reaches the primary's current WAL write position.
// The standby is matched by application_name, which a walreceiver sets to
// its cluster_name or "walreceiver" — the test has one standby, so any row
// will do.
func waitForCatchup(t *testing.T, primary, standby *tapnode.Node) {
	t.Helper()
	target := primary.SafePSQL("postgres", "SELECT pg_current_wal_lsn()")
	q := "SELECT '" + target + "'::pg_lsn <= replay_lsn AND state = 'streaming' FROM pg_catalog.pg_stat_replication"
	if !primary.PollQueryUntil("postgres", q, "t") {
		t.Fatalf("timed out waiting for %s to catch up to %s", standby.Name(), target)
	}
}
