package testport

import (
	"context"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/executor"
	"github.com/goopg/goopg/internal/initdb"
	"github.com/goopg/goopg/internal/storage"
	"github.com/goopg/goopg/internal/testutil/cluster"
	"github.com/goopg/goopg/internal/testutil/pgcluster"
	"github.com/goopg/goopg/internal/access/transam/xlog"
)

type physicalFailoverMode struct {
	name              string
	sessionSyncCommit string
	primaryExtraConf  []string
	minCommits        int64
	exact             bool
	workloadDeadline  time.Duration
}

func TestE2E_FailoverPGtoGoopg(t *testing.T) {
	// M0102-0009 RESOLVED (2026-06-13): the PG->goopg physical failover repro
	// (async / sync_remote_apply / sync_on) now reaches streaming state and
	// passes all modes; it is no longer "blocked". Gate only on short mode +
	// tool availability, matching the other heterogeneous E2E tests
	// (e2e_replication_test.go). Set GOOPG_SKIP_M0102_E2E=1 to opt out of the
	// (slow, PG-binary-dependent) repro locally.
	if testing.Short() || os.Getenv("GOOPG_SKIP_M0102_E2E") != "" {
		t.Skip("skipping heterogeneous failover e2e (short mode or GOOPG_SKIP_M0102_E2E set)")
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
			workloadDeadline: 20 * time.Second,
		},
		{
			name:              "sync_remote_apply",
			sessionSyncCommit: "remote_apply",
			primaryExtraConf: []string{
				"synchronous_standby_names = 'goopg_standby'",
				"synchronous_commit = 'local'",
			},
			minCommits:       3,
			exact:            true,
			workloadDeadline: 30 * time.Second,
		},
		{
			name:              "sync_on",
			sessionSyncCommit: "on",
			primaryExtraConf: []string{
				"synchronous_standby_names = 'goopg_standby'",
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
			runFailoverPGToGoopg(t, repo, pgBasebackupBin, psqlBin, mode)
		})
	}
}

