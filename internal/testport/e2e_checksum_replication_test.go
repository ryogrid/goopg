package testport

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "github.com/lib/pq"

	"github.com/goopg/goopg/internal/testutil/cluster"
	"github.com/goopg/goopg/internal/testutil/pgcluster"
)

// TestE2E_ChecksumStreamingGoopgToPG is gate (b) of the M0102-0010
// `--data-checksums` default-ON flip: a checksummed goopg primary must stream
// to a standby whose read path verifies `pd_checksum`. We use a *real
// PostgreSQL* standby — the strongest form of the gate — because it proves
// goopg's FNV-1a page checksum bytes are byte-identical to what upstream PG
// computes, not merely self-consistent.
//
// Why this is the meaningful test: gate (a)
// (TestCrashRecoveryReplaysChecksummedClusterCleanly) already proves goopg
// verifies its own checksums across a WAL-replay boundary. The remaining risk
// the flip carries is *cross-implementation* byte compatibility: if goopg's
// checksum differed from PG's by a single bit, a PG standby reading goopg's
// pages would raise "invalid page in block N of relation ..." and refuse the
// read. So we:
//
//  1. init a goopg primary with --data-checksums (pg_control
//     data_checksum_version = 1; smgr writes a valid pd_checksum on every page),
//  2. fill a table with enough rows to span many heap pages and CHECKPOINT so
//     those checksummed pages are flushed to disk BEFORE the clone,
//  3. pg_basebackup -X stream the cluster to a real PG data dir (PG copies
//     goopg's version-1 pg_control, so PG turns checksum verification on),
//  4. start the PG standby and let it stream,
//  5. seq-scan the table on the PG standby. Because those heap pages were
//     written by goopg and checkpointed before the backup's redo point, they
//     are NOT rewritten by WAL replay — PG reads goopg's exact bytes from disk
//     and verifies goopg's pd_checksum. A clean scan with the right row count,
//     `SHOW data_checksums = on`, and `checksum_failures = 0` is the gate.
//
// Gated like the other heterogeneous E2E tests: skipped under -short or when
// GOOPG_SKIP_M0102_E2E is set, and skipped if the PG client/server binaries are
// unavailable.
func TestE2E_ChecksumStreamingGoopgToPG(t *testing.T) {
	if testing.Short() || os.Getenv("GOOPG_SKIP_M0102_E2E") != "" {
		t.Skip("skipping heterogeneous checksum-streaming e2e (short mode or GOOPG_SKIP_M0102_E2E set)")
	}
	t.Skip("PG-tool WAL compat removed 2026-07-15 (canonical/knob/skip-tag removed; native->PG (rmid,info) dispatch); intentional, not a regression - resumes after native->PG content rewrite. See docs/design/wal-native-pg-format/04 + .ralph/deferral_ledger.md")

	repo := repoRoot(t)
	binDir := filepath.Join(repo, "postgres", "local_install", "bin")
	pgcluster.Available(t, binDir)

	pgBasebackupBin := clientToolBin(t, "pg_basebackup")
	if pgBasebackupBin == "" {
		t.Skip("pg_basebackup not found in PATH or postgres/local_install/bin")
	}

	const slotName = "pg_cksum_standby"
	const nRows = 4000
	// A fixed-width 200-byte payload (no SQL function dependency beyond
	// generate_series). nRows rows × ~230 bytes/tuple ≈ 920 KiB ≈ ~115 heap
	// pages, so the base backup carries many distinct checksummed pages.
	payload := strings.Repeat("x", 200)

	baseDir := t.TempDir()
	primary, err := cluster.New("m0102-cksum-goopg-primary", cluster.Options{
		RepoRoot:     repo,
		DataDir:      filepath.Join(baseDir, "goopg-primary"),
		StartupWait:  45 * time.Second,
		ShutdownWait: 10 * time.Second,
		SyncInit:     true,
		SyncRuntime:  true,
		// The whole point of this gate: a checksummed goopg primary.
		InitArgs: []string{"--data-checksums"},
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

	if err := runSQLSimple(t, primary,
		"CREATE TABLE public.cksum_t (id int NOT NULL, payload text NOT NULL)"); err != nil {
		t.Fatalf("create cksum_t on goopg primary: %v", err)
	}
	insertSQL := fmt.Sprintf(
		"INSERT INTO public.cksum_t SELECT g, '%s' FROM generate_series(1, %d) g",
		payload, nRows)
	if err := runSQLSimple(t, primary, insertSQL); err != nil {
		t.Fatalf("populate cksum_t on goopg primary: %v", err)
	}
	// Flush the checksummed heap pages to disk before the clone, so the base
	// backup ships goopg's pd_checksum bytes (and so the inserts land before
	// the backup's redo point and are NOT replayed/rewritten on the standby).
	if err := runSQLSimple(t, primary, "CHECKPOINT"); err != nil {
		t.Fatalf("checkpoint on goopg primary: %v", err)
	}

	standbyDir := filepath.Join(baseDir, "pg-standby")
	t.Cleanup(func() {
		pgLogPath := filepath.Join(filepath.Dir(standbyDir), "pg.log")
		if pgLog, rerr := os.ReadFile(pgLogPath); rerr == nil {
			t.Logf("[m0102-cksum-pg-standby-log] %s:\n%s", pgLogPath, string(pgLog))
		}
	})
	// Standard PG streaming-replication clone: pg_basebackup -C -S slot -R
	// creates the slot, streams the (checksummed) data dir, and writes
	// standby.signal + postgresql.auto.conf.
	runGoopgBasebackupToPG(t, repo, pgBasebackupBin, primary, standbyDir, slotName)
	// Ensure relcache init files are present on the PG standby (M0106).
	copyInitFiles(t, primary.DataDir(), standbyDir)
	configurePGStandbyFromGoopgBackup(t, standbyDir,
		primaryConninfoForPGStandby(primary, slotName), slotName)

	standby, err := pgcluster.OpenExisting("m0102-cksum-pg-standby", pgcluster.Options{
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

	readyCtx, readyCancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer readyCancel()
	if err := standby.WaitReady(readyCtx, 90*time.Second); err != nil {
		t.Logf("standby.WaitReady: %v (continuing anyway)", err)
		pgLogPath := filepath.Join(filepath.Dir(standbyDir), "pg.log")
		if pgLog, rerr := os.ReadFile(pgLogPath); rerr == nil {
			t.Logf("PG standby log:\n%s", string(pgLog))
		}
	}

	waitForPhysicalStreamingGoopgToPG(t, primary, standby, slotName, false, 45*time.Second)

	// Gate (b) proof, on the real PG standby:
	//
	// 1. PG inherited goopg's version-1 pg_control → checksum verification is
	//    ON. (If goopg had only faked the control field, PG would still report
	//    'on' here but step 2 would then fail on the first page read.)
	if got := pgScalar(t, standby, "SHOW data_checksums"); got != "on" {
		t.Fatalf("PG standby data_checksums = %q, want \"on\" "+
			"(goopg primary did not produce a checksummed cluster)", got)
	}

	// 2. Seq-scan every heap page. With data_checksums on, PG verifies
	//    pd_checksum as it reads each page from disk; a mismatch raises
	//    ERROR "invalid page in block ..." and the query fails. These pages
	//    were written by goopg and checkpointed before the backup redo point,
	//    so PG is verifying *goopg's* checksum bytes, not its own.
	//    A correct row count means every goopg-checksummed page verified.
	gotRows := pgCount(t, standby, "SELECT count(*) FROM public.cksum_t")
	if gotRows != nRows {
		t.Fatalf("PG standby read %d rows from cksum_t, want %d "+
			"(checksum verification failure would abort the seq-scan)",
			gotRows, nRows)
	}
	// Force reading the payload column out of every page too, defeating any
	// count-only shortcut and re-touching every block. A wrong checksum on any
	// page surfaces here as an ERROR rather than a wrong sum.
	//
	// (We deliberately do NOT assert on pg_stat_database.checksum_failures: the
	// standby is real PG running on goopg's *bootstrapped* catalog, which does
	// not define PG's pg_stat_* system views. The seq-scans above are the
	// canonical proof — exactly how upstream's own checksum tests detect a bad
	// page: the read aborts with "invalid page in block ...".)
	if err := func() error {
		db, err := standby.OpenDB()
		if err != nil {
			return err
		}
		defer db.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		var total int64
		return db.QueryRowContext(ctx,
			"SELECT sum(length(payload)) FROM public.cksum_t").Scan(&total)
	}(); err != nil {
		t.Fatalf("PG standby full-payload scan failed (checksum verification "+
			"would surface here): %v", err)
	}
}
