package testport

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/goopg/goopg/internal/testutil/tapnode"
)

// TestPort_Recovery002ArchivingFull ports postgres/src/test/recovery/t/002_archiving.pl
// to the M0149 contract: a hot standby restoring WAL from the primary's
// archive, archive_cleanup_command at a standby restartpoint,
// recovery_end_command at promotion, the promoted standby archiving its new
// timeline history file into the primary's archive, and a second standby
// restoring that history file at promotion while its recovery_end_command
// fails with a WARNING. It runs in three pairings: all-goopg, a PG 18.3
// primary with goopg standbys, and a goopg primary with PG 18.3 standbys.
//
// The goopg-primary/PG-standbys pairing passes (goopg's WAL archiver,
// M0149-0018a) and always runs; the two pairings with goopg standbys restore
// from the archive (M0149-0018b) but still miss the recovery-command and
// promotion checks (M0149-0018c), and run only with GOOPG_TAP_002=1.
func TestPort_Recovery002ArchivingFull(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping recovery TAP port in short mode")
	}
	gated := os.Getenv("GOOPG_TAP_002") != ""
	repo := repoRoot(t)
	tapnode.Available(t, repo)
	goopg := func(t *testing.T, name string) *tapnode.Node { return tapnode.NewGoopg(t, repo, name) }
	pg := func(t *testing.T, name string) *tapnode.Node { return tapnode.NewPG(t, repo, name) }
	goopgStandby := func(t *testing.T, name string, root *tapnode.Node, bkp string) *tapnode.Node {
		return tapnode.InitFromBackupGoopg(t, repo, name, root, bkp, false)
	}
	pgStandby := func(t *testing.T, name string, root *tapnode.Node, bkp string) *tapnode.Node {
		return tapnode.InitFromBackupPG(t, repo, name, root, bkp, false)
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
			if tc.name != "goopg-primary-pg18-standbys" && !gated {
				t.Skip("goopg-standby pairing not passing yet (M0149-0018c); set GOOPG_TAP_002=1 to run")
			}
			runArchiving(t, tc.primary(t, "primary"), func(name, bkp string, root *tapnode.Node) *tapnode.Node {
				return tc.standby(t, name, root, bkp)
			})
		})
	}
}

func runArchiving(t *testing.T, nodePrimary *tapnode.Node, standbyFromBackup func(name, bkp string, root *tapnode.Node) *tapnode.Node) {
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
	exists := func(path string) bool {
		_, err := os.Stat(path)
		return err == nil
	}

	// Initialize primary node, doing archives
	nodePrimary.EnableArchiving()
	backupName := "my_backup"

	// Start it
	nodePrimary.Start()

	// Take backup for standby
	bkp := nodePrimary.Backup(backupName)

	// Initialize standby node from backup, fetching WAL from archives
	nodeStandby := standbyFromBackup("standby", bkp, nodePrimary)
	nodeStandby.EnableRestoring(nodePrimary, true)
	// Reduce the timeout of the WAL retry interval so as the archive
	// detection of a new segment is faster.
	nodeStandby.AppendConf("wal_retrieve_retry_interval = '100ms'")

	// Set archive_cleanup_command and recovery_end_command, checking their
	// execution by the backend with dummy commands.
	dataDir := nodeStandby.DataDir()
	archiveCleanupCommandFile := "archive_cleanup_command.done"
	recoveryEndCommandFile := "recovery_end_command.done"
	nodeStandby.AppendConf("archive_cleanup_command = 'echo archive_cleanup_done > " + archiveCleanupCommandFile + "'")
	nodeStandby.AppendConf("recovery_end_command = 'echo recovery_ended_done > " + recoveryEndCommandFile + "'")
	nodeStandby.Start()

	// Create some content on primary
	nodePrimary.SafePSQL("postgres", "CREATE TABLE tab_int AS SELECT generate_series(1,1000) AS a")

	// Note the presence of this checkpoint for the archive_cleanup_command
	// check done below, before switching to a new segment.
	nodePrimary.SafePSQL("postgres", "CHECKPOINT")

	// Done after the checkpoint to ensure that it is replayed on the standby,
	// for archive_cleanup_command.
	currentLSN := nodePrimary.SafePSQL("postgres", "SELECT pg_current_wal_lsn();")

	// Force archiving of WAL file to make it present on primary
	nodePrimary.SafePSQL("postgres", "SELECT pg_switch_wal()")

	// Add some more content, it should not be present on standby
	nodePrimary.SafePSQL("postgres", "INSERT INTO tab_int VALUES (generate_series(1001,2000))")

	// Wait until necessary replay has been done on standby
	caughtupQuery := fmt.Sprintf("SELECT '%s'::pg_lsn <= pg_last_wal_replay_lsn()", currentLSN)
	if !nodeStandby.PollQueryUntil("postgres", caughtupQuery, "t") {
		t.Fatalf("Timed out while waiting for standby to catch up")
	}

	is(nodeStandby.SafePSQL("postgres", "SELECT count(*) FROM tab_int"), "1000", "check content from archives")

	// archive_cleanup_command is executed after generating a restart point,
	// with a checkpoint.
	nodeStandby.SafePSQL("postgres", "CHECKPOINT")
	ok(exists(filepath.Join(dataDir, archiveCleanupCommandFile)), "archive_cleanup_command executed on checkpoint")
	ok(!exists(filepath.Join(dataDir, recoveryEndCommandFile)), "recovery_end_command not executed yet")

	// Check the presence of temporary files specifically generated during
	// archive recovery. To ensure the presence of the temporary history
	// file, switch to a timeline large enough to allow a standby to recover
	// a history file from an archive. As this requires at least two
	// timeline switches, promote the existing standby first. Then create a
	// second standby based on the primary, using its archives. Finally, the
	// second standby is promoted.
	nodeStandby.Promote()

	// Wait until the history file has been stored on the archives of the
	// primary once the promotion of the standby completes. This ensures that
	// the second standby created below will be able to restore this file,
	// creating a RECOVERYHISTORY.
	primaryArchive := nodePrimary.ArchiveDir()
	caughtupQuery = fmt.Sprintf("SELECT size IS NOT NULL FROM pg_stat_file('%s/00000002.history', true)", primaryArchive)
	if !nodePrimary.PollQueryUntil("postgres", caughtupQuery, "t") {
		t.Fatalf("Timed out while waiting for archiving of 00000002.history")
	}

	// recovery_end_command should have been triggered on promotion.
	ok(exists(filepath.Join(dataDir, recoveryEndCommandFile)), "recovery_end_command executed after promotion")

	nodeStandby2 := standbyFromBackup("standby2", bkp, nodePrimary)
	nodeStandby2.EnableRestoring(nodePrimary, true)

	// Make execution of recovery_end_command fail. This should not affect
	// promotion, and its failure should be logged.
	nodeStandby2.AppendConf("recovery_end_command = 'echo recovery_end_failed > missing_dir/xyz.file'")
	nodeStandby2.Start()

	// Save the log location, to see the failure of recovery_end_command.
	logLocation := int64(0)
	if fi, err := os.Stat(nodeStandby2.LogPath()); err == nil {
		logLocation = fi.Size()
	}

	// Now promote standby2, and check that temporary files specifically
	// generated during archive recovery are removed by the end of recovery.
	nodeStandby2.Promote()

	// Check the logs of the standby to see that the commands have failed.
	logContents := ""
	if b, err := os.ReadFile(nodeStandby2.LogPath()); err == nil && int64(len(b)) >= logLocation {
		logContents = string(b[logLocation:])
	}
	standby2Data := nodeStandby2.DataDir()
	ok(regexp.MustCompile(`(?s)restored log file "00000002.history" from archive`).MatchString(logContents),
		"00000002.history retrieved from the archives")
	ok(!exists(filepath.Join(standby2Data, "pg_wal", "RECOVERYHISTORY")), "RECOVERYHISTORY removed after promotion")
	ok(!exists(filepath.Join(standby2Data, "pg_wal", "RECOVERYXLOG")), "RECOVERYXLOG removed after promotion")
	ok(regexp.MustCompile(`(?s)WARNING:.*recovery_end_command`).MatchString(logContents),
		"recovery_end_command failure detected in logs after promotion")
}

