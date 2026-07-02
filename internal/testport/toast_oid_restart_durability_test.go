package testport

// End-to-end coverage of TOAST chunk_id restart durability.
//
// goopg's TOAST chunk-id counter (executor.toastOIDCounter,
// internal/executor/toast.go) is process-local and always starts at 0 on a
// fresh process, but the TOAST relation it writes chunk rows into survives
// a restart on disk (checkpointed like any other relation). Without
// reseeding the counter from existing TOAST content at startup, the first
// TOASTed value written after a restart reissues chunk_id 1 — colliding
// with an earlier value's still-resident chunk_id 1 in the same table's
// TOAST relation — and DetoastValue's oid-only scan splices the two
// unrelated values' chunks together, corrupting reassembly for BOTH.
//
// Surfaced by WordPress-on-goopg (deferral ledger 2026-07-02): after heavy
// admin-dashboard traffic wrote several oversized (>8 KB) wp_options
// transients and a clean goopg restart, the neighboring wp_user_roles
// option (a ~3992-byte toasted value, itself well over the 2000-byte
// ToastThreshold) read back with foreign bytes from one of the
// post-restart transients.
//
// The fix seeds executor.toastOIDCounter once at startup
// (internal/initdb/open.go, after loadUserTablesFromHeap) by scanning
// every user table's TOAST relation for the highest chunk_id physically
// present and advancing the counter past it
// (executor.SeedToastOIDCounter/MaxToastChunkIDInRel).

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/testutil/cluster"
)

// TestPort_ToastValueSurvivesRestartWithoutCollision writes a TOASTed value,
// restarts the cluster, writes a second unrelated TOASTed value in the same
// table, and asserts neither value's bytes leak into the other.
func TestPort_ToastValueSurvivesRestartWithoutCollision(t *testing.T) {
	c, err := cluster.New("toast-oid-restart-durability", cluster.Options{
		RepoRoot:     repoRoot(t),
		DataDir:      filepath.Join(t.TempDir(), "data"),
		StartupWait:  20 * time.Second,
		ShutdownWait: 20 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	if err := runSQLSimple(t, c,
		"CREATE TABLE toastdur (id int, v text)"); err != nil {
		t.Fatalf("CREATE TABLE toastdur: %v", err)
	}

	// Pre-restart TOASTed value (mirrors wp_user_roles: just over the
	// 2000-byte ToastThreshold).
	if err := runSQLSimple(t, c,
		"INSERT INTO toastdur (id, v) VALUES (1, repeat('A', 3992))"); err != nil {
		t.Fatalf("pre-restart INSERT: %v", err)
	}
	if got := queryScalar(t, c,
		"SELECT count(*) FROM toastdur WHERE id = 1 AND v = repeat('A', 3992)"); got != "1" {
		t.Fatalf("pre-restart value not stored correctly: count = %q", got)
	}

	// Clean stop -> restart. executor.toastOIDCounter resets to 0 in the
	// new process; SeedToastOIDCounter must reseed it above the chunk_id
	// already used by row id=1's TOAST chunks before any new TOAST write.
	if err := c.Stop(cluster.ShutdownFast); err != nil {
		t.Fatalf("stop cluster: %v", err)
	}
	if err := c.Start(); err != nil {
		t.Fatalf("restart cluster: %v", err)
	}

	// Post-restart TOASTed value (mirrors an oversized theme-patterns
	// transient) in the SAME table, so it shares the SAME TOAST relation.
	if err := runSQLSimple(t, c,
		"INSERT INTO toastdur (id, v) VALUES (2, repeat('B', 30000))"); err != nil {
		t.Fatalf("post-restart INSERT: %v", err)
	}

	// (a) The post-restart value must be exactly what was written.
	if got := queryScalar(t, c,
		"SELECT count(*) FROM toastdur WHERE id = 2 AND v = repeat('B', 30000)"); got != "1" {
		t.Fatalf("post-restart value corrupted: count = %q", got)
	}

	// (b) The pre-restart value must be UNCHANGED — this is the actual
	// regression: without the fix, detoasting id=1 after the post-restart
	// TOAST OID collision returns id=2's (or a mix of both values') bytes.
	if got := queryScalar(t, c,
		"SELECT count(*) FROM toastdur WHERE id = 1 AND v = repeat('A', 3992)"); got != "1" {
		t.Fatalf("pre-restart value corrupted by post-restart TOAST OID collision: count = %q "+
			"(want 1 — the row's own 3992 'A' bytes)", got)
	}
	if got := queryScalar(t, c, "SELECT length(v) FROM toastdur WHERE id = 1"); got != "3992" {
		t.Fatalf("pre-restart value length changed: got %q, want 3992", got)
	}
}
