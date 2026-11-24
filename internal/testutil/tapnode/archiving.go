package tapnode

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// ArchiveDir is Cluster.pm's archive_dir: where the node's archive_command
// copies WAL (enable_archiving) and where restoring nodes fetch it from.
func (n *Node) ArchiveDir() string {
	return filepath.Join(filepath.Dir(n.DataDir()), n.name+"_archives")
}

// EnableArchiving is Cluster.pm's enable_archiving (init has_archiving):
// archive_mode = on with a cp archive_command into ArchiveDir. M0149-0018.
func (n *Node) EnableArchiving() {
	n.t.Helper()
	dir := n.ArchiveDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		n.t.Fatalf("tapnode %s: archive dir: %v", n.name, err)
	}
	n.AppendConf("archive_mode = on")
	n.AppendConf(fmt.Sprintf(`archive_command = 'cp "%%p" "%s/%%f"'`, dir))
}

// EnableRestoring is Cluster.pm's enable_restoring(root, standby): a
// restore_command copying from root's archive, then standby.signal (a
// standby) or recovery.signal (targeted archive recovery). M0149-0018.
func (n *Node) EnableRestoring(root *Node, standby bool) {
	n.t.Helper()
	n.AppendConf(fmt.Sprintf(`restore_command = 'cp "%s/%%f" "%%p"'`, root.ArchiveDir()))
	signal := "recovery.signal"
	if standby {
		signal = "standby.signal"
	}
	if err := os.WriteFile(filepath.Join(n.DataDir(), signal), nil, 0o600); err != nil {
		n.t.Fatalf("tapnode %s: %s: %v", n.name, signal, err)
	}
}

// StartExpectExit is 024's `pg_ctl start` followed by waiting for
// postmaster.pid to go away: the server is expected to stop by itself (a
// FATAL during recovery). It reports whether the server was gone within
// timeout. The node is left stopped. M0149-0020.
func (n *Node) StartExpectExit(timeout time.Duration) bool {
	n.t.Helper()
	deadline := time.Now().Add(timeout)
	if n.g != nil {
		n.g.SetStartArgs("--cluster-name=" + n.name)
		if err := n.g.Start(); err != nil {
			return true // exited before it became ready
		}
		for time.Now().Before(deadline) {
			if code, _, err := n.g.Status(); err == nil && code != 0 {
				// The pid file goes before the process does; wait for
				// the exit so nothing writes into the data dir after.
				return n.g.WaitExit(time.Until(deadline))
			}
			time.Sleep(100 * time.Millisecond)
		}
		return false
	}
	n.p.SetStartArgs("--cluster-name=" + n.name)
	_ = n.p.Start()
	pid := filepath.Join(n.DataDir(), "postmaster.pid")
	time.Sleep(200 * time.Millisecond)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(pid); errors.Is(err, os.ErrNotExist) {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}