// TestE2E_GoopgStandbyRestoresFromArchive pins standby-mode archive restore
// (M0149-0018b), the first half of 002 with a goopg primary and a goopg
// standby: the standby has restore_command and no primary_conninfo, takes
// its WAL from the primary's archive, follows a later pg_switch_wal, and
// promotes with the archived rows.
func TestE2E_GoopgStandbyRestoresFromArchive(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping archive restore E2E in short mode")
	}
	repo := repoRoot(t)
	tapnode.Available(t, repo)
	primary := tapnode.NewGoopg(t, repo, "primary")
	primary.EnableArchiving()
	primary.Start()
	bkp := primary.Backup("b")
	standby := tapnode.InitFromBackupGoopg(t, repo, "standby", primary, bkp, false)
	standby.EnableRestoring(primary, true)
	standby.AppendConf("wal_retrieve_retry_interval = '100ms'")
	standby.Start()

	primary.SafePSQL("postgres", "CREATE TABLE t AS SELECT generate_series(1,500) AS a")
	lsn := primary.SafePSQL("postgres", "SELECT pg_current_wal_lsn()")
	primary.SafePSQL("postgres", "SELECT pg_switch_wal()")
	if !standby.PollQueryUntil("postgres", fmt.Sprintf("SELECT '%s'::pg_lsn <= pg_last_wal_replay_lsn()", lsn), "t") {
		t.Fatal("standby did not replay the archived segment")
	}
	if got := standby.SafePSQL("postgres", "SELECT count(*) FROM t"); got != "500" {
		t.Fatalf("standby rows = %q, want 500", got)
	}
	primary.SafePSQL("postgres", "INSERT INTO t SELECT generate_series(501,700)")
	lsn = primary.SafePSQL("postgres", "SELECT pg_current_wal_lsn()")
	primary.SafePSQL("postgres", "SELECT pg_switch_wal()")
	if !standby.PollQueryUntil("postgres", fmt.Sprintf("SELECT '%s'::pg_lsn <= pg_last_wal_replay_lsn()", lsn), "t") {
		t.Fatal("standby did not follow the next archived segment")
	}
	standby.Promote()
	if got := standby.SafePSQL("postgres", "SELECT count(*) FROM t"); got != "700" {
		t.Fatalf("promoted standby rows = %q, want 700", got)
	}
	if !primary.PollQueryUntil("postgres", fmt.Sprintf("SELECT size IS NOT NULL FROM pg_stat_file('%s/00000002.history', true)", primary.ArchiveDir()), "t") {
		t.Fatal("promoted standby did not archive 00000002.history")
	}
}
