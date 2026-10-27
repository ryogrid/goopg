package testport

import (
	"context"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/access/transam/control"
	"github.com/goopg/goopg/internal/testutil/cluster"
	"github.com/goopg/goopg/internal/testutil/replcluster"
)

// TestE2E_PhysicalStandbyRestartPointAdvancesPgControl pins M0122-0013's
// restartpoint: a standby's CHECKPOINT (a recovery flush) adopts the last
// checkpoint record replay applied into pg_control — PG's CreateRestartPoint
// — so a restarted standby begins replay at that checkpoint's redo instead of
// at its base backup's. Before, the recovery flush left pg_control untouched
// and the redo stayed at the base backup's checkpoint forever. The standby
// must still restart cleanly and keep replicating from the advanced point.
func TestE2E_PhysicalStandbyRestartPointAdvancesPgControl(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping physical-replication restartpoint test in short mode")
	}

	rc, err := replcluster.New("e2e_phys_restartpoint", replcluster.Options{
		RepoRoot:     repoRoot(t),
		BaseDir:      t.TempDir(),
		SlotName:     "e2e_phys_restartpoint_slot",
		StartupWait:  30 * time.Second,
		ShutdownWait: 10 * time.Second,
		PreCloneHook: func(primary *cluster.Cluster) error {
			_, err := primary.Query(context.Background(), "CREATE TABLE restartpoint_t (id int)")
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

	before, err := control.ReadControlFile(rc.Standby.DataDir())
	if err != nil {
		t.Fatalf("read standby pg_control: %v", err)
	}

	// A primary checkpoint, then a row after it: once the standby shows the
	// row it has replayed the checkpoint record.
	if err := runSQLSimple(t, rc.Primary, "CHECKPOINT"); err != nil {
		t.Fatalf("primary checkpoint: %v", err)
	}
	primaryCtl, err := control.ReadControlFile(rc.Primary.DataDir())
	if err != nil {
		t.Fatalf("read primary pg_control: %v", err)
	}
	if err := runSQLSimple(t, rc.Primary, "INSERT INTO restartpoint_t VALUES (1)"); err != nil {
		t.Fatalf("insert after primary checkpoint: %v", err)
	}
	if got := waitForPhysicalIDs(t, rc.Standby, "restartpoint_t", 1, 30*time.Second); !equalIDs(got, []string{"1"}) {
		t.Fatalf("standby did not receive the post-checkpoint row: got %v, want [1]", got)
	}

	if err := runSQLSimple(t, rc.Standby, "CHECKPOINT"); err != nil {
		t.Fatalf("standby checkpoint (restartpoint): %v", err)
	}
	after, err := control.ReadControlFile(rc.Standby.DataDir())
	if err != nil {
		t.Fatalf("read standby pg_control after restartpoint: %v", err)
	}
	if after.CheckPointCopyRedo <= before.CheckPointCopyRedo {
		t.Fatalf("restartpoint did not advance the standby's redo: before %X after %X",
			before.CheckPointCopyRedo, after.CheckPointCopyRedo)
	}
	if after.CheckPointCopyRedo != primaryCtl.CheckPointCopyRedo || after.CheckPoint != primaryCtl.CheckPoint {
		t.Fatalf("standby adopted checkpoint %X (redo %X), want the primary's %X (redo %X)",
			after.CheckPoint, after.CheckPointCopyRedo, primaryCtl.CheckPoint, primaryCtl.CheckPointCopyRedo)
	}

	// Restart: replay begins at the restartpoint's redo; history and the
	// stream must both survive it.
	if err := rc.Standby.Stop(cluster.ShutdownFast); err != nil {
		t.Fatalf("stop standby: %v", err)
	}
	if err := rc.Standby.Start(); err != nil {
		t.Fatalf("restart standby: %v", err)
	}
	if err := runSQLSimple(t, rc.Primary, "INSERT INTO restartpoint_t VALUES (2)"); err != nil {
		t.Fatalf("insert after standby restart: %v", err)
	}
	if got := waitForPhysicalIDs(t, rc.Standby, "restartpoint_t", 2, 30*time.Second); !equalIDs(got, []string{"1", "2"}) {
		t.Fatalf("standby did not resume from its restartpoint: got %v, want [1 2]", got)
	}
}
