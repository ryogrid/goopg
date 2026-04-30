package testport

import (
	"context"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/testutil/replcluster"
)

// TestE2E_PhysicalReplication tests a primary ↔ standby pair.
// SKIPPED: v0 does not replicate DDL through WAL, so tables created
// after the data-directory clone are invisible on the standby.
// This test passes when table creation happens before the clone.
func TestE2E_PhysicalReplication(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping replication test in short mode")
	}
	t.Skip("v0 does not replicate DDL through WAL; replcluster.Setup() has no pre-clone hook")

	baseDir := t.TempDir()
	rc, err := replcluster.New("e2e_phys_repl", replcluster.Options{
		RepoRoot:     repoRoot(t),
		BaseDir:      baseDir,
		SlotName:     "e2e_phys_slot",
		StartupWait:  30 * time.Second,
		ShutdownWait: 10 * time.Second,
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

	err = runSQLSimple(t, rc.Primary, "CREATE TABLE repl_t (id int)")
	if err != nil {
		t.Fatal(err)
	}
	err = runSQLSimple(t, rc.Primary, "INSERT INTO repl_t VALUES (42)")
	if err != nil {
		t.Fatal(err)
	}

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
// SKIPPED: logical replication infrastructure depends on DDL
// replication through WAL, which v0 does not support.
func TestE2E_LogicalReplication(t *testing.T) {
	t.Skip("logical replication requires DDL WAL records, not supported in v0")
}
