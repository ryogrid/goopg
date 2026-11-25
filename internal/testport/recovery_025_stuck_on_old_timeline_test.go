package testport

import (
	"fmt"
	"path/filepath"
	"testing"

	"github.com/goopg/goopg/internal/testutil/tapnode"
)

// TestPort_Recovery025StuckOnOldTimelineFull ports postgres/src/test/recovery/t/025_stuck_on_old_timeline.pl
// to the M0149 contract: a standby is promoted, and a cascading standby made
// from its -Xnone backup — restoring from the primary's archive, which only
// ever receives timeline history files (cp_history_files), and streaming
// from the promoted standby with recovery_target_timeline = 'latest' — must
// follow the promoted standby onto its new timeline and replay what it
// writes. It runs in three pairings: all-goopg, a PG 18.3 primary with goopg
// standbys, and a goopg primary with PG 18.3 standbys (M0149-0026).
func TestPort_Recovery025StuckOnOldTimelineFull(t *testing.T) {
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
			runStuckOnOldTimeline(t, repo, tc.primary(t, "primary"), func(name, bkp string, root *tapnode.Node) *tapnode.Node {
				return tc.standby(t, name, root, bkp)
			})
		})
	}
}

func runStuckOnOldTimeline(t *testing.T, repo string, nodePrimary *tapnode.Node, fromBackup func(name, bkp string, root *tapnode.Node) *tapnode.Node) {
	is := func(got, want, name string) {
		t.Helper()
		if got != want {
			t.Errorf("not ok: %s: got %q, want %q", name, got, want)
		}
	}

	// Set up an archive command that will copy the history file but not the
	// WAL files, to simulate a cascading standby starting up after the
	// timeline history file reaches the archive but before any of the WAL
	// files get there.
	nodePrimary.EnableArchiving()
	cpHistoryFiles := filepath.Join(repo, "postgres", "src", "test", "recovery", "t", "cp_history_files")
	for _, l := range []string{
		"wal_level = replica", "max_wal_senders = 10",
		fmt.Sprintf(`archive_command = 'perl "%s" "%%p" "%s/%%f"'`, cpHistoryFiles, nodePrimary.ArchiveDir()),
		"wal_keep_size=128MB",
	} {
		nodePrimary.AppendConf(l)
	}
	nodePrimary.Start()

	// Take backup from primary
	backupName := "my_backup"
	bkp := nodePrimary.Backup(backupName)

	// Create streaming standby linking to primary (has_streaming). Cluster.pm's
	// has_archiving only creates the archive directory: the standby keeps the
	// primary's archive_command (cp_history_files into the primary's archive)
	// from the base backup, so after promotion its 00000002.history lands
	// there — the one file the cascade can restore.
	nodeStandby := fromBackup("standby", bkp, nodePrimary)
	nodeStandby.Start()

	// Take backup of standby, use -Xnone so that pg_wal is empty.
	bkpStandby := nodeStandby.Backup(backupName, "-Xnone")

	// Create cascading standby but don't start it yet. Must set up both
	// streaming and archiving.
	nodeCascade := fromBackup("cascade", bkpStandby, nodeStandby)
	nodeCascade.EnableRestoring(nodePrimary, true)
	nodeCascade.AppendConf("recovery_target_timeline='latest'")

	// Promote the standby.
	nodeStandby.Promote()

	// Wait for promotion to complete
	if !nodeStandby.PollQueryUntil("postgres", "SELECT NOT pg_is_in_recovery();", "t") {
		t.Fatalf("Timed out while waiting for promotion")
	}

	// Find next WAL segment to be archived
	walfileToBeArchived := nodeStandby.SafePSQL("postgres", "SELECT pg_walfile_name(pg_current_wal_lsn());")

	// Make WAL segment eligible for archival
	nodeStandby.SafePSQL("postgres", "SELECT pg_switch_wal()")

	// Wait until the WAL segment has been archived.
	archiveWaitQuery := "SELECT '" + walfileToBeArchived + "' <= last_archived_wal FROM pg_stat_archiver"
	if !nodeStandby.PollQueryUntil("postgres", archiveWaitQuery, "t") {
		t.Fatalf("Timed out while waiting for WAL segment to be archived")
	}

	// Start cascade node
	nodeCascade.Start()

	// Create some content on promoted standby and check its presence on the
	// cascading standby.
	nodeStandby.SafePSQL("postgres", "CREATE TABLE tab_int AS SELECT 1 AS a")

	// Wait for the replication to catch up (wait_for_catchup: replay mode,
	// the upstream's current write LSN, matched by application_name).
	target := nodeStandby.SafePSQL("postgres", "SELECT pg_current_wal_lsn()")
	q := fmt.Sprintf("SELECT '%s' <= replay_lsn AND state = 'streaming' FROM pg_catalog.pg_stat_replication WHERE application_name IN ('%s', 'walreceiver')",
		target, nodeCascade.Name())
	if !nodeStandby.PollQueryUntil("postgres", q, "t") {
		st, _, _ := nodeStandby.PSQLExit("postgres", "SELECT application_name, state, sent_lsn, replay_lsn FROM pg_catalog.pg_stat_replication")
		t.Fatalf("timed out waiting for cascade to replay %s; pg_stat_replication: %q", target, st)
	}

	// Check that cascading standby has the new content
	result := nodeCascade.SafePSQL("postgres", "SELECT count(*) FROM tab_int")
	is(result, "1", "check streamed content on cascade standby")
}
