package testport

import (
	"context"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/testutil/cluster"
	"github.com/goopg/goopg/internal/testutil/replcluster"
)

// TestE2E_NativeOnlyReplicationAndPromotion is perf-optimize3-dash S3b
// (doc 04 §4a): goopg→goopg physical replication, standby catch-up, and
// PROMOTION over the native WAL record family. Physical replication is
// family-agnostic (the walreceiver copies raw bytes via AppendRaw).
func TestE2E_NativeOnlyReplicationAndPromotion(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping replication test in short mode")
	}

	baseDir := t.TempDir()
	rc, err := replcluster.New("e2e_native_promote", replcluster.Options{
		RepoRoot:     repoRoot(t),
		BaseDir:      baseDir,
		SlotName:     "e2e_native_promote_slot",
		StartupWait:  30 * time.Second,
		ShutdownWait: 10 * time.Second,
		// Schema pre-clone, DML post-clone — same contract as
		// TestE2E_PhysicalReplication (live row changes stream; DDL is
		// created before the standby's basebackup clone).
		PreCloneHook: func(primary *cluster.Cluster) error {
			_, err := primary.Query(context.Background(), "CREATE TABLE promo_t (id int)")
			return err
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := rc.Setup(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rc.Stop() }()

	if err := runSQLSimple(t, rc.Primary, "INSERT INTO promo_t VALUES (7)"); err != nil {
		t.Fatal(err)
	}

	// Standby must replay the native-only stream.
	deadline := time.Now().Add(15 * time.Second)
	for {
		rows, qerr := rc.Standby.Query(context.Background(), "SELECT id FROM promo_t WHERE id = 7")
		if qerr == nil && len(rows) > 0 && rows[0][0] == "7" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("standby never replayed the native-only stream: lastErr=%v", qerr)
		}
		time.Sleep(500 * time.Millisecond)
	}

	// Promote the standby; it must accept writes afterwards.
	if err := rc.Promote(); err != nil {
		t.Fatalf("promote: %v", err)
	}
	deadline = time.Now().Add(15 * time.Second)
	for {
		err := runSQLSimple(t, rc.Standby, "INSERT INTO promo_t VALUES (8)")
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("promoted standby never accepted a write: %v", err)
		}
		time.Sleep(500 * time.Millisecond)
	}
	rows, err := rc.Standby.Query(context.Background(), "SELECT count(*) FROM promo_t")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0][0] != "2" {
		t.Fatalf("promoted standby count: want 2, got %v", rows)
	}
}
