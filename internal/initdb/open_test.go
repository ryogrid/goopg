package initdb

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestOpenAfterInitReturnsRuntime: the typical operator flow —
// goopg init writes the layout, goopg start opens it — produces a
// Runtime with all four handles populated.
func TestOpenAfterInitReturnsRuntime(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir}); err != nil {
		t.Fatal(err)
	}
	rt, err := Open(OpenOptions{DataDir: dir, PoolSlots: 4})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	if rt.StorageMgr == nil || rt.Pool == nil || rt.TxnMgr == nil || rt.Catalog == nil {
		t.Errorf("runtime has nil handle: %+v", rt)
	}
	if rt.DataDir == "" || !filepath.IsAbs(rt.DataDir) {
		t.Errorf("DataDir=%q want absolute path", rt.DataDir)
	}
}

// TestOpenRejectsUninitializedDir: pointing the server at a
// directory that goopg init never touched should fail fast with
// the diagnostic that names the missing PG_VERSION as the
// telltale.
func TestOpenRejectsUninitializedDir(t *testing.T) {
	dir := t.TempDir() // empty
	_, err := Open(OpenOptions{DataDir: dir})
	if err == nil {
		t.Fatal("expected error for uninitialized dir")
	}
	if !strings.Contains(err.Error(), "not initialized") && !strings.Contains(err.Error(), "PG_VERSION") {
		t.Errorf("err=%q want a hint about initialization", err.Error())
	}
}

// TestOpenRejectsMissingDir: clearer diagnostic when the path
// doesn't exist at all.
func TestOpenRejectsMissingDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nonexistent")
	_, err := Open(OpenOptions{DataDir: dir})
	if err == nil {
		t.Fatal("expected error for missing dir")
	}
	if !strings.Contains(err.Error(), "does not exist") {
		t.Errorf("err=%q want a 'does not exist' message", err.Error())
	}
}

// TestOpenRejectsVersionMismatch: surfacing a catalog-version
// mismatch is important so a binary upgrade can't silently corrupt
// an old data directory.
func TestOpenRejectsVersionMismatch(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "PG_VERSION"), []byte("99\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Open(OpenOptions{DataDir: dir})
	if err == nil {
		t.Fatal("expected error for version mismatch")
	}
	if !strings.Contains(err.Error(), "catalog version") {
		t.Errorf("err=%q want a catalog-version hint", err.Error())
	}
}

// TestRuntimeCloseIsIdempotent: a defer-Close after a successful
// Open shouldn't double-error if some other path already closed.
func TestRuntimeCloseIsIdempotent(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir}); err != nil {
		t.Fatal(err)
	}
	rt, err := Open(OpenOptions{DataDir: dir, PoolSlots: 2})
	if err != nil {
		t.Fatal(err)
	}
	if err := rt.Close(); err != nil {
		t.Fatalf("first close: %v", err)
	}
	if err := rt.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
}
