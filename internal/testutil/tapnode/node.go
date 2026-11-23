// Package tapnode is the Go counterpart of PostgreSQL's TAP
// PostgreSQL::Test::Cluster (src/test/perl/PostgreSQL/Test/Cluster.pm) for
// M0149's recovery/subscription ports: one Node API over a goopg server
// (internal/testutil/cluster) and a PostgreSQL 18.3 server
// (internal/testutil/pgcluster), so a ported test is written once and runs
// all-goopg and in the mixed topologies M0149's contract C3 requires.
//
// Methods mirror Cluster.pm's names where one exists (init, start,
// stop('immediate'), safe_psql) and are added as later ports need them, each
// working identically for both flavors. Design:
// docs/design/0100-0149/0149-0001b-tap-node-harness.md.
package tapnode

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/testutil/cluster"
	"github.com/goopg/goopg/internal/testutil/pgcluster"
)

// Flavor names the server implementation behind a Node.
type Flavor string

const (
	// Goopg is a goopg server.
	Goopg Flavor = "goopg"
	// PG is a PostgreSQL 18.3 server from postgres/local_install.
	PG Flavor = "pg18"
)

// StopMode is Cluster.pm's stop() mode.
type StopMode string

const (
	// StopFast is stop('fast'): a shutdown checkpoint, DB_SHUTDOWNED.
	StopFast StopMode = "fast"
	// StopImmediate is stop('immediate'): no checkpoint, the next start
	// runs crash recovery.
	StopImmediate StopMode = "immediate"
)

// Node is one server of a ported test.
type Node struct {
	t       testing.TB
	name    string
	flavor  Flavor
	repo    string
	binDir  string
	g       *cluster.Cluster
	p       *pgcluster.Cluster
	running bool
}

// Available skips t when the PG 18.3 client binaries the harness drives
// (psql, pg_ctl, postgres) are not built.
func Available(t testing.TB, repo string) {
	t.Helper()
	tt, ok := t.(*testing.T)
	if !ok {
		return
	}
	pgcluster.Available(tt, BinDir(repo))
}

// BinDir is the PG 18.3 bin directory under repo.
func BinDir(repo string) string { return filepath.Join(repo, "postgres", "local_install", "bin") }

// NewGoopg is Cluster.pm's new() + init() for a goopg node. The cluster is
// durable (synchronous initdb and fsync on), because ported recovery tests
// assert the durable path itself (test-gate-speedups/02 §4). It is stopped
// immediately at test cleanup if still running.
func NewGoopg(t testing.TB, repo, name string) *Node {
	t.Helper()
	g, err := cluster.New(name, cluster.Options{
		RepoRoot:     repo,
		DataDir:      filepath.Join(t.TempDir(), name),
		PSQLPath:     filepath.Join(BinDir(repo), "psql"),
		StartupWait:  60 * time.Second,
		ShutdownWait: 30 * time.Second,
		SyncInit:     true,
		SyncRuntime:  true,
	})
	if err != nil {
		t.Fatalf("tapnode %s: cluster.New: %v", name, err)
	}
	if err := g.Init(); err != nil {
		t.Fatalf("tapnode %s: init: %v", name, err)
	}
	n := &Node{t: t, name: name, flavor: Goopg, repo: repo, binDir: BinDir(repo), g: g}
	t.Cleanup(n.cleanup)
	return n
}

// Name is the node's name.
func (n *Node) Name() string { return n.name }

// Flavor is the node's server implementation.
func (n *Node) Flavor() Flavor { return n.flavor }

// DataDir is the node's data directory.
func (n *Node) DataDir() string {
	if n.g != nil {
		return n.g.DataDir()
	}
	return n.p.DataDir()
}

// LogPath is the server log file.
func (n *Node) LogPath() string {
	if n.g != nil {
		return n.g.LogPath()
	}
	return filepath.Join(filepath.Dir(n.p.DataDir()), "pg.log") // pgcluster's default
}

