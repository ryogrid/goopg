package replcluster

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/testutil/cluster"
)

// TestReplicationEndToEnd is the M0005 acceptance test. It:
//
//  1. Boots a primary + standby pair via Setup() — the standby's
//     data dir is cloned from the primary, standby.signal is laid
//     down, and primary_conninfo + primary_slot_name point at the
//     primary.
//  2. Waits for the standby's walreceiver to report streaming via
//     pg_stat_wal_receiver. This proves the wire connection is up
//     and WAL bytes are flowing.
//  3. Cross-checks pg_stat_replication on the primary: the active
//     walsender for the standby's slot must be present.
//  4. Drives a CHECKPOINT on the primary to force a WAL flush; the
//     standby's pg_stat_wal_receiver.received_lsn must advance past
//     the pre-checkpoint value.
//  5. Promotes the standby — verifies standby.signal is removed
//     (the v0 documented post-condition of `goopg promote`).
//
// The test does NOT exercise SQL-level row visibility on the
// standby because v0 doesn't yet persist its in-memory catalog
// across processes, so a CREATE TABLE on the primary is invisible
// to the standby's executor. The WAL-streaming + LSN-advance +
// promote-removes-signal observations are the strongest end-to-end
// check possible at this milestone.
//
// Skipped under -short because each go-run-driven cluster takes
// ~10s to bring up.
func TestReplicationEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping replcluster e2e in short mode")
	}
	repoRoot := repoRootForTest(t)

	rc, err := New("e2e", Options{
		RepoRoot:     repoRoot,
		BaseDir:      t.TempDir(),
		StartupWait:  30 * time.Second,
		ShutdownWait: 20 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := rc.Setup(); err != nil {
		t.Fatalf("Setup: %v", err)
	}
	defer func() { _ = rc.Stop() }()

	// Step 2: wait for the standby's walreceiver to register itself.
	// The pg_stat_wal_receiver view returns 0 rows until DialWalReceiver
	// completes its handshake.
	waitForReceiverStreaming(t, rc, 20*time.Second)

	// Step 3: the primary's pg_stat_replication should now show the
	// walsender for our slot.
	primaryRows := mustQuery(t, rc.Primary,
		"SELECT slot_name, state FROM pg_catalog.pg_stat_replication")
	foundSlot := false
	for _, row := range primaryRows {
		if len(row) >= 2 && row[0] == rc.SlotName && row[1] == "streaming" {
			foundSlot = true
			break
		}
	}
	if !foundSlot {
		t.Fatalf("pg_stat_replication missing walsender for slot %q (got %v)",
			rc.SlotName, primaryRows)
	}

	// Step 4: snapshot the standby's received_lsn, force a checkpoint
	// on the primary, then verify the standby moves past the
	// snapshot. The checkpoint marker is appended to WAL by the
	// primary, the walsender streams it, and the standby's
	// SetReceivedLSN bumps the view.
	beforeLSN := standbyReceivedLSN(t, rc)
	if err := rc.Primary.Checkpoint(); err != nil {
		t.Fatalf("primary checkpoint: %v", err)
	}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		afterLSN := standbyReceivedLSN(t, rc)
		if afterLSN != beforeLSN && afterLSN != "0/0" {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	finalLSN := standbyReceivedLSN(t, rc)
	if finalLSN == beforeLSN || finalLSN == "0/0" {
		t.Errorf("standby received_lsn did not advance after primary CHECKPOINT (before=%s after=%s)",
			beforeLSN, finalLSN)
	}

	// Step 5: promote the standby. After promote, standby.signal
	// must be gone and the standby is now usable as a primary.
	if err := rc.Promote(); err != nil {
		t.Fatalf("Promote: %v", err)
	}
	signalPath := filepath.Join(rc.Standby.DataDir(), "standby.signal")
	if _, err := os.Stat(signalPath); !os.IsNotExist(err) {
		t.Errorf("standby.signal still present after Promote (err=%v)", err)
	}
}

// waitForReceiverStreaming polls the standby's pg_stat_wal_receiver
// until a row appears with status='streaming'.
func waitForReceiverStreaming(t *testing.T, rc *ReplCluster, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		rows, err := rc.Standby.Query(context.Background(),
			"SELECT status FROM pg_catalog.pg_stat_wal_receiver")
		if err == nil {
			for _, row := range rows {
				if len(row) >= 1 && row[0] == "streaming" {
					return
				}
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("standby walreceiver did not reach streaming state within %s", timeout)
}

// standbyReceivedLSN returns the current value of
// pg_stat_wal_receiver.written_lsn, or "0/0" when the view is empty.
func standbyReceivedLSN(t *testing.T, rc *ReplCluster) string {
	t.Helper()
	rows, err := rc.Standby.Query(context.Background(),
		"SELECT written_lsn FROM pg_catalog.pg_stat_wal_receiver")
	if err != nil {
		t.Fatalf("query standby received_lsn: %v", err)
	}
	if len(rows) == 0 {
		return "0/0"
	}
	return rows[0][0]
}

// mustQuery is a thin wrapper that fails the test on query error.
func mustQuery(t *testing.T, c *cluster.Cluster, sql string) [][]string {
	t.Helper()
	rows, err := c.Query(context.Background(), sql)
	if err != nil {
		t.Fatalf("query %q: %v", sql, err)
	}
	return rows
}

// repoRootForTest finds the repo root by walking up from the test's
// working directory until a go.mod is found.
func repoRootForTest(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	dir := wd
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("could not find go.mod from %s", wd)
		}
		dir = parent
	}
}

// TestReplClusterNewValidates pins the constructor's argument
// validation so a typo'd test doesn't take 30s to fail with an
// opaque tcp error.
func TestReplClusterNewValidates(t *testing.T) {
	if _, err := New("", Options{RepoRoot: "/x"}); err == nil ||
		!strings.Contains(err.Error(), "name") {
		t.Errorf("New(empty name) err = %v, want name-related error", err)
	}
	if _, err := New("x", Options{}); err == nil ||
		!strings.Contains(err.Error(), "RepoRoot") {
		t.Errorf("New(empty RepoRoot) err = %v, want RepoRoot-related error", err)
	}
}
