package testport

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/testutil/cluster"
	"github.com/goopg/goopg/internal/testutil/replcluster"
)

// TestE2E_StandbyAttachRetainsUpstreamRowsAfterRestart exercises the CLOG
// standby-attach correctness invariant documented in internal/mvcc/clog.go
// (MarkUnknownAsAborted, ~lines 299-304):
//
//	"CAUTION for basebackup-attached clusters: upstream xids that pre-date the
//	 [standby's] bootstrap pg_xact ... would be wrongly stamped Aborted by this
//	 sweep. Such clusters MUST call InitializeAsCommitted with the upstream
//	 cluster's nextXid BEFORE this sweep runs so the upstream committed rows
//	 stay visible."
//
// The danger window is *restart-time recovery*: when the standby boots, its
// recovery path runs InitializeAsCommitted(upstream_nextXid) and then the
// implicit-abort MarkUnknownAsAborted sweep. If the ordering is wrong (or the
// upstream nextXid is not seeded), the upstream XIDs that committed the
// pre-clone rows get stamped Aborted and those rows DISAPPEAR after restart.
//
// This test proves the opposite: rows committed on the primary BEFORE the
// clone remain visible on the standby both immediately after attach AND after
// a full standby restart (Stop + Start), which is what actually drives the
// recovery-time InitializeAsCommitted/MarkUnknownAsAborted sequence.
func TestE2E_StandbyAttachRetainsUpstreamRowsAfterRestart(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping standby-attach round-trip test in short mode")
	}

	baseDir := t.TempDir()
	rc, err := replcluster.New("e2e_standby_attach", replcluster.Options{
		RepoRoot:     repoRoot(t),
		BaseDir:      baseDir,
		SlotName:     "e2e_standby_attach_slot",
		StartupWait:  30 * time.Second,
		ShutdownWait: 10 * time.Second,
		// PreCloneHook runs while the primary is live, BEFORE the standby's
		// data dir is cloned. The CREATE TABLE + INSERTs here consume XIDs on
		// the primary; those committed rows are the "upstream" rows the standby
		// inherits via the base copy. Their commit status lives only in the
		// primary's pg_xact, which the clone ships to the standby.
		PreCloneHook: func(primary *cluster.Cluster) error {
			if _, err := primary.Query(context.Background(), "CREATE TABLE attach_t (id int)"); err != nil {
				return err
			}
			for i := 1; i <= 5; i++ {
				if _, err := primary.Query(context.Background(),
					fmt.Sprintf("INSERT INTO attach_t VALUES (%d)", i)); err != nil {
					return err
				}
			}
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := rc.Setup(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rc.Stop() }()

	want := []string{"1", "2", "3", "4", "5"}

	// 1. The upstream committed rows must be visible on the standby after
	//    attach. Poll like the existing E2E test, since streaming/replay is
	//    asynchronous.
	if got := waitForUpstreamRows(t, rc.Standby, 5, 30*time.Second); !equalIDs(got, want) {
		t.Fatalf("standby did not show upstream rows after attach: got %v, want %v", got, want)
	}

	// 2. RESTART the standby. This is the load-bearing step: it forces the
	//    standby through its recovery path, which runs
	//    InitializeAsCommitted(upstream_nextXid) and then the implicit-abort
	//    MarkUnknownAsAborted sweep. If the ordering/seeding were wrong, the
	//    upstream XIDs would be stamped Aborted and the rows would vanish.
	if err := rc.Standby.Stop(cluster.ShutdownFast); err != nil {
		t.Fatalf("standby stop for restart: %v", err)
	}
	if err := rc.Standby.Start(); err != nil {
		t.Fatalf("standby start after restart: %v", err)
	}

	// 3. CORE ASSERTION: the SAME upstream rows are STILL visible after the
	//    restart. This proves the recovery sequence did NOT wrongly abort the
	//    upstream XIDs.
	if got := waitForUpstreamRows(t, rc.Standby, 5, 30*time.Second); !equalIDs(got, want) {
		t.Fatalf("standby LOST upstream rows after restart (CLOG invariant violated: "+
			"upstream XIDs wrongly aborted by MarkUnknownAsAborted): got %v, want %v", got, want)
	}

	// 4. Best-effort liveness probe: insert a NEW row on the primary after the
	//    standby restart and see whether it streams through. This is reported
	//    but NOT asserted: whether the standby's walreceiver auto-reconnects
	//    after a plain Stop+Start is a streaming-resumption concern that is
	//    orthogonal to the CLOG standby-attach invariant under test here
	//    (steps 1-3). Empirically the v0 harness does not re-establish the
	//    walreceiver stream after a standby restart, so failing the test on it
	//    would conflate two unrelated behaviours. We log the outcome instead.
	if _, err := rc.Primary.Query(context.Background(), "INSERT INTO attach_t VALUES (6)"); err != nil {
		t.Logf("post-restart primary insert failed (liveness probe skipped): %v", err)
		return
	}
	wantWithNew := []string{"1", "2", "3", "4", "5", "6"}
	if got := waitForUpstreamRows(t, rc.Standby, 6, 15*time.Second); equalIDs(got, wantWithNew) {
		t.Logf("liveness OK: standby resumed streaming after restart and saw row 6")
	} else {
		t.Logf("liveness note: standby did not stream post-restart insert within 15s "+
			"(walreceiver did not auto-reconnect after restart): got %v, want %v; "+
			"this does not affect the CLOG standby-attach invariant verified above", got, wantWithNew)
	}
}

// waitForUpstreamRows polls the standby for `SELECT id FROM attach_t ORDER BY
// id` until it sees at least wantCount rows or the timeout elapses, returning
// the last observed id slice. It tolerates transient query errors during the
// standby's startup/recovery window.
func waitForUpstreamRows(t *testing.T, c *cluster.Cluster, wantCount int, timeout time.Duration) []string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last []string
	var lastErr error
	for time.Now().Before(deadline) {
		rows, err := c.Query(context.Background(), "SELECT id FROM attach_t ORDER BY id")
		if err != nil {
			lastErr = err
			time.Sleep(500 * time.Millisecond)
			continue
		}
		ids := make([]string, 0, len(rows))
		for _, r := range rows {
			if len(r) > 0 {
				ids = append(ids, r[0])
			}
		}
		last = ids
		if len(ids) >= wantCount {
			return ids
		}
		time.Sleep(500 * time.Millisecond)
	}
	if lastErr != nil {
		t.Logf("waitForUpstreamRows: last query error: %v", lastErr)
	}
	return last
}

func equalIDs(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
