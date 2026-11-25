package testport

import (
	"testing"

	"github.com/goopg/goopg/internal/testutil/tapnode"
)

// TestPort_Recovery021RowVisibility ports
// postgres/src/test/recovery/t/021_row_visibility.pl (M0149-0012): a hot
// standby's snapshots see rows only once the primary's transaction — plain or
// prepared — has committed, through a standby session that stays open across
// the primary's changes. Run all-goopg and with PG 18.3 on either side.
func TestPort_Recovery021RowVisibility(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping recovery TAP port in short mode")
	}
	repo := repoRoot(t)
	tapnode.Available(t, repo)
	t.Run("all-goopg", func(t *testing.T) {
		primary := tapnode.NewGoopg(t, repo, "primary")
		runRowVisibility(t, primary, func(bkp string) *tapnode.Node {
			return tapnode.InitFromBackupGoopg(t, repo, "standby", primary, bkp, true)
		})
	})
	t.Run("pg18-primary-goopg-standby", func(t *testing.T) {
		primary := tapnode.NewPG(t, repo, "primary")
		runRowVisibility(t, primary, func(bkp string) *tapnode.Node {
			return tapnode.InitFromBackupGoopg(t, repo, "standby", primary, bkp, true)
		})
	})
	t.Run("goopg-primary-pg18-standby", func(t *testing.T) {
		primary := tapnode.NewGoopg(t, repo, "primary")
		runRowVisibility(t, primary, func(bkp string) *tapnode.Node {
			return tapnode.InitFromBackupPG(t, repo, "standby", primary, bkp, true)
		})
	})
}

func runRowVisibility(t *testing.T, primary *tapnode.Node, standbyFromBackup func(bkp string) *tapnode.Node) {
	ok := func(cond bool, name string) {
		t.Helper()
		if !cond {
			t.Errorf("not ok: %s", name)
		}
	}
	// Initialize primary node
	primary.AppendConf("max_prepared_transactions=10")
	primary.Start()

	// Initialize with empty test table
	primary.SafePSQL("postgres", "CREATE TABLE public.test_visibility (data text not null)")

	// Take backup
	bkp := primary.Backup("my_backup")

	// Create streaming standby from backup
	standby := standbyFromBackup(bkp)
	standby.AppendConf("max_prepared_transactions=10")
	standby.Start()

	// One psql to primary and standby each, for all queries. That allows
	// to check uncommitted changes being replicated and such.
	psqlPrimary := primary.PumpedPSQL("postgres")
	psqlStandby := standby.PumpedPSQL("postgres")

	// test: check that the data is not visible on the standby yet
	ok(psqlStandby.SendQueryAndWait("SELECT * FROM test_visibility ORDER BY data;", `^\(0 rows\)$`), "data not visible")

	// Insert data on the primary and wait for the standby to catch up
	primary.SafePSQL("postgres", "INSERT INTO test_visibility VALUES ('first insert')")
	waitForCatchup(t, primary, standby)

	ok(psqlStandby.SendQueryAndWait("SELECT * FROM test_visibility ORDER BY data;", `first insert.*\n\(1 row\)`), "insert visible")

	// UPDATE, but don't commit, to check visibility of a not committed change.
	ok(psqlPrimary.SendQueryAndWait(`
BEGIN;
UPDATE test_visibility SET data = 'first update' RETURNING data;
`, `^UPDATE 1$`), "UPDATE")

	primary.SafePSQL("postgres", "SELECT txid_current();") // ensure WAL flush
	waitForCatchup(t, primary, standby)

	ok(psqlStandby.SendQueryAndWait("SELECT * FROM test_visibility ORDER BY data;", `first insert.*\n\(1 row\)`), "uncommitted update invisible")

	// commit UPDATE, and check visibility
	ok(psqlPrimary.SendQueryAndWait("COMMIT;", `^COMMIT$`), "COMMIT")
	waitForCatchup(t, primary, standby)

	ok(psqlStandby.SendQueryAndWait("SELECT * FROM test_visibility ORDER BY data;", `first update\n\(1 row\)$`), "committed update visible")

	// Check that a prepared transaction's changes are only visible after
	// commit, not after preparation.
	ok(psqlPrimary.SendQueryAndWait(`
DELETE from test_visibility; -- delete old data, so we start with clean slate
BEGIN;
INSERT INTO test_visibility VALUES('inserted in prepared will_commit');
PREPARE TRANSACTION 'will_commit';`, `^PREPARE TRANSACTION$`), "prepared will_commit")

	ok(psqlPrimary.SendQueryAndWait(`
BEGIN;
INSERT INTO test_visibility VALUES('inserted in prepared will_abort');
PREPARE TRANSACTION 'will_abort';
`, `^PREPARE TRANSACTION$`), "prepared will_abort")

	waitForCatchup(t, primary, standby)

	ok(psqlStandby.SendQueryAndWait("SELECT * FROM test_visibility ORDER BY data;", `^\(0 rows\)$`), "uncommitted prepared invisible")

	// For some variation, finish prepared xacts via separate connections
	primary.SafePSQL("postgres", "COMMIT PREPARED 'will_commit';")
	primary.SafePSQL("postgres", "ROLLBACK PREPARED 'will_abort';")
	waitForCatchup(t, primary, standby)

	ok(psqlStandby.SendQueryAndWait("SELECT * FROM test_visibility ORDER BY data;", `will_commit.*\n\(1 row\)$`), "finished prepared visible")

	// explicitly shut down psql instances gracefully - to avoid hangs or
	// worse on windows
	psqlPrimary.Quit()
	psqlStandby.Quit()

	primary.Stop(tapnode.StopFast)
	standby.Stop(tapnode.StopFast)
}
