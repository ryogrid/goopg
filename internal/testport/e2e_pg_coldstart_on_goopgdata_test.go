package testport

// M0131-S4 — forward cold start: a cluster directory that goopg created and
// wrote, served by real PostgreSQL 18.3.
//
// Design: docs/design/0131-0004-forward-coldstart-e2e.md. This is S3's mirror
// image, and it discharges docs/design/0130-0002-pg-class-heap-persistence.md
// Guard #1, which has read "Needs E2E PG-attach test — not yet implemented"
// since the day it was written. Everything M0130 landed downstream of that
// guard was validated through the BASEBACKUP lane, where the directory PG boots
// on is a tar goopg produced and pg_basebackup extracted. Here PG is pointed at
// the LIVE directory a goopg server just shut down.
//
// The harness needs nothing new: pgcluster.OpenExisting takes any directory and
// Start execs `postgres -D -p -h` directly, so the listener configuration
// arrives on argv and goopg's postgresql.conf needs no edit (guard 5).

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/control"
	"github.com/goopg/goopg/internal/testutil/cluster"
	"github.com/goopg/goopg/internal/testutil/pgcluster"
)

func TestE2E_PGColdStartOnGoopgDataDir(t *testing.T) {
	if testing.Short() || os.Getenv("GOOPG_SKIP_M0131_E2E") != "" {
		t.Skip("skipping forward cold-start e2e (short mode or GOOPG_SKIP_M0131_E2E set)")
	}

	repo := repoRoot(t)
	binDir := filepath.Join(repo, "postgres", "local_install", "bin")
	pgcluster.Available(t, binDir)

	baseDir := t.TempDir()
	goopgDir := filepath.Join(baseDir, "goopgdata")

	// Step 1 — the source directory is built by `goopg init`. User/Database
	// default to postgres in internal/testutil/cluster, which is also what the
	// pgcluster handle below must be told explicitly (it would otherwise
	// default to $USER and find no matching pg_authid row).
	g, err := cluster.New("m0131-s4-goopg", cluster.Options{
		RepoRoot:     repo,
		DataDir:      goopgDir,
		StartupWait:  60 * time.Second,
		ShutdownWait: 30 * time.Second,
	})
	if err != nil {
		t.Fatalf("cluster.New goopg: %v", err)
	}
	if err := g.Init(); err != nil {
		t.Fatalf("goopg init: %v", err)
	}
	goopgStopped := false
	defer func() {
		if !goopgStopped {
			_ = g.Stop(cluster.ShutdownImmediate)
		}
	}()
	if err := g.Start(); err != nil {
		logTail, _ := os.ReadFile(g.LogPath())
		t.Fatalf("goopg.Start: %v\n--- goopg log ---\n%s", err, tailLines(string(logTail), 40))
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	// S4.1 — the workload is M0130 acceptance item 1, verbatim: CREATE TABLE /
	// ALTER TABLE … ADD COLUMN / CREATE SCHEMA / CREATE INDEX / CREATE DATABASE
	// / INSERT / UPDATE / DELETE.
	//
	// The ADD COLUMN carries a DEFAULT deliberately, so pg_attrdef 2604 and its
	// indexes 2656/2657 participate — that is the M0130 work whose only prior
	// validation was the basebackup lane.
	//
	// S4.5: UPDATE stays in the workload on purpose. goopg's general updateOp
	// fallback emits HeapDelete + HeapInsert as two records rather than one
	// atomic non-HOT update (deferral ledger M0118-0129, restated in M0130-S7.2).
	// PG replays goopg's WAL tail from its OWN startup here, not through a
	// walreceiver, so this is the first test to exercise those records on that
	// path. If the gap surfaces — a row visible twice, a row missing, a
	// complaint during startup replay — it gets diagnosed and ledgered, not
	// engineered around by dropping UPDATE.
	for _, stmt := range []string{
		"CREATE SCHEMA s4app",
		`CREATE TABLE public.s4_items (
			id    integer PRIMARY KEY,
			label text    NOT NULL,
			qty   integer NOT NULL
		)`,
		"CREATE INDEX s4_items_label_idx ON public.s4_items (label)",
		"CREATE TABLE s4app.s4_notes (id integer, note text)",
		`INSERT INTO public.s4_items (id, label, qty)
			SELECT g, 'label-' || g, g * 10 FROM generate_series(1, 20) g`,
		"UPDATE public.s4_items SET qty = qty + 1 WHERE id % 3 = 0",
		"DELETE FROM public.s4_items WHERE id > 15",
		"INSERT INTO s4app.s4_notes (id, note) VALUES (1, 'in a non-public schema')",
		"ALTER TABLE public.s4_items ADD COLUMN tag text DEFAULT 'dflt'",
	} {
		if err := runSQLSimple(t, g, stmt); err != nil {
			logTail, _ := os.ReadFile(g.LogPath())
			t.Fatalf("goopg workload %q: %v\n--- goopg log ---\n%s", stmt, err, tailLines(string(logTail), 40))
		}
	}

	// CREATE DATABASE plus a table inside it: the per-DB catalog directory is
	// what a hosted PG has to enumerate through pg_database, and reading a row
	// out of base/<newoid> exercises a directory goopg minted rather than one
	// initdb laid down.
	if err := runSQLSimple(t, g, "CREATE DATABASE s4other"); err != nil {
		t.Fatalf("goopg CREATE DATABASE: %v", err)
	}
	gOther, err := cluster.New("m0131-s4-goopg-other", cluster.Options{
		RepoRoot:     repo,
		DataDir:      goopgDir,
		ListenAddr:   g.ListenAddr(),
		Database:     "s4other",
		StartupWait:  60 * time.Second,
		ShutdownWait: 30 * time.Second,
	})
	if err != nil {
		t.Fatalf("cluster.New goopg (s4other handle): %v", err)
	}
	for _, stmt := range []string{
		"CREATE TABLE public.s4_other_rows (id integer PRIMARY KEY, payload text NOT NULL)",
		"INSERT INTO public.s4_other_rows (id, payload) VALUES (1, 'from the second database')",
	} {
		if err := runSQLSimple(t, gOther, stmt); err != nil {
			t.Fatalf("goopg workload in s4other %q: %v", stmt, err)
		}
	}

	// Capture goopg's own answers, so the PG-side assertions compare against
	// the authoring engine rather than against hand-computed constants — the
	// same discipline S3 uses in the other direction.
	wantCount := coldStartScalar(t, ctx, g, "SELECT count(*) FROM public.s4_items")
	wantSumQty := coldStartScalar(t, ctx, g, "SELECT sum(qty) FROM public.s4_items")
	wantLabel7 := coldStartScalar(t, ctx, g, "SELECT label FROM public.s4_items WHERE id = 7")
	wantQty7 := coldStartScalar(t, ctx, g, "SELECT qty FROM public.s4_items WHERE id = 7")
	wantLabel9 := coldStartScalar(t, ctx, g, "SELECT label FROM public.s4_items WHERE id = 9")
	wantQty9 := coldStartScalar(t, ctx, g, "SELECT qty FROM public.s4_items WHERE id = 9")
	wantNote := coldStartScalar(t, ctx, g, "SELECT note FROM s4app.s4_notes WHERE id = 1")
	wantTag := coldStartScalar(t, ctx, g, "SELECT tag FROM public.s4_items WHERE id = 7")
	wantOther := coldStartScalar(t, ctx, gOther, "SELECT payload FROM public.s4_other_rows WHERE id = 1")
	if wantCount != "15" {
		t.Fatalf("workload sanity: goopg reports %s rows in s4_items, want 15", wantCount)
	}
	if wantTag != "dflt" {
		t.Fatalf("workload sanity: ADD COLUMN … DEFAULT read back as %q, want \"dflt\"", wantTag)
	}

	// Step 3 — clean stop. Stop(ShutdownFast) takes the implicit shutdown
	// checkpoint (internal/server/server.go) and Runtime.Close calls
	// Checkpointer.CheckpointShutdown, which stamps pg_control.State =
	// DB_SHUTDOWNED. Guard 2 asserts that byte before the handover: PG's
	// StartupXLOG behaves differently on a non-shutdowned control file, and an
	// unclean source is out of scope for this lane exactly as it is for S3's.
	if err := g.Stop(cluster.ShutdownFast); err != nil {
		t.Fatalf("goopg.Stop(fast): %v", err)
	}
	goopgStopped = true

	cd, err := control.ReadControlFile(goopgDir)
	if err != nil {
		t.Fatalf("ReadControlFile after goopg stop: %v", err)
	}
	if cd == nil {
		t.Fatalf("ReadControlFile after goopg stop: no control file at %s", goopgDir)
	}
	if cd.State != control.DBStateShutdowned {
		t.Fatalf("pg_control.State = %d after fast shutdown, want DB_SHUTDOWNED (%d)",
			cd.State, control.DBStateShutdowned)
	}

	// Guard 2, second half. postmaster.pid IS written by goopg
	// (control.WritePIDFile from startControlPlane) and removed on a clean stop
	// (stopControlPlane → control.RemovePIDFile); postmaster.opts has no writer
	// anywhere in goopg. Asserting their absence here makes a surprise fail as
	// itself rather than as a confusing PG lock-file error three steps later.
	for _, name := range []string{"postmaster.pid", "postmaster.opts"} {
		if _, err := os.Stat(filepath.Join(goopgDir, name)); err == nil {
			t.Fatalf("%s present after a clean goopg stop — PG will refuse or misread the directory", name)
		}
	}

	// Guard 5 — PG boots with NO edit to goopg's postgresql.conf. A plain
	// `goopg init` writes config.SampleConfig() verbatim with every setting
	// commented out, and the harness's `fsync = off` (appended by cluster.Init)
	// is the single active line — a real PG GUC. An active goopg-private
	// setting would FATAL PG at startup, so this test is also the guard against
	// one ever being seeded uncommented.
	confPath := filepath.Join(goopgDir, "postgresql.conf")
	confBefore, err := os.ReadFile(confPath)
	if err != nil {
		t.Fatalf("read postgresql.conf: %v", err)
	}
	assertOnlyFsyncOffIsActive(t, string(confBefore))

	// Step 4 — real PG 18.3 on the same directory. OpenExisting runs neither
	// initdb nor appendConf; it only builds a handle over the existing tree.
	pg, err := pgcluster.OpenExisting("m0131-s4-pg", pgcluster.Options{
		RepoRoot:    repo,
		DataDir:     goopgDir,
		User:        "postgres",
		StartupWait: 60 * time.Second,
	})
	if err != nil {
		t.Fatalf("pgcluster.OpenExisting: %v", err)
	}
	defer func() { _ = pg.Stop() }()
	if err := pg.Start(); err != nil {
		t.Fatalf("postgres -D <goopg data dir>: %v", err)
	}
	readyCtx, readyCancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer readyCancel()
	if err := pg.WaitReady(readyCtx, 60*time.Second); err != nil {
		logTail, _ := os.ReadFile(pgLogPathFor(goopgDir))
		t.Fatalf("pg.WaitReady on a goopg-authored directory: %v\n--- PG log ---\n%s",
			err, tailLines(string(logTail), 60))
	}

	confAfter, err := os.ReadFile(confPath)
	if err != nil {
		t.Fatalf("re-read postgresql.conf: %v", err)
	}
	if string(confAfter) != string(confBefore) {
		t.Fatalf("postgresql.conf changed across the handover; nothing in this test may edit it")
	}

	// Step 5 — the measurement.
	//
	// Guard 3 / 0130-0002 Guard #1: `SELECT relname FROM pg_class` lists the
	// user tables. This is the sentence that guard has been waiting for.
	// The query carries an ORDER BY and PG does the sorting. It used to sort in
	// Go instead, because until M0131-S12 populated
	// pg_opclass_am_name_nsp_index (2686) a hosted PG could not sort at all —
	// see assertHostedPGCanSort below. Sorting server-side keeps the guard
	// honest: it now exercises the ordering path over a real goopg catalog scan,
	// not just over a VALUES list.
	assertHostedPGCanSort(t, pg, goopgDir)

	relnames := pgQueryColumn(t, pg, `SELECT c.relname
		FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname IN ('public', 's4app') AND c.relkind = 'r'
		ORDER BY c.relname`)
	if got := strings.Join(relnames, ","); got != "s4_items,s4_notes" {
		t.Fatalf("hosted PG lists user tables %q, want \"s4_items,s4_notes\" — "+
			"this is docs/design/0130-0002-pg-class-heap-persistence.md Guard #1", got)
	}

	assertSystemViewOIDsArePinnedToUpstream(t, pg)

	assertNailedSystemViewsAreEvaluable(t, pg, goopgDir)
	assertNonCorpusSystemViewIsStillAbsent(t, pg)

	// Row-level reads over user tables. S4.4 originally carried no view
	// assertion at all because a hosted PG could not evaluate ANY view on a
	// goopg directory; M0131-S6 flipped `relhasrules` and the probe directly
	// above is the assertion that gap left missing. Its TODO named
	// `pg_stat_activity`, which was the wrong target — that view is not in
	// goopg's nailed set (it has no bootstrapped pg_class/pg_rewrite rows at
	// all, so it would fail 42P01 for an unrelated reason and is S9's work).
	// The six views S6 actually enables are the ones probed.
	pgLog := pgLogPathFor(goopgDir)
	if got := s4Scalar(t, pg, pgLog, "SELECT count(*) FROM public.s4_items"); got != wantCount {
		t.Fatalf("hosted PG count(*) on a goopg-written heap = %q, goopg said %q", got, wantCount)
	}
	if got := s4Scalar(t, pg, pgLog, "SELECT sum(qty) FROM public.s4_items"); got != wantSumQty {
		t.Fatalf("hosted PG sum(qty) = %q, goopg said %q (UPDATE/DELETE visibility — "+
			"S4.5: a mismatch here is the non-atomic non-HOT UPDATE gap, ledger M0118-0129)", got, wantSumQty)
	}
	if got := s4Scalar(t, pg, pgLog, "SELECT count(*) FROM public.s4_items WHERE id > 15"); got != "0" {
		t.Fatalf("hosted PG still sees %q DELETEd rows (id > 15)", got)
	}
	// id=9 was UPDATEd (9 % 3 == 0), id=7 was not. Reading both keeps the
	// UPDATE gap visible as a row-content mismatch rather than as a count.
	//
	// The two columns are read as two separate scalars rather than as
	// `label || '/' || qty`. That is NOT cosmetic: `text || integer` resolves
	// to textanycat (oid 2003), a LANGUAGE SQL function, and calling any
	// non-builtin crashes the hosted backend — see
	// assertProconfigGapStillCrashesSQLFunctions, the second finding this test
	// measured. Builtins go through fmgrtab and never read pg_proc, which is
	// why every other probe here survives.
	if got := s4Scalar(t, pg, pgLog, "SELECT label FROM public.s4_items WHERE id = 7"); got != wantLabel7 {
		t.Fatalf("hosted PG row id=7 label = %q, goopg said %q", got, wantLabel7)
	}
	if got := s4Scalar(t, pg, pgLog, "SELECT qty FROM public.s4_items WHERE id = 7"); got != wantQty7 {
		t.Fatalf("hosted PG row id=7 qty = %q, goopg said %q", got, wantQty7)
	}
	if got := s4Scalar(t, pg, pgLog, "SELECT label FROM public.s4_items WHERE id = 9"); got != wantLabel9 {
		t.Fatalf("hosted PG row id=9 (UPDATEd) label = %q, goopg said %q — S4.5, see ledger M0118-0129", got, wantLabel9)
	}
	if got := s4Scalar(t, pg, pgLog, "SELECT qty FROM public.s4_items WHERE id = 9"); got != wantQty9 {
		t.Fatalf("hosted PG row id=9 (UPDATEd) qty = %q, goopg said %q — S4.5, see ledger M0118-0129", got, wantQty9)
	}

	// Guard 7 — index behaviour is live, not hypothetical. relhasindex is no
	// longer hardcoded false (pgClassRelhasindex, with pgIndexTupleKeys = true),
	// so PG may genuinely plan an index scan over a goopg-authored btree. An
	// `XX002 … contains corrupted page at block 0` here is blocker #12
	// resurfacing — a REGRESSION signal, not an expected wall.
	assertHostedPGIndexReadable(t, pg)

	// The ADD COLUMN … DEFAULT column: pg_attrdef 2604 + indexes 2656/2657,
	// previously validated only through the basebackup lane.
	assertFastDefaultGapReadsNullOnHostedPG(t, pg, pgLog, wantTag)
	// The non-public schema exercises pg_namespace reverse mapping.
	if got := s4Scalar(t, pg, pgLog, "SELECT note FROM s4app.s4_notes WHERE id = 1"); got != wantNote {
		t.Fatalf("hosted PG read of the non-public schema = %q, goopg said %q", got, wantNote)
	}

	// Guard 4 — zero FATAL in the PG log for the whole run so far. pgcluster
	// truncates the log on Start and allocates it as a sibling of the data dir,
	// so the scan covers exactly this attach and nothing else.
	//
	// It runs HERE rather than at the end because the two remaining probes are
	// destructive by construction: each locks in a measured gap whose current
	// behaviour is a backend PANIC or abort, which necessarily writes to this
	// log and takes the postmaster with it. Guard 4 has to see the log before
	// the known damage, not after.
	assertNoFatalInPGLog(t, pgLogPathFor(goopgDir))

	// DESTRUCTIVE PROBES — nothing may follow these two.
	assertGoopgCreatedDatabaseStillUnopenableByPG(t, repo, pg, goopgDir, wantOther)
	assertProconfigGapStillCrashesSQLFunctions(t, pg, pgLogPathFor(goopgDir))
}

// pgLogPathFor mirrors pgcluster's default LogPath allocation (a `pg.log`
// sibling of the data directory) so the test can read the log without the
// handle exposing it.
func pgLogPathFor(dataDir string) string {
	return filepath.Join(filepath.Dir(dataDir), "pg.log")
}

// assertOnlyFsyncOffIsActive enforces guard 5's precondition: the only
// uncommented assignment in the goopg-generated postgresql.conf is the
// harness's `fsync = off`. Any other active line is a goopg-private GUC that
// would FATAL the hosted postmaster, and the failure should name the line
// rather than surface as an opaque startup error.
func assertOnlyFsyncOffIsActive(t *testing.T, conf string) {
	t.Helper()
	var active []string
	for _, line := range strings.Split(conf, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		active = append(active, trimmed)
	}
	if len(active) != 1 || !strings.HasPrefix(active[0], "fsync") {
		t.Fatalf("goopg's postgresql.conf carries %d active line(s) %q; the forward cold start "+
			"requires all of them to be real PG GUCs, and a plain `goopg init` should leave only "+
			"the harness's `fsync = off`", len(active), active)
	}
}

// assertHostedPGIndexReadable runs an index-qualified predicate against the
// hosted PG and distinguishes the two failure shapes that matter: XX002
// ("contains corrupted page") is blocker #12 resurfacing in a goopg-authored
// btree, while a wrong row is an ordinary content mismatch.
func assertHostedPGIndexReadable(t *testing.T, pg *pgcluster.Cluster) {
	t.Helper()
	const query = "SELECT id FROM public.s4_items WHERE label = 'label-7'"
	// Ask the planner to prefer the index so the read actually goes through it
	// rather than through a seq scan over a 15-row table.
	forced := "SET enable_seqscan = off; " + query
	out, err := pgQueryScalarAllowError(pg, forced)
	if err != nil {
		if strings.Contains(out, "XX002") || strings.Contains(out, "corrupted page") {
			t.Fatalf("hosted PG index scan over a goopg-authored btree failed with a corrupted-page "+
				"error — this is blocker #12 (docs/design/0130-0010-pg183-standby-e2e-harness.md) "+
				"resurfacing on the cold-start lane, a REGRESSION not an expected wall:\n%s", out)
		}
		t.Fatalf("hosted PG index-qualified read %q failed: %v\n%s", forced, err, out)
	}
	// psql echoes the SET command tag before the SELECT's row, so the value is
	// the last line, not the whole output.
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if got := strings.TrimSpace(lines[len(lines)-1]); got != "7" {
		t.Fatalf("hosted PG index-qualified read returned %q (full output %q), want \"7\"", got, out)
	}
}

// assertHostedPGCanSort is the INVERTED form of what used to be
// `assertEmptyOpclassIndexStillBlocksSorts` — M0131-S12 landed and the
// fail-when-fixed assertion fired exactly as designed.
//
// The gap it measured: a real PG hosted on a goopg-created directory could not
// execute ANY sort, for ANY type. `SELECT x FROM (VALUES (2), (1)) v(x) ORDER BY x`
// failed with "could not identify an ordering operator for type integer" —
// nothing in that query touches goopg's own tables.
//
// Diagnosis (measured, not inferred). The chain is
// lookup_type_cache(TYPECACHE_LT_OPR) → GetDefaultOpClass, which at
// postgres/src/backend/commands/indexcmds.c:2374-2384 does:
//
//	rel  = table_open(OperatorClassRelationId /* 2616 */, AccessShareLock);
//	ScanKeyInit(&skey[0], Anum_pg_opclass_opcmethod, …, am_id);
//	scan = systable_beginscan(rel, OpclassAmNameNspIndexId /* 2686 */, true, …);
//
// `indexOK = true` with no seq-scan fallback — the identical shape as blockers
// #7/#8 and M0131-S5. goopg's pg_opclass HEAP was already complete and correct
// (177 rows; int4_ops is oid 1978, opcmethod 403, opcdefault 't'; 38 default
// btree opclasses) and pg_index carried valid rows for 2686/2687. Only the index
// CONTENT was missing, so the scan returned zero rows and PG concluded that no
// type has a default btree opclass.
//
// The fix is `bootstrapPgOpclassAmNameNspIndex`
// (internal/initdb/btree_index_bootstrap.go), which bulk-loads 2686 from the same
// heap TIDs `bootstrapPgOpclassOidIndex` (2687) uses. This assertion now holds it
// in the POSITIVE direction: a regression here means 2686 went empty again.
//
// Keep the probe FROM-less/table-less on purpose: it isolates the opclass
// lookup from anything about goopg's own heaps. The ORDER BY over real goopg
// tables is asserted separately by the Guard #1 query at the call site, which
// S12 restored to its ORDER BY form.
func assertHostedPGCanSort(t *testing.T, pg *pgcluster.Cluster, goopgDir string) {
	t.Helper()
	const probe = "SELECT string_agg(x::text, ',' ORDER BY x) FROM (VALUES (2), (1), (3)) v(x)"
	out, err := pgQueryScalarAllowError(pg, probe)
	if err != nil {
		// Report the on-disk size of 2686 in both databases: an 8 KB file is
		// the bare `makeBtreeRootPage()` placeholder (the pre-S12 state, i.e.
		// the bootstrapper did not run), while a larger file means the index
		// was built but PG could not use its content.
		var sizes []string
		for _, db := range []string{"1", "5"} {
			p := filepath.Join(goopgDir, "base", db, "2686")
			if st, sErr := os.Stat(p); sErr == nil {
				sizes = append(sizes, fmt.Sprintf("%s=%d bytes", p, st.Size()))
			} else {
				sizes = append(sizes, fmt.Sprintf("%s: %v", p, sErr))
			}
		}
		t.Fatalf("a hosted PG can no longer sort (%q): M0131-S12 REGRESSED — "+
			"pg_opclass_am_name_nsp_index (2686) is empty again, see "+
			"bootstrapPgOpclassAmNameNspIndex.\non-disk: %s\n%s",
			probe, strings.Join(sizes, ", "), out)
	}
	if got := strings.TrimSpace(out); got != "1,2,3" {
		t.Fatalf("hosted PG sorted to %q, want \"1,2,3\" — the 2686 index is populated but "+
			"produces the wrong order (key layout or sort order in "+
			"bootstrapPgOpclassAmNameNspIndex)", got)
	}
}

// assertGoopgCreatedDatabaseStillUnopenableByPG locks in the fourth finding.
//
// A database goopg minted at RUNTIME with `CREATE DATABASE` cannot be connected
// to by the hosted PG at all:
//
//	psql: connection to server … failed: PANIC: could not open critical system index 2662
//
// 2662 is pg_class_oid_index. `RelationCacheInitializePhase3`
// (postgres/src/backend/utils/cache/relcache.c) nails and opens a small set of
// critical indexes during InitPostgres, and a failure there is a PANIC, not an
// ERROR — so the whole cluster goes down with the connection attempt.
//
// This is a scope statement about goopg's CREATE DATABASE, not about the cold
// start: the `postgres` database in the SAME directory serves every assertion
// above correctly. initdb lays down base/1 and base/5 with real bootstrapped
// btree content (internal/initdb/btree_index_bootstrap.go writes each index
// into base/1 AND base/5); the runtime CREATE DATABASE path clones what goopg
// itself needs, and goopg reads its catalogs without those indexes, so nothing
// in the goopg-only world notices.
//
// Filed as M0131-S15 with a deferral-ledger row. When S15 lands, this assertion
// FAILS — invert it then to a direct equality against goopgAnswer.
//
// DESTRUCTIVE: the PANIC terminates the postmaster.
func assertGoopgCreatedDatabaseStillUnopenableByPG(t *testing.T, repo string, pg *pgcluster.Cluster, dataDir, goopgAnswer string) {
	t.Helper()
	pgOther, err := pgcluster.OpenExisting("m0131-s4-pg-other", pgcluster.Options{
		RepoRoot: repo,
		DataDir:  dataDir,
		Port:     pg.Port(),
		User:     "postgres",
		Database: "s4other",
	})
	if err != nil {
		t.Fatalf("pgcluster.OpenExisting (s4other handle): %v", err)
	}
	out, err := pgQueryScalarAllowError(pgOther, "SELECT payload FROM public.s4_other_rows WHERE id = 1")
	if err == nil {
		t.Fatalf("the hosted PG can now read inside a goopg-CREATE DATABASE-minted database "+
			"(got %q, goopg said %q) — M0131-S15 has landed. INVERT this assertion to a "+
			"direct equality and delete this helper "+
			"(docs/design/0131-0004-forward-coldstart-e2e.md §Findings)",
			strings.TrimSpace(out), goopgAnswer)
	}
	if !strings.Contains(out, "could not open critical system index") {
		t.Fatalf("connecting to the goopg-created database failed, but NOT through the known "+
			"missing-critical-index gap (M0131-S15) — this is something else:\n%s", out)
	}
}

// assertFastDefaultGapReadsNullOnHostedPG locks in the third finding — again
// fail-when-fixed — while still asserting the part of M0130's pg_attrdef work
// that DOES survive the handover.
//
// `ALTER TABLE … ADD COLUMN tag text DEFAULT 'dflt'` reads back as 'dflt' on
// goopg and as NULL on the hosted PG, for all 15 pre-existing rows.
//
// Diagnosis (measured). The pg_attrdef side is entirely correct — the hosted PG
// reads `pg_get_expr(adbin, adrelid)` as `'dflt'::text` for adnum 4 and
// pg_class.relnatts as 4, which is exactly the M0130 pg_attrdef 2604/2656/2657
// work being validated outside the basebackup lane for the first time. What is
// missing is PG's **fast-default** mechanism: since PG 11, ADD COLUMN with a
// non-volatile DEFAULT does not rewrite the heap; it stores the value in
// pg_attribute.attmissingval with atthasmissing = true, and every physically
// short tuple materialises the missing value on read. goopg neither rewrites
// the rows nor records the missing value, so PG sees short tuples with no
// missing value and yields NULL.
//
// Underneath that sits a second, sharper fact: `attmissingval` does not exist
// as a column at all on the hosted PG —
// `SELECT attmissingval FROM pg_attribute` errors 42703 — so goopg's
// pg_attribute heap is short of at least one attribute in its own
// self-description (the rows for relid 1249), not merely unpopulated for
// s4_items.
//
// Filed as M0131-S14 with a deferral-ledger row. When S14 lands, this assertion
// FAILS — invert it then to a direct equality against goopg's own answer.
func assertFastDefaultGapReadsNullOnHostedPG(t *testing.T, pg *pgcluster.Cluster, logPath, goopgAnswer string) {
	t.Helper()
	// The half that works, asserted positively: pg_attrdef survived the cold
	// start with the right expression on the right attnum.
	// No `||` in these queries — that would call textanycat and take the
	// backend down (M0131-S13, the second finding).
	if got := s4Scalar(t, pg, logPath,
		"SELECT adnum FROM pg_attrdef WHERE adrelid = 'public.s4_items'::regclass"); got != "4" {
		t.Fatalf("hosted PG read of goopg's pg_attrdef adnum = %q, want \"4\" — "+
			"this is M0130's pg_attrdef 2604/2656/2657 work, and it is NOT the known "+
			"fast-default gap (M0131-S14)", got)
	}
	if got := s4Scalar(t, pg, logPath,
		"SELECT pg_get_expr(adbin, adrelid) FROM pg_attrdef WHERE adrelid = 'public.s4_items'::regclass"); got != "'dflt'::text" {
		t.Fatalf("hosted PG read of goopg's pg_attrdef expression = %q, want \"'dflt'::text\" — "+
			"this is M0130's pg_attrdef 2604/2656/2657 work, and it is NOT the known "+
			"fast-default gap (M0131-S14)", got)
	}
	// The half that does not, asserted in the fail-when-fixed direction.
	nulls := s4Scalar(t, pg, logPath, "SELECT count(*) FROM public.s4_items WHERE tag IS NULL")
	if nulls != "15" {
		t.Fatalf("hosted PG now materialises the ADD COLUMN … DEFAULT value for %s of 15 "+
			"pre-existing rows (goopg reads %q) — M0131-S14 has landed. INVERT this "+
			"assertion to a direct equality "+
			"(docs/design/0131-0004-forward-coldstart-e2e.md §Findings)",
			"15-"+nulls, goopgAnswer)
	}
}

// assertProconfigGapStillCrashesSQLFunctions locks in the second finding, in
// the same fail-when-fixed direction.
//
// `SELECT 'a'::text || 1` crashes the hosted backend outright:
//
//	TRAP: failed Assert("ARR_NDIM(array) == 1"), File: "guc.c", Line: 6411
//	  ExceptionalCondition → TransformGUCArray → fmgr_security_definer
//	client backend (PID …) was terminated by signal 6: Aborted
//
// Diagnosis (measured). goopg writes **every one** of its 3397 pg_proc rows
// with a NON-NULL proconfig — probed directly on the hosted PG:
// `SELECT count(*) FROM pg_proc WHERE proconfig IS NOT NULL` returns 3397, the
// full row count, while upstream's initdb leaves proconfig NULL on all of them
// (prosecdef is correctly 'f' everywhere, so it is proconfig alone). The bytes
// behind that non-null attribute are not a valid 1-D text[], so
// TransformGUCArray's assertion fires. Upstream reaches it from
// fmgr_info_cxt_security (postgres/src/backend/utils/fmgr/fmgr.c:203-211):
//
//	if (!ignore_security &&
//	    (procedureStruct->prosecdef ||
//	     !heap_attisnull(procedureTuple, Anum_pg_proc_proconfig, NULL) || …))
//	        finfo->fn_addr = fmgr_security_definer;
//
// The blast radius is precisely bounded by fmgr_isbuiltin: a function whose OID
// is in the compiled-in fmgrtab never reads pg_proc at all, so int4eq, textout,
// count and sum are all fine — which is why the rest of this test passes. It is
// the LANGUAGE SQL builtins that break, textanycat (oid 2003) among them, and
// `text || integer` resolves to exactly that.
//
// This is an assert-enabled PG build, so the failure is loud. A production
// build would instead walk a bogus ArrayType — worse, not better.
//
// Filed as M0131-S13 with a deferral-ledger row. When S13 lands, this assertion
// FAILS — invert it then: fold `label || '/' || qty` back into the row-content
// reads above and delete this helper.
func assertProconfigGapStillCrashesSQLFunctions(t *testing.T, pg *pgcluster.Cluster, logPath string) {
	t.Helper()
	const probe = "SELECT 'a'::text || 1"
	out, err := pgQueryScalarAllowError(pg, probe)
	if err == nil {
		t.Fatalf("a hosted PG can now call a LANGUAGE SQL builtin (%q returned %q) — "+
			"M0131-S13 has landed. INVERT this assertion: restore the "+
			"`label || '/' || qty` form of the row-content reads and delete this helper "+
			"(docs/design/0131-0004-forward-coldstart-e2e.md §Findings)",
			probe, strings.TrimSpace(out))
	}
	logData, _ := os.ReadFile(logPath)
	if !strings.Contains(string(logData), "TransformGUCArray") {
		t.Fatalf("hosted PG failed %q, but NOT through the known pg_proc.proconfig gap "+
			"(M0131-S13) — this is a different failure:\npsql: %s\n--- PG log ---\n%s",
			probe, out, tailLines(string(logData), 60))
	}
}

// pgQueryColumn runs a one-column SELECT through psql -tA and returns the
// non-empty lines. Ordering is whatever the plan produces — callers sort in Go
// until M0131-S12 lands.
// assertSystemViewOIDsArePinnedToUpstream is M0131-S8a's acceptance measurement,
// and it can only be taken here: it asks a REAL PG 18.3, hosted on a goopg
// catalog, what OID each nailed system view has — and requires the answer to be
// the OID that PG's own initdb would have assigned.
//
// S8a chose Option A (pin to upstream) over rewriting relids inside every
// captured ev_action blob, because a view-on-view's blob embeds its base view's
// initdb-assigned OID. This probe is the claim's other half: the unit guards in
// internal/initdb check goopg's tables against a table, whereas this checks
// goopg's on-disk bytes against a live PG's own name→OID resolution.
//
// The probe is `::regclass::oid` rather than a SELECT from the views: the nailed
// views still carry relhasrules='f', so a hosted PG cannot EVALUATE them yet
// (that is M0131-S6). Name resolution needs only the pg_class row, which is
// exactly what this test is measuring.
// nailedSystemViewProbeSet is the on-disk system-view corpus this hosted-PG
// lane probes, with the OID PG 18.3's OWN initdb assigns to each.
//
// These are literals on purpose — the point of both probes below is to compare
// goopg's on-disk bytes against the oracle, not against goopg's own
// systemViewOIDPins() table (which internal/testport cannot see anyway, and
// which would make the check circular if it could).
//
// The first six are M0131-S8a's replication views; the next 22 are
// M0131-S9.1's SRF-only tranche — every one `FROM <set-returning function>`
// with no catalog relation and no view dependency, hence zero in-band :relid
// in its ev_action. M0131-S9.1b adds the last two, pg_stat_bgwriter (12293)
// and pg_stat_checkpointer (12297): they have no FROM clause AT ALL
// (system_views.sql:1150-1169), so their Query carries an RTE_RESULT — the
// fifth RTE kind, which 0131-0009 §"Two unmeasured ev_action shapes" flagged
// as never having been round-tripped through a hosted PG. Their presence in
// THIS list is the measurement. Captured 2026-08-11 from a throwaway
// `initdb --no-sync`.
//
// Deliberately absent: pg_timezone_abbrevs (12122), whose ORDER BY needs a
// pg_amop row goopg does not bootstrap ("operator 664 is not a valid ordering
// operator") — this probe is what found that, and it is ledgered.
func nailedSystemViewProbeSet() []struct {
	view string
	oid  string
} {
	return []struct {
		view string
		oid  string
	}{
		{"pg_catalog.pg_locks", "12073"},
		{"pg_catalog.pg_cursors", "12077"},
		{"pg_catalog.pg_prepared_statements", "12095"},
		{"pg_catalog.pg_settings", "12104"},
		{"pg_catalog.pg_file_settings", "12110"},
		{"pg_catalog.pg_hba_file_rules", "12114"},
		{"pg_catalog.pg_ident_file_mappings", "12118"},
		{"pg_catalog.pg_timezone_names", "12126"},
		{"pg_catalog.pg_config", "12130"},
		{"pg_catalog.pg_shmem_allocations", "12134"},
		{"pg_catalog.pg_shmem_allocations_numa", "12138"},
		{"pg_catalog.pg_backend_memory_contexts", "12142"},
		{"pg_catalog.pg_stat_replication", "12231"},
		{"pg_catalog.pg_stat_slru", "12236"},
		{"pg_catalog.pg_stat_wal_receiver", "12240"},
		{"pg_catalog.pg_stat_recovery_prefetch", "12244"},
		{"pg_catalog.pg_stat_subscription", "12248"},
		{"pg_catalog.pg_stat_ssl", "12253"},
		{"pg_catalog.pg_stat_gssapi", "12257"},
		{"pg_catalog.pg_replication_slots", "12261"},
		{"pg_catalog.pg_stat_replication_slots", "12266"},
		{"pg_catalog.pg_stat_archiver", "12289"},
		{"pg_catalog.pg_stat_bgwriter", "12293"},
		{"pg_catalog.pg_stat_checkpointer", "12297"},
		{"pg_catalog.pg_stat_io", "12301"},
		{"pg_catalog.pg_stat_wal", "12305"},
		{"pg_catalog.pg_stat_progress_basebackup", "12329"},
		{"pg_catalog.pg_replication_origin_status", "12343"},
		{"pg_catalog.pg_wait_events", "12351"},
		{"pg_catalog.pg_aios", "12355"},
	}
}

func assertSystemViewOIDsArePinnedToUpstream(t *testing.T, pg *pgcluster.Cluster) {
	t.Helper()
	for _, tc := range nailedSystemViewProbeSet() {
		got, err := pgQueryScalarAllowError(pg, "SELECT '"+tc.view+"'::regclass::oid")
		if err != nil {
			t.Fatalf("hosted PG cannot resolve %s at all: %v\n%s", tc.view, err, got)
		}
		if g := strings.TrimSpace(got); g != tc.oid {
			t.Fatalf("hosted PG resolves %s to OID %s, want upstream's %s — "+
				"M0131-S8a pins goopg's system-view OIDs to PG 18.3's initdb "+
				"assignment (internal/initdb/system_view_oid_pins.go). A "+
				"mismatch means a captured ev_action that embeds this view's "+
				"relid names a relation this cluster does not have.",
				tc.view, g, tc.oid)
		}
	}
}

// assertNailedSystemViewsAreEvaluable is the M0131-S6 acceptance probe (design
// 0131-0006 guard 4): a real PG 18.3 hosted on a goopg-initdb'd directory must
// be able to EVALUATE each of the six nailed replication views, not merely
// resolve its name.
//
// Before S6 every bootstrapped view row carried relhasrules=false, so
// RelationBuildDesc took the else arm at relcache.c:1249-1255, rd_rules stayed
// NULL, and the rewriter rejected the query — "cannot open relation … this
// operation is not supported for views" (42809). Reaching this point means PG
// scanned goopg's own pg_rewrite heap through index 2693, found the _RETURN
// rule, and substituted its Query for the RTE.
//
// The views are probed ONE AT A TIME and a failure is reported per view
// (t.Errorf, not Fatalf) with the PG log tail attached — that IS S6.6's risk
// control: the six blobs are independent, so naming every failing view in one
// run is what localises a bad tupledesc (the known shape is
// populate_compact_attribute_internal, tupdesc.c:105) instead of forcing a
// manual bisect. This is how the pg_subscription 9-vs-18 attribute gap was
// isolated: five views passed and only pg_stat_subscription reported
// "cache lookup failed for attribute 10 of relation 6100".
//
// LIMIT 0 is deliberate. These views read live shared-memory replication state
// through SRFs; on a standalone hosted PG they are legitimately empty, and the
// assertion under test is rule expansion, not row content.
func assertNailedSystemViewsAreEvaluable(t *testing.T, pg *pgcluster.Cluster, goopgDir string) {
	t.Helper()
	// M0131-S9.1 widened this from the six replication views to the whole
	// on-disk corpus (nailedSystemViewProbeSet), and S9.1b added the
	// RTE_RESULT pair. The per-view Errorf is what makes that affordable: 30
	// independent blobs, and a bad tupledesc names its own view instead of
	// forcing a bisect.
	for _, tc := range nailedSystemViewProbeSet() {
		view := tc.view
		out, err := pgQueryScalarAllowError(pg, "SELECT * FROM "+view+" LIMIT 0")
		if err != nil {
			logTail, _ := os.ReadFile(pgLogPathFor(goopgDir))
			t.Errorf("hosted PG cannot evaluate %s: %v\n%s\n"+
				"M0131-S6 flipped pg_class.relhasrules for the six nailed views "+
				"(internal/initdb/initdb.go, pgClassRow) so PG would scan "+
				"pg_rewrite for their _RETURN rules. A 42809 here means the flag "+
				"did not reach this directory's base/{1,5}/1259; any other error "+
				"means the rule was found and something downstream of "+
				"RelationBuildRuleLock rejected it.\n--- PG log ---\n%s",
				view, err, out, tailLines(string(logTail), 60))
		}
	}
}

// assertNonCorpusSystemViewIsStillAbsent mechanises the BEFORE half of design
// 0131-0009's guard #2 (M0131-S9.1b).
//
// The evaluability probe above proves only an after-state: every view goopg
// seeds on disk can be evaluated by a hosted PG. On its own that is compatible
// with a world where PG resolves these names for some reason unrelated to
// goopg's seeding — so the guard is only half a guard. The missing half is a
// view that goopg has NOT adopted, which must fail with 42P01
// (undefined_table), because goopg's ~118 system relations are catalog.Table
// {Virtual:true} objects that never reach the pg_class heap
// (internal/catalog/catalog.go:335-342, skipped at :7025-7034). Passing both
// halves is what makes the corpus list, and not something ambient, the cause.
//
// pg_tables is the probe on purpose: it is the named head of the M0131-S9.2
// tranche (views over real catalogs). When S9.2 lands this assertion FAILS —
// invert it then by moving pg_tables into nailedSystemViewProbeSet() and
// re-pointing this helper at whatever the next un-adopted view is. That is the
// same fail-when-fixed discipline as the S12/S13 finding locks above.
func assertNonCorpusSystemViewIsStillAbsent(t *testing.T, pg *pgcluster.Cluster) {
	t.Helper()
	const view = "pg_catalog.pg_tables"
	out, err := pgQueryScalarAllowError(pg, "SELECT * FROM "+view+" LIMIT 0")
	if err == nil {
		t.Errorf("hosted PG evaluated %s, which is NOT in the on-disk corpus "+
			"(nailedSystemViewProbeSet) — either M0131-S9.2 has landed (then add "+
			"pg_tables to the corpus and re-point this probe at the next "+
			"un-adopted view), or the evaluability probe above is passing for a "+
			"reason unrelated to goopg's seeding. Output: %q", view, strings.TrimSpace(out))
		return
	}
	// 42P01 specifically: "does not exist" is the virtual-relation gap. Any
	// other error (42809 no-rules, a crash, a tupledesc elog) would mean the
	// row IS on disk and something downstream rejected it, which is a
	// different — and interesting — failure.
	if !strings.Contains(out, "does not exist") {
		t.Errorf("hosted PG rejected %s, but NOT with the expected 42P01 "+
			"undefined_table — a non-corpus view is supposed to be missing from "+
			"the pg_class heap entirely:\n%s", view, out)
	}
}

func pgQueryColumn(t *testing.T, pg *pgcluster.Cluster, sqlText string) []string {
	t.Helper()
	out, err := pgQueryScalarAllowError(pg, sqlText)
	if err != nil {
		t.Fatalf("hosted PG query %q: %v\n%s", sqlText, err, out)
	}
	var vals []string
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			vals = append(vals, line)
		}
	}
	return vals
}

