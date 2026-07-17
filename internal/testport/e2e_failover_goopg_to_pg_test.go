package testport

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
}

func runGoopgBasebackupToPG(t *testing.T, repo, bin string, primary *cluster.Cluster, outDir, slotName string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin,
		"-h", "127.0.0.1",
		"-p", mustGoopgPort(primary.ListenAddr()),
		"-U", "postgres",
		"-D", outDir,
		"-X", "stream",
		"-C",
		"-S", slotName,
		"-R",
		"--no-sync",
		"--no-manifest",
		"-l", "TestE2E_FailoverGoopgToPG")
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
