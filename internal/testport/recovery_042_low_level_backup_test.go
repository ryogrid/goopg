package testport

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/testutil/tapnode"
)

// TestPort_Recovery042LowLevelBackupFull ports postgres/src/test/recovery/t/042_low_level_backup.pl
// to the M0149 contract: a backup taken with pg_backup_start /
// pg_backup_stop and a file copy recovers wrongly without backup_label (crash
// recovery from pg_control's later checkpoint: the canary table is missing)
// and correctly with it (backup recovery from the label's redo LSN, WAL from
// the archive). It runs in three pairings: all-goopg, a PG 18.3 primary with
// goopg recovery nodes, and a goopg primary with PG 18.3 recovery nodes.
// As in Cluster.pm, init_from_backup's has_restoring makes the second node a
// standby (standby => 1 by default) (M0149-0021, -0021a).
func TestPort_Recovery042LowLevelBackupFull(t *testing.T) {
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
		primary  func(t *testing.T, name string) *tapnode.Node
		recovery func(t *testing.T, name string, root *tapnode.Node, bkp string) *tapnode.Node
	}{
		{"all-goopg", goopg, goopgFromBackup},
		{"pg18-primary-goopg-recovery", pg, goopgFromBackup},
		{"goopg-primary-pg18-recovery", goopg, pgFromBackup},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runLowLevelBackup(t, tc.primary(t, "primary"), func(name, bkp string, root *tapnode.Node) *tapnode.Node {
				return tc.recovery(t, name, root, bkp)
			})
		})
	}
}

func runLowLevelBackup(t *testing.T, nodePrimary *tapnode.Node, fromBackup func(name, bkp string, root *tapnode.Node) *tapnode.Node) {
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

	// Start primary node with archiving.
	nodePrimary.EnableArchiving()
	for _, l := range []string{"wal_level = replica", "max_wal_senders = 10"} {
		nodePrimary.AppendConf(l)
	}
	nodePrimary.Start()

	// Start backup.
	psql := nodePrimary.BackgroundPSQL("postgres", true)
	psql.QuerySafe("SET client_min_messages TO WARNING")
	psql.QuerySafe("select pg_backup_start('test label')")

	// Copy files.
	backupDir := filepath.Join(t.TempDir(), "backup1")
	if out, err := exec.Command("cp", "-a", nodePrimary.DataDir(), backupDir).CombinedOutput(); err != nil {
		t.Fatalf("copy data dir: %v\n%s", err, out)
	}

	// Cleanup some files/paths that should not be in the backup. Also remove
	// pg_control because it needs to be copied later.
	for _, f := range []string{"postmaster.pid", "postmaster.opts", "global/pg_control"} {
		if err := os.Remove(filepath.Join(backupDir, f)); err != nil && !(f == "postmaster.opts" && os.IsNotExist(err)) {
			t.Fatalf("unable to unlink %s/%s: %v", backupDir, f, err)
		}
	}
	if err := os.RemoveAll(filepath.Join(backupDir, "pg_wal")); err != nil {
		t.Fatalf("unable to unlink contents of %s/pg_wal: %v", backupDir, err)
	}
	if err := os.Mkdir(filepath.Join(backupDir, "pg_wal"), 0o700); err != nil {
		t.Fatal(err)
	}

	// Create a table that will be used to verify that recovery started at the
	// correct location, rather than a location recorded in the control file.
	nodePrimary.SafePSQL("postgres", "create table canary (id int)")

	// Advance the checkpoint location in pg_control past the location where
	// the backup started. Switch WAL to put the checkpoint in a new segment.
	segmentName := nodePrimary.SafePSQL("postgres", "select pg_walfile_name(pg_switch_wal())")

	// Ensure that the segment just switched from is archived.
	if !nodePrimary.PollQueryUntil("postgres", "SELECT last_archived_wal FROM pg_stat_archiver", segmentName) {
		t.Fatalf("Timed out while waiting for archiving of switched segment to finish")
	}

	nodePrimary.SafePSQL("postgres", "checkpoint")

	// Copy pg_control last so it contains the new checkpoint.
	copyFile(t, filepath.Join(nodePrimary.DataDir(), "global", "pg_control"), filepath.Join(backupDir, "global", "pg_control"))

	// Save the name of the segment that pg_backup_stop() will archive.
	stopSegmentName := nodePrimary.SafePSQL("postgres", "SELECT pg_walfile_name(pg_current_wal_lsn())")

	// Stop backup and get backup_label, the last segment is archived.
	backupLabel := psql.QuerySafe("select labelfile from pg_backup_stop()")
	_ = psql.Quit()

	// Recover the backup without backup_label: recovery will not work
	// correctly, i.e. the canary table will be missing. Provide only the WAL
	// segment that recovery will think it needs.
	nodeReplica := fromBackup("replica_fail", backupDir, nodePrimary)
	nodeReplica.AppendConf("archive_mode = off")

	canaryQuery := "select count(*) from pg_class where relname = 'canary'"

	copyFile(t, filepath.Join(nodePrimary.ArchiveDir(), stopSegmentName), filepath.Join(nodeReplica.DataDir(), "pg_wal", stopSegmentName))

	nodeReplica.Start()

	is(nodeReplica.SafePSQL("postgres", canaryQuery), "0", "canary is missing")

	// Check log to ensure that crash recovery was used as there is no
	// backup_label.
	ok(strings.Contains(nodeReplica.LogFrom(0), "database system was not properly shut down; automatic recovery in progress"),
		"verify backup recovery performed with crash recovery")

	nodeReplica.Stop(tapnode.StopImmediate)

	// Save backup_label into the backup directory and recover using the
	// primary's archive. This time recovery will succeed and the canary table
	// will be present.
	fh, err := os.OpenFile(filepath.Join(backupDir, "backup_label"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("could not open backup_label: %v", err)
	}
	if _, err := fh.WriteString(backupLabel + "\n"); err != nil {
		t.Fatal(err)
	}
	_ = fh.Close()

	nodeReplica = fromBackup("replica_success", backupDir, nodePrimary)
	nodeReplica.EnableRestoring(nodePrimary, true) // init_from_backup has_restoring: standby => 1
	nodeReplica.Start()

	is(nodeReplica.SafePSQL("postgres", canaryQuery), "1", "canary is present")

	// Check log to ensure that backup_label was used for recovery.
	ok(strings.Contains(nodeReplica.LogFrom(0), "starting backup recovery with redo LSN"),
		"verify backup recovery performed with backup_label")
}

func copyFile(t *testing.T, src, dst string) {
	t.Helper()
	b, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("unable to copy %s: %v", src, err)
	}
	if err := os.WriteFile(dst, b, 0o600); err != nil {
		t.Fatalf("unable to copy %s: %v", src, err)
	}
}
