package testport

import (
	"fmt"
	"testing"

	"github.com/goopg/goopg/internal/testutil/tapnode"
)

// TestPort_Recovery045ArchiveRestartpointFull ports postgres/src/test/recovery/t/045_archive_restartpoint.pl
// to the M0149 contract: a primary initialised with 1 MB WAL segments
// archives 320 segments (a CHECKPOINT and pg_switch_wal() each), and a node
// restored from a backup taken before them replays the archive with
// max_wal_size = 2 MB — restartpoints and segment removal during archive
// recovery — up to recovery_target_lsn with recovery_target_action = pause,
// where it must be reachable and report the target as replayed. It runs in
// three pairings: all-goopg, a PG 18.3 primary with a goopg restore node,
// and a goopg primary with a PG 18.3 restore node. The restore node is a
// standby (Cluster.pm init_from_backup has_restoring: standby => 1), so it
// waits for more WAL at the end of the archive instead of ending recovery
// (M0149-0022).
func TestPort_Recovery045ArchiveRestartpointFull(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping recovery TAP port in short mode")
	}
	repo := repoRoot(t)
	tapnode.Available(t, repo)
	const walSegsize = 1 // MB
	initArgs := []string{"--wal-segsize", fmt.Sprint(walSegsize)}
	goopg := func(t *testing.T, name string) *tapnode.Node { return tapnode.NewGoopg(t, repo, name, initArgs...) }
	pg := func(t *testing.T, name string) *tapnode.Node { return tapnode.NewPG(t, repo, name, initArgs...) }
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
		{"pg18-primary-goopg-restore", pg, goopgFromBackup},
		{"goopg-primary-pg18-restore", goopg, pgFromBackup},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runArchiveRestartpoint(t, walSegsize, tc.primary(t, "primary"), func(name, bkp string, root *tapnode.Node) *tapnode.Node {
				return tc.recovery(t, name, root, bkp)
			})
		})
	}
}

func runArchiveRestartpoint(t *testing.T, walSegsize int, nodePrimary *tapnode.Node, fromBackup func(name, bkp string, root *tapnode.Node) *tapnode.Node) {
	const archiveMaxMB = 320

	// Initialize primary node
	nodePrimary.EnableArchiving()
	for _, l := range []string{"wal_level = replica", "max_wal_senders = 10"} {
		nodePrimary.AppendConf(l)
	}
	nodePrimary.Start()
	backupName := "my_backup"
	bkp := nodePrimary.Backup(backupName)

	nodePrimary.SafePSQL("postgres", fmt.Sprintf(
		"DO $$BEGIN FOR i IN 1..%d LOOP CHECKPOINT; PERFORM pg_switch_wal(); END LOOP; END$$;", archiveMaxMB/walSegsize))

	// Force archiving of WAL file containing recovery target
	untilLSN := nodePrimary.SafePSQL("postgres", "SELECT pg_current_wal_lsn()")
	nodePrimary.SafePSQL("postgres", "SELECT pg_switch_wal()")
	nodePrimary.Stop(tapnode.StopFast)

	// Archive recovery
	nodeRestore := fromBackup("restore", bkp, nodePrimary)
	nodeRestore.EnableRestoring(nodePrimary, true) // init_from_backup has_restoring: standby => 1
	nodeRestore.AppendConf(fmt.Sprintf("recovery_target_lsn = '%s'", untilLSN))
	nodeRestore.AppendConf("recovery_target_action = pause")
	nodeRestore.AppendConf(fmt.Sprintf("max_wal_size = %d", 2*walSegsize))
	nodeRestore.AppendConf("log_checkpoints = on")

	nodeRestore.Start()

	// Wait until restore has replayed enough data
	caughtupQuery := fmt.Sprintf("SELECT '%s'::pg_lsn <= pg_last_wal_replay_lsn()", untilLSN)
	if !nodeRestore.PollQueryUntil("postgres", caughtupQuery, "t") {
		t.Fatalf("Timed out while waiting for restore to catch up")
	}

	nodeRestore.Stop(tapnode.StopFast)
}
