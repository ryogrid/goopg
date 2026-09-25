package cluster

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestClusterInitAndConfigAppend(t *testing.T) {
	repoRoot := repoRoot(t)
	base := t.TempDir()
	c, err := New("cfg", Options{
		RepoRoot: repoRoot,
		DataDir:  filepath.Join(base, "data"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Init(); err != nil {
		t.Fatal(err)
	}
	if err := c.AppendPostgresqlConf("shared_buffers = 32MB"); err != nil {
		t.Fatal(err)
	}
	if err := c.AppendPGHBA("host all all 127.0.0.1/32 trust"); err != nil {
		t.Fatal(err)
	}
	cfg, err := os.ReadFile(filepath.Join(c.DataDir(), "postgresql.conf"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(cfg), "shared_buffers = 32MB") {
		t.Fatalf("postgresql.conf missing appended line: %q", string(cfg))
	}
	hba, err := os.ReadFile(filepath.Join(c.DataDir(), "pg_hba.conf"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(hba), "host all all 127.0.0.1/32 trust") {
		t.Fatalf("pg_hba.conf missing appended line: %q", string(hba))
	}
}

func TestClusterLifecycleAndQuery(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping lifecycle integration test in short mode")
	}
	repoRoot := repoRoot(t)
	base := t.TempDir()
	c, err := New("life", Options{
		RepoRoot:     repoRoot,
		DataDir:      filepath.Join(base, "data"),
		StartupWait:  20 * time.Second,
		ShutdownWait: 20 * time.Second,
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
	defer func() { _ = c.Stop(ShutdownImmediate) }()

	code, msg, err := c.Status()
	if err != nil {
		t.Fatal(err)
	}
	if code != 0 {
		t.Fatalf("status exit=%d msg=%q", code, msg)
	}

	rows, err := c.Query(context.Background(), "SELECT 1")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || len(rows[0]) != 1 || rows[0][0] != "1" {
		t.Fatalf("Query SELECT 1 rows=%v", rows)
	}
}

func TestClusterPSQLSelect1(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping psql integration test in short mode")
	}
	if _, err := exec.LookPath("psql"); err != nil {
		t.Skip("psql not installed")
	}
	repoRoot := repoRoot(t)
	base := t.TempDir()
	c, err := New("psql", Options{
		RepoRoot:     repoRoot,
		DataDir:      filepath.Join(base, "data"),
		StartupWait:  20 * time.Second,
		ShutdownWait: 20 * time.Second,
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
	defer func() { _ = c.Stop(ShutdownImmediate) }()

	res, err := c.PSQL("-Atqc", "SELECT 1")
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("psql exit=%d stderr=%q", res.ExitCode, res.Stderr)
	}
	if strings.TrimSpace(res.Stdout) != "1" {
		t.Fatalf("psql stdout=%q want 1", res.Stdout)
	}
}

func repoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	cur := wd
	for {
		if _, err := os.Stat(filepath.Join(cur, "go.mod")); err == nil {
			return cur
		}
		next := filepath.Dir(cur)
		if next == cur {
			t.Fatalf("could not find go.mod from %s", wd)
		}
		cur = next
	}
}

// TestStartFailureLogTailIsInlined pins that a start failure carries the
// cluster log's CONTENT, not merely its path.
//
// The error used to name the cluster.log path and stop there. That path lives
// inside a per-run temporary tree, and the nightly's testport stage runs in a
// throwaway worktree under tmp/nightly-src-<run>/ that is deleted when the run
// finishes — so by the time anyone read the failure, the file it pointed at was
// gone. TestPort_PgoutputInterop* failed with exactly that error on two
// consecutive nights and neither root cause could be recovered, because the
// only evidence was behind a deleted path. The failure does not reproduce on
// demand, so the practical way to learn its cause is to make the next
// occurrence carry it.
//
// The three cases below are the three states the log can be in when a start
// fails, and each must degrade to something readable rather than to a panic or
// to an error that replaces the start failure itself.
func TestStartFailureLogTailIsInlined(t *testing.T) {
	t.Run("content is inlined", func(t *testing.T) {
		dir := t.TempDir()
		logPath := filepath.Join(dir, "cluster.log")
		const needle = "FATAL: could not bind IPv4 address"
		if err := os.WriteFile(logPath, []byte("starting\n"+needle+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		c := &Cluster{logPath: logPath}
		got := c.startFailureLogTail()
		if !strings.Contains(got, needle) {
			t.Errorf("tail = %q, want it to contain %q — a start failure must carry "+
				"the log's content, because the path is deleted with the run", got, needle)
		}
	})

	t.Run("oversized log is truncated to the tail", func(t *testing.T) {
		dir := t.TempDir()
		logPath := filepath.Join(dir, "cluster.log")
		// The interesting lines are always the LAST ones, so a large log must
		// keep its end, not its beginning.
		const needle = "THE-ACTUAL-FAILURE"
		body := strings.Repeat("noise\n", 4096) + needle + "\n"
		if err := os.WriteFile(logPath, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		c := &Cluster{logPath: logPath}
		got := c.startFailureLogTail()
		if !strings.Contains(got, needle) {
			t.Errorf("tail dropped the final line; a truncated tail must keep the END of the log")
		}
		if len(got) > startFailureLogTailBytes+256 {
			t.Errorf("tail is %d bytes, want it bounded near %d — an unbounded dump "+
				"buries the assertion that reported it", len(got), startFailureLogTailBytes)
		}
	})

	t.Run("unreadable log degrades instead of masking the start failure", func(t *testing.T) {
		c := &Cluster{logPath: filepath.Join(t.TempDir(), "definitely-absent.log")}
		got := c.startFailureLogTail()
		if got == "" || !strings.Contains(got, "unreadable") {
			t.Errorf("tail = %q, want a readable note — reading the log is best-effort "+
				"and must never replace the start failure being reported", got)
		}
	})
}
