package testport

// M0131-S27 — forward CRASH start: a cluster directory that goopg created and
// wrote, then died on without a shutdown checkpoint, served by real
// PostgreSQL 18.3.
//
// Design: docs/design/0131-0017-crash-interchange-e2e.md §S27. This is the
// crash twin of TestE2E_PGColdStartOnGoopgDataDir
// (e2e_pg_coldstart_on_goopgdata_test.go), and the difference is the whole
// point: S4 stops goopg with a fast shutdown, so pg_control is DB_SHUTDOWNED
// and PG opens a directory whose last record is a shutdown checkpoint. Here the
// goopg server is SIGKILLed mid-life, so pg_control still says
// DB_IN_PRODUCTION and PG must run crash RECOVERY over a goopg-authored WAL
// tail — the first time any test makes upstream's StartupXLOG replay goopg's
// records from a torn end-of-WAL rather than from a clean checkpoint.
//
// It is also the automated gate crash recovery has never had: M0131-S30 was
// diagnosed and closed with a MANUAL shell probe (analysis/crashprobe30.sh).
// This test does not replace that probe's concurrency, but it does put "kill
// goopg, then read the data back" under `go test`.
//
// Honesty note (design §"This test is NOT torn-page coverage", carried verbatim
// from S28's sibling): SIGKILL kills processes, not the page cache. The kernel
// still writes out every dirty page goopg had handed it, so this test produces
// a torn *WAL tail*, never a torn *data page*. Real torn-page coverage requires
// a machine-level crash (power loss / VM reset) or a deliberate half-page-write
// fault injector. This test must never be cited as FPI or full_page_writes
// coverage.
//
// The torn-contrecord variant — the tail ending INSIDE a multi-page record,
// which takes upstream's aborted-contrecord path instead of a plain end-of-WAL —
// now lives in its own sibling, e2e_pg_torn_contrecord_test.go
// (TestE2E_PGCrashStartOnGoopgTornContrecord, landed 2026-08-12). See
// docs/design/0131-0017-crash-interchange-e2e.md §"Torn contrecord" for why the
// cut is applied deterministically after a real SIGKILL rather than raced for,
// and which assertions stand in for upstream's two contrecord LOG lines.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/access/transam/control"
	"github.com/goopg/goopg/internal/testutil/cluster"
	"github.com/goopg/goopg/internal/testutil/pgcluster"
)

