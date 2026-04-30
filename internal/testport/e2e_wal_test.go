package testport

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/testutil/cluster"
)

// TestE2E_WALRedoSmoke verifies the server produces WAL segments.
func TestE2E_WALRedoSmoke(t *testing.T) {
	c := newCluster(t, "e2e_wal_smoke")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	runSQL(t, c, "CREATE TABLE wal_t (id int)")
	runSQL(t, c, "INSERT INTO wal_t VALUES (1), (2), (3)")

	// Force a checkpoint to flush WAL
	if err := c.Checkpoint(); err != nil {
		t.Fatalf("Checkpoint: %v", err)
	}

	runSQL(t, c, "DROP TABLE wal_t")

	// Check that WAL files exist
	dataDir := c.DataDir()
	walDir := filepath.Join(dataDir, "pg_wal")
	entries, err := os.ReadDir(walDir)
	if err != nil {
		t.Fatalf("reading pg_wal: %v", err)
	}
	// Look for any WAL-like files (can be segment files or status files)
	walFiles := make([]string, 0)
	for _, e := range entries {
		// WAL segments in PG format: 00000001xxxxxxxxxxxxxxxx
		if !e.IsDir() && (strings.HasPrefix(e.Name(), "00000001") || strings.HasPrefix(e.Name(), "0")) {
			walFiles = append(walFiles, e.Name())
		}
	}
	if len(walFiles) == 0 {
		// Just log the directory contents for debugging
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("no WAL files found in %s; contents: %v", walDir, names)
	}
	t.Logf("WAL directory entries: %v", walFiles)
}
