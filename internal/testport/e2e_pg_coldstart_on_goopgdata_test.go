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
	"strconv"
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
	assertInformationSchemaDomainsResolvable(t, pg, goopgDir)
	assertInformationSchemaDataTablesReadable(t, pg, goopgDir)
	assertInformationSchemaViewsEvaluable(t, pg, goopgDir)
	assertHostedPGSeesPgRewriteToastRelation(t, pg)

	// Row-level reads over user tables. S4.4 originally carried no view
	// assertion at all because a hosted PG could not evaluate ANY view on a
	// goopg directory; M0131-S6 flipped `relhasrules` and the probe directly
	// above is the assertion that gap left missing. Its TODO named
	// `pg_stat_activity`, which was the wrong target — that view is not in
	// goopg's nailed set (it has no bootstrapped pg_class/pg_rewrite rows at
	// all, so it would fail 42P01 for an unrelated reason and is S9's work).
	// M0131-S9.2b did that work: pg_stat_activity (12226) is now IN the probe
	// set above, so the TODO is discharged rather than merely re-scoped.
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
	// The two columns are read as ONE concatenated scalar, and that form is
	// the assertion (M0131-S13.4). `text || integer` resolves to textanycat
	// (oid 2003), a LANGUAGE SQL function — NOT an fmgrtab builtin — so this
	// probe reaches fmgr_info_cxt_security and reads the row's pg_proc tuple.
	// Until S13 it aborted the backend with `Assert("ARR_NDIM(array) == 1")`
	// because every seeded proconfig was a non-NULL empty ArrayType shell, and
	// this test had to split the read into two builtin-only scalars plus a
	// separate fail-when-fixed helper. The concatenation IS the regression
	// tripwire now: a SIGABRT here means the seed path went back to writing
	// empty varlena shells for the nullable pg_proc attributes.
	if got := s4Scalar(t, pg, pgLog, "SELECT label || '/' || qty FROM public.s4_items WHERE id = 7"); got != wantLabel7+"/"+wantQty7 {
		t.Fatalf("hosted PG row id=7 label/qty = %q, goopg said %q", got, wantLabel7+"/"+wantQty7)
	}
	if got := s4Scalar(t, pg, pgLog, "SELECT label || '/' || qty FROM public.s4_items WHERE id = 9"); got != wantLabel9+"/"+wantQty9 {
		t.Fatalf("hosted PG row id=9 (UPDATEd) label/qty = %q, goopg said %q — S4.5, see ledger M0118-0129",
			got, wantLabel9+"/"+wantQty9)
	}
	// The proconfig gap's own probe, kept as a direct positive assertion now
	// that it passes: a LANGUAGE SQL builtin must be callable at all.
	assertHostedPGCanCallSQLBuiltins(t, pg, pgLog)

	// Guard 7 — index behaviour is live, not hypothetical. relhasindex is no
	// longer hardcoded false (pgClassRelhasindex, with pgIndexTupleKeys = true),
	// so PG may genuinely plan an index scan over a goopg-authored btree. An
	// `XX002 … contains corrupted page at block 0` here is blocker #12
	// resurfacing — a REGRESSION signal, not an expected wall.
	assertHostedPGIndexReadable(t, pg)

	// The ADD COLUMN … DEFAULT column: pg_attrdef 2604 + indexes 2656/2657,
	// previously validated only through the basebackup lane.
	assertFastDefaultMaterialisesOnHostedPG(t, pg, pgLog, wantTag)
	// The non-public schema exercises pg_namespace reverse mapping.
	if got := s4Scalar(t, pg, pgLog, "SELECT note FROM s4app.s4_notes WHERE id = 1"); got != wantNote {
		t.Fatalf("hosted PG read of the non-public schema = %q, goopg said %q", got, wantNote)
	}

	// Guard 4 — zero FATAL in the PG log for the whole run so far. pgcluster
	// truncates the log on Start and allocates it as a sibling of the data dir,
	// so the scan covers exactly this attach and nothing else.
	//
	// It runs HERE rather than at the end because the remaining probe USED to
	// be destructive by construction: it locked in a measured gap whose
	// behaviour was a backend PANIC, which necessarily wrote to this log and
	// took the postmaster with it. That gap is fixed (M0131-S15), so the last
	// probe is now an ordinary read — but Guard 4 stays here so that a
	// REGRESSION of it is reported by the probe's own specific message rather
	// than as an opaque "FATAL in the log". (Until M0131-S13 the proconfig
	// probe was a second destructive one; it is now a plain assertion run
	// above, inside the row-content reads.)
	assertNoFatalInPGLog(t, pgLogPathFor(goopgDir))

	// A database goopg minted at RUNTIME with CREATE DATABASE — the last of
	// the four findings this test originally measured (M0131-S15).
	assertHostedPGReadsGoopgCreatedDatabase(t, repo, pg, goopgDir, wantOther)
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

// assertHostedPGReadsGoopgCreatedDatabase is the S15.4 INVERSION of the former
// assertGoopgCreatedDatabaseStillUnopenableByPG: the fourth finding this test
// measured is FIXED (M0131-S15, 2026-08-12), so the probe now asserts success.
//
// A database goopg minted at RUNTIME with `CREATE DATABASE` used to be
// unconnectable by the hosted PG at all:
//
//	psql: connection to server … failed: PANIC: could not open critical system index 2662
//
// 2662 is pg_class_oid_index. `RelationCacheInitializePhase3`
// (postgres/src/backend/utils/cache/relcache.c) nails and opens a small set of
// critical indexes during InitPostgres, and a failure there is a PANIC, not an
// ERROR — so the whole cluster went down with the connection attempt.
//
// Diagnosis (measured). It was never about the runtime path's catalog cloning:
// CREATE DATABASE copies template0's whole on-disk image
// (initdb.copyBootstrapCatalogImage), and template0 itself was the stale one.
// bootstrapPostgresDatabase materialised base/4 EARLY — from a base/1 that the
// btree bulk-loaders had not yet populated — and then stamped metapage-only
// placeholders over its index files, so every runtime database inherited a
// 1-page, leaf-less pg_class_oid_index while base/{1,5} carried the real one.
// The fix is `initdb.refreshTemplate0Image`, which re-clones base/4 from the
// finished base/5 at the END of initdb, matching upstream's own ordering
// (initdb.c make_template0 creates template0 from a fully bootstrapped
// template1, which is why a real cluster's base/4 == base/1 byte for byte).
//
// Kept as the LAST probe in the test even though it is no longer destructive:
// its position is what makes Guard 4's "no FATAL in the log" scan meaningful
// for everything above it.
func assertHostedPGReadsGoopgCreatedDatabase(t *testing.T, repo string, pg *pgcluster.Cluster, dataDir, goopgAnswer string) {
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
	if err != nil {
		if strings.Contains(out, "could not open critical system index") {
			t.Fatalf("M0131-S15 REGRESSED: the hosted PG PANICs again on a critical system "+
				"index inside a goopg-CREATE DATABASE-minted database — template0's on-disk "+
				"image (base/4) is stale again, see initdb.refreshTemplate0Image and its "+
				"guard TestTemplate0ImageMatchesPostgresDatabase.\n%s", out)
		}
		t.Fatalf("hosted PG read inside the goopg-created database s4other failed: %v\n%s", err, out)
	}
	if got := strings.TrimSpace(out); got != goopgAnswer {
		t.Fatalf("hosted PG read inside the goopg-created database s4other = %q, goopg said %q",
			got, goopgAnswer)
	}
}