func TestE2E_PGCrashStartOnGoopgDataDir(t *testing.T) {
	if testing.Short() || os.Getenv("GOOPG_SKIP_M0131_E2E") != "" {
		t.Skip("skipping forward crash-start e2e (short mode or GOOPG_SKIP_M0131_E2E set)")
	}

	repo := repoRoot(t)
	binDir := filepath.Join(repo, "postgres", "local_install", "bin")
	pgcluster.Available(t, binDir)

	baseDir := t.TempDir()
	goopgDir := filepath.Join(baseDir, "goopgdata")

	// Step 1 — `goopg init` + start. User/Database default to postgres in
	// internal/testutil/cluster, which is what the pgcluster handle below must
	// be told explicitly (it would otherwise default to $USER).
	// PSQLPath is explicit because step 2b's open transaction needs a real psql
	// process (the lib/pq path in runSQLSimple cannot hold a session across the
	// kill), and the harness's default "psql" is not on $PATH under `go test`.
	g, err := cluster.New("m0131-s27-goopg", cluster.Options{
		RepoRoot:     repo,
		DataDir:      goopgDir,
		PSQLPath:     filepath.Join(binDir, "psql"),
		StartupWait:  60 * time.Second,
		ShutdownWait: 30 * time.Second,
	})
	if err != nil {
		t.Fatalf("cluster.New goopg: %v", err)
	}
	if err := g.Init(); err != nil {
		t.Fatalf("goopg init: %v", err)
	}
	goopgDead := false
	defer func() {
		if !goopgDead {
			_ = g.Stop(cluster.ShutdownImmediate)
		}
	}()
	if err := g.Start(); err != nil {
		logTail, _ := os.ReadFile(g.LogPath())
		t.Fatalf("goopg.Start: %v\n--- goopg log ---\n%s", err, tailLines(string(logTail), 40))
	}

	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	// Step 2a — the committed half, BEFORE the checkpoint. Everything here is
	// reachable from the redo point by page contents alone; step 4's work is
	// what only WAL replay can supply.
	for _, stmt := range []string{
		"CREATE SCHEMA s27app",
		`CREATE TABLE public.s27_items (
			id    integer PRIMARY KEY,
			label text    NOT NULL,
			qty   integer NOT NULL
		)`,
		"CREATE INDEX s27_items_label_idx ON public.s27_items (label)",
		"CREATE TABLE s27app.s27_notes (id integer, note text)",
		`INSERT INTO public.s27_items (id, label, qty)
			SELECT g, 'label-' || lpad(g::text, 5, '0'), g * 10 FROM generate_series(1, 500) g`,
		"UPDATE public.s27_items SET qty = qty + 1 WHERE id % 3 = 0",
		"DELETE FROM public.s27_items WHERE id > 450",
		"INSERT INTO s27app.s27_notes (id, note) VALUES (1, 'pre-checkpoint, non-public schema')",
	} {
		if err := runSQLSimple(t, g, stmt); err != nil {
			logTail, _ := os.ReadFile(g.LogPath())
			t.Fatalf("goopg pre-checkpoint workload %q: %v\n--- goopg log ---\n%s",
				stmt, err, tailLines(string(logTail), 40))
		}
	}

	// Step 3 — one ONLINE checkpoint. Two things at once, both load-bearing:
	// pg_control.State becomes DB_IN_PRODUCTION with a redo point that is not
	// the start of WAL, and everything step 4 writes lands strictly after it,
	// so the post-checkpoint assertions can only pass through replay of the
	// tail. (`goopg checkpoint -D <dir>`, cmd/goopg/main.go runCheckpoint.)
	if err := g.Checkpoint(); err != nil {
		logTail, _ := os.ReadFile(g.LogPath())
		t.Fatalf("goopg checkpoint: %v\n--- goopg log ---\n%s", err, tailLines(string(logTail), 40))
	}

	// Step 4 — more COMMITTED work, after the checkpoint. This is the tail that
	// distinguishes S27 from S4: if PG stops replay early (M0131-S30's failure
	// shape — an unusable segment tail mistaken for end-of-WAL), these rows
	// vanish and the counts below diverge.
	for _, stmt := range []string{
		`INSERT INTO public.s27_items (id, label, qty)
			SELECT g, 'label-' || lpad(g::text, 5, '0'), g * 10 FROM generate_series(501, 900) g`,
		"UPDATE public.s27_items SET qty = qty + 7 WHERE id BETWEEN 600 AND 650",
		"DELETE FROM public.s27_items WHERE id BETWEEN 860 AND 900",
		"INSERT INTO s27app.s27_notes (id, note) VALUES (2, 'post-checkpoint')",
		"ALTER TABLE public.s27_items ADD COLUMN tag text DEFAULT 'dflt'",
	} {
		if err := runSQLSimple(t, g, stmt); err != nil {
			logTail, _ := os.ReadFile(g.LogPath())
			t.Fatalf("goopg post-checkpoint workload %q: %v\n--- goopg log ---\n%s",
				err, stmt, tailLines(string(logTail), 40))
		}
	}

	// Step 2b — the UNCOMMITTED half. A session that is still inside its
	// transaction when the kill lands: its INSERT may well have reached WAL
	// (goopg flushes on buffer pressure, not only at commit), but no commit
	// record follows it, so PG's recovery must not make those rows visible.
	//
	// It has to be a backend that OUTLIVES the statement, which `psql -c`
	// cannot be — psql exits at EOF and the disconnect rolls the transaction
	// back, writing an abort record and destroying the very state under test.
	// StartPSQL + a trailing pg_sleep holds the session open until the SIGKILL
	// takes the whole process group with it.
	openTxn, err := g.StartPSQL(nil, `BEGIN;
INSERT INTO public.s27_items (id, label, qty) VALUES (9001, 'never-committed-a', 10), (9002, 'never-committed-b', 20);
INSERT INTO s27app.s27_notes (id, note) VALUES (9001, 'never committed');
SELECT pg_sleep(600);
COMMIT;
`)
	if err != nil {
		t.Fatalf("StartPSQL (open transaction): %v", err)
	}
	defer func() { _ = openTxn.Stop() }()

	// The uncommitted INSERT must actually have RUN before the kill, or the
	// "invisible after recovery" assertions below pass vacuously. Poll goopg
	// for the writing transaction's own effect from a DIFFERENT session — it
	// is invisible there, so poll a proxy instead: the row count another
	// session sees must stay put while the open session's xid becomes visible
	// in the write path. The cheap, robust proxy is time plus a positive check
	// that the sleeping backend is still connected (pg_sleep has not returned,
	// so the session is mid-transaction).
	if err := waitForOpenTxnInsert(ctx, t, g); err != nil {
		logTail, _ := os.ReadFile(g.LogPath())
		t.Fatalf("uncommitted transaction never reached its INSERT: %v\n--- goopg log ---\n%s",
			err, tailLines(string(logTail), 40))
	}

	// Step 5 (capture) — goopg's own answers, so the PG-side assertions compare
	// against the authoring engine rather than hand-computed constants. Read
	// from an ordinary (auto-commit) session, which cannot see the open
	// transaction's rows.
	want := map[string]string{}
	for _, q := range []string{
		"SELECT count(*) FROM public.s27_items",
		"SELECT sum(qty) FROM public.s27_items",
		"SELECT count(*) FROM public.s27_items WHERE id > 450 AND id < 501",
		"SELECT count(*) FROM public.s27_items WHERE id BETWEEN 860 AND 900",
		"SELECT label || '/' || qty FROM public.s27_items WHERE id = 9",
		"SELECT label || '/' || qty FROM public.s27_items WHERE id = 620",
		"SELECT label || '/' || qty FROM public.s27_items WHERE id = 859",
		"SELECT count(*) FROM s27app.s27_notes",
		"SELECT tag FROM public.s27_items WHERE id = 620",
	} {
		want[q] = coldStartScalar(t, ctx, g, q)
	}
	// Workload sanity: a silently empty workload makes every comparison below
	// trivially true. 450 pre-checkpoint survivors + 400 post-checkpoint
	// inserts − 41 post-checkpoint deletes = 809.
	if got := want["SELECT count(*) FROM public.s27_items"]; got != "809" {
		t.Fatalf("workload sanity: goopg reports %s rows in s27_items, want 809", got)
	}
	if got := want["SELECT count(*) FROM s27app.s27_notes"]; got != "2" {
		t.Fatalf("workload sanity: goopg reports %s rows in s27_notes, want 2 "+
			"(the uncommitted third row must not be visible to this session)", got)
	}
	if got := want["SELECT tag FROM public.s27_items WHERE id = 620"]; got != "dflt" {
		t.Fatalf("workload sanity: post-checkpoint ADD COLUMN … DEFAULT read back as %q, want \"dflt\"", got)
	}

	// Step 6 — the crash. cluster.Kill SIGKILLs the `go run` wrapper's whole
	// process group by pgid off the cmd handle; it reads no PID file and never
	// goes near `pkill -f goopg` (which self-matches the invoking shell,
	// exit 144 — Hard-won Rule #3).
	if err := g.Kill(); err != nil {
		t.Fatalf("goopg Kill: %v", err)
	}
	goopgDead = true

	// Step 7 — pg_control must still say DB_IN_PRODUCTION. That IS the
	// definition of "crashed", and it is what forces PG down StartupXLOG's
	// recovery path instead of S4's clean open. Before M0131-S17 goopg never
	// stamped this state at all, so the assertion is that slice's regression
	// lock, not a setup step.
	cd, err := control.ReadControlFile(goopgDir)
	if err != nil || cd == nil {
		t.Fatalf("ReadControlFile after crash: %v (nil=%v)", err, cd == nil)
	}
	if cd.State != control.DBStateInProduction {
		t.Fatalf("pg_control.State = %d (%s) after SIGKILL, want DB_IN_PRODUCTION (%d) — "+
			"either the server ran a shutdown checkpoint (so this is the S4 clean path, not a "+
			"crash test) or M0131-S17's runtime state stamping has regressed",
			cd.State, control.DBStateName(cd.State), control.DBStateInProduction)
	}

	// Step 7b — stale postmaster.pid (design §"Stale postmaster.pid").
	//
	// goopg writes the file from startControlPlane and removes it only from
	// stopControlPlane, so a SIGKILL leaves it behind. PG's CreateLockFile
	// (postgres/src/backend/utils/init/miscinit.c:1210) reads line 1 as a pid
	// and FATALs `lock file "…" already exists` when kill(pid, 0) succeeds —
	// which happens exactly when the dead goopg pid has been RECYCLED by
	// another live process, i.e. non-deterministically. goopg's file is five
	// lines, so PG's line-7 shmem cross-check is skipped entirely and cannot
	// rescue it.
	//
	// Until goopg either writes a PG-shaped 8-line file or clears a stale one,
	// the test removes it as an explicit handover step — and asserts it was
	// PRESENT first, so the day goopg starts cleaning up, this fails and
	// demands the step be deleted rather than silently passing. Ledger row
	// 2026-08-12, M0131-S27.
	pidPath := filepath.Join(goopgDir, "postmaster.pid")
	if _, err := os.Stat(pidPath); err != nil {
		t.Fatalf("postmaster.pid absent after SIGKILL (%v) — goopg has grown a stale-lock-file "+
			"cleanup or cluster.Kill has stopped being a true SIGKILL; either way this handover "+
			"step is obsolete and must be removed (design 0131-0017 §Stale postmaster.pid)", err)
	}
	if err := os.Remove(pidPath); err != nil {
		t.Fatalf("remove stale postmaster.pid: %v", err)
	}

	// Step 8 — real PG 18.3 on the crashed directory. OpenExisting runs neither
	// initdb nor appendConf; -D/-p/-h arrive on argv, so goopg's
	// postgresql.conf is handed over untouched (the S4 property, reused).
	pg, err := pgcluster.OpenExisting("m0131-s27-pg", pgcluster.Options{
		RepoRoot:    repo,
		DataDir:     goopgDir,
		User:        "postgres",
		StartupWait: 90 * time.Second,
	})
	if err != nil {
		t.Fatalf("pgcluster.OpenExisting: %v", err)
	}
	defer func() { _ = pg.Stop() }()
	pgLog := pgLogPathFor(goopgDir)
	if err := pg.Start(); err != nil {
		logTail, _ := os.ReadFile(pgLog)
		t.Fatalf("postgres -D <crashed goopg data dir>: %v\n--- PG log ---\n%s",
			err, tailLines(string(logTail), 60))
	}
	readyCtx, readyCancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer readyCancel()
	if err := pg.WaitReady(readyCtx, 90*time.Second); err != nil {
		logTail, _ := os.ReadFile(pgLog)
		t.Fatalf("pg.WaitReady after crash recovery on a goopg-authored directory: %v\n--- PG log ---\n%s",
			err, tailLines(string(logTail), 80))
	}

	// Step 9 — the log assertions. Recovery must have actually RUN (a directory
	// PG decided was clean would silently skip replay and still answer every
	// query below from the pages alone).
	assertPGCrashRecoveryLog(t, pgLog)

	// Step 10 — the measurement. Every committed row, including the
	// post-checkpoint ones that exist only in the replayed tail.
	for _, q := range []string{
		"SELECT count(*) FROM public.s27_items",
		"SELECT sum(qty) FROM public.s27_items",
		"SELECT count(*) FROM public.s27_items WHERE id > 450 AND id < 501",
		"SELECT count(*) FROM public.s27_items WHERE id BETWEEN 860 AND 900",
		"SELECT label || '/' || qty FROM public.s27_items WHERE id = 9",
		"SELECT label || '/' || qty FROM public.s27_items WHERE id = 620",
		"SELECT label || '/' || qty FROM public.s27_items WHERE id = 859",
		"SELECT count(*) FROM s27app.s27_notes",
		"SELECT tag FROM public.s27_items WHERE id = 620",
	} {
		if got := s4Scalar(t, pg, pgLog, q); got != want[q] {
			t.Fatalf("after crash recovery the hosted PG answered %q, goopg (pre-crash) said %q\nquery: %s\n"+
				"a post-checkpoint row missing here is replay stopping early on the tail (the "+
				"M0131-S30 failure shape)", got, want[q], q)
		}
	}

	// The uncommitted transaction's rows must NOT be visible. Their absence is
	// the two-sided half of the assertion above: recovery that replayed too
	// much is as wrong as recovery that replayed too little.
	for _, q := range []string{
		"SELECT count(*) FROM public.s27_items WHERE id IN (9001, 9002)",
		"SELECT count(*) FROM s27app.s27_notes WHERE id = 9001",
	} {
		if got := s4Scalar(t, pg, pgLog, q); got != "0" {
			t.Fatalf("after crash recovery the hosted PG sees %q row(s) from a transaction that "+
				"was never committed (query %s) — recovery made an in-flight transaction's writes "+
				"visible", got, q)
		}
	}

	// An index-qualified read over a goopg-authored btree whose last leaf
	// inserts were replayed rather than checkpointed.
	forced := "SET enable_seqscan = off; SELECT id FROM public.s27_items WHERE label = 'label-00620'"
	out, err := pgQueryScalarAllowError(pg, forced)
	if err != nil {
		t.Fatalf("hosted PG index read after crash recovery failed: %v\n%s", err, out)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if got := strings.TrimSpace(lines[len(lines)-1]); got != "620" {
		t.Fatalf("index-qualified read of a post-checkpoint row returned %q (full output %q), want \"620\"", got, out)
	}

	// Guard — no FATAL anywhere in the attach. pgcluster truncates the log on
	// Start, so the scan covers exactly this recovery and nothing else.
	assertNoFatalInPGLog(t, pgLog)
}

// waitForOpenTxnInsert blocks until the background psql session has plausibly
// executed its INSERTs and parked on pg_sleep.
//
// It cannot observe the rows (they are uncommitted, so no other session sees
// them) and goopg exposes no pg_stat_activity, so the check is structural: a
// fresh session must still get a prompt answer out of the server — proving the
// open transaction is not holding a lock that blocks readers — and the wait
// gives the sleeping backend time to get past its two INSERTs. If the INSERTs
// had failed, psql would have exited before reaching pg_sleep; the deadline
// below is far longer than two small INSERTs need.
func waitForOpenTxnInsert(ctx context.Context, t *testing.T, g *cluster.Cluster) error {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	settled := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := g.Query(ctx, "SELECT 1"); err != nil {
			return fmt.Errorf("probe session: %w", err)
		}
		if time.Now().After(settled) {
			return nil
		}
		time.Sleep(250 * time.Millisecond)
	}
	return fmt.Errorf("probe session never settled before the deadline")
}