func runFailoverPGToGoopg(t *testing.T, repo, pgBasebackupBin, psqlBin string, mode physicalFailoverMode) {
	t.Helper()

	const slotName = "goopg_standby"
	baseDir := t.TempDir()
	primary, err := pgcluster.New("m0102-pg-primary", pgcluster.Options{
		RepoRoot:    repo,
		DataDir:     filepath.Join(baseDir, "pg-primary"),
		User:        "postgres",
		WalLevel:    "replica",
		ExtraConf:   mode.primaryExtraConf,
		StartupWait: 30 * time.Second,
	})
	if err != nil {
		t.Fatalf("pgcluster.New: %v", err)
	}
	defer func() { _ = primary.Stop() }()
	if err := primary.Start(); err != nil {
		t.Fatalf("primary.Start: %v", err)
	}
	readyCtx, readyCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer readyCancel()
	if err := primary.WaitReady(readyCtx, 15*time.Second); err != nil {
		t.Fatalf("primary.WaitReady: %v", err)
	}

	primary.Exec(t, "CREATE TABLE public.bench_log (client int NOT NULL, src text NOT NULL)")
	primary.Exec(t, fmt.Sprintf("SELECT pg_create_physical_replication_slot('%s')", slotName))

	standbyDir := filepath.Join(baseDir, "goopg-standby")
	runPGBasebackup(t, repo, pgBasebackupBin, primary, standbyDir, slotName)
	normalizePGWALSegmentNames(t, standbyDir)
	configureGoopgStandbyFromPGBackup(t, standbyDir, primary.Conninfo(slotName), slotName)

	standby, err := cluster.New("m0102-goopg-standby", cluster.Options{
		RepoRoot:     repo,
		DataDir:      standbyDir,
		StartupWait:  45 * time.Second,
		ShutdownWait: 10 * time.Second,
		SyncInit:     true,
		SyncRuntime:  true,
	})
	if err != nil {
		t.Fatalf("cluster.New standby: %v", err)
	}
	defer func() { _ = standby.Stop(cluster.ShutdownImmediate) }()
	if err := standby.Start(); err != nil {
		t.Fatalf("standby.Start: %v", err)
	}

	waitForPhysicalStreaming(t, primary, standby, slotName, mode.exact, 45*time.Second)

	primary.Exec(t, "INSERT INTO public.bench_log (client, src) VALUES (-999, 'bootstrap')")
	bootLSN := primary.QueryScalar(t, "SELECT pg_current_wal_lsn()")
	waitForGoopgCount(t, standby,
		"SELECT count(*) FROM public.bench_log WHERE client = -999",
		1, 30*time.Second, func() string { return walProgressDiagnostics(primary, standby, bootLSN) })

	dsn := fmt.Sprintf("host=%s port=%d user=%s dbname=%s sslmode=disable",
		primary.Host(), primary.Port(), primary.User(), primary.Database())
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

	waitForGoopgCount(t, standby,
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

	// The async lower bound is what the standby had already REPLAYED when
	// the primary died: promotion never rolls back replayed WAL, in PG or
	// in goopg. A fixed "killCommitted - N" bound assumed the stream lags
	// by at most N commits, which no async guarantee says; on a saturated
	// nightly host the walsender had shipped 2 of 38 (AI-20261014-001833-001).
	var preKillReplayed int64
	if !mode.exact {
		preKillReplayed = goopgCount(t, standby,
			"SELECT count(*) FROM public.bench_log WHERE src = 'pre'")
	}
	if err := primary.Kill(); err != nil {
		t.Fatalf("primary.Kill: %v", err)
	}
	if err := runGoopgPromote(repo, standby.DataDir()); err != nil {
		t.Fatalf("goopg promote: %v", err)
	}

	// M0106-0010 batched-34: verify finalizePromotion wrote the TLI-2
	// history file so downstream PG standbys can follow the history chain.
	histPath := filepath.Join(standby.DataDir(), "pg_wal", "00000002.history")
	if _, err := os.Stat(histPath); os.IsNotExist(err) {
		t.Fatalf("timeline history file missing after promotion: %s", histPath)
	}

	if err := runMultiHostInsert(repo, psqlBin, primary, standby,
		"INSERT INTO public.bench_log (client, src) VALUES (-1, 'post')"); err != nil {
		t.Fatalf("post-failover insert: %v", err)
	}
	checkPromotedTimelineWAL(t, standby.DataDir())

	preCount := goopgCount(t, standby,
		"SELECT count(*) FROM public.bench_log WHERE src = 'pre'")
	if mode.exact {
		if preCount != killCommitted {
			t.Fatalf("sync_remote_apply zero-loss violated: count(*)=%d want %d",
				preCount, killCommitted)
		}
	} else {
		if preCount < preKillReplayed {
			t.Fatalf("async failover lost replayed rows: count(*)=%d replayed_before_kill=%d killCommitted=%d",
				preCount, preKillReplayed, killCommitted)
		}
		t.Logf("async failover kept %d of %d committed rows (%d replayed before the kill)",
			preCount, killCommitted, preKillReplayed)
		if preCount > killCommitted {
			t.Fatalf("async failover observed extra pre rows: count(*)=%d killCommitted=%d",
				preCount, killCommitted)
		}
	}

	if got := goopgScalar(t, standby,
		"SELECT src FROM public.bench_log WHERE client = -1"); got != "post" {
		t.Fatalf("post-failover row src=%q want post", got)
	}
}

// checkPromotedTimelineWAL reads the promoted standby's new timeline with
// PG 18.3's pg_waldump from the switch point its history file records. The
// first record must be XLOG_END_OF_RECOVERY naming both timelines, as
// CreateEndOfRecoveryRecord writes it, and the records after it (the
// post-promotion insert) must decode: the writer switched timelines at once
// (Writer.SwitchTimeline) instead of writing TLI-1 WAL until a restart.
func checkPromotedTimelineWAL(t *testing.T, dataDir string) {
	t.Helper()
	waldump := clientToolBin(t, "pg_waldump")
	if waldump == "" {
		t.Log("pg_waldump not found; skipping the new-timeline WAL check")
		return
	}
	walDir := filepath.Join(dataDir, "pg_wal")
	hist, err := xlog.ReadHistory(walDir, 2)
	if err != nil || len(hist) == 0 {
		t.Fatalf("read 00000002.history: %v (entries %v)", err, hist)
	}
	sw := hist[len(hist)-1].SwitchLSN
	start := fmt.Sprintf("%X/%X", sw>>32, uint32(sw))
	out, err := exec.Command(waldump, "--path", walDir, "--timeline", "2", "--start", start, "--limit", "3").CombinedOutput()
	if err != nil {
		t.Fatalf("pg_waldump --timeline 2 --start %s: %v\n%s", start, err, out)
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) < 3 || !strings.Contains(lines[0], "desc: END_OF_RECOVERY tli 2; prev tli 1;") {
		t.Fatalf("new timeline's WAL from %s, want END_OF_RECOVERY first and two more records:\n%s", start, out)
	}
	// pg_waldump prints LSNs as %X/%08X (PG 18 LSN_FORMAT_ARGS).
	if !strings.Contains(lines[0], fmt.Sprintf("lsn: %X/%08X,", sw>>32, uint32(sw))) {
		t.Errorf("END_OF_RECOVERY is not at the switch point %s:\n%s", start, lines[0])
	}
}

func runPGBasebackup(t *testing.T, repo, bin string, primary *pgcluster.Cluster, outDir, slotName string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin,
		"-h", primary.Host(),
		"-p", strconv.Itoa(primary.Port()),
		"-U", primary.User(),
		"-D", outDir,
		"-X", "stream",
		"-S", slotName,
		"--no-sync",
		"--no-manifest",
		"-l", "TestE2E_FailoverPGtoGoopg")
	cmd.Env = clientToolEnv(repo)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("pg_basebackup failed: %v\n%s", err, out)
	}
}