// assertFastDefaultMaterialisesOnHostedPG is the S14.4 INVERSION of the former
// assertFastDefaultGapReadsNullOnHostedPG: the third finding this test measured
// is FIXED (M0131-S14.2, 2026-08-12), so the probe now asserts success.
//
// `ALTER TABLE … ADD COLUMN tag text DEFAULT 'dflt'` used to read back as
// 'dflt' on goopg and as NULL on the hosted PG, for all 15 pre-existing rows.
//
// Diagnosis (measured). The pg_attrdef side was always correct — the hosted PG
// reads `pg_get_expr(adbin, adrelid)` as `'dflt'::text` for adnum 4 and
// pg_class.relnatts as 4, which is exactly the M0130 pg_attrdef 2604/2656/2657
// work being validated outside the basebackup lane for the first time. What was
// missing is PG's **fast-default** mechanism: since PG 11, ADD COLUMN with a
// non-volatile DEFAULT does not rewrite the heap; it stores the value in
// pg_attribute.attmissingval with atthasmissing = true, and every physically
// short tuple materialises the missing value on read. goopg recorded the value
// only in its own in-memory catalog.Column.MissingValue, so PG saw short tuples
// with no missing value and yielded NULL. S14.2 writes the catalog half too —
// a one-element ArrayType built exactly as StoreAttrMissingVal builds it
// (postgres/src/backend/catalog/heap.c:2030) — so the hosted PG's own
// getmissingattr() materialises the value with no heap rewrite on either side.
//
// Underneath that sat a second, sharper fact, and THAT half is now FIXED
// (M0131-S14.1): `SELECT attmissingval FROM pg_attribute` used to error 42703
// on the hosted PG. Not because the column was missing from goopg's heap — the
// rows for relid 1249 always had 25 attributes — but because the nailed
// pg_class row said relnatts = 24. PG unlinks goopg's pg_internal.init at
// startup (xlog.c:5633 RelationCacheInitFileRemove) and builds pg_attribute
// from its own compiled 25-column Desc_pg_attribute, then
// RelationCacheInitializePhase3 copies rd_rel out of goopg's pg_class tuple —
// and the short relnatts truncated the last column off the descriptor. In PG18
// order that last column is attmissingval, hence the 42703.
//
// Fixing relnatts exposed the permutation underneath it: goopg ordered the five
// nullable trailing columns attacl/attoptions/attfdwoptions/attmissingval/
// attstattarget, PG18 orders them attstattarget/attacl/attoptions/
// attfdwoptions/attmissingval (attstattarget moved to #21 when it became
// nullable; the old code's comment still claimed #4, which was PG16's). All
// five NULL made the two indistinguishable — same null bitmap — but
// `ALTER COLUMN … SET STATISTICS` writes attstattarget, and a hosted PG would
// then have read that int2 as attacl. Both halves are asserted positively below.
//
// What remains deferred is S14.3 — goopg's OWN reader still materialises from
// catalog.Column.MissingValue rather than from the heap's attmissingval, so the
// two halves are written together but read independently (ledgered).
func assertFastDefaultMaterialisesOnHostedPG(t *testing.T, pg *pgcluster.Cluster, logPath, goopgAnswer string) {
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
	// S14.1, asserted positively: pg_attribute must describe itself to the
	// hosted PG with all 25 PG18 attributes, in PG18 order. Reading
	// attmissingval AT ALL is the 42703 regression tripwire; reading the tail's
	// attnums is the permutation tripwire. Both queries are answered from
	// goopg's own heap rows for relid 1249.
	// Scoped to relid 1249's OWN rows: none of pg_attribute's 25 self-describing
	// attributes has a fast default, so this count stays 0 forever, while a
	// 42703 (rather than a number) is the relnatts-went-short signal. It is
	// deliberately NOT the unscoped count any more — since S14.2 a user table's
	// ADD COLUMN … DEFAULT legitimately makes that non-zero.
	if got := s4Scalar(t, pg, logPath,
		"SELECT count(*) FROM pg_attribute WHERE attrelid = 1249 AND attmissingval IS NOT NULL"); got != "0" {
		t.Fatalf("hosted PG reads %q non-NULL pg_attribute.attmissingval rows for relid 1249, want 0 — "+
			"a 42703 here instead means goopg's nailed pg_class relnatts for OID 1249 "+
			"went short again (M0131-S14.1)", got)
	}
	if got := s4Scalar(t, pg, logPath,
		`SELECT string_agg(attname, ',' ORDER BY attnum)
		   FROM pg_attribute WHERE attrelid = 1249 AND attnum > 20`); got !=
		"attstattarget,attacl,attoptions,attfdwoptions,attmissingval" {
		t.Fatalf("goopg's pg_attribute self-description tail = %q, want PG18's "+
			"\"attstattarget,attacl,attoptions,attfdwoptions,attmissingval\" — the four "+
			"sibling column lists have drifted apart again (M0131-S14.1)", got)
	}

	// S14.2, asserted positively — the catalog half of the fast default.
	if got := s4Scalar(t, pg, logPath,
		`SELECT atthasmissing FROM pg_attribute
		   WHERE attrelid = 'public.s4_items'::regclass AND attname = 'tag'`); got != "t" {
		t.Fatalf("hosted PG reads atthasmissing = %q for s4_items.tag, want \"t\" — "+
			"buildUserPGAttributeRow stopped writing the fast default (M0131-S14.2)", got)
	}
	// …and the read PG derives from it, with no heap rewrite on either side:
	// every one of the 15 pre-ALTER rows is physically short, so PG must
	// materialise the value through getmissingattr().
	if nulls := s4Scalar(t, pg, logPath,
		"SELECT count(*) FROM public.s4_items WHERE tag IS NULL"); nulls != "0" {
		t.Fatalf("hosted PG reads NULL tag on %q of the 15 pre-existing rows (goopg reads %q) — "+
			"the fast-default value is not reaching PG's getmissingattr (M0131-S14.2)",
			nulls, goopgAnswer)
	}
	if got := s4Scalar(t, pg, logPath,
		"SELECT count(*) FROM public.s4_items WHERE tag = 'dflt'"); got != "15" {
		t.Fatalf("hosted PG materialises tag = 'dflt' on %q of the 15 pre-existing rows "+
			"(goopg reads %q) — the attmissingval element decoded to something else "+
			"(M0131-S14.2)", got, goopgAnswer)
	}
}