// Start is Cluster.pm's start(): it fails the test when the server does not
// come up, with the log tail.
func (n *Node) Start() {
	n.t.Helper()
	// Cluster.pm's start passes --cluster-name=<node> on the command line
	// (not in postgresql.conf, so a backup does not copy it to standbys);
	// a standby's walreceiver reports it as its application_name.
	var err error
	if n.g != nil {
		n.g.SetStartArgs("--cluster-name=" + n.name)
		err = n.g.Start()
	} else {
		n.p.SetStartArgs("--cluster-name=" + n.name)
		err = n.p.Start()
		if err == nil {
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			err = n.p.WaitReady(ctx, 90*time.Second)
			cancel()
		}
	}
	if err != nil {
		n.t.Fatalf("tapnode %s (%s): start: %v\n--- log tail ---\n%s", n.name, n.flavor, err, n.logTail(60))
	}
	n.running = true
}

// Stop is Cluster.pm's stop(mode).
func (n *Node) Stop(mode StopMode) {
	n.t.Helper()
	var err error
	switch {
	case n.g != nil && mode == StopImmediate:
		err = n.g.Stop(cluster.ShutdownImmediate)
	case n.g != nil:
		err = n.g.Stop(cluster.ShutdownFast)
	case mode == StopImmediate:
		err = n.p.Kill() // pg_ctl -m immediate stop
	default:
		err = n.p.Stop() // SIGINT: fast shutdown
	}
	if err != nil {
		n.t.Fatalf("tapnode %s (%s): stop %s: %v", n.name, n.flavor, mode, err)
	}
	n.running = false
}

// SafePSQL is Cluster.pm's safe_psql(dbname, sql): it runs sql through the
// PG 18.3 psql with ON_ERROR_STOP, fails the test on any error, and returns
// the unaligned, tuples-only output with the trailing newline removed.
func (n *Node) SafePSQL(db, sqlText string) string {
	n.t.Helper()
	out, err := n.PSQL(db, sqlText)
	if err != nil {
		n.t.Fatalf("tapnode %s (%s): safe_psql(%s, %q): %v\n%s", n.name, n.flavor, db, sqlText, err, out)
	}
	return out
}

