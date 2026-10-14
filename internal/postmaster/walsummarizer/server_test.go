package walsummarizer

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/backup/walsummary"
	"github.com/goopg/goopg/internal/testutil/cluster"
)

// TestServerSummarizesWAL runs the summarizer inside a real goopg server
// (M0122-0009 S3b). summarize_wal is PGC_SIGHUP: WalSummarizerMain runs while
// it is on and exits when it is turned off, and summarization resumes from
// the summaries on disk (GetOldestUnsummarizedLSN), so the files stay
// contiguous across a reload off/on and a restart. Each CHECKPOINT writes
// an XLOG_CHECKPOINT_REDO record, which closes a summary.
func TestServerSummarizesWAL(t *testing.T) {
	if testing.Short() {
		t.Skip("starts a goopg server")
	}
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	c, err := cluster.New("walsum", cluster.Options{
		RepoRoot:     root,
		DataDir:      filepath.Join(t.TempDir(), "data"),
		StartupWait:  60 * time.Second,
		ShutdownWait: 30 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Init(); err != nil {
		t.Fatal(err)
	}
	if err := c.AppendPostgresqlConf("summarize_wal = on"); err != nil {
		t.Fatal(err)
	}
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	defer c.Stop(cluster.ShutdownFast)
	ctx := context.Background()
	exec := func(sql string) [][]string {
		t.Helper()
		rows, err := c.Query(ctx, sql)
		if err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
		return rows
	}
	files := func() []walsummary.File {
		t.Helper()
		l, err := walsummary.List(c.DataDir(), 0, 0, 0)
		if err != nil {
			t.Fatal(err)
		}
		return l
	}
	// waitFiles polls until at least n summaries exist: the summarizer reads
	// a redo record once the checkpoint has flushed it.
	waitFiles := func(n int) []walsummary.File {
		t.Helper()
		deadline := time.Now().Add(20 * time.Second)
		for {
			if l := files(); len(l) >= n {
				return l
			}
			if time.Now().After(deadline) {
				t.Fatalf("waited for %d summaries, have %v", n, files())
			}
			time.Sleep(100 * time.Millisecond)
		}
	}

	exec("CREATE TABLE ws(a int)")
	exec("INSERT INTO ws SELECT generate_series(1, 3000)")
	exec("CHECKPOINT")
	first := waitFiles(1)
	rel := exec("SELECT relfilenode FROM pg_class WHERE relname = 'ws'")[0][0]
	n := exec("SELECT count(*) FROM pg_available_wal_summaries() s, pg_wal_summary_contents(s.tli, s.start_lsn, s.end_lsn) c WHERE c.relfilenode = " + rel)
	if n[0][0] == "0" {
		t.Errorf("no summary lists a block of ws (relfilenode %s)", rel)
	}

	// Off: no new summary, even across a checkpoint.
	if err := c.AppendPostgresqlConf("summarize_wal = off"); err != nil {
		t.Fatal(err)
	}
	if err := c.Reload(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(500 * time.Millisecond)
	exec("INSERT INTO ws VALUES (1)")
	exec("CHECKPOINT")
	time.Sleep(time.Second)
	if got := files(); len(got) != len(first) {
		t.Fatalf("summarize_wal = off still wrote summaries: %v", got)
	}

	// Back on: resumes where it stopped, with no gap.
	if err := c.AppendPostgresqlConf("summarize_wal = on"); err != nil {
		t.Fatal(err)
	}
	if err := c.Reload(); err != nil {
		t.Fatal(err)
	}
	exec("INSERT INTO ws VALUES (2)")
	exec("CHECKPOINT")
	waitFiles(len(first) + 1)

	// A restart resumes from the files too.
	if err := c.Restart(cluster.ShutdownFast); err != nil {
		t.Fatal(err)
	}
	before := len(files())
	exec("INSERT INTO ws VALUES (3)")
	exec("CHECKPOINT")
	all := waitFiles(before + 1)
	if ok, missing := walsummary.AreComplete(all, all[0].Start, all[len(all)-1].End); !ok {
		t.Errorf("summaries %v have a gap at %X", all, missing)
	}
}

// TestServerSummarizerState pins pg_get_wal_summarizer_state()
// (GetWalSummarizerState) through the same off → on → off sequence a
// scratch PG 18.3 answered with: 0, 0/0, 0/0, NULL before the summarizer
// has run; the timeline, a pid and summarized_lsn <= pending_lsn while it
// runs; pending_lsn = summarized_lsn and a NULL pid once it has stopped.
func TestServerSummarizerState(t *testing.T) {
	if testing.Short() {
		t.Skip("starts a goopg server")
	}
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	c, err := cluster.New("walsumstate", cluster.Options{
		RepoRoot:     root,
		DataDir:      filepath.Join(t.TempDir(), "data"),
		StartupWait:  60 * time.Second,
		ShutdownWait: 30 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Init(); err != nil {
		t.Fatal(err)
	}
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	defer c.Stop(cluster.ShutdownFast)
	ctx := context.Background()
	state := func() []string {
		t.Helper()
		rows, err := c.Query(ctx, "SELECT summarized_tli, summarized_lsn, pending_lsn, coalesce(summarizer_pid::text, 'NULL') FROM pg_get_wal_summarizer_state()")
		if err != nil || len(rows) != 1 {
			t.Fatalf("pg_get_wal_summarizer_state: %v %v", rows, err)
		}
		return rows[0]
	}
	lsn := func(s string) uint64 {
		var hi, lo uint64
		if _, err := fmt.Sscanf(s, "%X/%X", &hi, &lo); err != nil {
			t.Fatalf("bad pg_lsn %q", s)
		}
		return hi<<32 | lo
	}
	if got := strings.Join(state(), "|"); got != "0|0/0|0/0|NULL" {
		t.Fatalf("before the summarizer ran: %s, want 0|0/0|0/0|NULL", got)
	}
	if err := c.AppendPostgresqlConf("summarize_wal = on"); err != nil {
		t.Fatal(err)
	}
	if err := c.Reload(); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Query(ctx, "CHECKPOINT"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(20 * time.Second)
	var run []string
	for {
		run = state()
		if run[0] == "1" && run[3] != "NULL" && lsn(run[1]) > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("summarizer never reported running: %v", run)
		}
		time.Sleep(100 * time.Millisecond)
	}
	if lsn(run[1]) > lsn(run[2]) {
		t.Errorf("running: summarized_lsn %s > pending_lsn %s", run[1], run[2])
	}
	// While it runs: one pg_stat_activity row with backend_type
	// walsummarizer, and its WAL page reads in pg_stat_io (WS-002 polls
	// sum(reads) > 0 there).
	one := func(sql string) string {
		t.Helper()
		rows, err := c.Query(ctx, sql)
		if err != nil || len(rows) != 1 {
			t.Fatalf("%s: %v %v", sql, rows, err)
		}
		return rows[0][0]
	}
	const actSQL = "SELECT count(*) FROM pg_stat_activity WHERE backend_type = 'walsummarizer'"
	const ioSQL = "SELECT reads > 0 AND read_bytes = reads * 8192 FROM pg_stat_io WHERE backend_type = 'walsummarizer' AND object = 'wal' AND context = 'normal'"
	if got := one(actSQL); got != "1" {
		t.Errorf("pg_stat_activity walsummarizer rows while running = %s, want 1", got)
	}
	if got := one(ioSQL); got != "true" {
		t.Errorf("pg_stat_io walsummarizer WAL reads while running: %s, want reads > 0", got)
	}
	if err := c.AppendPostgresqlConf("summarize_wal = off"); err != nil {
		t.Fatal(err)
	}
	if err := c.Reload(); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(20 * time.Second)
	for {
		st := state()
		if st[3] == "NULL" {
			if st[0] != "1" || st[1] != st[2] || lsn(st[1]) < lsn(run[1]) {
				t.Errorf("stopped: %v, want tli 1 and pending_lsn = summarized_lsn >= %s", st, run[1])
			}
			if got := one(actSQL); got != "0" {
				t.Errorf("pg_stat_activity walsummarizer rows after stopping = %s, want 0", got)
			}
			if got := one(ioSQL); got != "true" {
				t.Errorf("pg_stat_io walsummarizer reads must stay cumulative after stopping: %s", got)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("summarizer never reported stopped: %v", st)
		}
		time.Sleep(100 * time.Millisecond)
	}
}
