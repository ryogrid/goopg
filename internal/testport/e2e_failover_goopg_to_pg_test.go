package testport

import (
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
	"github.com/goopg/goopg/internal/wal"
)

func TestE2E_FailoverGoopgToPG(t *testing.T) {
	if os.Getenv("GOOPG_RUN_BLOCKED_M0102_E2E") == "" {
		t.Skip("blocked: set GOOPG_RUN_BLOCKED_M0102_E2E=1 to run the goopg->PG physical failover repro")
	}

	if testing.Short() {
		t.Skip("skipping heterogeneous failover e2e in short mode")
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
	})
	if err != nil {
		t.Fatalf("cluster.New primary: %v", err)
	}
	defer func() { _ = primary.Stop(cluster.ShutdownImmediate) }()
	if err := primary.Init(); err != nil {
		t.Fatalf("primary.Init: %v", err)
	}
	createGoopgPhysicalReplicationSlot(t, primary.DataDir(), slotName)
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
	runGoopgBasebackupToPG(t, repo, pgBasebackupBin, primary, standbyDir, slotName)
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
	readyCtx, readyCancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer readyCancel()
	if err := standby.WaitReady(readyCtx, 20*time.Second); err != nil {
		t.Fatalf("standby.WaitReady: %v", err)
	}

	waitForPhysicalStreamingGoopgToPG(t, primary, standby, slotName, mode.exact, 45*time.Second)

	if err := runSQLSimple(t, primary,
		"INSERT INTO public.bench_log (client, src) VALUES (-999, 'bootstrap')"); err != nil {
		t.Fatalf("bootstrap insert on goopg primary: %v", err)
	}
	waitForPGCount(t, standby,
		"SELECT count(*) FROM public.bench_log WHERE client = -999",
		1, 30*time.Second)

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
		"-S", slotName,
		"--no-sync",
		"--no-manifest",
		"-l", "TestE2E_FailoverGoopgToPG")
	cmd.Env = clientToolEnv(repo)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("pg_basebackup from goopg failed: %v\n%s", err, out)
	}
}

func createGoopgPhysicalReplicationSlot(t *testing.T, dataDir, slotName string) {
	t.Helper()
	slots, err := wal.OpenSlots(dataDir)
	if err != nil {
		t.Fatalf("open goopg replication slots: %v", err)
	}
	if _, err := slots.Create(slotName, wal.SlotPhysical, 0); err != nil && err != wal.ErrSlotExists {
		t.Fatalf("create goopg physical replication slot %q: %v", slotName, err)
	}
}

func configurePGStandbyFromGoopgBackup(t *testing.T, dataDir, conninfo, slotName string) {
	t.Helper()
	conf := fmt.Sprintf(
		"primary_conninfo = '%s'\nprimary_slot_name = '%s'\nwal_receiver_status_interval = 1\n",
		conninfo, slotName)
	if err := os.WriteFile(filepath.Join(dataDir, "postgresql.auto.conf"), []byte(conf), 0o600); err != nil {
		t.Fatalf("write postgresql.auto.conf: %v", err)
	}
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
		standbyReady := standby.QueryScalar(t,
			"SELECT status FROM pg_catalog.pg_stat_wal_receiver") == "streaming"
		if primaryReady && syncReady && standbyReady {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("physical replication did not reach streaming state within %s (requireSync=%v)", timeout, requireSync)
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
