package testport

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/goopg/goopg/internal/testutil/tapnode"
)

// TestPort_Recovery020ArchiveStatusFull ports postgres/src/test/recovery/t/020_archive_status.pl
// to the M0149 contract: a failing archive_command leaves .ready files and
// pg_stat_archiver failure counts; .ready survives crash recovery and a cold
// backup; fixing the command archives the backlog into .done; a standby
// with archive_mode = on drops the backup's .ready files and marks restored
// segments .done, one with archive_mode = always keeps and creates .ready
// files and archives them itself; pg_stat_reset_shared('archiver'); the
// archiver's shutdown message; pg_backup_start's label-length error. It
// runs in three pairings: all-goopg, a PG 18.3 primary with goopg standbys,
// and a goopg primary with PG 18.3 standbys.
func TestPort_Recovery020ArchiveStatusFull(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping recovery TAP port in short mode")
	}
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
			runArchiveStatus(t, tc.primary(t, "primary"), func(name, bkp string, root *tapnode.Node) *tapnode.Node {
				return tc.standby(t, name, root, bkp)
			})
		})
	}
}

func runArchiveStatus(t *testing.T, primary *tapnode.Node, standbyFromBackup func(name, bkp string, root *tapnode.Node) *tapnode.Node) {
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

	primary.EnableArchiving()
	primary.AppendConf("autovacuum = off")
	primary.Start()
	primaryData := primary.DataDir()

	// Temporarily use an archive_command value to make the archiver fail,
	// knowing that archiving is enabled.
	incorrectCommand := `cp "%p_does_not_exist" "%f_does_not_exist"`
	primary.SafePSQL("postgres", "ALTER SYSTEM SET archive_command TO '"+incorrectCommand+"';\nSELECT pg_reload_conf();")

	// Save the WAL segment currently in use and switch to a new segment.
	segmentName1 := primary.SafePSQL("postgres", "SELECT pg_walfile_name(pg_current_wal_lsn())")
	segmentPath1 := "pg_wal/archive_status/" + segmentName1
	segmentPath1Ready := segmentPath1 + ".ready"
	segmentPath1Done := segmentPath1 + ".done"
	primary.SafePSQL("postgres", "CREATE TABLE mine AS SELECT generate_series(1,10) AS x;\nSELECT pg_switch_wal();\nCHECKPOINT;")

	// Wait for an archive failure.
	if !primary.PollQueryUntil("postgres", "SELECT failed_count > 0 FROM pg_stat_archiver", "t") {
		t.Fatalf("Timed out while waiting for archiving to fail")
	}
	ok(exists(filepath.Join(primaryData, segmentPath1Ready)), ".ready file exists for WAL segment "+segmentName1+" waiting to be archived")
	ok(!exists(filepath.Join(primaryData, segmentPath1Done)), ".done file does not exist for WAL segment "+segmentName1+" waiting to be archived")
	is(primary.SafePSQL("postgres", "SELECT archived_count, last_failed_wal FROM pg_stat_archiver"), "0|"+segmentName1,
		"pg_stat_archiver failed to archive "+segmentName1)

	// Crash the cluster for the next test in charge of checking that
	// non-archived WAL segments are not removed.
	primary.Stop(tapnode.StopImmediate)

	// Recovery tests for the archiving with a standby partially check the
	// recovery behavior when restoring a backup taken using a snapshot with
	// no pg_backup_start/stop. In this situation, the recovered standby
	// should enter first crash recovery then switch to regular archive
	// recovery.
	bkp := primary.BackupFsCold("backup")
	primary.Start()
	ok(exists(filepath.Join(primaryData, segmentPath1Ready)), ".ready file for WAL segment "+segmentName1+" still exists after crash recovery on primary")

	// Allow WAL archiving again and wait for a success.
	primary.SafePSQL("postgres", "ALTER SYSTEM RESET archive_command;\nSELECT pg_reload_conf();")
	if !primary.PollQueryUntil("postgres", "SELECT archived_count FROM pg_stat_archiver", "1") {
		t.Fatalf("Timed out while waiting for archiving to finish")
	}
	ok(!exists(filepath.Join(primaryData, segmentPath1Ready)), ".ready file for archived WAL segment "+segmentName1+" removed")
	ok(exists(filepath.Join(primaryData, segmentPath1Done)), ".done file for archived WAL segment "+segmentName1+" exists")
	is(primary.SafePSQL("postgres", "SELECT last_archived_wal FROM pg_stat_archiver"), segmentName1,
		"archive success reported in pg_stat_archiver for WAL segment "+segmentName1)

	// Create some WAL activity and a new checkpoint so as the next standby
	// can create a restartpoint. As this standby starts in crash recovery
	// because of the cold backup taken previously, it needs a clean restart
	// point to get to a consistent state.
	segmentName2 := primary.SafePSQL("postgres", "SELECT pg_walfile_name(pg_current_wal_lsn())")
	segmentPath2 := "pg_wal/archive_status/" + segmentName2
	segmentPath2Ready := segmentPath2 + ".ready"
	segmentPath2Done := segmentPath2 + ".done"
	primary.SafePSQL("postgres", "INSERT INTO mine SELECT generate_series(10,20) AS x;\nCHECKPOINT;")

	// Switch to a new segment and use the returned LSN to make sure that
	// standbys have caught up to this point.
	primaryLSN := primary.SafePSQL("postgres", "SELECT pg_switch_wal();")
	if !primary.PollQueryUntil("postgres", "SELECT last_archived_wal FROM pg_stat_archiver", segmentName2) {
		t.Fatalf("Timed out while waiting for archiving to finish")
	}

	// Test standby with archive_mode = on.
	standby1 := standbyFromBackup("standby", bkp, primary)
	standby1.EnableRestoring(primary, true)
	standby1.AppendConf("archive_mode = on")
	standby1Data := standby1.DataDir()
	standby1.Start()

	// Wait for the replay of the segment switch done previously, ensuring
	// that all segments needed are restored from the archives.
	if !standby1.PollQueryUntil("postgres", fmt.Sprintf("SELECT pg_wal_lsn_diff(pg_last_wal_replay_lsn(), '%s') >= 0", primaryLSN), "t") {
		t.Fatalf("Timed out while waiting for xlog replay on standby1")
	}
	standby1.SafePSQL("postgres", "CHECKPOINT")

	// Recovery with archive_mode=on does not keep .ready signal files
	// inherited from backup. Note that this WAL segment existed in the
	// backup.
	ok(!exists(filepath.Join(standby1Data, segmentPath1Ready)),
		".ready file for WAL segment "+segmentName1+" present in backup got removed with archive_mode=on on standby")

	// Recovery with archive_mode=on should not create .ready files. Note
	// that this segment did not exist in the backup.
	ok(!exists(filepath.Join(standby1Data, segmentPath2Ready)),
		".ready file for WAL segment "+segmentName2+" not created on standby when archive_mode=on on standby")

	// Recovery with archive_mode = on creates .done files.
	ok(exists(filepath.Join(standby1Data, segmentPath2Done)),
		".done file for WAL segment "+segmentName2+" created when archive_mode=on on standby")

	// Test recovery with archive_mode = always, which should always keep
	// .ready files if archiving is enabled, though here we want the archive
	// command to fail to persist the .ready files. Note that this node has
	// inherited the archive command of the previous cold backup that will
	// cause archiving failures.
	standby2 := standbyFromBackup("standby2", bkp, primary)
	standby2.EnableRestoring(primary, true)
	standby2.AppendConf("archive_mode = always")
	standby2Data := standby2.DataDir()
	standby2.Start()

	// Wait for the replay of the segment switch done previously.
	if !standby2.PollQueryUntil("postgres", fmt.Sprintf("SELECT pg_wal_lsn_diff(pg_last_wal_replay_lsn(), '%s') >= 0", primaryLSN), "t") {
		t.Fatalf("Timed out while waiting for xlog replay on standby2")
	}
	standby2.SafePSQL("postgres", "CHECKPOINT")

	ok(exists(filepath.Join(standby2Data, segmentPath1Ready)),
		".ready file for WAL segment "+segmentName1+" existing in backup is kept with archive_mode=always on standby")
	ok(exists(filepath.Join(standby2Data, segmentPath2Ready)),
		".ready file for WAL segment "+segmentName2+" created with archive_mode=always on standby")

	// Reset statistics of the archiver for the next checks.
	standby2.SafePSQL("postgres", "SELECT pg_stat_reset_shared('archiver')")

	// Now crash the cluster to check that recovery step does not remove
	// non-archived WAL segments on a standby where archiving is enabled.
	standby2.Stop(tapnode.StopImmediate)
	standby2.Start()
	ok(exists(filepath.Join(standby2Data, segmentPath1Ready)),
		"WAL segment still ready to archive after crash recovery on standby with archive_mode=always")

	// Allow WAL archiving again, and wait for the segments to be archived.
	standby2.SafePSQL("postgres", "ALTER SYSTEM RESET archive_command;\nSELECT pg_reload_conf();")
	if !standby2.PollQueryUntil("postgres", "SELECT last_archived_wal FROM pg_stat_archiver", segmentName2) {
		t.Fatalf("Timed out while waiting for archiving to finish")
	}
	is(standby2.SafePSQL("postgres", "SELECT archived_count FROM pg_stat_archiver"), "2",
		"correct number of WAL segments archived from standby")
	ok(!exists(filepath.Join(standby2Data, segmentPath1Ready)) && !exists(filepath.Join(standby2Data, segmentPath2Ready)),
		".ready files removed after archive success with archive_mode=always on standby")
	ok(exists(filepath.Join(standby2Data, segmentPath1Done)) && exists(filepath.Join(standby2Data, segmentPath2Done)),
		".done files created after archive success with archive_mode=always on standby")

	// Check that the archiver process calls the shell archive module's
	// shutdown callback.
	standby2.AppendConf("log_min_messages = debug1")
	standby2.Reload()

	// Run a query to make sure that the reload has taken effect.
	standby2.SafePSQL("postgres", "SELECT 1")
	logLocation := standby2.LogSize()
	standby2.Stop(tapnode.StopFast)
	logfile := standby2.LogFrom(logLocation)
	ok(regexp.MustCompile(`archiver process shutting down`).MatchString(logfile),
		"check shutdown callback of shell archive module")

	// Test that we can enter and leave backup mode without crashes
	_, stderr, cmdret := primary.PSQLExit("postgres",
		"SELECT pg_backup_start('onebackup'); SELECT pg_backup_stop();SELECT pg_backup_start(repeat('x', 1026))")
	is(fmt.Sprint(cmdret), "3", "psql fails correctly")
	ok(regexp.MustCompile(`backup label too long`).MatchString(stderr), "pg_backup_start fails gracefully", stderr)
	primary.SafePSQL("postgres", "SELECT pg_backup_start('onebackup'); SELECT pg_backup_stop();")
	primary.SafePSQL("postgres", "SELECT pg_backup_start('twobackup')")
}
