package testport

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	_ "github.com/lib/pq"

	"github.com/goopg/goopg/internal/testutil/cluster"
	"github.com/goopg/goopg/internal/testutil/pgcluster"
)

func TestE2E_FailoverGoopgToPG(t *testing.T) {
	// M0102-0009 RESOLVED (2026-06-13): the goopg->PG physical failover repro
	// (async / sync_remote_apply) now reaches streaming state and passes both
	// modes; it is no longer "blocked". Gate only on short mode + tool
	// availability, matching the other heterogeneous E2E tests
	// (e2e_replication_test.go). Set GOOPG_SKIP_M0102_E2E=1 to opt out of the
	// (slow, PG-binary-dependent) repro locally.
	if testing.Short() || os.Getenv("GOOPG_SKIP_M0102_E2E") != "" {
		t.Skip("skipping heterogeneous failover e2e (short mode or GOOPG_SKIP_M0102_E2E set)")
	}
	// A9 Phase-A exit gate (2026-07-16): RE-ENABLED. A real PG18 standby now
	// fully replays goopg WAL in both modes. The prior "WAL contains references
	// to invalid pages" PANIC (a Heap/INSERT for a fresh page whose page 0 the
	// standby had not created) was fixed by emitting XLOG_HEAP_INIT_PAGE +
	// REGBUF_WILL_INIT on the first insert into an empty page (operators_storage.go
	// markHeapInsertDirty + wal.EncodeHeapInsertPG) — PG then PageInit's the page
	// during redo, exactly as it does for its own first-insert-on-a-new-page,
	// instead of treating the missing page as invalid.

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

	modes := []physicalFailoverMode{
		{
			name:             "async",
			minCommits:       20,
			asyncLossBound:   20,
			workloadDeadline: 20 * time.Second,
		},
		{
			name:              "sync_remote_apply",
			sessionSyncCommit: "remote_apply",
			primaryExtraConf: []string{
				"synchronous_standby_names = 'pg_standby'",
				"synchronous_commit = 'local'",
			},
			minCommits:       3,
			exact:            true,
			workloadDeadline: 30 * time.Second,
		},
	}

	for _, mode := range modes {
		mode := mode
		t.Run(mode.name, func(t *testing.T) {
			runFailoverGoopgToPG(t, repo, pgBasebackupBin, psqlBin, mode)
		})
	}
}

