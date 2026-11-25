package testport

import (
	"testing"

	"github.com/goopg/goopg/internal/testutil/tapnode"
)

// TestPort_Recovery023PitrPreparedXactFull ports postgres/src/test/recovery/t/023_pitr_prepared_xact.pl
// to the M0149 contract: archive recovery (init_from_backup standby => 0,
// has_restoring) to a restore point taken just after PREPARE TRANSACTION,
// with recovery_target_action = 'promote', leaves the prepared transaction
// on the promoted node, where COMMIT PREPARED makes exactly its row visible;
// a checkpoint on the new timeline is found again by crash recovery after an
// immediate stop. It runs in three pairings: all-goopg, a PG 18.3 primary
// with a goopg PITR node, and a goopg primary with a PG 18.3 PITR node
// (M0149-0024).
func TestPort_Recovery023PitrPreparedXactFull(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping recovery TAP port in short mode")
	}
	repo := repoRoot(t)
	tapnode.Available(t, repo)
	goopg := func(t *testing.T, name string) *tapnode.Node { return tapnode.NewGoopg(t, repo, name) }
	pg := func(t *testing.T, name string) *tapnode.Node { return tapnode.NewPG(t, repo, name) }
	goopgFromBackup := func(t *testing.T, name string, root *tapnode.Node, bkp string) *tapnode.Node {
		return tapnode.InitFromBackupGoopg(t, repo, name, root, bkp, false)
	}
	pgFromBackup := func(t *testing.T, name string, root *tapnode.Node, bkp string) *tapnode.Node {
		return tapnode.InitFromBackupPG(t, repo, name, root, bkp, false)
	}
	cases := []struct {
		name    string
		primary func(t *testing.T, name string) *tapnode.Node
		pitr    func(t *testing.T, name string, root *tapnode.Node, bkp string) *tapnode.Node
	}{
		{"all-goopg", goopg, goopgFromBackup},
		{"pg18-primary-goopg-pitr", pg, goopgFromBackup},
		{"goopg-primary-pg18-pitr", goopg, pgFromBackup},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runPitrPreparedXact(t, tc.primary(t, "primary"), func(name, bkp string, root *tapnode.Node) *tapnode.Node {
				return tc.pitr(t, name, root, bkp)
			})
		})
	}
}

func runPitrPreparedXact(t *testing.T, nodePrimary *tapnode.Node, fromBackup func(name, bkp string, root *tapnode.Node) *tapnode.Node) {
	is := func(got, want, name string) {
		t.Helper()
		if got != want {
			t.Errorf("not ok: %s: got %q, want %q", name, got, want)
		}
	}

	// Initialize and start primary node with WAL archiving
	nodePrimary.EnableArchiving()
	for _, l := range []string{"wal_level = replica", "max_wal_senders = 10", "max_prepared_transactions = 10"} {
		nodePrimary.AppendConf(l)
	}
	nodePrimary.Start()

	// Take backup
	bkp := nodePrimary.Backup("my_backup")

	// Initialize node for PITR targeting a very specific restore point, just
	// after a PREPARE TRANSACTION is issued so as we finish with a promoted
	// node where this 2PC transaction needs an explicit COMMIT PREPARED.
	nodePitr := fromBackup("node_pitr", bkp, nodePrimary)
	nodePitr.EnableRestoring(nodePrimary, false) // standby => 0
	nodePitr.AppendConf("recovery_target_name = 'rp'")
	nodePitr.AppendConf("recovery_target_action = 'promote'")

	// Workload with a prepared transaction and the target restore point.
	_, _ = nodePrimary.PSQL("postgres", `
CREATE TABLE foo(i int);
BEGIN;
INSERT INTO foo VALUES(1);
PREPARE TRANSACTION 'fooinsert';
SELECT pg_create_restore_point('rp');
INSERT INTO foo VALUES(2);
`)

	// Find next WAL segment to be archived
	walfileToBeArchived := nodePrimary.SafePSQL("postgres", "SELECT pg_walfile_name(pg_current_wal_lsn());")

	// Make WAL segment eligible for archival
	nodePrimary.SafePSQL("postgres", "SELECT pg_switch_wal()")

	// Wait until the WAL segment has been archived.
	archiveWaitQuery := "SELECT '" + walfileToBeArchived + "' <= last_archived_wal FROM pg_stat_archiver;"
	if !nodePrimary.PollQueryUntil("postgres", archiveWaitQuery, "t") {
		t.Fatalf("Timed out while waiting for WAL segment to be archived")
	}

	// Now start the PITR node.
	nodePitr.Start()

	// Wait until the PITR node exits recovery.
	if !nodePitr.PollQueryUntil("postgres", "SELECT pg_is_in_recovery() = 'f';", "t") {
		t.Fatalf("Timed out while waiting for PITR promotion")
	}

	// Commit the prepared transaction in the latest timeline and check its
	// result. There should only be one row in the table, coming from the
	// prepared transaction. The row from the INSERT after the restore point
	// should not show up, since our recovery target was older than the
	// second INSERT done.
	if _, err := nodePitr.PSQL("postgres", "COMMIT PREPARED 'fooinsert';"); err != nil {
		t.Logf("COMMIT PREPARED: %v", err)
	}
	result := nodePitr.SafePSQL("postgres", "SELECT * FROM foo;")
	is(result, "1", "check table contents after COMMIT PREPARED")

	// Insert more data and do a checkpoint. These should be generated on the
	// timeline chosen after the PITR promotion.
	_, _ = nodePitr.PSQL("postgres", `
INSERT INTO foo VALUES(3);
CHECKPOINT;
`)

	// Enforce recovery, the checkpoint record generated previously should
	// still be found.
	nodePitr.Stop(tapnode.StopImmediate)
	nodePitr.Start()
}
