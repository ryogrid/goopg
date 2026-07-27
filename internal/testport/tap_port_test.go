package testport

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/testutil/cluster"
)

func TestPort_Initdb001(t *testing.T) {
	// upstream: postgres/src/bin/initdb/t/001_initdb.pl
	c := newCluster(t, "initdb001")
	if err := c.Init(); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"PG_VERSION", "postgresql.conf", "pg_hba.conf", "base", "global", "pg_wal", "pg_xact"} {
		if _, err := os.Stat(filepath.Join(c.DataDir(), p)); err != nil {
			t.Fatalf("missing %s: %v", p, err)
		}
	}
}

func TestPort_PgCtl001StartStop(t *testing.T) {
	// upstream: postgres/src/bin/pg_ctl/t/001_start_stop.pl
	c := newCluster(t, "pgctl001")
	mustInitStart(t, c)
	if err := c.Stop(cluster.ShutdownFast); err != nil {
		t.Fatal(err)
	}
	if err := c.WaitForStatus(3, 5*time.Second); err != nil {
		t.Fatal(err)
	}
}

func TestPort_PgCtl002Status(t *testing.T) {
	// upstream: postgres/src/bin/pg_ctl/t/002_status.pl
	c := newCluster(t, "pgctl002")
	if err := c.Init(); err != nil {
		t.Fatal(err)
	}
	if err := c.WaitForStatus(3, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()
	if err := c.WaitForStatus(0, 10*time.Second); err != nil {
		t.Fatal(err)
	}
}

func TestPort_PgCtl003PromoteAdapted(t *testing.T) {
	// upstream: postgres/src/bin/pg_ctl/t/003_promote.pl
	// goopg v0 has no replication/promote mode; we port this as a control-plane
	// checkpoint smoke covering an online sync operation.
	c := newCluster(t, "pgctl003")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()
	if err := c.Checkpoint(); err != nil {
		t.Fatal(err)
	}
}

func TestPort_PgCtl004LogrotateAdapted(t *testing.T) {
	// upstream: postgres/src/bin/pg_ctl/t/004_logrotate.pl
	// goopg v0 uses a single append log file; ported as truncate+reload+append.
	c := newCluster(t, "pgctl004")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()
	if err := c.TruncateLog(); err != nil {
		t.Fatal(err)
	}
	if err := c.Reload(); err != nil {
		t.Fatal(err)
	}
	if err := c.Checkpoint(); err != nil {
		t.Fatal(err)
	}
	if err := c.WaitForLogContains("checkpoint", 5*time.Second); err != nil {
		t.Fatal(err)
	}
}

func TestPort_Pgbench001WithServer(t *testing.T) {
	// upstream: postgres/src/bin/pgbench/t/001_pgbench_with_server.pl
	if _, err := exec.LookPath("pgbench"); err != nil {
		t.Skip("pgbench not installed")
	}
	c := newCluster(t, "pgbench001")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()
	res, err := c.PGbench("-S", "-t", "2")
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("pgbench exit=%d stderr=%q", res.ExitCode, res.Stderr)
	}
}

func TestPort_Pgbench002NoServer(t *testing.T) {
	// upstream: postgres/src/bin/pgbench/t/002_pgbench_no_server.pl
	if _, err := exec.LookPath("pgbench"); err != nil {
		t.Skip("pgbench not installed")
	}
	c := newCluster(t, "pgbench002")
	if err := c.Init(); err != nil {
		t.Fatal(err)
	}
	res, err := c.PGbench("-S", "-t", "1")
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode == 0 {
		t.Fatalf("pgbench expected failure without server; stdout=%q stderr=%q", res.Stdout, res.Stderr)
	}
}

func TestPort_Psql001Basic(t *testing.T) {
	// upstream: postgres/src/bin/psql/t/001_basic.pl
	if _, err := exec.LookPath("psql"); err != nil {
		t.Skip("psql not installed")
	}
	c := newCluster(t, "psql001")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()
	res, err := c.PSQL("-Atqc", "SELECT 1")
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("psql exit=%d stderr=%q", res.ExitCode, res.Stderr)
	}
	if strings.TrimSpace(res.Stdout) != "1" {
		t.Fatalf("stdout=%q want 1", res.Stdout)
	}
}

func TestPort_Psql010TabCompletionAdapted(t *testing.T) {
	// upstream: postgres/src/bin/psql/t/010_tab_completion.pl
	// Tab-completion needs an interactive tty; ported as psql meta-command smoke.
	if _, err := exec.LookPath("psql"); err != nil {
		t.Skip("psql not installed")
	}
	c := newCluster(t, "psql010")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()
	res, err := c.PSQL("-Atqc", "\\h SELECT")
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("psql help exit=%d stderr=%q", res.ExitCode, res.Stderr)
	}
}

func TestPort_Psql020CancelAdapted(t *testing.T) {
	// upstream: postgres/src/bin/psql/t/020_cancel.pl
	// Ported as client-side interrupt smoke: kill an active psql process and
	// verify the server remains responsive.
	if _, err := exec.LookPath("psql"); err != nil {
		t.Skip("psql not installed")
	}
	c := newCluster(t, "psql020")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	bg, err := c.StartPSQL(nil, "")
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	_ = bg.Stop()
	_ = bg.Wait()

	rows, err := c.Query(context.Background(), "SELECT 1")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || len(rows[0]) != 1 || rows[0][0] != "1" {
		t.Fatalf("server not responsive after client cancel; rows=%v", rows)
	}
}

func newCluster(t *testing.T, name string) *cluster.Cluster {
	t.Helper()
	c, err := cluster.New(name, cluster.Options{
		RepoRoot:     repoRoot(t),
		DataDir:      filepath.Join(t.TempDir(), "data"),
		StartupWait:  20 * time.Second,
		ShutdownWait: 20 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// newDurableCluster is newCluster for tests on the durability allowlist
// (ci/design/test-gate-speedups/02 §4): crash/kill-then-reopen recovery,
// basebackup/replication, and restart-durability tests keep the synced init
// path (no template clone) and runtime fsync, because the durable path is
// part of what they assert.
func newDurableCluster(t *testing.T, name string) *cluster.Cluster {
	t.Helper()
	c, err := cluster.New(name, cluster.Options{
		RepoRoot:     repoRoot(t),
		DataDir:      filepath.Join(t.TempDir(), "data"),
		StartupWait:  20 * time.Second,
		ShutdownWait: 20 * time.Second,
		SyncInit:     true,
		SyncRuntime:  true,
	})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func mustInitStart(t *testing.T, c *cluster.Cluster) {
	t.Helper()
	if err := c.Init(); err != nil {
		t.Fatal(err)
	}
	if err := c.Start(); err != nil {
		t.Fatal(err)
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