// PSQL is Cluster.pm's psql(dbname, sql) without the failure: it returns
// psql's stdout (unaligned, tuples only, trailing newline trimmed) and an
// error carrying stderr when psql exits non-zero.
func (n *Node) PSQL(db, sqlText string) (string, error) {
	// Cluster.pm feeds the SQL on stdin (`psql -f -`), so psql sends each
	// statement as its own query, each in its own transaction. `-c` would
	// send the whole script as one multi-statement query, one implicit
	// transaction block, which refuses VACUUM and changes what TRUNCATE
	// logs. A file gives psql the same statement-by-statement reading.
	f, err := os.CreateTemp(n.t.TempDir(), "psql-*.sql")
	if err != nil {
		return "", err
	}
	if _, err := f.WriteString(sqlText); err != nil {
		f.Close()
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	args := []string{"-X", "-A", "-t", "-q", "-v", "ON_ERROR_STOP=1", "-d", db, "-f", f.Name()}
	if n.g != nil {
		res, err := n.g.PSQL(args...)
		if err != nil {
			return "", err
		}
		if res.ExitCode != 0 {
			return strings.TrimRight(res.Stdout, "\n"), fmt.Errorf("psql exit %d: %s", res.ExitCode, strings.TrimSpace(res.Stderr))
		}
		return strings.TrimRight(res.Stdout, "\n"), nil
	}
	full := append([]string{"-h", n.p.Host(), "-p", fmt.Sprint(n.p.Port()), "-U", n.p.User()}, args...)
	cmd := exec.Command(filepath.Join(n.binDir, "psql"), full...)
	cmd.Env = append(os.Environ(), "LC_ALL=C", "LANG=C", "PGPASSWORD=",
		"LD_LIBRARY_PATH="+filepath.Join(n.repo, "postgres", "local_install", "lib"))
	var stdout, stderr strings.Builder
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return strings.TrimRight(stdout.String(), "\n"), fmt.Errorf("%v: %s", err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimRight(stdout.String(), "\n"), nil
}

// ColdStartPG is M0149 C3's single-node cross-check: it copies src's data
// directory (src must be stopped — typically by StopImmediate, so the copy
// carries a WAL tail to recover) and starts a PostgreSQL 18.3 server on the
// copy. The returned node is running; the test then repeats its assertions
// against it. src is left untouched, so the test can still restart it.
// Precedent: e2e_pg_crashstart_on_goopgdata_test.go.
func ColdStartPG(t testing.TB, src *Node, name string) *Node {
	t.Helper()
	if src.running {
		t.Fatalf("tapnode: ColdStartPG(%s): source node %s is still running", name, src.name)
	}
	dst := filepath.Join(t.TempDir(), name)
	if out, err := exec.Command("cp", "-a", src.DataDir(), dst).CombinedOutput(); err != nil {
		t.Fatalf("tapnode: copy %s → %s: %v\n%s", src.DataDir(), dst, err, out)
	}
	// cp -a copies pg_tblspc's symlinks as symlinks, so the copy shares
	// src's external tablespaces. Re-pointing the links would not isolate
	// them: redo of XLOG_TBLSPC_CREATE recreates each link to the logged
	// location (tblspc_redo). A test with external tablespaces snapshots
	// them with SnapshotDir and restores them before src restarts
	// (M0149-0003).
	// A goopg immediate stop or SIGKILL leaves its lock file, as PG's
	// postmaster does after a crash; the copy is a different directory, so
	// the stale file only blocks PG's start. PG removes it itself when the
	// owning PID is gone, but goopg's PID may be reused by now.
	_ = os.Remove(filepath.Join(dst, "postmaster.pid"))
	p, err := pgcluster.OpenExisting(name, pgcluster.Options{
		RepoRoot:    src.repo,
		DataDir:     dst,
		User:        "postgres",
		StartupWait: 90 * time.Second,
	})
	if err != nil {
		t.Fatalf("tapnode %s: pgcluster.OpenExisting: %v", name, err)
	}
	n := &Node{t: t, name: name, flavor: PG, repo: src.repo, binDir: BinDir(src.repo), p: p}
	t.Cleanup(n.cleanup)
	n.Start()
	return n
}

func (n *Node) cleanup() {
	if !n.running {
		return
	}
	if n.g != nil {
		_ = n.g.Stop(cluster.ShutdownImmediate)
	} else {
		_ = n.p.Stop()
	}
	n.running = false
}

func (n *Node) logTail(lines int) string {
	b, err := os.ReadFile(n.LogPath())
	if err != nil {
		return fmt.Sprintf("(no log: %v)", err)
	}
	all := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if len(all) > lines {
		all = all[len(all)-lines:]
	}
	return strings.Join(all, "\n")
}

// AppendConf is Cluster.pm's append_conf('postgresql.conf', line) for a goopg
// node; it fails the test when the file cannot be written.
func (n *Node) AppendConf(line string) {
	n.t.Helper()
	if n.g == nil {
		// PG node: append to its postgresql.conf, as Cluster.pm's
		// append_conf does (M0149-0010b).
		f, err := os.OpenFile(filepath.Join(n.p.DataDir(), "postgresql.conf"), os.O_APPEND|os.O_WRONLY, 0)
		if err == nil {
			_, err = fmt.Fprintln(f, line)
			if cerr := f.Close(); err == nil {
				err = cerr
			}
		}
		if err != nil {
			n.t.Fatalf("tapnode %s: append_conf %q: %v", n.name, line, err)
		}
		return
	}
	if err := n.g.AppendPostgresqlConf(line); err != nil {
		n.t.Fatalf("tapnode %s: append_conf %q: %v", n.name, line, err)
	}
}

// SnapshotDir copies dir aside and returns a function that restores it,
// replacing whatever the directory then holds. A test that cold-starts PG
// on a copy of a node sharing an external tablespace restores the
// tablespace before the node itself restarts, so each server recovers the
// same crashed state.
func SnapshotDir(t testing.TB, dir string) (restore func()) {
	t.Helper()
	saved := filepath.Join(t.TempDir(), "snapshot")
	if out, err := exec.Command("cp", "-a", dir, saved).CombinedOutput(); err != nil {
		t.Fatalf("tapnode: snapshot %s: %v\n%s", dir, err, out)
	}
	return func() {
		t.Helper()
		if err := os.RemoveAll(dir); err != nil {
			t.Fatalf("tapnode: restore %s: %v", dir, err)
		}
		if out, err := exec.Command("cp", "-a", saved, dir).CombinedOutput(); err != nil {
			t.Fatalf("tapnode: restore %s: %v\n%s", dir, err, out)
		}
	}
}
