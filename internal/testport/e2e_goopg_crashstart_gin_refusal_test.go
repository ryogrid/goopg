package testport

// M0131-S28, GIN-refusal variant — the OTHER half of the reverse crash E2E.
//
// TestE2E_GoopgCrashStartOnPGDataDir (e2e_goopg_crashstart_on_pgdata_test.go)
// proves goopg can replay a crash tail a real PostgreSQL 18.3 authored. This
// test proves the complementary property, which is just as much a correctness
// property: when the tail contains something goopg genuinely cannot replay, it
// must REFUSE — loudly, specifically, and with a non-zero exit — rather than
// start and serve an index that silently disagrees with its heap.
//
// The chosen unreplayable thing is a GIN index (RM_GIN_ID = 13,
// postgres/src/include/access/rmgrlist.h:43). M0131-S25 built the refusal:
// internal/wal/index_am_refusal.go names the access method, and
// preflightIndexAMRecords scans the replay range BEFORE the first page is
// written so one failed start reports the whole boundary and the data
// directory is left byte-identical for a real PG to finish recovering.
//
// Three things are asserted, and the design doc (§S28, "Second variant") is
// explicit that a skip is never an acceptable substitute for any of them:
//
//  1. the workload really did put RM_GIN records in the crash tail — verified
//     with upstream pg_waldump, not assumed from the DDL;
//  2. goopg exits NON-ZERO. An engine that logs a complaint and then serves
//     the directory anyway is the failure mode this whole slice exists to
//     prevent;
//  3. the message is S25's specific one — it names `gin`, rmid=13 and the
//     "nothing has been replayed" pre-flight wording. A generic
//     "unsupported xlog record rmid=13" would mean the pre-flight scan was
//     bypassed and the per-record arm caught it instead, i.e. after the prefix
//     had already been applied to the pages.
//
// Unlike the main variant, this test has NO self-arming skip and must never
// grow one: it does not depend on S21/S22/S24 opcode coverage, because the
// pre-flight scan runs before any record is applied. If S21's opcode work is
// incomplete, the GIN refusal still comes first — which is exactly the
// ordering guarantee being tested.

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/control"
	"github.com/goopg/goopg/internal/testutil/cluster"
	"github.com/goopg/goopg/internal/testutil/pgcluster"
)