// configureGoopgStandbyFromPGBackup points a base backup taken from a PG
// cluster at its new primary. The backup's postgresql.auto.conf is the
// source cluster's — when that cluster was itself a standby it still names
// the OLD primary — and the auto file overrides postgresql.conf, in goopg as
// in PG. So the settings are appended to postgresql.auto.conf, where the
// later occurrence wins, exactly as pg_basebackup -R does
// (WriteRecoveryConfig); postgresql.conf gets them too, replacing PG's
// full sample, which names parameters goopg does not register.
func configureGoopgStandbyFromPGBackup(t *testing.T, dataDir, conninfo, slotName string) {
	t.Helper()
	conf := fmt.Sprintf(
		"primary_conninfo = '%s'\nprimary_slot_name = '%s'\nwal_receiver_status_interval = 1\n",
		conninfo, slotName)
	if err := os.WriteFile(filepath.Join(dataDir, "postgresql.conf"), []byte(conf), 0o600); err != nil {
		t.Fatalf("write postgresql.conf: %v", err)
	}
	auto, err := os.OpenFile(filepath.Join(dataDir, "postgresql.auto.conf"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("open postgresql.auto.conf: %v", err)
	}
	if _, err := auto.WriteString(conf); err != nil {
		t.Fatalf("append postgresql.auto.conf: %v", err)
	}
	if err := auto.Close(); err != nil {
		t.Fatalf("close postgresql.auto.conf: %v", err)
	}
	if err := initdb.CreateStandbySignal(dataDir); err != nil {
		t.Fatalf("CreateStandbySignal: %v", err)
	}
}

func normalizePGWALSegmentNames(t *testing.T, dataDir string) {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(dataDir, "pg_wal"))
	if err != nil {
		t.Fatalf("read pg_wal: %v", err)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		tli, segno, ok := xlog.ParseXLogFileName(entry.Name(), 16<<20)
		if !ok {
			continue
		}
		oldPath := filepath.Join(dataDir, "pg_wal", entry.Name())
		newName := xlog.XLogFileName(tli, segno, 16<<20)
		newPath := filepath.Join(dataDir, "pg_wal", newName)
		if oldPath == newPath {
			continue
		}
		if err := os.Rename(oldPath, newPath); err != nil {
			t.Fatalf("rename WAL segment %s -> %s: %v", entry.Name(), newName, err)
		}
	}
}

