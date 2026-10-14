package walsummarizer

import (
	"context"
	"path/filepath"
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