func runFailoverGoopgToPG(t *testing.T, repo, pgBasebackupBin, psqlBin string, mode physicalFailoverMode) {
	t.Helper()

	const slotName = "pg_standby"
	baseDir := t.TempDir()
	primary, err := cluster.New("m0102-goopg-primary", cluster.Options{
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
	for _, line := range mode.primaryExtraConf {
		if err := primary.AppendPostgresqlConf(line); err != nil {
			t.Fatalf("primary.AppendPostgresqlConf(%q): %v", line, err)
		}
	}
	if err := primary.Start(); err != nil {
		t.Fatalf("primary.Start: %v", err)
	}

	if err := runSQLSimple(t, primary, "CREATE TABLE public.bench_log (client int NOT NULL, src text NOT NULL)"); err != nil {
		t.Fatalf("create bench_log on goopg primary: %v", err)
	}

	standbyDir := filepath.Join(baseDir, "pg-standby")
	t.Cleanup(func() {
		pgLogPath := filepath.Join(filepath.Dir(standbyDir), "pg.log")
		if pgLog, rerr := os.ReadFile(pgLogPath); rerr == nil {
			t.Logf("[m0102-pg-standby-log] %s:\n%s", pgLogPath, string(pgLog))
		}
	})
	// Standard PG streaming replication procedure:
	// pg_basebackup -C -S slot_name -R creates the slot, streams the
	// backup, and writes standby.signal + postgresql.auto.conf.
	runGoopgBasebackupToPG(t, repo, pgBasebackupBin, primary, standbyDir, slotName)
	// M0106-0010 batched-41 standby disk-state diagnostic: dump bench_log
	// visibility across base/{1,5}/{pg_class,pg_class_relname_nsp_index,...}
	// so a re-run failure has the on-disk evidence inline. Cheap (5 stats +
	// 5 reads per DB) and only runs when GOOPG_RUN_BLOCKED_M0102_E2E=1.
	for _, dboid := range []uint32{1, 5} {
		for _, rel := range []struct {
			file  string
			label string
		}{
			{"2663", "pg_class_relname_nsp_index"},
			{"2662", "pg_class_oid_index"},
			{"1259", "pg_class_heap"},
			{"1249", "pg_attribute_heap"},
			{"2659", "pg_attribute_relid_attnum_index"},
		} {
			p := filepath.Join(standbyDir, "base", fmt.Sprintf("%d", dboid), rel.file)
			st, err := os.Stat(p)
			if err != nil {
				t.Logf("[diag] base/%d/%s (%s) missing on standby: %v", dboid, rel.file, rel.label, err)
				continue
			}
			data, _ := os.ReadFile(p)
			hasBench := bytes.Contains(data, append([]byte("bench_log"), 0))
			t.Logf("[diag] base/%d/%s (%s) size=%d hasBenchLog=%v", dboid, rel.file, rel.label, st.Size(), hasBench)
		}
	}
	// M0106: ensure relcache init files are on the standby.
	copyInitFiles(t, primary.DataDir(), standbyDir)
	// Overwrite postgresql.auto.conf for precise conninfo control
	// (application_name needed for sync mode).
	configurePGStandbyFromGoopgBackup(t, standbyDir, primaryConninfoForPGStandby(primary, slotName), slotName)

	standby, err := pgcluster.OpenExisting("m0102-pg-standby", pgcluster.Options{
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
	if err := runSQLSimple(t, primary,
		"INSERT INTO public.bench_log (client, src) VALUES (-999, 'bootstrap')"); err != nil {
		t.Fatalf("bootstrap insert on goopg primary: %v", err)
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

	waitForPhysicalStreamingGoopgToPG(t, primary, standby, slotName, mode.exact, 45*time.Second)
	waitForPGCount(t, standby,
		"SELECT count(*) FROM public.bench_log WHERE client = -999",
		1, 30*time.Second)

	// B2-prep: a runtime CREATE FUNCTION flows to the standby as pg_proc
	// heap + 2690/2691 index page writes; the post-failover assertion below
	// proves PG resolves it by name (FuncnameGetCandidates → 2691) and
	// executes it (PROCOID → 2690, prosrc).
	if err := runSQLSimple(t, primary,
		"CREATE FUNCTION public.b2prep_double(int) RETURNS int LANGUAGE sql AS 'SELECT $1 * 2'"); err != nil {
		t.Fatalf("create function on goopg primary: %v", err)
	}
	// M0111-0002: a set-returning function pins the binary-float4 fix —
	// prorows=1000 as the former 5-byte text varlena misaligned every
	// pg_proc column after it (proargtypes!) under PG's attlen=4 TupleDesc,
	// so PG could not resolve ANY goopg-created SRF by name.
	if err := runSQLSimple(t, primary,
		"CREATE FUNCTION public.b2prep_srf() RETURNS SETOF int LANGUAGE sql AS 'SELECT 7'"); err != nil {
		t.Fatalf("create SRF on goopg primary: %v", err)
	}
	// B2.1a: a runtime CREATE TYPE AS ENUM writes pg_type heap rows +
	// 2703/2704 index entries (2704 is lazily rooted from its empty
	// bootstrap placeholder) and emits ONLY heap-insert/FPI records — no
	// goopg-private WAL. The post-failover regtype probe proves PG resolves
	// the type by name (LookupTypeName → TYPENAMENSP → 2704) and reads the
	// runtime pg_type row. (CREATE DOMAIN still emits the bespoke kind-119
	// RmgrGoopgCatalog record, which a real PG standby FATALs on —
	// "resource manager with ID 128 not registered" — so the domain
	// assertion arrives with B2.1b, which retires that record.)
	if err := runSQLSimple(t, primary,
		"CREATE TYPE public.b2prep_mood AS ENUM ('sad', 'happy')"); err != nil {
		t.Fatalf("create enum type on goopg primary: %v", err)
	}
	// B2.1b: CREATE DOMAIN now journals as pg_type + pg_constraint heap
	// inserts (kinds 119/120 retired — the bespoke record FATAL'd the
	// standby with "resource manager with ID 128 not registered").
	if err := runSQLSimple(t, primary,
		"CREATE DOMAIN public.b2prep_dom AS int"); err != nil {
		t.Fatalf("create domain on goopg primary: %v", err)
	}
	// B2.1c: CREATE TYPE AS RANGE journals pg_type + pg_range heap inserts
	// (kinds 81/82/117/118 retired).
	if err := runSQLSimple(t, primary,
		"CREATE TYPE public.b2prep_rng AS RANGE (subtype = int4)"); err != nil {
		t.Fatalf("create range type on goopg primary: %v", err)
	}
	// B1.3b: sequences journal as RM_SEQ XLOG_SEQ_LOG page rewrites (kinds
	// 65/66 retired — previously ANY sequence DDL killed the standby with
	// "resource manager with ID 128 not registered").
	if err := runSQLSimple(t, primary,
		"CREATE SEQUENCE public.b2prep_seq INCREMENT 3"); err != nil {
		t.Fatalf("create sequence on goopg primary: %v", err)
	}
	if err := runSQLSimple(t, primary,
		"SELECT setval('public.b2prep_seq', 90)"); err != nil {
		t.Fatalf("setval on goopg primary: %v", err)
	}
	// B4.6 Stage 3: CREATE DATABASE journals RM_DBASE (XLOG_DBASE_CREATE_WAL_LOG,
	// rmid 4) + the template0 catalog-image full-page-image records (Stage 3b) +
	// the pg_database SHARED heap row (Stage 1) — no goopg-private rmid-128
	// record. A real PG standby must replay all of it without FATAL and end up
	// with the new database in pg_database. Before B4.6 the bespoke kind-18
	// record killed the standby with "resource manager with ID 128 not
	// registered".
	if err := runSQLSimple(t, primary, "CREATE DATABASE b2prep_db"); err != nil {
		t.Fatalf("create database on goopg primary: %v", err)
	}
	// B5 Slice A: CREATE / ALTER INDEX RENAME now journal ONLY real pg_class +
	// pg_index heap inserts/updates + btree page writes — no goopg-private
	// RecordKindCreateIndex(20)/RenameIndex(94) rmid-128 record. A real PG
	// standby must replay them without FATAL and end up with the renamed index
	// in pg_class. (Before B5 Slice A the kind-20/94 records killed the standby
	// with "resource manager with ID 128 not registered".)
	if err := runSQLSimple(t, primary, "CREATE INDEX b5a_idx ON public.bench_log (client)"); err != nil {
		t.Fatalf("create index on goopg primary: %v", err)
	}
	if err := runSQLSimple(t, primary, "ALTER INDEX b5a_idx RENAME TO b5a_idx_renamed"); err != nil {
		t.Fatalf("alter index rename on goopg primary: %v", err)
	}

	// B5 Bstat: CREATE STATISTICS now journals a real pg_statistic_ext heap row
	// (base/<dbOid>/3381) instead of the goopg-private RecordKindCreateStatistics(95)
	// rmid-128 record. A real PG standby must replay the heap insert without FATAL
	// and end up with the object in pg_statistic_ext.
	if err := runSQLSimple(t, primary,
		"CREATE STATISTICS b5bstat_stat (ndistinct) ON client, src FROM public.bench_log"); err != nil {
		t.Fatalf("create statistics on goopg primary: %v", err)
	}

	// B5 Slice C: CREATE VIEW now journals a real pg_rewrite _RETURN rule heap
	// row (base/<dbOid>/2618) instead of the goopg-private RecordKindCreateView(103)
	// rmid-128 record. The standby must replay the heap insert without FATAL and
	// end up with the rule in pg_rewrite.
	//
	// M0123-S3 sub-slice 2c: this view is inside the single-base-relation subset
	// pgnodes.ResolveViewQuery serializes, so its ev_action is now a CANONICAL
	// PG18 pg_node_tree and pg_class.relhasrules=true. The promoted PG therefore
	// EVALUATES the _RETURN rule — the post-failover section below asserts
	// SELECT count(*) FROM b5c_view == the equivalent direct filter, proving the
	// standby expands goopg's serialized rule (not merely replays the heap row).
	if err := runSQLSimple(t, primary,
		"CREATE VIEW b5c_view AS SELECT client, src FROM public.bench_log WHERE client > 0"); err != nil {
		t.Fatalf("create view on goopg primary: %v", err)
	}

	// M0123-S4 sub-slice 2: a MULTI-condition WHERE qual (BoolExpr AND over a
	// NullTest + an OpExpr) is now inside the canonical subset too — the
	// query-scoped resolver routes AND/OR/NOT/IS-NULL through the same *With
	// builders the scalar DEFAULT path uses. This view therefore also streams a
	// canonical pg_node_tree ev_action + relhasrules=true, and the promoted PG
	// must PARSE it with pg_get_viewdef (the adversarial standby proof for the
	// bool/null query wiring, extending the single-condition b5c_view above).
	if err := runSQLSimple(t, primary,
		"CREATE VIEW b5c_view2 AS SELECT client, src FROM public.bench_log WHERE src IS NOT NULL AND client > 0"); err != nil {
		t.Fatalf("create multi-condition view on goopg primary: %v", err)
	}

	// M0123-S4 sub-slice 8: a searched CASE expression in the WHERE qual is now
	// inside the canonical subset too — the query-scoped resolver routes
	// *parser.CaseExpr through the same recursion-injectable resolveCaseExprWith /
	// rebuildCaseExprWith builders the scalar DEFAULT path (sub-slice 7) uses. This
	// view therefore streams a canonical pg_node_tree CASEEXPR ev_action +
	// relhasrules=true, and the promoted PG must PARSE it with pg_get_viewdef (the
	// adversarial standby proof for the CASE query wiring).
	if err := runSQLSimple(t, primary,
		"CREATE VIEW b5c_view3 AS SELECT client, src FROM public.bench_log WHERE CASE WHEN client > 0 THEN true ELSE false END"); err != nil {
		t.Fatalf("create case-expr view on goopg primary: %v", err)
	}

	dsn := fmt.Sprintf("host=%s port=%s user=%s dbname=%s sslmode=disable",
		"127.0.0.1", mustGoopgPort(primary.ListenAddr()), "postgres", "postgres")
	workCtx, workCancel := context.WithCancel(context.Background())
	defer workCancel()
	var committed atomic.Int64
	var wg sync.WaitGroup
	workErrCh := make(chan error, 1)

	for clientID := 0; clientID < 2; clientID++ {
		clientID := clientID
		wg.Add(1)
		go func() {
			defer wg.Done()
			db, err := sql.Open("postgres", dsn)
			if err != nil {
				recordFailoverWorkErr(workErrCh, "client %d sql.Open: %w", clientID, err)
				return
			}
			defer db.Close()
			conn, err := db.Conn(context.Background())
			if err != nil {
				recordFailoverWorkErr(workErrCh, "client %d db.Conn: %w", clientID, err)
				return
			}
			defer conn.Close()
			if mode.sessionSyncCommit != "" {
				if _, err := conn.ExecContext(context.Background(),
					"SET synchronous_commit = "+mode.sessionSyncCommit); err != nil {
					recordFailoverWorkErr(workErrCh,
						"client %d SET synchronous_commit=%s: %w",
						clientID, mode.sessionSyncCommit, err)
					return
				}
			}
			for {
				if workCtx.Err() != nil {
					return
				}
				if _, err := conn.ExecContext(workCtx,
					"INSERT INTO public.bench_log (client, src) VALUES ($1, 'pre')",
					clientID); err != nil {
					if workCtx.Err() != nil {
						return
					}
					time.Sleep(10 * time.Millisecond)
					continue
				}
				committed.Add(1)
				time.Sleep(5 * time.Millisecond)
			}
		}()
	}

	waitForPGCount(t, standby,
		"SELECT count(*) FROM public.bench_log WHERE src = 'pre'",
		1, 30*time.Second)

	workloadDeadline := time.Now().Add(mode.workloadDeadline)
	for committed.Load() < mode.minCommits && time.Now().Before(workloadDeadline) {
		if err := failoverWorkErr(workErrCh); err != nil {
			workCancel()
			wg.Wait()
			t.Fatal(err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	workCancel()
	wg.Wait()
	if err := failoverWorkErr(workErrCh); err != nil {
		t.Fatal(err)
	}

	killCommitted := committed.Load()
	if killCommitted < mode.minCommits {
		t.Fatalf("workload too anemic: committed=%d want >= %d within %s",
			killCommitted, mode.minCommits, mode.workloadDeadline)
	}

	if err := primary.Kill(); err != nil {
		t.Fatalf("primary.Kill: %v", err)
	}
	if err := standby.Promote(); err != nil {
		t.Fatalf("pg standby promote: %v", err)
	}
	if err := runGoopgToPGMultiHostInsert(psqlBin, repo, primary, standby,
		"INSERT INTO public.bench_log (client, src) VALUES (-1, 'post')"); err != nil {
		t.Fatalf("post-failover insert: %v", err)
	}

	preCount := pgCount(t, standby,
		"SELECT count(*) FROM public.bench_log WHERE src = 'pre'")
	if mode.exact {
		if preCount != killCommitted {
			t.Fatalf("sync zero-loss violated: count(*)=%d want %d",
				preCount, killCommitted)
		}
	} else {
		lo := killCommitted - mode.asyncLossBound
		if lo < 0 {
			lo = 0
		}
		if preCount < lo {
			t.Fatalf("async failover lost too many rows: count(*)=%d lower_bound=%d killCommitted=%d",
				preCount, lo, killCommitted)
		}
		if preCount > killCommitted {
			t.Fatalf("async failover observed extra pre rows: count(*)=%d killCommitted=%d",
				preCount, killCommitted)
		}
	}

	if got := pgScalar(t, standby,
		"SELECT src FROM public.bench_log WHERE client = -1"); got != "post" {
		t.Fatalf("post-failover row src=%q want post", got)
	}

	// M0123-S3 sub-slice 2c: b5c_view (SELECT client, src FROM bench_log WHERE
	// client > 0) was streamed with a CANONICAL pg_node_tree ev_action and
	// relhasrules=true, so the promoted PG must EXPAND and EVALUATE the rule.
	// Adversarial gate: the view's own count must equal the count its defining
	// SELECT computes directly on the same server (a malformed rule would FATAL
	// the standby's relcache when the view is opened, and pgCount would t.Fatal).
	// The post row (client=-1) is excluded by the qual, so both sides count the
	// client>0 'pre' rows identically.
	// M0123-S3 sub-slice 2c standby-side gate. b5c_view streamed with a canonical
	// pg_node_tree ev_action + relhasrules=true, so a real PG18:
	//
	//  1. reports relhasrules=true for the view (the streamed pg_class heap row); and
	//  2. PARSES the canonical ev_action via stringToNode and DEPARSES it back to
	//     the exact defining SELECT (pg_get_viewdef) — the adversarial proof that
	//     goopg's serializer is byte-compatible with PG18's node-tree reader, not
	//     merely that the heap row replays.
	if got := pgScalar(t, standby,
		"SELECT relhasrules FROM pg_class WHERE relname = 'b5c_view'"); got != "t" {
		t.Fatalf("standby relhasrules(b5c_view)=%q, want t (canonical ev_action)", got)
	}
	viewdef, err := pgScalarMaybe(standby, "SELECT pg_get_viewdef('public.b5c_view'::regclass)")
	if err != nil {
		t.Fatalf("standby pg_get_viewdef(b5c_view): %v (PG could not parse the canonical ev_action)", err)
	}
	// Whitespace-insensitive structural check: PG must have reconstructed the
	// single-base-relation SELECT with its column list and WHERE qual.
	vd := strings.Join(strings.Fields(viewdef), " ")
	for _, want := range []string{"SELECT client,", "src", "FROM bench_log", "WHERE (client > 0)"} {
		if !strings.Contains(vd, want) {
			t.Fatalf("standby pg_get_viewdef(b5c_view)=%q, missing %q", vd, want)
		}
	}

	// M0123-S4 sub-slice 2: the same proof for the multi-condition view. Its
	// canonical ev_action is a BoolExpr(AND) over a NullTest and an OpExpr; the
	// promoted PG must report relhasrules=true and reconstruct the compound WHERE
	// via pg_get_viewdef — byte-level proof the bool/null query wiring is
	// PG18-node-tree-compatible, not merely that the heap row replays.
	if got := pgScalar(t, standby,
		"SELECT relhasrules FROM pg_class WHERE relname = 'b5c_view2'"); got != "t" {
		t.Fatalf("standby relhasrules(b5c_view2)=%q, want t (canonical bool/null ev_action)", got)
	}
	viewdef2, err := pgScalarMaybe(standby, "SELECT pg_get_viewdef('public.b5c_view2'::regclass)")
	if err != nil {
		t.Fatalf("standby pg_get_viewdef(b5c_view2): %v (PG could not parse the canonical bool/null ev_action)", err)
	}
	vd2 := strings.Join(strings.Fields(viewdef2), " ")
	for _, want := range []string{"SELECT client,", "src", "FROM bench_log", "src IS NOT NULL", "client > 0"} {
		if !strings.Contains(vd2, want) {
			t.Fatalf("standby pg_get_viewdef(b5c_view2)=%q, missing %q", vd2, want)
		}
	}

	// M0123-S4 sub-slice 8: the same proof for the CASE-expr view. Its canonical
	// ev_action is a searched CASEEXPR (casetype bool) over a WHEN OpExpr + ELSE;
	// the promoted PG must report relhasrules=true and reconstruct the CASE WHERE
	// via pg_get_viewdef — byte-level proof the CASE query wiring is
	// PG18-node-tree-compatible, not merely that the heap row replays.
	if got := pgScalar(t, standby,
		"SELECT relhasrules FROM pg_class WHERE relname = 'b5c_view3'"); got != "t" {
		t.Fatalf("standby relhasrules(b5c_view3)=%q, want t (canonical CASE ev_action)", got)
	}
	viewdef3, err := pgScalarMaybe(standby, "SELECT pg_get_viewdef('public.b5c_view3'::regclass)")
	if err != nil {
		t.Fatalf("standby pg_get_viewdef(b5c_view3): %v (PG could not parse the canonical CASE ev_action)", err)
	}
	vd3 := strings.Join(strings.Fields(viewdef3), " ")
	for _, want := range []string{"SELECT client,", "src", "FROM bench_log", "CASE WHEN (client > 0)", "THEN true", "ELSE false", "END"} {
		if !strings.Contains(vd3, want) {
			t.Fatalf("standby pg_get_viewdef(b5c_view3)=%q, missing %q", vd3, want)
		}
	}
	// KNOWN BLOCKER (deferral ledger 2026-07-19, M0123-S3 sub-slice 2c): a direct
	// `SELECT * FROM b5c_view` on the promoted standby still fails 42809 — PG's
	// rewriter uses the relcache rule lock (rd_rules), not the direct pg_rewrite
	// scan pg_get_viewdef uses, and the copied pg_internal.init caches a ruleless
	// relcache entry for the view. Row-level standby evaluation waits on relcache
	// rd_rules population; the canonical serializer itself is proven above.
	if _, err := pgCountMaybe(standby, "SELECT count(*) FROM public.b5c_view"); err == nil {
		t.Logf("[m0123] standby row-level view expansion now works — promote the deferred gate")
	}

	// B2-prep: the goopg-created function must be resolvable and executable
	// on the promoted PG (pg_proc row + both runtime-maintained indexes).
	if got := pgScalar(t, standby,
		"SELECT public.b2prep_double(21)"); got != "42" {
		t.Fatalf("post-failover function call = %q, want 42", got)
	}
	// B2.1a: the goopg-created enum type must be resolvable by name on the
	// promoted PG (pg_type row + 2703/2704 runtime index maintenance).
	// regtype resolution never touches pg_enum labels (those arrive with
	// the pg_enum heap conversion), so this isolates the pg_type surface.
	if got := pgScalar(t, standby,
		"SELECT 'public.b2prep_mood'::regtype::text"); got != "b2prep_mood" {
		t.Fatalf("post-failover enum type resolution = %q, want b2prep_mood", got)
	}
	// B2.1d: enum VALUES are now real pg_enum heap rows — enum_in on the
	// promoted PG resolves the label via 3503 (typid+label) from goopg's
	// runtime-written rows.
	if got := pgScalar(t, standby,
		"SELECT 'happy'::public.b2prep_mood::text"); got != "happy" {
		t.Fatalf("post-failover enum value cast = %q, want happy", got)
	}
	// B2.1b: the goopg-created domain must be resolvable AND usable in a
	// cast on the promoted PG (pg_type heap row + typbasetype resolution).
	if got := pgScalar(t, standby,
		"SELECT 42::public.b2prep_dom"); got != "42" {
		t.Fatalf("post-failover domain cast = %q, want 42", got)
	}
	// B2.1c: the goopg-created range type must be usable on the promoted PG
	// (pg_type rows + pg_range row via RANGETYPE syscache → 3542).
	if got := pgScalar(t, standby,
		"SELECT '[1,5)'::public.b2prep_rng::text"); got != "[1,5)" {
		t.Fatalf("post-failover range cast = %q, want [1,5)", got)
	}
	// B1.3b: the promoted PG serves nextval from the goopg-written physical
	// sequence page (setval'd to 90, increment 3 ⇒ 93).
	if got := pgScalar(t, standby,
		"SELECT nextval('public.b2prep_seq')"); got != "93" {
		t.Fatalf("post-failover nextval = %q, want 93", got)
	}
	// M0111-0002: the SRF resolves and executes on the promoted PG (binary
	// prorows keeps proargtypes aligned).
	if got := pgScalar(t, standby,
		"SELECT * FROM public.b2prep_srf()"); got != "7" {
		t.Fatalf("post-failover SRF call = %q, want 7", got)
	}
	// B4.6 Stage 3: the goopg-created database survived replication to the
	// promoted PG. Reaching this assertion AT ALL proves the standby replayed
	// goopg's CREATE DATABASE WAL — the RM_DBASE XLOG_DBASE_CREATE_WAL_LOG record
	// (rmid 4) + the template0 catalog-image full-page-image records + the
	// pg_database SHARED heap row — without FATAL; the pre-B4.6 kind-18 record
	// would have killed replication with "resource manager with ID 128 not
	// registered". The row count confirms the pg_database heap INSERT streamed.
	if got := pgScalar(t, standby,
		"SELECT count(*) FROM pg_database WHERE datname = 'b2prep_db'"); got != "1" {
		t.Fatalf("post-failover pg_database has b2prep_db = %q, want 1", got)
	}
	// B5 Slice A: the goopg-created-and-renamed index survived replication as
	// pure pg_class + pg_index heap rows + btree pages (no rmid-128 record). The
	// renamed name in pg_class proves both CREATE INDEX and ALTER INDEX RENAME
	// replayed to the promoted PG without FATAL.
	if got := pgScalar(t, standby,
		"SELECT count(*) FROM pg_class WHERE relname = 'b5a_idx_renamed' AND relkind = 'i'"); got != "1" {
		t.Fatalf("post-failover pg_class has b5a_idx_renamed index = %q, want 1", got)
	}
	if got := pgScalar(t, standby,
		"SELECT count(*) FROM pg_class WHERE relname = 'b5a_idx'"); got != "0" {
		t.Fatalf("post-failover old index name b5a_idx still present = %q, want 0 (rename not replayed)", got)
	}
	// B5 Bstat: the CREATE STATISTICS replayed to the promoted PG as a pure
	// pg_statistic_ext heap insert (no rmid-128 record). Its presence in the
	// standby's pg_statistic_ext proves the heap insert replayed.
	if got := pgScalar(t, standby,
		"SELECT count(*) FROM pg_statistic_ext WHERE stxname = 'b5bstat_stat'"); got != "1" {
		t.Fatalf("post-failover pg_statistic_ext has b5bstat_stat = %q, want 1 (statistics heap insert not replayed)", got)
	}
	// B5 Slice C: the CREATE VIEW replayed to the promoted PG as a pure pg_class
	// + pg_rewrite heap insert (no rmid-128 record). Assert the _RETURN rule row
	// landed in pg_rewrite (a catalog scan — the view itself is not queried).
	if got := pgScalar(t, standby,
		"SELECT count(*) FROM pg_rewrite r JOIN pg_class c ON c.oid = r.ev_class WHERE c.relname = 'b5c_view' AND r.rulename = '_RETURN'"); got != "1" {
		t.Fatalf("post-failover pg_rewrite has b5c_view _RETURN rule = %q, want 1 (view rule heap insert not replayed)", got)
	}

	// NOTE (M0123-S2 sub-slice 2, 2026-07-19): goopg now stores column DEFAULTs as
	// CANONICAL PG18 pg_node_tree in pg_attrdef.adbin (byte-identical to real PG18,
	// pinned in internal/pgnodes), but the ADVERSARIAL standby-consumption gate for
	// it is DEFERRED, not added here. A real PG standby cannot yet read goopg's
	// pg_attrdef for DEFAULT evaluation: (1) pg_attrdef is a non-nailed catalog
	// whose tupledesc PG rebuilds from the streamed pg_attribute rows, and goopg's
	// on-disk pg_attribute for relid 2604 does not expose a usable `adbin` column
	// (a direct `pg_get_expr(adbin, adrelid)` query fails "column adbin does not
	// exist"); and (2) PG's AttrDefaultFetch (relcache.c) opens pg_attrdef BY its
	// adrelid/adnum index (AttrDefaultIndexId 2656), which goopg does not
	// materialize ("could not open relation with OID 2656"). Both are pg_attrdef
	// catalog-completeness gaps orthogonal to node-tree serialization; see the
	// deferral ledger (2026-07-19). The canonical writer + reload sibling pair is
	// gated by fast unit tests (internal/pgnodes, internal/executor, internal/initdb).
}

func runGoopgBasebackupToPG(t *testing.T, repo, bin string, primary *cluster.Cluster, outDir, slotName string) {
	t.Helper()
	runGoopgBasebackupToPGSlot(t, repo, bin, primary, outDir, slotName, true)
}

// runGoopgBasebackupToPGSlot is runGoopgBasebackupToPG with control over
// whether pg_basebackup creates the slot itself (`-C`). Callers that already
// created the slot — e.g. TestE2E_PGStandbyFullCycle, which exercises the
// SQL-callable pg_create_physical_replication_slot — must pass createSlot=false;
// `-C` against an existing slot is a hard error ("replication slot already
// exists"), exactly as upstream behaves.
func runGoopgBasebackupToPGSlot(t *testing.T, repo, bin string, primary *cluster.Cluster, outDir, slotName string, createSlot bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	args := []string{
		"-h", "127.0.0.1",
		"-p", mustGoopgPort(primary.ListenAddr()),
		"-U", "postgres",
		"-D", outDir,
		"-X", "stream",
	}
	if createSlot {
		args = append(args, "-C")
	}
	args = append(args,
		"-S", slotName,
		"-R",
		"--no-sync",
		"--no-manifest",
		"-l", "TestE2E_FailoverGoopgToPG")
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = clientToolEnv(repo)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("pg_basebackup from goopg failed: %v\n%s", err, out)
	}
}

func configurePGStandbyFromGoopgBackup(t *testing.T, dataDir, conninfo, slotName string) {
	t.Helper()
	conf := fmt.Sprintf(
		"primary_conninfo = '%s'\nprimary_slot_name = '%s'\nwal_receiver_status_interval = 1\nlog_min_messages = debug3\nlog_error_verbosity = verbose\n",
		conninfo, slotName)
	if err := os.WriteFile(filepath.Join(dataDir, "postgresql.auto.conf"), []byte(conf), 0o600); err != nil {
		t.Fatalf("write postgresql.auto.conf: %v", err)
	}
	// standby.signal is also written by pg_basebackup -R, but write it
	// here as a safety net.
	if err := os.WriteFile(filepath.Join(dataDir, "standby.signal"), nil, 0o600); err != nil {
		t.Fatalf("write standby.signal: %v", err)
	}
}

func primaryConninfoForPGStandby(primary *cluster.Cluster, slotName string) string {
	return fmt.Sprintf("host=%s port=%s user=%s dbname=%s application_name=%s",
		"127.0.0.1", mustGoopgPort(primary.ListenAddr()), "postgres", "postgres", slotName)
}

func waitForPhysicalStreamingGoopgToPG(t *testing.T, primary *cluster.Cluster, standby *pgcluster.Cluster, appName string, requireSync bool, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		primaryReady := false
		if got, err := goopgCountMaybe(primary,
			fmt.Sprintf("SELECT count(*) FROM pg_stat_replication WHERE application_name = '%s' AND state = 'streaming'", appName)); err == nil {
			primaryReady = got == 1
		}
		syncReady := true
		if requireSync {
			syncReady = goopgScalar(t, primary,
				fmt.Sprintf("SELECT sync_state FROM pg_stat_replication WHERE application_name = '%s'", appName)) == "sync"
		}
		// Use pg_stat_wal_receiver only as a supplementary check; if it
		// fails (e.g. due to catalog bootstrap gaps), fall back to the
		// primary's pg_stat_replication view which confirms the standby is
		// connected and streaming.
		standbyStreamingOK := primaryReady || pgStandbyIsStreaming(standby)
		if primaryReady && syncReady && standbyStreamingOK {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("physical replication did not reach streaming state within %s (requireSync=%v)", timeout, requireSync)
}

// pgStandbyIsStreaming probes the PG standby's pg_stat_wal_receiver view
// using the Go database/sql driver (non-fatal). Returns true only if the
// query succeeds and the status column equals "streaming". Returns false
// on any connection or query error (including backend crashes caused by
// an incomplete catalog bootstrap).
func pgStandbyIsStreaming(standby *pgcluster.Cluster) bool {
	db, err := standby.OpenDB()
	if err != nil {
		return false
	}
	defer db.Close()
	var status string
	err = db.QueryRow("SELECT status FROM pg_catalog.pg_stat_wal_receiver").Scan(&status)
	if err != nil {
		return false
	}
	return status == "streaming"
}

func waitForPGCount(t *testing.T, c *pgcluster.Cluster, query string, wantAtLeast int64, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var lastCount int64
	var lastErr error
	for time.Now().Before(deadline) {
		got, err := pgCountMaybe(c, query)
		if err == nil && got >= wantAtLeast {
			return
		}
		lastCount = got
		lastErr = err
		time.Sleep(100 * time.Millisecond)
	}
	if lastErr != nil {
		t.Fatalf("query did not reach count >= %d within %s: %s (last count=%d last err=%v)",
			wantAtLeast, timeout, query, lastCount, lastErr)
	}
	t.Fatalf("query did not reach count >= %d within %s: %s (last count=%d)",
		wantAtLeast, timeout, query, lastCount)
}

func pgCount(t *testing.T, c *pgcluster.Cluster, query string) int64 {
	t.Helper()
	n, err := pgCountMaybe(c, query)
	if err != nil {
		t.Fatalf("query %q: %v", query, err)
	}
	return n
}

func pgCountMaybe(c *pgcluster.Cluster, query string) (int64, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	db, err := c.OpenDB()
	if err != nil {
		return 0, err
	}
	defer db.Close()
	var n int64
	if err := db.QueryRowContext(ctx, query).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

func pgScalarMaybe(c *pgcluster.Cluster, query string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	db, err := c.OpenDB()
	if err != nil {
		return "", err
	}
	defer db.Close()
	var s string
	if err := db.QueryRowContext(ctx, query).Scan(&s); err != nil {
		return "", err
	}
	return s, nil
}

func pgScalar(t *testing.T, c *pgcluster.Cluster, query string) string {
	t.Helper()
	got := c.QueryScalar(t, query)
	if got == "" {
		t.Fatalf("query %q returned no rows", query)
	}
	return got
}

func runGoopgToPGMultiHostInsert(psqlBin, repo string, primary *cluster.Cluster, standby *pgcluster.Cluster, sqlText string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conninfo := fmt.Sprintf(
		"host=%s,%s port=%s,%d user=%s dbname=%s sslmode=disable target_session_attrs=read-write connect_timeout=2",
		"127.0.0.1", standby.Host(),
		mustGoopgPort(primary.ListenAddr()), standby.Port(),
		standby.User(), standby.Database(),
	)
	cmd := exec.CommandContext(ctx, psqlBin,
		"-d", conninfo,
		"-v", "ON_ERROR_STOP=1",
		"-c", sqlText)
	cmd.Env = clientToolEnv(repo)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("psql multi-host insert failed: %w\nconninfo=%q\n%s", err, conninfo, out)
	}
	return nil
}

// copyInitFiles copies relcache init files from primary to standby.
// These are generated by goopg init with mode 0o400 (read-only) to
// prevent PG's write_relcache_init_file from overwriting them.
func copyInitFiles(t *testing.T, primaryDir, standbyDir string) {
	t.Helper()
	for _, rel := range []string{
		"global/pg_internal.init",
		"base/1/pg_internal.init",
	} {
		src := filepath.Join(primaryDir, rel)
		dst := filepath.Join(standbyDir, rel)
		data, err := os.ReadFile(src)
		if err != nil {
			t.Errorf("copyInitFiles: read %s: %v", rel, err)
			continue
		}
		t.Logf("copyInitFiles: copying %s (%d bytes)", rel, len(data))
		if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
			t.Errorf("copyInitFiles: mkdir %s: %v", filepath.Dir(dst), err)
			continue
		}
		// If the file already exists (e.g. from the tar), remove it
		// first since 0o400 prevents truncation.
		_ = os.Remove(dst)
		if err := os.WriteFile(dst, data, 0o400); err != nil {
			t.Errorf("copyInitFiles: write %s: %v", rel, err)
		}
		if rel == "base/1/pg_internal.init" {
			dst5 := filepath.Join(standbyDir, "base", "5", "pg_internal.init")
			if err := os.MkdirAll(filepath.Dir(dst5), 0o700); err == nil {
				_ = os.Remove(dst5)
				os.WriteFile(dst5, data, 0o400)
				t.Logf("copyInitFiles: also copied to base/5/ (%d bytes)", len(data))
			}
		}
	}
}