func waitForPhysicalStreaming(t *testing.T, primary *pgcluster.Cluster, standby *cluster.Cluster, appName string, requireSync bool, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		primaryReady := primary.QueryScalar(t,
			fmt.Sprintf("SELECT count(*) FROM pg_stat_replication WHERE application_name = '%s' AND state = 'streaming'", appName)) == "1"
		syncReady := !requireSync || primary.QueryScalar(t,
			fmt.Sprintf("SELECT sync_state FROM pg_stat_replication WHERE application_name = '%s'", appName)) == "sync"
		standbyReady := false
		rows, err := standby.Query(context.Background(),
			"SELECT status FROM pg_catalog.pg_stat_wal_receiver")
		if err == nil && len(rows) > 0 && len(rows[0]) > 0 && rows[0][0] == "streaming" {
			standbyReady = true
		}
		if primaryReady && syncReady && standbyReady {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("physical replication did not reach streaming state within %s (requireSync=%v)", timeout, requireSync)
}

func waitForGoopgCount(t *testing.T, c *cluster.Cluster, query string, wantAtLeast int64, timeout time.Duration, extra ...func() string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var (
		lastCount int64
		lastErr   error
	)
	for time.Now().Before(deadline) {
		got, err := goopgCountMaybe(c, query)
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
	more := ""
	for _, f := range extra {
		more += f()
	}
	t.Fatalf("query did not reach count >= %d within %s: %s (last count=%d%s%s)",
		wantAtLeast, timeout, query, lastCount, benchLogDiagnostics(c, query), more)
}

// walProgressDiagnostics places a row the standby never showed on the WAL
// stream: the primary's WAL position right after the row's commit
// (wantLSN), what the primary's walsender reports sending and the standby
// confirming (pg_stat_replication), and what the standby received and
// replayed. sent < wantLSN means the row was never shipped; receive >=
// wantLSN > replay means it arrived but was not replayed; replay >= wantLSN
// means it was replayed but is not visible. Added for
// AI-20261021-010544-001, whose failure carried only "streaming" and an
// empty heap file (which pages still in the buffer pool also show).
// Best-effort: every failed probe degrades to its error text.
func walProgressDiagnostics(primary *pgcluster.Cluster, standby *cluster.Cluster, wantLSN string) string {
	ctx := context.Background()
	out := fmt.Sprintf(" want_lsn=%s", wantLSN)
	if db, err := primary.OpenDB(); err != nil {
		out += fmt.Sprintf(" primary_err=%v", err)
	} else {
		var cur string
		if err := db.QueryRowContext(ctx, "SELECT pg_current_wal_lsn()::text").Scan(&cur); err != nil {
			out += fmt.Sprintf(" primary_lsn_err=%v", err)
		} else {
			out += " primary_lsn=" + cur
		}
		rows, err := db.QueryContext(ctx, `SELECT application_name, state, coalesce(sent_lsn::text, ''),
			coalesce(write_lsn::text, ''), coalesce(flush_lsn::text, ''), coalesce(replay_lsn::text, '')
			FROM pg_stat_replication`)
		if err != nil {
			out += fmt.Sprintf(" pg_stat_replication_err=%v", err)
		} else {
			for rows.Next() {
				var app, state, sent, write, flush, replay string
				if err := rows.Scan(&app, &state, &sent, &write, &flush, &replay); err == nil {
					out += fmt.Sprintf(" walsender[%s %s sent=%s write=%s flush=%s replay=%s]", app, state, sent, write, flush, replay)
				}
			}
			rows.Close()
		}
		_ = db.Close()
	}
	if rows, err := standby.Query(ctx,
		"SELECT pg_last_wal_receive_lsn(), pg_last_wal_replay_lsn(), pg_is_in_recovery()"); err != nil {
		out += fmt.Sprintf(" standby_lsn_err=%v", err)
	} else if len(rows) > 0 {
		out += fmt.Sprintf(" standby_receive_replay_recovery=%v", rows[0])
	}
	return out
}

func goopgCount(t *testing.T, c *cluster.Cluster, query string) int64 {
	t.Helper()
	rows, err := c.Query(context.Background(), query)
	if err != nil {
		t.Fatalf("query %q: %v", query, err)
	}
	if len(rows) == 0 || len(rows[0]) == 0 {
		t.Fatalf("query %q returned no rows", query)
	}
	n, err := strconv.ParseInt(rows[0][0], 10, 64)
	if err != nil {
		t.Fatalf("parse count %q for %q: %v", rows[0][0], query, err)
	}
	return n
}

func goopgScalar(t *testing.T, c *cluster.Cluster, query string) string {
	t.Helper()
	rows, err := c.Query(context.Background(), query)
	if err != nil {
		t.Fatalf("query %q: %v", query, err)
	}
	if len(rows) == 0 || len(rows[0]) == 0 {
		t.Fatalf("query %q returned no rows", query)
	}
	return rows[0][0]
}

func goopgCountMaybe(c *cluster.Cluster, query string) (int64, error) {
	rows, err := c.Query(context.Background(), query)
	if err != nil || len(rows) == 0 || len(rows[0]) == 0 {
		if err != nil {
			return 0, err
		}
		return 0, fmt.Errorf("query %q returned no rows", query)
	}
	n, err := strconv.ParseInt(rows[0][0], 10, 64)
	if err != nil {
		return 0, err
	}
	return n, nil
}

func benchLogDiagnostics(c *cluster.Cluster, query string) string {
	if !strings.Contains(strings.ToLower(query), "bench_log") {
		return ""
	}
	ctx := context.Background()
	parts := make([]string, 0, 3)
	if total, err := goopgCountMaybe(c, "SELECT count(*) FROM public.bench_log"); err != nil {
		parts = append(parts, fmt.Sprintf(" total_err=%v", err))
	} else {
		parts = append(parts, fmt.Sprintf(" total=%d", total))
	}
	if rows, err := c.Query(ctx, "SELECT client, src FROM public.bench_log ORDER BY client LIMIT 5"); err != nil {
		parts = append(parts, fmt.Sprintf(" sample_err=%v", err))
	} else {
		parts = append(parts, fmt.Sprintf(" sample=%v", rows))
	}
	if rows, err := c.Query(ctx, "SELECT count(*) FROM pg_catalog.pg_class WHERE relname = 'bench_log'"); err != nil {
		parts = append(parts, fmt.Sprintf(" pg_class_err=%v", err))
	} else if len(rows) > 0 && len(rows[0]) > 0 {
		parts = append(parts, fmt.Sprintf(" pg_class_count=%s", rows[0][0]))
	}
	if rows, err := c.Query(ctx,
		"SELECT status, written_lsn, latest_end_lsn FROM pg_catalog.pg_stat_wal_receiver"); err != nil {
		parts = append(parts, fmt.Sprintf(" wal_receiver_err=%v", err))
	} else if len(rows) > 0 {
		parts = append(parts, fmt.Sprintf(" wal_receiver=%v", rows))
	}
	if heapDiag := benchLogHeapFileDiagnostics(c); heapDiag != "" {
		parts = append(parts, " "+heapDiag)
	}
	if tail := clusterLogTail(c.LogPath(), 12); tail != "" {
		parts = append(parts, fmt.Sprintf(" log_tail=%q", tail))
	}
	return strings.Join(parts, "")
}

func benchLogHeapFileDiagnostics(c *cluster.Cluster) string {
	ctx := context.Background()
	relSource := "relfilenode"
	relRows, err := c.Query(ctx,
		"SELECT relfilenode FROM pg_catalog.pg_class WHERE relname = 'bench_log'")
	if err != nil {
		relSource = "oid"
		relRows, err = c.Query(ctx,
			"SELECT oid FROM pg_catalog.pg_class WHERE relname = 'bench_log'")
		if err != nil {
			return fmt.Sprintf("heap_file_rel_err=%v", err)
		}
	}
	if len(relRows) == 0 || len(relRows[0]) == 0 {
		return "heap_file_rel_missing"
	}
	relfilenode := strings.TrimSpace(relRows[0][0])
	if relfilenode == "" || relfilenode == "0" {
		return fmt.Sprintf("heap_file=unavailable(source=%s rel=%s)", relSource, relfilenode)
	}
	matches, err := filepath.Glob(filepath.Join(c.DataDir(), "base", "*", relfilenode))
	if err != nil {
		return fmt.Sprintf("heap_file_glob_err=%v", err)
	}
	if len(matches) == 0 {
		return fmt.Sprintf("heap_file(source=%s) rel=%s missing", relSource, relfilenode)
	}
	parts := make([]string, 0, len(matches))
	for _, path := range matches {
		info, err := os.Stat(path)
		if err != nil {
			parts = append(parts, fmt.Sprintf("%s stat_err=%v", path, err))
			continue
		}
		parts = append(parts, fmt.Sprintf("%s size=%d %s", path, info.Size(), describeHeapPage(path)))
	}
	return fmt.Sprintf("heap_file(source=%s)=[%s]", relSource, strings.Join(parts, ", "))
}

func describeHeapPage(path string) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Sprintf("page0_read_err=%v", err)
	}
	if len(raw) < storage.BlockSize {
		return fmt.Sprintf("page0_short=%d", len(raw))
	}
	page := storage.Page(raw[:storage.BlockSize])
	hdr, err := storage.Header(page)
	if err != nil {
		return fmt.Sprintf("page0_header_err=%v", err)
	}
	lpCount, err := storage.PageLinePointerCount(page)
	if err != nil {
		return fmt.Sprintf("page0_lower=%d upper=%d lp_err=%v", hdr.Lower(), hdr.Upper(), err)
	}
	parts := []string{
		fmt.Sprintf("page0_lower=%d", hdr.Lower()),
		fmt.Sprintf("upper=%d", hdr.Upper()),
		fmt.Sprintf("lsn=%d", hdr.LSN()),
		fmt.Sprintf("lp_count=%d", lpCount),
	}
	for slot := uint16(1); slot <= uint16(lpCount) && slot <= 2; slot++ {
		item, err := storage.PageGetItemID(page, slot)
		if err != nil {
			parts = append(parts, fmt.Sprintf("slot%d_err=%v", slot, err))
			continue
		}
		parts = append(parts, fmt.Sprintf("slot%d_flags=%d", slot, item.Flags))
		parts = append(parts, fmt.Sprintf("slot%d_len=%d", slot, item.Length))
		if item.Flags != storage.ItemIDNormal {
			continue
		}
		tuple, err := storage.PageGetHeapTuple(page, slot)
		if err != nil {
			parts = append(parts, fmt.Sprintf("slot%d_tuple_err=%v", slot, err))
			continue
		}
		parts = append(parts, fmt.Sprintf("slot%d_xmin=%d", slot, tuple.Header.Xmin))
		parts = append(parts, fmt.Sprintf("slot%d_xmax=%d", slot, tuple.Header.Xmax))
		parts = append(parts, fmt.Sprintf("slot%d_hoff=%d", slot, tuple.Header.Hoff))
		parts = append(parts, fmt.Sprintf("slot%d_infomask=%#x", slot, tuple.Header.Infomask))
		parts = append(parts, fmt.Sprintf("slot%d_infomask2=%#x", slot, tuple.Header.Infomask2))
		parts = append(parts, fmt.Sprintf("slot%d_raw=%s", slot, hex.EncodeToString(tuple.Data)))
		parts = append(parts, fmt.Sprintf("slot%d_decode=%s", slot, decodeBenchLogTuple(tuple.Data)))
	}
	return strings.Join(parts, " ")
}

