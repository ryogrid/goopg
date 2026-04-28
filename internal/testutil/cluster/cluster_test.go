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
