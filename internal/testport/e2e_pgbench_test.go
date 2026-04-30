package testport

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/testutil/cluster"
)

func TestE2E_PgbenchWorkload(t *testing.T) {
	pgbench := pgbenchPath(t)
	if pgbench == "" {
		t.Skip("pgbench not found")
	}

	c, err := cluster.New("e2e_pgbench", cluster.Options{
		RepoRoot:      repoRoot(t),
		DataDir:       t.TempDir(),
		StartupWait:   30 * time.Second,
		ShutdownWait:  10 * time.Second,
		PGbenchPath:   pgbench,
	})
	if err != nil {
		t.Fatal(err)
	}
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	// Set LD_LIBRARY_PATH so the in-tree pgbench resolves libpq symbols.
	libDir := filepath.Join(repoRoot(t), "postgres", "local_install", "lib")
	prev := os.Getenv("LD_LIBRARY_PATH")
	os.Setenv("LD_LIBRARY_PATH", libDir+":"+prev)
	defer os.Setenv("LD_LIBRARY_PATH", prev)

	// pgbench -i (initialize)
	res, err := c.PGbench("-i")
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("pgbench -i exit=%d stderr=%q", res.ExitCode, res.Stderr)
	}

	// pgbench -S -t 5 (select-only)
	res, err = c.PGbench("-S", "-t", "5")
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("pgbench -S exit=%d stderr=%q", res.ExitCode, res.Stderr)
	}
	if !strings.Contains(res.Stdout, "tps") {
		t.Logf("pgbench -S output: %s", res.Stdout)
	}
}

// pgbenchPath returns the path to pgbench, checking the in-tree path first.
func pgbenchPath(t *testing.T) string {
	t.Helper()
	root := repoRoot(t)
	local := filepath.Join(root, "postgres", "local_install", "bin", "pgbench")
	if s, err := os.Stat(local); err == nil && s.Mode().IsRegular() {
		return local
	}
	return ""
}