// s4Scalar is pgcluster.QueryScalar plus the PG log tail on failure. Every
// interesting failure in this lane is a backend-side event (a crash, a FATAL, a
// corrupted-page report), and the psql-side error text alone names none of them.
func s4Scalar(t *testing.T, pg *pgcluster.Cluster, logPath, sqlText string) string {
	t.Helper()
	out, err := pgQueryScalarAllowError(pg, sqlText)
	if err != nil {
		logTail, _ := os.ReadFile(logPath)
		t.Fatalf("hosted PG query %s: %v\n%s\n--- PG log ---\n%s",
			sqlText, err, out, tailLines(string(logTail), 60))
	}
	return strings.TrimSpace(out)
}

// assertNoFatalInPGLog is guard 4.
func assertNoFatalInPGLog(t *testing.T, logPath string) {
	t.Helper()
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read PG log %s: %v", logPath, err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.Contains(line, "FATAL") || strings.Contains(line, "PANIC") {
			t.Fatalf("hosted PG logged a FATAL/PANIC serving the goopg directory:\n%s\n--- full log ---\n%s",
				line, tailLines(string(data), 60))
		}
	}
}

// pgQueryScalarAllowError is pgcluster.QueryScalar without the t.Fatalf, so a
// caller can classify the error text instead of dying on it.
func pgQueryScalarAllowError(pg *pgcluster.Cluster, sqlText string) (string, error) {
	out, err := pg.PSQLCombined("-tA", "-v", "ON_ERROR_STOP=1", "-c", sqlText)
	return out, err
}
