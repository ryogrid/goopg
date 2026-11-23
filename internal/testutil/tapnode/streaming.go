package tapnode

import (
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

// Backup is Cluster.pm's backup(name): pg_basebackup --no-sync
// --checkpoint=fast of the running node into a fresh directory, whose path
// it returns (Cluster.pm keeps it under the node's backup_dir). M0149-0010.
func (n *Node) Backup(name string) string {
	n.t.Helper()
	host, port, user := n.connParams()
	dst := filepath.Join(n.t.TempDir(), "backup-"+name)
	cmd := exec.Command(filepath.Join(n.binDir, "pg_basebackup"), "--no-sync",
		"--pgdata", dst, "--host", host, "--port", port, "--username", user, "--checkpoint", "fast")
	if out, err := cmd.CombinedOutput(); err != nil {
		n.t.Fatalf("tapnode %s: pg_basebackup %s: %v\n%s", n.name, name, err, out)
	}
	return dst
}

// connstr is Cluster.pm's connstr: the libpq string a standby's
// primary_conninfo points at.
func (n *Node) connstr() string {
	host, port, user := n.connParams()
	return fmt.Sprintf("port=%s host=%s user=%s dbname=postgres", port, host, user)
}

// InitFromBackupGoopg is Cluster.pm's init_from_backup(root, backup,
// has_streaming => hasStreaming) for a goopg node: the backup directory is
// copied into the node's data directory and, with hasStreaming,
// enable_streaming appends primary_conninfo and set_standby_mode writes
// standby.signal. M0149-0010.
func InitFromBackupGoopg(t testing.TB, repo, name string, root *Node, backupPath string, hasStreaming bool) *Node {
	t.Helper()
	dataDir := filepath.Join(t.TempDir(), name)
	if out, err := exec.Command("cp", "-a", backupPath, dataDir).CombinedOutput(); err != nil {
		t.Fatalf("tapnode %s: copy backup %s: %v\n%s", name, backupPath, err, out)
	}
	_ = os.Chmod(dataDir, 0o700)
	g, err := cluster.New(name, cluster.Options{
		RepoRoot:     repo,
		DataDir:      dataDir,
		PSQLPath:     filepath.Join(BinDir(repo), "psql"),
		StartupWait:  60 * time.Second,
		ShutdownWait: 30 * time.Second,
		SyncInit:     true,
		SyncRuntime:  true,
	})
	if err != nil {
		t.Fatalf("tapnode %s: cluster.New: %v", name, err)
	}
	n := &Node{t: t, name: name, flavor: Goopg, repo: repo, binDir: BinDir(repo), g: g}
	t.Cleanup(n.cleanup)
	if hasStreaming {
		n.AppendConf("primary_conninfo='" + root.connstr() + "'")
		if err := os.WriteFile(filepath.Join(dataDir, "standby.signal"), nil, 0o600); err != nil {
			t.Fatalf("tapnode %s: standby.signal: %v", name, err)
		}
	}
	return n
}

// EnableStreaming is Cluster.pm's enable_streaming(root): append
// primary_conninfo pointing at root and write standby.signal, so the
// node's next start follows root as a streaming standby.
func (n *Node) EnableStreaming(root *Node) {
	n.t.Helper()
	n.AppendConf("primary_conninfo='" + root.connstr() + "'")
	if err := os.WriteFile(filepath.Join(n.DataDir(), "standby.signal"), nil, 0o600); err != nil {
		n.t.Fatalf("tapnode %s: standby.signal: %v", n.name, err)
	}
}

// Promote is Cluster.pm's promote: pg_ctl promote, which waits until the
// server reports it has left recovery. M0149-0010.
func (n *Node) Promote() {
	n.t.Helper()
	cmd := exec.Command(filepath.Join(n.binDir, "pg_ctl"), "-D", n.DataDir(), "-l", n.LogPath(), "promote")
	if out, err := cmd.CombinedOutput(); err != nil {
		n.t.Fatalf("tapnode %s: pg_ctl promote: %v\n%s\n--- log tail ---\n%s", n.name, err, out, n.logTail(60))
	}
}

// Reload is Cluster.pm's reload: pg_ctl reload (SIGHUP to the postmaster).
func (n *Node) Reload() {
	n.t.Helper()
	cmd := exec.Command(filepath.Join(n.binDir, "pg_ctl"), "-D", n.DataDir(), "reload")
	if out, err := cmd.CombinedOutput(); err != nil {
		n.t.Fatalf("tapnode %s: pg_ctl reload: %v\n%s", n.name, err, out)
	}
}

// Restart is Cluster.pm's restart: a fast stop and a start.
func (n *Node) Restart() {
	n.t.Helper()
	n.Stop(StopFast)
	n.Start()
}

// PollQueryUntil is Cluster.pm's poll_query_until(db, query, expected = 't'):
// it reruns query every 100 ms until its output equals expected, for up to
// 180 s (timeout_default), and reports whether it did.
func (n *Node) PollQueryUntil(db, query, expected string) bool {
	n.t.Helper()
	deadline := time.Now().Add(180 * time.Second)
	for time.Now().Before(deadline) {
		out, _, exit := n.PSQLExit(db, query)
		if exit == 0 && strings.TrimRight(out, "\n") == expected {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}

// NewPG is Cluster.pm's new() + init(allows_streaming => 1) for a PG 18.3
// node: wal_level replica, max_wal_senders/max_replication_slots 10 and
// hot_standby on, as init writes them. M0149-0010b.
func NewPG(t testing.TB, repo, name string) *Node {
	t.Helper()
	p, err := pgcluster.New(name, pgcluster.Options{
		RepoRoot:            repo,
		DataDir:             filepath.Join(t.TempDir(), name),
		User:                "postgres",
		WalLevel:            "replica",
		MaxWalSenders:       10,
		MaxReplicationSlots: 10,
		ExtraConf:           []string{"hot_standby = on", "wal_log_hints = on"},
		StartupWait:         90 * time.Second,
	})
	if err != nil {
		t.Fatalf("tapnode %s: pgcluster.New: %v", name, err)
	}
	n := &Node{t: t, name: name, flavor: PG, repo: repo, binDir: BinDir(repo), p: p}
	t.Cleanup(n.cleanup)
	return n
}

// InitFromBackupPG is init_from_backup(root, backup, has_streaming) for a
// PG 18.3 node. The backup may come from a goopg or a PG primary.
func InitFromBackupPG(t testing.TB, repo, name string, root *Node, backupPath string, hasStreaming bool) *Node {
	t.Helper()
	dataDir := filepath.Join(t.TempDir(), name)
	if out, err := exec.Command("cp", "-a", backupPath, dataDir).CombinedOutput(); err != nil {
		t.Fatalf("tapnode %s: copy backup %s: %v\n%s", name, backupPath, err, out)
	}
	_ = os.Chmod(dataDir, 0o700)
	p, err := pgcluster.OpenExisting(name, pgcluster.Options{
		RepoRoot:    repo,
		DataDir:     dataDir,
		User:        "postgres",
		StartupWait: 90 * time.Second,
	})
	if err != nil {
		t.Fatalf("tapnode %s: pgcluster.OpenExisting: %v", name, err)
	}
	n := &Node{t: t, name: name, flavor: PG, repo: repo, binDir: BinDir(repo), p: p}
	t.Cleanup(n.cleanup)
	if hasStreaming {
		n.AppendConf("primary_conninfo='" + root.connstr() + "'")
		if err := os.WriteFile(filepath.Join(dataDir, "standby.signal"), nil, 0o600); err != nil {
			t.Fatalf("tapnode %s: standby.signal: %v", name, err)
		}
	}
	return n
}
