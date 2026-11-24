package tapnode

import (
	"fmt"
	"os"
	"path/filepath"
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