func TestE2E_GoopgCrashStartOnPGDataDirGINRefusal(t *testing.T) {
	if testing.Short() || os.Getenv("GOOPG_SKIP_M0131_E2E") != "" {
		t.Skip("skipping GIN-refusal crash-start e2e (short mode or GOOPG_SKIP_M0131_E2E set)")
	}

	repo := repoRoot(t)
	binDir := filepath.Join(repo, "postgres", "local_install", "bin")
	pgcluster.Available(t, binDir)

	baseDir := t.TempDir()
	pgDir := filepath.Join(baseDir, "pgdata")

	// Same cluster shape as the main variant: user postgres so the PG-authored
	// pg_authid row matches the superuser goopg's harness connects as, and
	// wal_level=replica because that is the representative production setting.
	pg, err := pgcluster.New("m0131-s28gin-pg", pgcluster.Options{
		RepoRoot:    repo,
		DataDir:     pgDir,
		User:        "postgres",
		WalLevel:    "replica",
		StartupWait: 30 * time.Second,
	})
	if err != nil {
		t.Fatalf("pgcluster.New: %v", err)
	}
	pgDead := false
	defer func() {
		if !pgDead {
			_ = pg.Stop()
		}
	}()
	if err := pg.Start(); err != nil {
		t.Fatalf("pg.Start: %v", err)
	}
	readyCtx, readyCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer readyCancel()
	if err := pg.WaitReady(readyCtx, 30*time.Second); err != nil {
		t.Fatalf("pg.WaitReady: %v", err)
	}

	runGINWorkload(t, pg)

	if err := pg.KillHard(); err != nil {
		t.Fatalf("pg.KillHard: %v", err)
	}
	pgDead = true

	// Crash, not shutdown — the same two proofs the main variant carries. A
	// clean pg_control would send goopg down the S3 open path, where there is
	// no replay range to pre-flight and this test would pass vacuously.
	if _, err := os.Stat(filepath.Join(pgDir, "postmaster.pid")); err != nil {
		t.Fatalf("postmaster.pid absent after KillHard (%v) — pgcluster.KillHard has stopped "+
			"being a true SIGKILL, so this is no longer a crash test", err)
	}
	cd, err := control.ReadControlFile(pgDir)
	if err != nil || cd == nil {
		t.Fatalf("ReadControlFile after crash: %v (nil=%v)", err, cd == nil)
	}
	if cd.State == control.DBStateShutdowned {
		t.Fatalf("pg_control.State = DB_SHUTDOWNED after KillHard — the postmaster ran a "+
			"shutdown checkpoint, so goopg would take the clean-open path and never "+
			"reach the index-AM pre-flight (state=%d)", cd.State)
	}

	// Assertion 1: the tail really contains GIN records. Without this the test
	// could pass because goopg refused for some unrelated reason, or fail
	// mysteriously the day PG stops emitting GIN WAL for this workload.
	dump := dumpCrashTail(t, binDir, pgDir)
	if !strings.Contains(dump, "rmgr: Gin") {
		t.Fatalf("crash tail contains no `rmgr: Gin` record — the GIN workload did not reach WAL, "+
			"so goopg's refusal (or non-refusal) below would prove nothing\npg_waldump tail:\n%s",
			tailLines(dump, 20))
	}

	// Assertions 2 and 3 need the process's EXIT STATUS, which cluster.Start
	// does not surface (it reports "process exited early"), so goopg is run in
	// the foreground here. The cluster handle is built only for its data dir
	// and free listen address; Init() is deliberately NOT called — PG's own
	// directory is handed over untouched.
	g, err := cluster.New("m0131-s28gin-goopg", cluster.Options{
		RepoRoot:     repo,
		DataDir:      pgDir,
		StartupWait:  90 * time.Second,
		ShutdownWait: 20 * time.Second,
	})
	if err != nil {
		t.Fatalf("cluster.New goopg: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "run", "./cmd/goopg", "start", "-D", pgDir, "-listen", g.ListenAddr())
	cmd.Dir = repo
	out, runErr := cmd.CombinedOutput()
	output := string(out)

	if runErr == nil {
		// It started. Stop it before failing, so a regression does not leave a
		// listening server behind for every later test in the package.
		_ = g.Stop(cluster.ShutdownImmediate)
		t.Fatalf("goopg STARTED on a crashed PG data directory whose WAL tail contains GIN records — "+
			"it has no GIN redo, so the index it is now serving disagrees with its heap.\n"+
			"--- goopg output ---\n%s", tailLines(output, 40))
	}
	var exitErr *exec.ExitError
	if !errors.As(runErr, &exitErr) {
		t.Fatalf("goopg did not exit with a status (%v) — expected a non-zero exit, "+
			"not a spawn failure\n--- output ---\n%s", runErr, tailLines(output, 40))
	}
	if code := exitErr.ExitCode(); code == 0 {
		t.Fatalf("goopg exited 0 while reporting an error — a refusal that exits 0 is "+
			"indistinguishable from success to any supervisor\n--- output ---\n%s",
			tailLines(output, 40))
	}

	// Assertion 3: S25's specific message, checked fragment by fragment so a
	// failure says which part of the contract broke.
	for _, want := range []string{
		"index-AM records goopg has no redo for", // the pre-flight wording…
		"nothing has been replayed",              // …and its ordering guarantee
		"gin (rmid=13",                           // the access method, by name and by id
		"REINDEX",                                // the operator's way out
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("goopg's refusal does not contain %q — M0131-S25's specific index-AM message "+
				"(internal/wal/index_am_refusal.go preflightIndexAMRecords) has regressed to a "+
				"generic one\n--- output ---\n%s", want, tailLines(output, 40))
		}
	}
	if strings.Contains(output, "unsupported xlog record rmid=13") {
		t.Fatalf("goopg refused via the PER-RECORD arm, not the pre-flight scan — that happens only "+
			"after the prefix has already been applied, so the data directory has been mutated and "+
			"is no longer one a real PG can finish recovering\n--- output ---\n%s", tailLines(output, 40))
	}

	// The refusal's promise is that the directory is untouched, so PG must
	// still be able to finish recovery on it. That is the operator-visible
	// value of pre-flighting, and it is cheap to prove here.
	//
	// OpenExisting, not New: New runs initdb (and would refuse on a non-empty
	// directory anyway). The postgresql.conf PG itself wrote is the one that
	// must be reused — rewriting it here would change the very bytes the
	// assertion is about.
	pg2, err := pgcluster.OpenExisting("m0131-s28gin-pg-recover", pgcluster.Options{
		RepoRoot:    repo,
		DataDir:     pgDir,
		LogPath:     filepath.Join(baseDir, "pg-recover.log"),
		User:        "postgres",
		StartupWait: 60 * time.Second,
	})
	if err != nil {
		t.Fatalf("pgcluster.New (recover): %v", err)
	}
	defer func() { _ = pg2.Stop() }()
	// The stale postmaster.pid a true SIGKILL leaves behind is PG's own to
	// handle: it re-reads it, finds no live process, and proceeds.
	if err := pg2.Start(); err != nil {
		t.Fatalf("real PG could not finish recovery on the directory goopg refused: %v — "+
			"the pre-flight refusal is supposed to leave the directory byte-identical", err)
	}
	recoverCtx, recoverCancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer recoverCancel()
	if err := pg2.WaitReady(recoverCtx, 60*time.Second); err != nil {
		t.Fatalf("PG did not become ready after recovering the refused directory: %v", err)
	}
	if got := pg2.QueryScalar(t, "SELECT count(*) FROM public.s28_gin"); got != "2000" {
		t.Fatalf("after PG finished recovery, s28_gin holds %q rows, want 2000", got)
	}
	// And the GIN index itself still answers — the rows goopg would have
	// corrupted are intact.
	if got := pg2.QueryScalar(t, "SELECT count(*) FROM public.s28_gin WHERE tags @> ARRAY['tag-7']"); got == "0" {
		t.Fatalf("the GIN index answers 0 rows for a tag every 10th row carries — "+
			"the index goopg refused to replay did not survive PG's own recovery (got %q)", got)
	}
}

// runGINWorkload creates a GIN index and writes enough through it that the
// crash tail carries RM_GIN records.
//
// Two mechanisms are exercised on purpose, because they produce different GIN
// opcodes and a refusal must cover both:
//
//   - ordinary INSERTs land in the index's PENDING LIST (fastupdate defaults to
//     on for gin, postgres/src/backend/access/gin/ginutil.c ginoptions), which
//     emits XLOG_GIN_INSERT with a ginxlogInsertListPage payload plus
//     XLOG_GIN_UPDATE_META_PAGE;
//   - gin_clean_pending_list() then moves those entries into the real entry
//     tree, emitting the XLOG_GIN_INSERT / XLOG_GIN_SPLIT records that a
//     data-page algorithm produces (postgres/src/backend/access/gin/ginfast.c
//     ginInsertCleanup).
//
// The index build itself is NOT enough: CREATE INDEX logs the finished index
// with log_newpage_range, i.e. rmid 0 XLOG_FPI records, not RM_GIN ones.
func runGINWorkload(t *testing.T, pg *pgcluster.Cluster) {
	t.Helper()

	pg.Exec(t, `CREATE TABLE public.s28_gin (id integer PRIMARY KEY, tags text[] NOT NULL)`)
	pg.Exec(t, `CREATE INDEX s28_gin_tags_idx ON public.s28_gin USING gin (tags)`)
	pg.Exec(t, `INSERT INTO public.s28_gin (id, tags)
		SELECT g, ARRAY['tag-' || (g % 10), 'row-' || g]
		FROM generate_series(1, 2000) g`)
	pg.Exec(t, `SELECT gin_clean_pending_list('public.s28_gin_tags_idx'::regclass)`)

	if got := pg.QueryScalar(t, "SELECT count(*) FROM public.s28_gin"); got != "2000" {
		t.Fatalf("workload sanity: PG reports %s rows in s28_gin, want 2000", got)
	}
}