// assertHostedPGCanCallSQLBuiltins is the S13.4 INVERSION of the former
// assertProconfigGapStillCrashesSQLFunctions: the second finding this test
// measured is FIXED (M0131-S13, 2026-08-12), so the probe now asserts success.
//
// Before the fix, `SELECT 'a'::text || 1` crashed the hosted backend outright:
//
//	TRAP: failed Assert("ARR_NDIM(array) == 1"), File: "guc.c", Line: 6411
//	  ExceptionalCondition → TransformGUCArray → fmgr_security_definer
//	client backend (PID …) was terminated by signal 6: Aborted
//
// Diagnosis (measured, and the fix follows from it). goopg wrote **every one**
// of its 3397 pg_proc rows with a NON-NULL proconfig — probed directly on the
// hosted PG:
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
// The fix (M0131-S13): initdb's pgProcRow now writes executor.NullDatum for
// every absent nullable varlena attribute — proconfig, proacl, probin,
// prosqlbody, protrftypes and the OUT-arg trio — matching the runtime sibling
// buildPGProcRow, which had used NullDatum all along. Unit tripwire:
// internal/initdb/pg_proc_nullable_varlena_test.go.
//
// Two assertions, because either alone passes vacuously: the call must
// SUCCEED, and pg_proc must report zero non-NULL proconfig rows (a hosted PG
// that somehow answered "a1" through a different route would still be broken).
func assertHostedPGCanCallSQLBuiltins(t *testing.T, pg *pgcluster.Cluster, logPath string) {
	t.Helper()
	const probe = "SELECT 'a'::text || 1"
	out, err := pgQueryScalarAllowError(pg, probe)
	if err != nil || strings.TrimSpace(out) != "a1" {
		logData, _ := os.ReadFile(logPath)
		t.Fatalf("hosted PG cannot call the LANGUAGE SQL builtin textanycat: %q returned %q (err=%v) — "+
			"M0131-S13 has REGRESSED (a TransformGUCArray trap in the log below means the "+
			"pg_proc seed is writing empty varlena shells again)\n--- PG log ---\n%s",
			probe, strings.TrimSpace(out), err, tailLines(string(logData), 60))
	}
	got := s4Scalar(t, pg, logPath, "SELECT count(*) FROM pg_proc WHERE proconfig IS NOT NULL")
	if got != "0" {
		t.Fatalf("hosted PG counts %q pg_proc rows with a non-NULL proconfig, want 0 "+
			"(upstream initdb leaves every one NULL) — M0131-S13", got)
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
// M0131-S9.2a adds the first three that are NOT function-only: pg_views
// (12028), pg_tables (12033) and pg_matviews (12038) each join pg_class
// against pg_namespace (and, for two, pg_tablespace), so their ev_action is
// the corpus's first to carry RTE_JOIN. Every base OID is a sub-12000
// bootstrap constant, so these blobs still embed no in-band :relid and the
// tranche needed no view-on-view ordering.
//
// M0131-S9.2b adds the two that read SHARED catalogs: pg_roles (12000) is
// `FROM pg_authid` (1260) LEFT JOIN pg_db_role_setting (2964), and
// pg_stat_activity (12226) joins pg_stat_get_activity(NULL) against pg_authid
// AND pg_database (1262) — the first blob mixing an SRF with catalog relations
// in one join tree. Both base sets are sub-12000 bootstrap constants, so still
// no in-band :relid.
//
// M0131-S9.2c adds the authid family — and with it the FIRST view-on-view edge
// the corpus has ever hosted. pg_shadow (12005) is catalog-direct (pg_authid
// 1260 LEFT JOIN pg_db_role_setting 2964); pg_user (12014) is `FROM pg_shadow`
// and its blob embeds `:relid 12005`, the corpus's first in-band relid. Under
// the Option-A identity pinning of 0131-0008 that embedded OID needs no
// rewriting, and a hosted PG evaluating pg_user HERE is the measurement that
// proves it: PG resolves 12005 through goopg's own pg_class heap and
// substitutes pg_shadow's rule in turn.
//
// Deliberately absent from this tranche: pg_group (12010), whose ARRAY(SubLink)
// needs pg_type.typarray for OIDOID and goopg seeds that column as 0 for every
// row ("could not find array type for data type oid") — this probe is what
// found that, and it is ledgered.
//
// (pg_timezone_abbrevs (12122) was absent for the same class of reason — its
// ORDER BY needed the pg_amop lookup through index 2653, which M0131-S12 later
// bulk-loaded; M0131-S9.3d re-measures it here and it now evaluates.)
// Deliberately absent:
// (pg_indexes (12043), S9.2a's fourth candidate, was absent here for the same
// class of reason — its 9002 B ev_action was the first to breach the 8000 B
// inline-tuple budget. M0131-S20.2b captures it out of line and it joins the
// set below; assertNonCorpusSystemViewIsStillAbsent moved on to pg_policies.)
//
// M0131-S9.3a adds the per-table statistics family — the first tranche where
// view-on-view edges are the POINT rather than an incident: pg_stat_all_tables
// (12146) and pg_stat_xact_all_tables (12151) are the bases, and the four
// pg_stat_{sys,user}_tables / pg_stat_xact_{sys,user}_tables dependents each
// embed one of those two OIDs in their own ev_action. A hosted PG evaluating
// all six here is what proves guard #4's base-before-dependent pin ordering
// holds at more than one edge.
//
// M0131-S9.3b finishes S9.3's reachable population with the per-index and
// per-sequence statistics families: bases pg_stat_all_indexes (12187),
// pg_statio_all_indexes (12200) and pg_statio_all_sequences (12213), each with
// its {sys,user} dependent pair. That takes the corpus to 58 views over ten
// view-on-view edges, and makes the two index bases (6826 B / 6799 B stored)
// the closest any seeded blob sits to the 8000 B inline budget.
//
// M0131-S9.3c adds the two views S9.2 captured but had to withhold, and it
// took no capture work at all to do it: pg_group (12010) and
// pg_publication_tables (12068) were both blocked by the SAME pg_type
// bootstrap defect one column apart — typarray = 0 for every row (get_array_type,
// pg_group's ARRAY(SubLink)) and typelem = 0 for every row (get_element_type,
// pg_publication_tables' ArrayCoerceExpr). Populating both columns from the
// PG 18.3 catalog (initdb.pgTypeElemArray) cleared two ceilings at once, so a
// hosted PG evaluating these two here is what proves the population is real
// rather than merely well-formed.
//
// (The pg_statio_*_tables triple was absent for the same reason: its base
// pg_statio_all_tables (12174) stores at 10475 B, over the 8000 B inline
// heap-tuple budget, and under guard #4 a dependent cannot be pinned ahead of
// its base. M0131-S20.2b seeds the base out of line and all three join the set
// below.)
//
// M0131-S9.3d adds the REMAINDER tranche — eleven views that no earlier tranche
// had a reason to skip, found by asking the oracle for every pg_catalog view
// NOT already pinned: pg_available_extensions (12081) and
// pg_available_extension_versions (12085) (SRF-only, S9.1's shape),
// pg_timezone_abbrevs (12122) (ceiling #2, re-measured after S12),
// pg_stat_user_functions (12279) / pg_stat_xact_user_functions (12284), and the
// five command-progress views 12309/12314/12319/12324/12333 that join
// pg_stat_get_progress_info() against pg_database and pg_class. That takes the
// corpus to 71 of upstream's 80 pg_catalog views.
//
// M0131-S20.2b adds the six the pg_rewrite TOAST work was holding — pg_indexes
// (12043), pg_stats (12053), pg_stats_ext (12058) and the pg_statio_*_tables
// triple (12174 stored OUT OF LINE, its 12179/12183 dependents inline) — for a
// corpus of 77. Evaluating them here is the acceptance for the whole S20.2
// chain: each of the four external values reaches this probe only through
// heap_fetch_toast_slice → pg_toast_2618_index → pglz → stringToNode.
//
// M0131-S9.3e and S9.3f close that class of blocker by bootstrapping the
// missing base catalogs as EMPTY nailed heaps: pg_policy (3256) for
// pg_policies (12018), then pg_seclabel (3596) + pg_largeobject_metadata
// (2995) for pg_seclabels (12099). The corpus is 79 of upstream's 80.
//
// ONE is still missing, and it is blocked one layer BELOW the corpus tooling
// rather than by size: pg_stats_ext_exprs (no pg_type row for 10029,
// pg_statistic's composite rowtype — ceiling #6).
func nailedSystemViewProbeSet() []struct {
	view string
	oid  string
} {
	return []struct {
		view string
		oid  string
	}{
		{"pg_catalog.pg_roles", "12000"},
		{"pg_catalog.pg_shadow", "12005"},
		{"pg_catalog.pg_group", "12010"},
		{"pg_catalog.pg_user", "12014"},
		// M0131-S9.3e: adopted once pg_policy (3256) became an on-disk
		// nailed heap — this view's FROM list opens it, and until then a
		// hosted PG failed with "could not open relation with OID 3256".
		{"pg_catalog.pg_policies", "12018"},
		{"pg_catalog.pg_rules", "12023"},
		{"pg_catalog.pg_views", "12028"},
		{"pg_catalog.pg_tables", "12033"},
		{"pg_catalog.pg_matviews", "12038"},
		{"pg_catalog.pg_indexes", "12043"},
		{"pg_catalog.pg_sequences", "12048"},
		{"pg_catalog.pg_stats", "12053"},
		{"pg_catalog.pg_stats_ext", "12058"},
		// M0131-S9.3g: the LAST of upstream's 80, and the corpus's only
		// member that needed a pg_type row for a CATALOG's own rowtype.
		// unnest(stxdexpr) yields _pg_statistic (10028) elements, so
		// lookup_type_cache() resolves 10029 and dereferences its typrelid;
		// with no such row a hosted PG raised "type with OID 10029 does not
		// exist" and then died on Assert("OidIsValid(typentry->typrelid)")
		// (typcache.c:3082) in abort. pgTypeCanonical(10029) +
		// pgTypeRelidOverlay + nailedLocalRels{2619}.RelType = 10029.
		{"pg_catalog.pg_stats_ext_exprs", "12063"},
		{"pg_catalog.pg_publication_tables", "12068"},
		{"pg_catalog.pg_locks", "12073"},
		{"pg_catalog.pg_cursors", "12077"},
		{"pg_catalog.pg_available_extensions", "12081"},
		{"pg_catalog.pg_available_extension_versions", "12085"},
		{"pg_catalog.pg_prepared_xacts", "12090"},
		{"pg_catalog.pg_prepared_statements", "12095"},
		// M0131-S9.3f: adopted once pg_seclabel (3596) and
		// pg_largeobject_metadata (2995) became on-disk nailed heaps. It is
		// also the corpus's largest ev_action by 3× (203378 B raw, 35379 B
		// stored over 18 TOAST chunks), so evaluating it here exercises the
		// S20.2 out-of-line path further than any other member.
		{"pg_catalog.pg_seclabels", "12099"},
		{"pg_catalog.pg_settings", "12104"},
		{"pg_catalog.pg_file_settings", "12110"},
		{"pg_catalog.pg_hba_file_rules", "12114"},
		{"pg_catalog.pg_ident_file_mappings", "12118"},
		{"pg_catalog.pg_timezone_abbrevs", "12122"},
		{"pg_catalog.pg_timezone_names", "12126"},
		{"pg_catalog.pg_config", "12130"},
		{"pg_catalog.pg_shmem_allocations", "12134"},
		{"pg_catalog.pg_shmem_allocations_numa", "12138"},
		{"pg_catalog.pg_backend_memory_contexts", "12142"},
		{"pg_catalog.pg_stat_all_tables", "12146"},
		{"pg_catalog.pg_stat_xact_all_tables", "12151"},
		{"pg_catalog.pg_stat_sys_tables", "12156"},
		{"pg_catalog.pg_stat_xact_sys_tables", "12161"},
		{"pg_catalog.pg_stat_user_tables", "12165"},
		{"pg_catalog.pg_stat_xact_user_tables", "12170"},
		{"pg_catalog.pg_statio_all_tables", "12174"},
		{"pg_catalog.pg_statio_sys_tables", "12179"},
		{"pg_catalog.pg_statio_user_tables", "12183"},
		{"pg_catalog.pg_stat_all_indexes", "12187"},
		{"pg_catalog.pg_stat_sys_indexes", "12192"},
		{"pg_catalog.pg_stat_user_indexes", "12196"},
		{"pg_catalog.pg_statio_all_indexes", "12200"},
		{"pg_catalog.pg_statio_sys_indexes", "12205"},
		{"pg_catalog.pg_statio_user_indexes", "12209"},
		{"pg_catalog.pg_statio_all_sequences", "12213"},
		{"pg_catalog.pg_statio_sys_sequences", "12218"},
		{"pg_catalog.pg_statio_user_sequences", "12222"},
		{"pg_catalog.pg_stat_activity", "12226"},
		{"pg_catalog.pg_stat_replication", "12231"},
		{"pg_catalog.pg_stat_slru", "12236"},
		{"pg_catalog.pg_stat_wal_receiver", "12240"},
		{"pg_catalog.pg_stat_recovery_prefetch", "12244"},
		{"pg_catalog.pg_stat_subscription", "12248"},
		{"pg_catalog.pg_stat_ssl", "12253"},
		{"pg_catalog.pg_stat_gssapi", "12257"},
		{"pg_catalog.pg_replication_slots", "12261"},
		{"pg_catalog.pg_stat_replication_slots", "12266"},
		{"pg_catalog.pg_stat_database", "12270"},
		{"pg_catalog.pg_stat_database_conflicts", "12275"},
		{"pg_catalog.pg_stat_user_functions", "12279"},
		{"pg_catalog.pg_stat_xact_user_functions", "12284"},
		{"pg_catalog.pg_stat_archiver", "12289"},
		{"pg_catalog.pg_stat_bgwriter", "12293"},
		{"pg_catalog.pg_stat_checkpointer", "12297"},
		{"pg_catalog.pg_stat_io", "12301"},
		{"pg_catalog.pg_stat_wal", "12305"},
		{"pg_catalog.pg_stat_progress_analyze", "12309"},
		{"pg_catalog.pg_stat_progress_vacuum", "12314"},
		{"pg_catalog.pg_stat_progress_cluster", "12319"},
		{"pg_catalog.pg_stat_progress_create_index", "12324"},
		{"pg_catalog.pg_stat_progress_basebackup", "12329"},
		{"pg_catalog.pg_stat_progress_copy", "12333"},
		{"pg_catalog.pg_user_mappings", "12338"},
		{"pg_catalog.pg_replication_origin_status", "12343"},
		{"pg_catalog.pg_stat_subscription_stats", "12347"},
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
	// RTE_RESULT pair. The per-view Errorf is what makes that affordable: 37
	// independent blobs, and a bad tupledesc names its own view instead of
	// forcing a bisect.
	//
	// S9.2c made one of them NOT independent: pg_user expands to pg_shadow,
	// so `SELECT * FROM pg_user` is the first probe here whose success needs
	// TWO of goopg's pg_rewrite rows and a relid lookup BETWEEN them. It is
	// the acceptance measurement for the Option-A identity pinning.
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
// information_schema.tables is the probe, and it is the FIFTH relation to hold
// this role. The discipline is fail-when-fixed: each predecessor was chosen
// because it was blocked for a MEASURED reason, and each was retired by the
// slice that unblocked it. M0131-S9.2a adopted pg_tables (the original probe);
// M0131-S20.2b adopted pg_indexes, whose blocker was the 8000 B
// inline-heap-tuple budget (9002 B stored — design 0131-0009's TOAST ceiling
// #1), once DECLARE_TOAST(pg_rewrite, 2838, 2839) plus the S20.2a chunk writer
// made an out-of-line ev_action storable; M0131-S9.3e adopted pg_policies by
// bootstrapping pg_policy (3256); M0131-S9.3f adopted pg_seclabels by
// bootstrapping pg_seclabel (3596) and pg_largeobject_metadata (2995).
//
// S9.3f is where the pg_catalog supply of probes runs out. The corpus is 79 of
// upstream's 80 and the ONE remaining view, pg_stats_ext_exprs (12063,
// ceiling #6 — no pg_type row for 10029), is unusable HERE for a reason that
// has nothing to do with whether it is absent: its failure trips
// Assert("OidIsValid(typentry->typrelid)") (typcache.c:3082) and takes the
// backend down with it, and an "is it still absent?" guard must fail QUIETLY.
//
// So the probe crosses into the next slice's territory instead (F27). S9.4's
// subject, `information_schema`, is absent one level lower than any predecessor
// — not a view goopg failed to seed but a NAMESPACE goopg never bootstrapped at
// all when this probe was first pointed here (internal/initdb/initdb.go seeded
// exactly three: pg_catalog, public, pg_toast). M0133-S1 changed that — it
// seeded the information_schema NAMESPACE (13273) plus the five domains and two
// domain CHECK constraints — but NOT the 65 views, so `information_schema.tables`
// (a view, M0133-S4's work) still fails 42P01 and this probe stays green. The
// day S4 seeds `tables` itself, this assertion fails and whoever lands that
// slice must re-point it (the domains are asserted separately by
// assertInformationSchemaDomainsResolvable).
func assertNonCorpusSystemViewIsStillAbsent(t *testing.T, pg *pgcluster.Cluster) {
	t.Helper()
	// M0131-S9.3f re-pointed this from pg_seclabels (adopted by that slice,
	// which bootstrapped pg_seclabel 3596 and pg_largeobject_metadata 2995)
	// to information_schema.tables. M0133-S4 adopted tables (its first tranche),
	// then columns (its second), so the probe is re-pointed to
	// information_schema.element_types — the largest remaining view (10956 B
	// stored, over the inline budget), and a tranche-4 view-on-view (it embeds
	// :relid 13553 data_type_privileges), so it fails QUIETLY with 42P01 until
	// the last tranche lands.
	const view = "information_schema.element_types"
	out, err := pgQueryScalarAllowError(pg, "SELECT * FROM "+view+" LIMIT 0")
	if err == nil {
		t.Errorf("hosted PG evaluated %s, which is NOT in the on-disk corpus "+
			"(informationSchemaViewProbeSet) — either goopg's initdb now bootstraps "+
			"it (M0133-S4; then add it to the corpus and re-point this probe at the "+
			"next un-adopted relation), or the evaluability probe above is passing "+
			"for a reason unrelated to goopg's seeding. Output: %q",
			view, strings.TrimSpace(out))
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

// assertInformationSchemaDomainsResolvable is M0133-S1's acceptance probe: a
// hosted PG cold-started on a goopg $PGDATA must resolve the information_schema
// namespace (13273) and its five domains, and the two domain CHECK constraints
// must actually ENFORCE (not merely exist). goopg seeds the namespace + domains
// + array peers + the two pg_constraint rows in one bootstrap step; this probe
// is what distinguishes "seeded" from "a namespace that exists and is empty".
func assertInformationSchemaDomainsResolvable(t *testing.T, pg *pgcluster.Cluster, goopgDir string) {
	t.Helper()
	// Resolvability: cast succeeds and format_type names the domain by OID.
	for _, tc := range []struct {
		query string
		want  string
	}{
		{`SELECT 'abc'::information_schema.sql_identifier`, "abc"}, // over name (19), no CHECK
		{`SELECT 5::information_schema.cardinal_number`, "5"},      // over int4 (23)
		{`SELECT 'YES'::information_schema.yes_or_no`, "YES"},      // over varchar(3)
		{`SELECT format_type(13287, NULL)`, "information_schema.cardinal_number"}, // domain on disk, schema-qualified (not in search_path)
		{`SELECT typtype FROM pg_type WHERE oid = 13287`, "d"},                       // and it is a domain
	} {
		got := s4Scalar(t, pg, pgLogPathFor(goopgDir), tc.query)
		if got != tc.want {
			t.Errorf("hosted PG %q = %q, want %q", tc.query, got, tc.want)
		}
	}
	// Enforcement: the two CHECKs must reject their violating values. The
	// failure text names the constraint, proving it was read from pg_constraint
	// via pg_constraint_contypid_index (2666), not merely absent.
	for _, tc := range []struct {
		query string
		name  string
	}{
		{`SELECT (-1)::information_schema.cardinal_number`, "cardinal_number_domain_check"},
		{`SELECT 'MAYBE'::information_schema.yes_or_no`, "yes_or_no_check"},
	} {
		out, err := pgQueryScalarAllowError(pg, tc.query)
		if err == nil {
			t.Errorf("hosted PG accepted %s (want a domain CHECK violation)", tc.query)
			continue
		}
		if !strings.Contains(out, "violates check constraint") || !strings.Contains(out, tc.name) {
			t.Errorf("hosted PG rejected %s, but not with the expected check-constraint "+
				"violation naming %q:\n%s", tc.query, tc.name, out)
		}
	}
}

// assertInformationSchemaDataTablesReadable is M0133-S3's acceptance probe: a
// hosted PG cold-started on a goopg $PGDATA must READ REAL ROWS out of the four
// information_schema data tables, not merely plan a query over them. This is
// the first bulk data load goopg's initdb performs — the four tables are
// ordinary heaps (relkind 'r') whose 801 rows upstream COPYs/INSERTs from
// sql_features.txt, and whose columns are the S1 domains.
func assertInformationSchemaDataTablesReadable(t *testing.T, pg *pgcluster.Cluster, goopgDir string) {
	t.Helper()
	logPath := pgLogPathFor(goopgDir)

	// Row counts — the tables answer with their full captured data set.
	for _, tc := range []struct{ query, want string }{
		{`SELECT count(*) FROM information_schema.sql_features`, "755"},
		{`SELECT count(*) FROM information_schema.sql_sizing`, "23"},
		{`SELECT count(*) FROM information_schema.sql_implementation_info`, "12"},
		{`SELECT count(*) FROM information_schema.sql_parts`, "11"},
	} {
		if got := s4Scalar(t, pg, logPath, tc.query); got != tc.want {
			t.Errorf("hosted PG %q = %q, want %q", tc.query, got, tc.want)
		}
	}

	// Real cell reads, not just counts — the first sql_features row in heap
	// order (B011 / "Embedded Ada", deterministic: the heap is written in
	// capture order). The WHERE-clause read below deliberately exercises the
	// domain-typed comparison `feature_id = 'B011'` (character_data = unknown),
	// which failed "operator is not unique" until typispreferred was fixed for
	// the eight PG18 preferred types (text 25 is the preferred type of category
	// S; func_select_candidate needs it to disambiguate). The cell content itself
	// is pinned field-for-field by TestInformationSchemaTableRowsMatchCapture.
	if got := s4Scalar(t, pg, logPath,
		`SELECT feature_id FROM information_schema.sql_features LIMIT 1`); got != "B011" {
		t.Errorf("hosted PG sql_features[0] feature_id = %q, want %q", got, "B011")
	}
	if got := s4Scalar(t, pg, logPath,
		`SELECT feature_name FROM information_schema.sql_features WHERE feature_id = 'B011'`); got != "Embedded Ada" {
		t.Errorf("hosted PG sql_features WHERE feature_id='B011' = %q, want %q (domain-typed "+
			"comparison must resolve to texteq now that typispreferred is seeded)", got, "Embedded Ada")
	}

	// The composite rowtype is on disk and is a real composite pointing back at
	// the table (typrelid = table OID). oid equality is bootstrapped, so `=`
	// on pg_type.oid is fine here.
	if got := s4Scalar(t, pg, logPath,
		`SELECT typtype FROM pg_type WHERE oid = 13458`); got != "c" {
		t.Errorf("hosted PG sql_features rowtype typtype = %q, want %q", got, "c")
	}
	if got := s4Scalar(t, pg, logPath,
		`SELECT typrelid FROM pg_type WHERE oid = 13458`); got != "13456" {
		t.Errorf("hosted PG sql_features rowtype typrelid = %q, want %q", got, "13456")
	}
}

// informationSchemaViewProbeSet is the M0133-S4 analogue of
// nailedSystemViewProbeSet: the information_schema views goopg has adopted on
// disk, with the upstream OID each is pinned to (Option A, see
// internal/initdb/information_schema_view_oid_pins.go). Tranche 1 is all 33
// catalog-direct, under-inline-budget views — the four formerly withheld on
// incomplete goopg catalog descriptors (character_sets, collations,
// collation_character_set_applicability read pg_collation cols 9–12; triggers
// reads pg_trigger cols 3/9–19) landed with the descriptor-completion slice.
// Tranche 2 (2026-08-14) adds the ten catalog-direct TOAST views, whose stored
// ev_action exceeds the 8000 B inline budget and is externalised into
// pg_toast_2618 by the M0131-S20.2 writer. Tranche 3 (2026-08-14) adds the
// four helper-function views (key_column_usage, parameters, sequences,
// triggered_update_columns) — catalog-direct, under the inline budget, and
// embedding an in-band :funcid to the S2 helpers (13274..13285) but no
// in-band :relid. The remaining 18 view-on-view views land in tranche 4.
func informationSchemaViewProbeSet() []struct {
	view string
	oid  string
} {
	return []struct {
		view string
		oid  string
	}{
		{"information_schema.information_schema_catalog_name", "13293"},
		{"information_schema.applicable_roles", "13302"},
		{"information_schema.attributes", "13311"},
		{"information_schema.character_sets", "13316"},
		{"information_schema.check_constraint_routine_usage", "13321"},
		{"information_schema.check_constraints", "13326"},
		{"information_schema.collations", "13331"},
		{"information_schema.collation_character_set_applicability", "13336"},
		{"information_schema.column_column_usage", "13341"},
		{"information_schema.column_domain_usage", "13346"},
		{"information_schema.column_privileges", "13351"},
		{"information_schema.column_udt_usage", "13356"},
		{"information_schema.columns", "13361"},
		{"information_schema.constraint_column_usage", "13366"},
		{"information_schema.constraint_table_usage", "13371"},
		{"information_schema.domain_constraints", "13376"},
		{"information_schema.domain_udt_usage", "13381"},
		{"information_schema.domains", "13385"},
		{"information_schema.enabled_roles", "13390"},
		{"information_schema.key_column_usage", "13394"},
		{"information_schema.parameters", "13399"},
		{"information_schema.referential_constraints", "13404"},
		{"information_schema.routine_column_usage", "13413"},
		{"information_schema.routine_privileges", "13418"},
		{"information_schema.routine_routine_usage", "13427"},
		{"information_schema.routine_sequence_usage", "13432"},
		{"information_schema.routine_table_usage", "13437"},
		{"information_schema.routines", "13442"},
		{"information_schema.schemata", "13447"},
		{"information_schema.sequences", "13451"},
		{"information_schema.table_constraints", "13476"},
		{"information_schema.table_privileges", "13481"},
		{"information_schema.tables", "13490"},
		{"information_schema.transforms", "13495"},
		{"information_schema.triggered_update_columns", "13500"},
		{"information_schema.triggers", "13505"},
		{"information_schema.udt_privileges", "13510"},
		{"information_schema.usage_privileges", "13519"},
		{"information_schema.user_defined_types", "13528"},
		{"information_schema.view_column_usage", "13533"},
		{"information_schema.view_routine_usage", "13538"},
		{"information_schema.view_table_usage", "13543"},
		{"information_schema.views", "13548"},
		{"information_schema._pg_foreign_table_columns", "13563"},
		{"information_schema._pg_foreign_data_wrappers", "13572"},
		{"information_schema._pg_foreign_servers", "13584"},
		{"information_schema._pg_foreign_tables", "13596"},
	}
}

// assertInformationSchemaViewsEvaluable is M0133-S4's acceptance probe: a hosted
// PG 18.3 cold-started on a goopg $PGDATA must (a) resolve each adopted
// information_schema view to its pinned upstream OID and (b) EVALUATE it via its
// _RETURN rule. LIMIT 0 keeps the assertion on rule expansion rather than row
// content — these views read goopg's catalog heaps, and row content is pinned
// separately by the data-table probe above. Per-view Errorf (not Fatalf) is the
// same risk control as the pg_catalog probe: 33 independent blobs, and a bad
// tupledesc names its own view.
func assertInformationSchemaViewsEvaluable(t *testing.T, pg *pgcluster.Cluster, goopgDir string) {
	t.Helper()
	for _, tc := range informationSchemaViewProbeSet() {
		got, err := pgQueryScalarAllowError(pg, "SELECT '"+tc.view+"'::regclass::oid")
		if err != nil {
			t.Errorf("hosted PG cannot resolve %s at all: %v\n%s", tc.view, err, got)
			continue
		}
		if g := strings.TrimSpace(got); g != tc.oid {
			t.Errorf("hosted PG resolves %s to OID %s, want upstream's %s — "+
				"information_schema_view_oid_pins.go pins goopg's view OIDs to "+
				"PG 18.3's initdb assignment", tc.view, g, tc.oid)
			continue
		}
		out, err := pgQueryScalarAllowError(pg, "SELECT * FROM "+tc.view+" LIMIT 0")
		if err != nil {
			logTail, _ := os.ReadFile(pgLogPathFor(goopgDir))
			t.Errorf("hosted PG cannot evaluate %s: %v\n%s\n--- PG log ---\n%s",
				tc.view, err, out, tailLines(string(logTail), 60))
		}
	}
}

// assertHostedPGSeesPgRewriteToastRelation is M0131-S20.1's acceptance probe:
// a hosted PG cold-started on a goopg $PGDATA must find pg_rewrite's TOAST
// relation pair — DECLARE_TOAST(pg_rewrite, 2838, 2839),
// postgres/src/include/catalog/pg_rewrite.h:54 — which goopg's initdb
// bootstrapped for the first time in this slice.
//
// Why it earns its place next to the system-view probes: eight of the nine
// upstream pg_catalog views still missing from the on-disk corpus are missing
// for exactly one reason, and it is this pair's absence. Their ev_action
// pg_node_trees do not fit an inline heap tuple (pg_indexes 9002 B …
// pg_seclabels 35379 B against MaxHeapTupleSize ≈ 8160 B), so they must be
// stored out of line — and an external varatt pointer is only meaningful if
// the relation it names exists and is reachable.
//
// The three reads escalate deliberately:
//
//  1. pg_class(2618).reltoastrelid — the field PG's detoast path dereferences
//     (heap_fetch_toast_slice → table_open(toastrelid)). A 0 here turns every
//     external pointer into "missing chunk number 0 for toast value".
//  2. the pair's own pg_class rows, resolved by NAME through
//     pg_class_relname_nsp_index, which is what proves relnamespace = 99
//     (pg_toast) reached both the heap row and the index key.
//  3. a real read of the TOAST heap, which opens the relation, resolves its
//     physical file and reads block 0.
//  4. (M0131-S20.2b) the acceptance the first three only set up: a hosted PG
//     DETOASTING one of these values end to end. `SELECT * FROM pg_indexes`
//     makes PG read the pg_rewrite tuple, see an 18-byte VARTAG_ONDISK pointer
//     in ev_action, open 2838, walk index 2839 for chunk_id 12047, reassemble
//     five 1996-byte chunks, pglz-decompress the result and stringToNode the
//     70408-byte Query — every byte of which goopg's initdb wrote. Nothing
//     short of this exercises the writer: reads 1-3 pass against an EMPTY
//     TOAST heap, which is exactly the state S20.1 left and S20.2a could not
//     leave (the writer was inert until this slice's captures landed).
//
// The chunk count is asserted against upstream's own, not merely against
// non-zero: 40 chunks over the five externalised values goopg's initdb writes
// (pg_indexes 5, pg_stats 5, pg_stats_ext 6, pg_statio_all_tables 6, and
// M0131-S9.3f's pg_seclabels 18 — the corpus's largest value by 3×, and on its
// own nearly half the TOAST heap). A drift here means the chunker's split
// changed, which changes bytes a foreign engine reads.
func assertHostedPGSeesPgRewriteToastRelation(t *testing.T, pg *pgcluster.Cluster) {
	t.Helper()
	if got := pgQueryColumn(t, pg, "SELECT reltoastrelid FROM pg_class WHERE oid = 2618"); len(got) != 1 || got[0] != "2838" {
		t.Errorf("hosted PG reads pg_rewrite.reltoastrelid = %v, want [2838] — "+
			"without it an external ev_action pointer cannot be detoasted", got)
	}
	got := pgQueryColumn(t, pg,
		"SELECT relname::text || '/' || relkind::text || '/' || relnamespace::regnamespace::text "+
			"FROM pg_class WHERE oid IN (2838, 2839) ORDER BY oid")
	want := []string{"pg_toast_2618/t/pg_toast", "pg_toast_2618_index/i/pg_toast"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("hosted PG reads the pg_rewrite TOAST pair as %v, want %v", got, want)
	}
	// One row per externalised ev_action, "chunk_id/chunks/stored bytes".
	// chunk_id is the rule OID + 1 (F22), so this list pins the value ids
	// against upstream's own — a goopg pg_toast_2618 that is merely
	// well-formed would still fail here.
	//
	// The byte counts are goopg's, and they are 3-4% BELOW upstream's for
	// every value (measured, in the trailing comments): goopg's pglz encoder
	// finds shorter output than PG's for the same input, so its TOAST heap is
	// byte-divergent from upstream's even though the DETOASTED value is
	// identical — which the pg_indexes read below proves end to end. It also
	// costs pg_stats_ext one whole chunk (6 where upstream writes 7).
	// Recorded rather than "fixed": nothing in PG's reader depends on the
	// split, and matching upstream's exact pglz output would mean reproducing
	// its match-search heuristics, not its format. Ledgered.
	wantChunks := []string{
		"12047/5/8674",   // pg_indexes           (upstream: 5 chunks / 9002 B)
		"12057/5/8985",   // pg_stats             (upstream: 5 / 9316)
		"12062/6/11743",  // pg_stats_ext         (upstream: 7 / 12196)
		"12067/6/11089",  // pg_stats_ext_exprs   (upstream: 6 / 11481) — M0131-S9.3g
		"12103/18/34093", // pg_seclabels         (upstream: 18 / 35379) — M0131-S9.3f
		"12178/6/10125",  // pg_statio_all_tables (upstream: 6 / 10475)
		// M0133-S4 tranche 2 — the ten information_schema TOAST views.
		"13315/7/13160",  // attributes           (upstream: 7 / 13608)
		"13330/8/15510",  // check_constraints    (upstream: 9 / 16059)
		"13355/6/10396",  // column_privileges    (upstream: 6 / 10776)
		"13365/12/23386", // columns              (upstream: 13 / 24201)
		"13370/5/8502",   // constraint_column_usage (upstream: 5 / 8878)
		"13389/5/8110",   // domains              (upstream: 5 / 8356)
		"13408/7/13086",  // referential_constraints (upstream: 7 / 13463)
		"13446/5/9731",   // routines             (upstream: 6 / 10091)
		"13499/10/18840", // transforms           (upstream: 10 / 19659)
		"13523/10/18574", // usage_privileges     (upstream: 10 / 19211)
	}
	gotChunks := pgQueryColumn(t, pg,
		"SELECT chunk_id::text || '/' || count(*)::text || '/' || sum(length(chunk_data))::text "+
			"FROM pg_toast.pg_toast_2618 "+
			"GROUP BY chunk_id ORDER BY chunk_id")
	if strings.Join(gotChunks, " ") != strings.Join(wantChunks, " ") {
		t.Errorf("hosted PG reads pg_toast_2618 as %v, want %v — each entry is "+
			"chunk_id/chunk-count for one externalised ev_action, chunk_id being "+
			"the rule OID + 1 (M0131-S20.2a F22)", gotChunks, wantChunks)
	}

	// The end-to-end detoast. Everything above is reachable with an EMPTY
	// TOAST heap; this is not. pg_indexes' rule is stored out of line, so
	// evaluating it forces PG through heap_fetch_toast_slice → index 2839 →
	// pglz_decompress → stringToNode on bytes goopg's initdb wrote.
	out, err := pgQueryScalarAllowError(pg, "SELECT count(*) FROM pg_catalog.pg_indexes")
	if err != nil {
		t.Errorf("hosted PG cannot evaluate pg_indexes, whose 9002 B ev_action is "+
			"stored out of line in pg_toast_2618 under chunk_id 12047: %v\n%s\n"+
			"A \"missing chunk number N for toast value 12047\" here means the "+
			"chunk rows or their 2839 index entries are wrong; a stringToNode "+
			"error means the reassembled bytes are (the S20.2a break directions "+
			"were a dropped va_tcinfo word and a va_rawsize taken from the "+
			"compressed size).", err, strings.TrimSpace(out))
		return
	}
	if n, convErr := strconv.Atoi(strings.TrimSpace(out)); convErr != nil || n <= 0 {
		t.Errorf("hosted PG detoasted pg_indexes but it yielded %q, want a positive "+
			"row count — a goopg cluster has indexes", strings.TrimSpace(out))
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
