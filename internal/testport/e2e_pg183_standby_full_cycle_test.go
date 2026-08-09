package testport

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/testutil/cluster"
	"github.com/goopg/goopg/internal/testutil/pgcluster"
)

// TestE2E_PGStandbyFullCycle exercises the M0130 acceptance scenario:
// goopg primary → pg_basebackup → PG 18.3 standby → stream DDL+DML →
// promote PG → reverse-attach goopg standby on the new timeline.
//
// This is the ultimate acceptance vehicle for M0130: it validates
// goopg's cluster-directory compatibility (S1–S3), WAL record fidelity
// (S4–S7), multi-timeline streaming (S8), and the full replication
// cycle end-to-end.
func TestE2E_PGStandbyFullCycle(t *testing.T) {
	if testing.Short() || os.Getenv("GOOPG_SKIP_M0130_E2E") != "" {
		t.Skip("skipping M0130 full-cycle E2E (short mode or GOOPG_SKIP_M0130_E2E set)")
	}

	repo := repoRoot(t)
	binDir := filepath.Join(repo, "postgres", "local_install", "bin")
	pgcluster.Available(t, binDir)

	pgBasebackupBin := clientToolBin(t, "pg_basebackup")
	if pgBasebackupBin == "" {
		t.Skip("pg_basebackup not found in PATH or postgres/local_install/bin")
	}
	psqlBin := clientToolBin(t, "psql")
	if psqlBin == "" {
		t.Skip("psql not found in PATH or postgres/local_install/bin")
	}

	const (
		forwardSlot = "s10_forward"
		reverseSlot = "s10_reverse"
	)
	baseDir := t.TempDir()

	// ── Phase A: goopg primary → pg_basebackup → PG standby ──────────

	primary, err := cluster.New("s10-goopg-primary", cluster.Options{
		RepoRoot:     repo,
		DataDir:      filepath.Join(baseDir, "goopg-primary"),
		StartupWait:  45 * time.Second,
		ShutdownWait: 10 * time.Second,
		SyncInit:     true,
		SyncRuntime:  true,
	})
	if err != nil {
		t.Fatalf("cluster.New primary: %v", err)
	}
	defer func() { _ = primary.Stop(cluster.ShutdownImmediate) }()
	if err := primary.Init(); err != nil {
		t.Fatalf("primary.Init: %v", err)
	}
	if err := primary.Start(); err != nil {
		t.Fatalf("primary.Start: %v", err)
	}

	// Initial workload: table + row that the standby inherits via base backup.
	if err := runSQLSimple(t, primary,
		"CREATE TABLE public.s10_t (id int NOT NULL, val text NOT NULL)"); err != nil {
		t.Fatalf("create s10_t: %v", err)
	}
	if err := runSQLSimple(t, primary,
		"INSERT INTO public.s10_t (id, val) VALUES (1, 'basebackup')"); err != nil {
		t.Fatalf("insert basebackup row: %v", err)
	}

	// Create physical replication slot for the PG standby.
	if err := runSQLSimple(t, primary,
		fmt.Sprintf("SELECT pg_create_physical_replication_slot('%s')", forwardSlot)); err != nil {
		t.Fatalf("create replication slot %s: %v", forwardSlot, err)
	}

	// pg_basebackup from goopg primary.
	standbyDir := filepath.Join(baseDir, "pg-standby")
	t.Cleanup(func() {
		pgLogPath := filepath.Join(filepath.Dir(standbyDir), "pg.log")
		if pgLog, rerr := os.ReadFile(pgLogPath); rerr == nil {
			t.Logf("[s10-pg-standby-log] %s:\n%s", pgLogPath, string(pgLog))
		}
	})
	// createSlot=false: the slot was already created above via
	// pg_create_physical_replication_slot(). `-C` on an existing slot is a
	// hard error in upstream pg_basebackup.
	runGoopgBasebackupToPGSlot(t, repo, pgBasebackupBin, primary, standbyDir, forwardSlot, false)

	// Copy relcache init files so PG can resolve catalog entries.
	copyInitFiles(t, primary.DataDir(), standbyDir)

	// Configure PG standby.
	configurePGStandbyFromGoopgBackup(t, standbyDir,
		primaryConninfoForPGStandby(primary, forwardSlot), forwardSlot)

	// Start PG standby from the goopg-created base backup.
	standby, err := pgcluster.OpenExisting("s10-pg-standby", pgcluster.Options{
		RepoRoot:    repo,
		DataDir:     standbyDir,
		StartupWait: 45 * time.Second,
	})
	if err != nil {
		t.Fatalf("pgcluster.OpenExisting standby: %v", err)
	}
	defer func() { _ = standby.Stop() }()
	if err := standby.Start(); err != nil {
		t.Fatalf("standby.Start: %v", err)
	}

	// Verify the basebackup row is visible on the standby.
	if err := runSQLSimple(t, primary,
		"INSERT INTO public.s10_t (id, val) VALUES (2, 'post-backup')"); err != nil {
		t.Fatalf("insert post-backup row: %v", err)
	}

	readyCtx, readyCancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer readyCancel()
	if err := standby.WaitReady(readyCtx, 90*time.Second); err != nil {
		t.Logf("standby.WaitReady: %v (continuing anyway)", err)
		pgLogPath := filepath.Join(filepath.Dir(standbyDir), "pg.log")
		if pgLog, rerr := os.ReadFile(pgLogPath); rerr == nil {
			t.Logf("PG standby log:\n%s", string(pgLog))
		}
	}

	waitForPhysicalStreamingGoopgToPG(t, primary, standby, forwardSlot, false, 45*time.Second)

	// Verify the basebackup and post-backup rows are visible on the standby.
	if got := pgScalar(t, standby,
		"SELECT val FROM public.s10_t WHERE id = 1"); got != "basebackup" {
		t.Fatalf("standby row id=1 = %q, want basebackup", got)
	}
	if got := pgScalar(t, standby,
		"SELECT val FROM public.s10_t WHERE id = 2"); got != "post-backup" {
		t.Fatalf("standby row id=2 = %q, want post-backup", got)
	}

	// ── Phase B: DDL + DML workload on primary, verify on standby ──

	// DDL: CREATE TABLE with a different shape.
	if err := runSQLSimple(t, primary,
		"CREATE TABLE public.s10_ddl (a int, b text, c float8)"); err != nil {
		t.Fatalf("create s10_ddl: %v", err)
	}
	if err := runSQLSimple(t, primary,
		"INSERT INTO public.s10_ddl (a, b, c) VALUES (10, 'ddl-row', 3.14)"); err != nil {
		t.Fatalf("insert ddl row: %v", err)
	}

	// DDL: CREATE INDEX.
	if err := runSQLSimple(t, primary,
		"CREATE INDEX s10_t_val_idx ON public.s10_t (val)"); err != nil {
		t.Fatalf("create index: %v", err)
	}

	// DDL: ADD COLUMN.
	if err := runSQLSimple(t, primary,
		"ALTER TABLE public.s10_t ADD COLUMN extra int DEFAULT 0"); err != nil {
		t.Fatalf("add column: %v", err)
	}
	if err := runSQLSimple(t, primary,
		"INSERT INTO public.s10_t (id, val, extra) VALUES (3, 'with-extra', 99)"); err != nil {
		t.Fatalf("insert with extra column: %v", err)
	}

	// Verify DDL replay on standby: the new table, index, and column exist.
	waitForPGCount(t, standby,
		"SELECT count(*) FROM public.s10_ddl WHERE a = 10", 1, 30*time.Second)
	if got := pgScalar(t, standby,
		"SELECT c FROM public.s10_ddl WHERE a = 10"); got != "3.14" {
		t.Fatalf("standby s10_ddl.c = %q, want 3.14", got)
	}

	// Verify the ADD COLUMN took effect on the standby.
	waitForPGCount(t, standby,
		"SELECT count(*) FROM public.s10_t WHERE id = 3 AND extra = 99", 1, 30*time.Second)

	// Verify the index exists on the standby.
	if got := pgScalar(t, standby,
		"SELECT count(*) FROM pg_class WHERE relname = 's10_t_val_idx' AND relkind = 'i'"); got != "1" {
		t.Fatalf("standby index s10_t_val_idx count = %q, want 1", got)
	}

	// ── Phase C: Failover — kill goopg primary, promote PG standby ──

	if err := primary.Kill(); err != nil {
		t.Fatalf("primary.Kill: %v", err)
	}
	if err := standby.Promote(); err != nil {
		t.Fatalf("pg standby promote: %v", err)
	}

	// Verify promoted PG is writable.
	standby.Exec(t, "INSERT INTO public.s10_t (id, val, extra) VALUES (4, 'post-promote', 1)")
	if got := pgScalar(t, standby,
		"SELECT val FROM public.s10_t WHERE id = 4"); got != "post-promote" {
		t.Fatalf("post-promote row id=4 = %q, want post-promote", got)
	}

	// ── Phase D: Reverse attach — goopg standby against promoted PG ──

	// Create a replication slot on the promoted PG for the reverse goopg standby.
	standby.Exec(t, fmt.Sprintf("SELECT pg_create_physical_replication_slot('%s')", reverseSlot))

	// pg_basebackup from the promoted PG for the reverse goopg standby.
	reverseDir := filepath.Join(baseDir, "goopg-reverse-standby")
	// Phase D runs inside t.TempDir(), which Go removes when the test ends —
	// so a reverse-standby start failure would otherwise point at a path that
	// no longer exists by the time anyone looks. Dump the log inline, the same
	// way the PG standby's log is dumped above.
	t.Cleanup(func() {
		if log, rerr := os.ReadFile(filepath.Join(reverseDir, "cluster.log")); rerr == nil {
			t.Logf("[s10-goopg-reverse-standby-log] %s/cluster.log:\n%s", reverseDir, string(log))
		}
	})
	runPGBasebackup(t, repo, pgBasebackupBin, standby, reverseDir, reverseSlot)
	normalizePGWALSegmentNames(t, reverseDir)
	configureGoopgStandbyFromPGBackup(t, reverseDir, standby.Conninfo(reverseSlot), reverseSlot)

	// Start goopg as a standby against the promoted PG primary.
	// The data dir was originally a goopg-created base backup (via pg_basebackup
	// from goopg), PG replayed WAL into it, promoted, and now goopg starts
	// against it — validating bidirectional cluster-directory compatibility
	// (goal 1) and the multi-timeline reverse attach (goal 4 + S8).
	reverseStandby, err := cluster.New("s10-goopg-reverse", cluster.Options{
		RepoRoot:     repo,
		DataDir:      reverseDir,
		StartupWait:  45 * time.Second,
		ShutdownWait: 10 * time.Second,
		SyncInit:     true,
		SyncRuntime:  true,
	})
	if err != nil {
		t.Fatalf("cluster.New reverse standby: %v", err)
	}
	defer func() { _ = reverseStandby.Stop(cluster.ShutdownImmediate) }()
	if err := reverseStandby.Start(); err != nil {
		t.Fatalf("reverseStandby.Start: %v", err)
	}

	// Verify streaming on the new timeline.
	waitForPhysicalStreamingPGtoGoopg(t, standby, reverseStandby, reverseSlot, 60*time.Second)

	// INSERT on promoted PG, verify visible on goopg reverse standby.
	standby.Exec(t, "INSERT INTO public.s10_t (id, val, extra) VALUES (5, 'reverse-attach', 2)")
	waitForGoopgCount(t, reverseStandby,
		"SELECT count(*) FROM public.s10_t WHERE id = 5 AND val = 'reverse-attach'",
		1, 30*time.Second)

	// Verify all prior rows are visible on the reverse standby, proving
	// the full history survived the cycle.
	for _, tc := range []struct {
		id  int
		val string
	}{
		{1, "basebackup"},
		{2, "post-backup"},
		{3, "with-extra"},
		{4, "post-promote"},
		{5, "reverse-attach"},
	} {
		if got := goopgScalar(t, reverseStandby,
			fmt.Sprintf("SELECT val FROM public.s10_t WHERE id = %d", tc.id)); got != tc.val {
			t.Fatalf("reverse standby row id=%d = %q, want %q", tc.id, got, tc.val)
		}
	}

	// Verify DDL tables exist on reverse standby.
	if got := goopgScalar(t, reverseStandby,
		"SELECT count(*) FROM public.s10_ddl WHERE a = 10"); got != "1" {
		t.Fatalf("reverse standby s10_ddl count = %q, want 1", got)
	}

	t.Logf("[M0130-S10] full cycle PASSED: goopg→PG standby→promote→goopg reverse standby")
}

// waitForPhysicalStreamingPGtoGoopg waits for the goopg standby to reach
// streaming state when connected to a PG primary.
func waitForPhysicalStreamingPGtoGoopg(t *testing.T, pgPrimary *pgcluster.Cluster, goopgStandby *cluster.Cluster, appName string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		// Check PG side: the standby appears in pg_stat_replication.
		pgReady := pgPrimary.QueryScalar(t,
			fmt.Sprintf("SELECT count(*) FROM pg_stat_replication WHERE application_name = '%s' AND state = 'streaming'", appName)) == "1"

		// Check goopg side: pg_stat_wal_receiver reports streaming.
		goopgReady := false
		rows, err := goopgStandby.Query(context.Background(),
			"SELECT status FROM pg_catalog.pg_stat_wal_receiver")
		if err == nil && len(rows) > 0 && len(rows[0]) > 0 && rows[0][0] == "streaming" {
			goopgReady = true
		}

		if pgReady && goopgReady {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("PG→goopg physical replication did not reach streaming state within %s", timeout)
}
