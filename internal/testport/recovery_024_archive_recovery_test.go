package testport

import (
	"regexp"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/testutil/tapnode"
)

// TestPort_Recovery024ArchiveRecoveryFull ports postgres/src/test/recovery/t/024_archive_recovery.pl
// to the M0149 contract: WAL generated while the origin ran with
// wal_level=minimal carries an XLOG_PARAMETER_CHANGE record, and both archive
// recovery and a standby restoring that WAL from the archive stop with
// FATAL `WAL was generated with "wal_level=minimal", cannot continue
// recovering`. It runs in three pairings: all-goopg, a PG 18.3 origin with
// goopg recovery nodes, and a goopg origin with PG 18.3 recovery nodes
// (M0149-0020).
func TestPort_Recovery024ArchiveRecoveryFull(t *testing.T) {
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
		name     string
		origin   func(t *testing.T, name string) *tapnode.Node
		recovery func(t *testing.T, name string, root *tapnode.Node, bkp string) *tapnode.Node
	}{
		{"all-goopg", goopg, goopgFromBackup},
		{"pg18-origin-goopg-recovery", pg, goopgFromBackup},
		{"goopg-origin-pg18-recovery", goopg, pgFromBackup},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runArchiveRecoveryWalLevel(t, tc.origin(t, "orig"), func(name, bkp string, root *tapnode.Node) *tapnode.Node {
				return tc.recovery(t, name, root, bkp)
			})
		})
	}
}

func runArchiveRecoveryWalLevel(t *testing.T, node *tapnode.Node, fromBackup func(name, bkp string, root *tapnode.Node) *tapnode.Node) {
	ok := func(cond bool, name string, detail ...any) {
		t.Helper()
		if !cond {
			t.Errorf("not ok: %s %v", name, detail)
		}
	}

	node.EnableArchiving()
	replicaConfig := []string{"wal_level = replica", "archive_mode = on", "max_wal_senders = 10", "hot_standby = off"}
	for _, l := range replicaConfig {
		node.AppendConf(l)
	}
	node.Start()

	// Take backup
	backupName := "my_backup"
	bkp := node.Backup(backupName)

	// Restart node with wal_level = minimal and WAL archiving disabled
	// to generate WAL with that setting. Note that such WAL has not been
	// archived yet at this point.
	node.AppendConf("wal_level = minimal")
	node.AppendConf("archive_mode = off")
	node.AppendConf("max_wal_senders = 0")
	node.Restart()

	// Restart node with wal_level = replica and WAL archiving enabled
	// to archive WAL previously generated with wal_level = minimal.
	// We ensure the WAL file containing the record indicating the change
	// of wal_level to minimal is archived by checking pg_stat_archiver.
	for _, l := range replicaConfig {
		node.AppendConf(l)
	}
	node.Restart()

	// Find next WAL segment to be archived
	walfileToBeArchived := node.SafePSQL("postgres", "SELECT pg_walfile_name(pg_current_wal_lsn());")

	// Make WAL segment eligible for archival
	node.SafePSQL("postgres", "SELECT pg_switch_wal()")

	// Wait until the WAL segment has been archived.
	archiveWaitQuery := "SELECT '" + walfileToBeArchived + "' <= last_archived_wal FROM pg_stat_archiver;"
	if !node.PollQueryUntil("postgres", archiveWaitQuery, "t") {
		t.Fatalf("Timed out while waiting for WAL segment to be archived")
	}
	node.Stop(tapnode.StopFast)

	// Initialize new node from backup, and start archive recovery. Check that
	// archive recovery fails with an error when it detects the WAL record
	// indicating the change of wal_level to minimal and node stops.
	testRecoveryWalLevelMinimal := func(nodeName, nodeText string, standby bool) {
		recoveryNode := fromBackup(nodeName, bkp, node)
		recoveryNode.EnableRestoring(node, standby)

		// We need to manually wait for the server to stop as the
		// start is expected to fail.
		stopped := recoveryNode.StartExpectExit(3 * time.Minute)
		logfile := recoveryNode.LogFrom(0)
		ok(stopped, nodeText+" stops")
		ok(regexp.MustCompile(`FATAL: .* WAL was generated with "wal_level=minimal", cannot continue recovering`).MatchString(logfile),
			nodeText+` ends with an error because it finds WAL generated with "wal_level=minimal"`)
	}

	// Test for archive recovery
	testRecoveryWalLevelMinimal("archive_recovery", "archive recovery", false)

	// Test for standby server
	testRecoveryWalLevelMinimal("standby", "standby", true)
}
