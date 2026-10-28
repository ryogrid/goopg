package testport

import (
	"context"
	"os"
	"regexp"
	"strconv"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/access/transam/control"
	"github.com/goopg/goopg/internal/testutil/cluster"
	"github.com/goopg/goopg/internal/testutil/replcluster"
)

// walSegmentName matches a WAL segment file name: TLI, log id, segment, as
// XLogFileName writes them.
var walSegmentName = regexp.MustCompile(`^([0-9A-F]{8})([0-9A-F]{8})([0-9A-F]{8})$`)

// walSegnos lists the segment numbers of dataDir's pg_wal segment files
// (16 MiB segments: 256 per log id).
func walSegnos(t *testing.T, dataDir string) []uint64 {
	t.Helper()
	ents, err := os.ReadDir(dataDir + "/pg_wal")
	if err != nil {
		t.Fatalf("read pg_wal: %v", err)
	}
	var out []uint64
	for _, e := range ents {
		m := walSegmentName.FindStringSubmatch(e.Name())
		if m == nil || e.IsDir() {
			continue
		}
		hi, _ := strconv.ParseUint(m[2], 16, 32)
		lo, _ := strconv.ParseUint(m[3], 16, 32)
		out = append(out, hi*256+lo)
	}
	return out
}

// TestE2E_PhysicalStandbyRestartPointRemovesOldWAL pins M0122-0013's
// restartpoint WAL retention: after a standby CHECKPOINT adopts a replayed
// checkpoint, the segments wholly before that checkpoint's redo segment are
// gone from the standby's pg_wal — PG's CreateRestartPoint deletes them
// (KeepLogSeg + RemoveOldXlogFiles from the restartpoint's redo). Before, a
// standby kept every segment it ever received. Recycled segments are renamed
// to future names, so either way no segment older than the redo's remains.
// The standby must still restart and keep streaming afterwards.
func TestE2E_PhysicalStandbyRestartPointRemovesOldWAL(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping physical-replication restartpoint WAL test in short mode")
	}
	rc, err := replcluster.New("e2e_phys_rp_wal", replcluster.Options{
		RepoRoot:     repoRoot(t),
		BaseDir:      t.TempDir(),
		SlotName:     "e2e_phys_rp_wal_slot",
		StartupWait:  30 * time.Second,
		ShutdownWait: 10 * time.Second,
		PreCloneHook: func(primary *cluster.Cluster) error {
			_, err := primary.Query(context.Background(), "CREATE TABLE rp_wal_t (id int, pad text)")
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

	// About 40 MB of rows: the WAL crosses several 16 MiB segments.
	if err := runSQLSimple(t, rc.Primary, "INSERT INTO rp_wal_t SELECT g, repeat(md5(g::text), 32) FROM generate_series(1, 40000) g"); err != nil {
		t.Fatalf("fill: %v", err)
	}
	if err := runSQLSimple(t, rc.Primary, "CHECKPOINT"); err != nil {
		t.Fatalf("primary checkpoint: %v", err)
	}
	primaryCtl, err := control.ReadControlFile(rc.Primary.DataDir())
	if err != nil {
		t.Fatalf("read primary pg_control: %v", err)
	}
	if err := runSQLSimple(t, rc.Primary, "INSERT INTO rp_wal_t VALUES (-1, 'marker')"); err != nil {
		t.Fatalf("marker: %v", err)
	}
	if got := waitForPhysicalIDs(t, rc.Standby, "rp_wal_t WHERE id = -1", 1, 60*time.Second); !equalIDs(got, []string{"-1"}) {
		t.Fatalf("standby did not replay past the checkpoint: got %v", got)
	}
	redoSeg := primaryCtl.CheckPointCopyRedo / (16 << 20)
	before := walSegnos(t, rc.Standby.DataDir())
	oldBefore := 0
	for _, s := range before {
		if s < redoSeg {
			oldBefore++
		}
	}
	if oldBefore == 0 {
		t.Fatalf("fixture: the standby holds no segment before the redo segment %d (segments %v)", redoSeg, before)
	}

	if err := runSQLSimple(t, rc.Standby, "CHECKPOINT"); err != nil {
		t.Fatalf("standby checkpoint (restartpoint): %v", err)
	}
	after, err := control.ReadControlFile(rc.Standby.DataDir())
	if err != nil || after.CheckPointCopyRedo != primaryCtl.CheckPointCopyRedo {
		t.Fatalf("standby did not adopt the primary's checkpoint (redo %v, err %v)", after, err)
	}
	for _, s := range walSegnos(t, rc.Standby.DataDir()) {
		if s < redoSeg {
			t.Fatalf("segment %d before the restartpoint's redo segment %d survived (had %d old segments before)", s, redoSeg, oldBefore)
		}
	}

	// The standby restarts from its restartpoint and keeps streaming.
	if err := rc.Standby.Stop(cluster.ShutdownFast); err != nil {
		t.Fatalf("stop standby: %v", err)
	}
	if err := rc.Standby.Start(); err != nil {
		t.Fatalf("restart standby: %v", err)
	}
	if err := runSQLSimple(t, rc.Primary, "INSERT INTO rp_wal_t VALUES (-2, 'after')"); err != nil {
		t.Fatalf("insert after restart: %v", err)
	}
	if got := waitForPhysicalIDs(t, rc.Standby, "rp_wal_t WHERE id < 0", 2, 60*time.Second); !equalIDs(got, []string{"-2", "-1"}) && !equalIDs(got, []string{"-1", "-2"}) {
		t.Fatalf("standby did not resume after the restartpoint: got %v", got)
	}
}