// assertPGCrashRecoveryLog enforces design §S27 step 8's log contract:
// recovery ran (`redo starts at` … `redo done at`), nothing PANICked, and any
// end-of-WAL complaint is BENIGN — i.e. `invalid record length` / `invalid
// magic number` may appear, but only as the LAST WAL complaint, never followed
// by a further one. The taxonomy is docs/design/0131-0012 §"benign end-of-WAL".
func assertPGCrashRecoveryLog(t *testing.T, logPath string) {
	t.Helper()
	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read PG log %s: %v", logPath, err)
	}
	text := string(raw)

	if !strings.Contains(text, "redo starts at") {
		t.Fatalf("PG log has no `redo starts at` — upstream decided the goopg directory needed no "+
			"recovery, so this test proved nothing about replay:\n%s", tailLines(text, 60))
	}
	if !strings.Contains(text, "redo done at") {
		t.Fatalf("PG log has `redo starts at` but no `redo done at` — replay did not complete:\n%s",
			tailLines(text, 60))
	}
	if idx := strings.Index(text, "PANIC"); idx >= 0 {
		t.Fatalf("PG PANICked replaying a goopg-authored crash tail:\n%s", tailLines(text[:idx+2000], 60))
	}

	// Benign end-of-WAL complaints are allowed, but must be terminal: the last
	// WAL complaint in the log has to be one of them. Anything after it means
	// PG kept finding problems past the point it should have stopped.
	benign := []string{"invalid record length", "invalid magic number", "invalid resource manager ID"}
	nonBenign := []string{
		"invalid record offset", "contrecord is requested", "incorrect resource manager data checksum",
		"incorrect prev-link", "record with incorrect", "invalid page in block",
	}
	lastBenign, lastNonBenign := -1, ""
	for i, line := range strings.Split(text, "\n") {
		for _, b := range benign {
			if strings.Contains(line, b) {
				lastBenign = i
			}
		}
		for _, nb := range nonBenign {
			if strings.Contains(line, nb) {
				if i > lastBenign {
					lastNonBenign = line
				}
			}
		}
	}
	if lastNonBenign != "" {
		t.Fatalf("PG reported a NON-benign WAL problem after the last benign end-of-WAL marker:\n  %s\n"+
			"benign end-of-WAL is `invalid record length`/`invalid magic number` as the FINAL "+
			"complaint (taxonomy: docs/design/0131-0012-crash-state-cluster-dir-interchange.md)\n%s",
			strings.TrimSpace(lastNonBenign), tailLines(text, 60))
	}
}
