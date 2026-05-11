package testport

import (
	"context"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/testutil/cluster"
	"github.com/goopg/goopg/internal/testutil/replcluster"
)

// TestE2E_PhysicalReplication tests a primary ↔ standby pair end-to-end.
// The table is created via PreCloneHook (before the standby data-dir copy),
// so it is present on the standby from the start. After streaming begins,
// an INSERT on the primary is verified to appear on the standby.
func TestE2E_PhysicalReplication(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping replication test in short mode")
	}

	baseDir := t.TempDir()
	rc, err := replcluster.New("e2e_phys_repl", replcluster.Options{
		RepoRoot:     repoRoot(t),
		BaseDir:      baseDir,
		SlotName:     "e2e_phys_slot",
		StartupWait:  30 * time.Second,
		ShutdownWait: 10 * time.Second,
		PreCloneHook: func(primary *cluster.Cluster) error {
			_, err := primary.Query(context.Background(), "CREATE TABLE repl_t (id int)")
			return err
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := rc.Setup(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = rc.Stop()
	}()

	// Insert on primary after standby is streaming.
	if err := runSQLSimple(t, rc.Primary, "INSERT INTO repl_t VALUES (42)"); err != nil {
		t.Fatal(err)
	}

	// Wait up to 15 s for standby to replay the insert.
	var lastErr error
	for i := 0; i < 30; i++ {
		time.Sleep(500 * time.Millisecond)
		rows, err := rc.Standby.Query(context.Background(), "SELECT id FROM repl_t WHERE id = 42")
		if err == nil && len(rows) > 0 && rows[0][0] == "42" {
			return
		}
		if err != nil {
			lastErr = err
		}
	}
	if lastErr != nil {
		t.Fatalf("standby never saw the row after ~15s: %v", lastErr)
	}
	t.Fatal("standby never saw the row after ~15s: timeout")
}

// TestE2E_LogicalReplication tests logical replication.
// SKIPPED: logical replication requires full applyDelete/applyUpdate
// implementation (M0094-0002).
func TestE2E_LogicalReplication(t *testing.T) {
	t.Skip("logical replication: applyDelete/applyUpdate not yet implemented (M0094-0002)")
}