func decodeBenchLogTuple(data []byte) string {
	cols := []catalog.Column{
		{Name: "client", Type: catalog.Type{Name: "int4"}, Ordinal: 0},
		{Name: "src", Type: catalog.Type{Name: "text"}, Ordinal: 1},
	}
	row := make(executor.Row, len(cols))
	if err := executor.DecodeRowInto(row, cols, data); err != nil {
		return fmt.Sprintf("err=%v", err)
	}
	return fmt.Sprintf("ok(client=%d,src=%q)", row[0].Int, row[1].StringValue())
}

func clusterLogTail(path string, maxLines int) string {
	if maxLines <= 0 {
		return ""
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Sprintf("read_log_err=%v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) == 0 || (len(lines) == 1 && lines[0] == "") {
		return ""
	}
	if len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
	}
	return strings.Join(lines, " | ")
}

func runGoopgPromote(repo, dataDir string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "run", "./cmd/goopg", "promote", "-D", dataDir)
	cmd.Dir = repo
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w\n%s", err, out)
	}
	return nil
}

func runMultiHostInsert(repo, psqlBin string, primary *pgcluster.Cluster, standby *cluster.Cluster, sqlText string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conninfo := fmt.Sprintf(
		"host=%s,%s port=%d,%s user=%s dbname=%s sslmode=disable target_session_attrs=read-write connect_timeout=2",
		primary.Host(), "127.0.0.1",
		primary.Port(), mustGoopgPort(standby.ListenAddr()),
		primary.User(), primary.Database(),
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

func mustGoopgPort(addr string) string {
	_, port, err := netSplitHostPort(addr)
	if err != nil {
		panic(err)
	}
	return port
}

func netSplitHostPort(addr string) (string, string, error) {
	parts := strings.Split(addr, ":")
	if len(parts) != 2 {
		return "", "", fmt.Errorf("split host/port %q", addr)
	}
	return parts[0], parts[1], nil
}

func clientToolEnv(repo string) []string {
	libDir := filepath.Join(repo, "postgres", "local_install", "lib")
	prev := os.Getenv("LD_LIBRARY_PATH")
	ld := libDir
	if prev != "" {
		ld += ":" + prev
	}
	return append(os.Environ(), "PGPASSWORD=", "LD_LIBRARY_PATH="+ld)
}

func recordFailoverWorkErr(errCh chan error, format string, args ...any) {
	select {
	case errCh <- fmt.Errorf(format, args...):
	default:
	}
}

func failoverWorkErr(errCh chan error) error {
	select {
	case err := <-errCh:
		return err
	default:
		return nil
	}
}
