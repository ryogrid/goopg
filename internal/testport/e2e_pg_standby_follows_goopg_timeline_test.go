package testport

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/testutil/cluster"
	"github.com/goopg/goopg/internal/testutil/pgcluster"
	"github.com/goopg/goopg/internal/testutil/replcluster"
)

// TestE2E_PGStandbyFollowsGoopgTimelineSwitch is PG's 004_timeline_switch
// shape with a goopg upstream and a PG 18.3 downstream, the oracle for the
// walsender's historic-timeline path:
//
//  1. goopg primary A streams to goopg standby B; a PG standby C is
//     pg_basebackup'd from A on timeline 1.
//  2. A stops and B is promoted onto timeline 2.
//  3. C starts against B. Its walreceiver fetches 00000002.history, asks
//     for timeline 1, and B streams timeline 1's WAL up to its switch point,
//     ends the copy, and reports (next_tli 2, next_tli_startpos). C's startup
//     process then moves to timeline 2 (recovery_target_timeline = 'latest',
//     the default) and streams it.
//
// A row written on B after the promotion reaching C, with C's walreceiver
// on timeline 2, shows PG accepted every step.
func TestE2E_PGStandbyFollowsGoopgTimelineSwitch(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping replication test in short mode")
	}
	repo := repoRoot(t)
	pgcluster.Available(t, filepath.Join(repo, "postgres", "local_install", "bin"))
	pgBasebackupBin := clientToolBin(t, "pg_basebackup")
	if pgBasebackupBin == "" {
		t.Skip("pg_basebackup not found")
	}

	baseDir := t.TempDir()
	rc, err := replcluster.New("e2e_tl_switch", replcluster.Options{
		RepoRoot:     repo,
		BaseDir:      baseDir,
		SlotName:     "e2e_tl_switch_slot",
		StartupWait:  30 * time.Second,
		ShutdownWait: 10 * time.Second,
		PreCloneHook: func(primary *cluster.Cluster) error {
			if _, err := primary.Query(context.Background(), "CREATE TABLE tl_t (id int)"); err != nil {
				return err
			}
			_, err := primary.Query(context.Background(), "INSERT INTO tl_t VALUES (1)")
			return err
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := rc.Setup(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rc.Stop() }()

	// C: a PG standby on timeline 1, backed up from A.
	const pgSlot = "tl_pg"
	standbyDir := filepath.Join(baseDir, "pg-standby")
	t.Cleanup(func() {
		if pgLog, rerr := os.ReadFile(filepath.Join(baseDir, "pg.log")); rerr == nil && t.Failed() {
			t.Logf("[pg-standby log]\n%s", pgLog)
		}
	})
	runGoopgBasebackupToPGSlot(t, repo, pgBasebackupBin, rc.Primary, standbyDir, pgSlot, true)

	// B replays past the backup, then A stops and B is promoted.
	if err := runSQLSimple(t, rc.Primary, "INSERT INTO tl_t VALUES (2)"); err != nil {
		t.Fatal(err)
	}
	waitGoopgRow(t, rc.Standby, "SELECT id FROM tl_t WHERE id = 2", "2")
	if err := rc.Primary.Stop(cluster.ShutdownFast); err != nil {
		t.Fatalf("stop primary: %v", err)
	}
	if err := rc.Promote(); err != nil {
		t.Fatalf("promote: %v", err)
	}
	deadline := time.Now().Add(15 * time.Second)
	for runSQLSimple(t, rc.Standby, "INSERT INTO tl_t VALUES (3)") != nil {
		if time.Now().After(deadline) {
			t.Fatal("promoted standby never accepted a write")
		}
		time.Sleep(300 * time.Millisecond)
	}
	if err := runSQLSimple(t, rc.Standby, "SELECT pg_create_physical_replication_slot('"+pgSlot+"')"); err != nil {
		t.Fatalf("create slot on B: %v", err)
	}

	// C follows B across the switch.
	configurePGStandbyFromGoopgBackup(t, standbyDir, primaryConninfoForPGStandby(rc.Standby, pgSlot), pgSlot)
	pgStandby, err := pgcluster.OpenExisting("tl-pg-standby", pgcluster.Options{
		RepoRoot:    repo,
		DataDir:     standbyDir,
		StartupWait: 45 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = pgStandby.Stop() }()
	if err := pgStandby.Start(); err != nil {
		t.Fatalf("pg standby start: %v", err)
	}
	waitForPGCount(t, pgStandby, "SELECT count(*) FROM tl_t WHERE id = 3", 1, 60*time.Second)
	if got := pgScalar(t, pgStandby, "SELECT received_tli FROM pg_stat_wal_receiver"); got != "2" {
		t.Errorf("PG standby walreceiver timeline = %q, want 2", got)
	}
	if got := pgScalar(t, pgStandby, "SELECT count(*) FROM tl_t"); got != "3" {
		t.Errorf("PG standby rows = %q, want 3", got)
	}
}

// waitGoopgRow polls a goopg cluster until query returns want.
func waitGoopgRow(t *testing.T, c *cluster.Cluster, query, want string) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		rows, err := c.Query(context.Background(), query)
		if err == nil && len(rows) > 0 && rows[0][0] == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%q never returned %q: rows=%v err=%v", query, want, rows, err)
		}
		time.Sleep(300 * time.Millisecond)
	}
}
