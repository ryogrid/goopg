package initdb

import (
	"path/filepath"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/wal"
)

// TestTransformDDLRecoveryReplaysCreate confirms the M0119-0004 restart-
// persistence hook: a CREATE TRANSFORM WAL record written by a pre-crash run
// is replayed into the catalog's transform registry on the post-crash Open
// path, so pg_transform (pg_dump's getTransforms/dumpTransform) round-trips
// after restart even though the goopg process never wrote a JSON snapshot.
// The OID and resolved function OIDs are preserved across the restart.
func TestTransformDDLRecoveryReplaysCreate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	rt1, err := Open(OpenOptions{DataDir: dir, PoolSlots: 4})
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	const wantOID = uint32(40456)
	if _, _, werr := rt1.WAL.Append(wal.EncodeCreateTransform("integer", "sql", wantOID, 3721, 2406)); werr != nil {
		_ = rt1.Close()
		t.Fatalf("WAL.Append create-transform: %v", werr)
	}
	if ferr := rt1.WAL.FlushUpTo(rt1.WAL.WrittenLSN()); ferr != nil {
		_ = rt1.Close()
		t.Fatalf("FlushUpTo: %v", ferr)
	}
	if err := rt1.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}

	// Second Open: WAL replay must surface the transform in the registry
	// without any JSON snapshot help.
	rt2, err := Open(OpenOptions{DataDir: dir, PoolSlots: 4})
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer rt2.Close()

	cat, ok := rt2.Catalog.(*catalog.InMemory)
	if !ok {
		t.Fatalf("Catalog is %T, expected *catalog.InMemory", rt2.Catalog)
	}
	if !cat.TransformExists("integer", "sql") {
		t.Fatalf("after WAL replay, TransformExists(integer, sql) = false")
	}
	tfs := cat.ListTransforms()
	if len(tfs) != 1 {
		t.Fatalf("after WAL replay, ListTransforms() = %d entries, want 1", len(tfs))
	}
	if tfs[0].OID != wantOID || tfs[0].FromFuncOID != 3721 || tfs[0].ToFuncOID != 2406 {
		t.Errorf("after WAL replay, transform = %+v, want OID=%d FromFuncOID=3721 ToFuncOID=2406", tfs[0], wantOID)
	}
}

// TestTransformDDLRecoveryReplaysDropAfterCreate confirms that a CREATE
// followed by a DROP cancels out — the registry agrees with the most recent
// durable record, not the first one.
func TestTransformDDLRecoveryReplaysDropAfterCreate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	rt1, err := Open(OpenOptions{DataDir: dir, PoolSlots: 4})
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	if _, _, werr := rt1.WAL.Append(wal.EncodeCreateTransform("integer", "sql", 40500, 3721, 2406)); werr != nil {
		_ = rt1.Close()
		t.Fatalf("WAL.Append create: %v", werr)
	}
	if _, _, werr := rt1.WAL.Append(wal.EncodeDropTransform("integer", "sql")); werr != nil {
		_ = rt1.Close()
		t.Fatalf("WAL.Append drop: %v", werr)
	}
	if ferr := rt1.WAL.FlushUpTo(rt1.WAL.WrittenLSN()); ferr != nil {
		_ = rt1.Close()
		t.Fatalf("FlushUpTo: %v", ferr)
	}
	if err := rt1.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}

	rt2, err := Open(OpenOptions{DataDir: dir, PoolSlots: 4})
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer rt2.Close()

	cat := rt2.Catalog.(*catalog.InMemory)
	if cat.TransformExists("integer", "sql") {
		t.Errorf("after CREATE + DROP replay, TransformExists(integer, sql) = true")
	}
}

// TestReplayTransformDDLRecordsHandlesMissingWalDir verifies the recovery
// hook is idempotent when invoked against a missing pg_wal directory (brand
// new initdb).
func TestReplayTransformDDLRecordsHandlesMissingWalDir(t *testing.T) {
	cat := catalog.NewInMemory()
	if err := replayTransformDDLRecords(filepath.Join(t.TempDir(), "nonexistent"), cat); err != nil {
		t.Fatalf("replay against missing dir: %v", err)
	}
	if cat.TransformExists("integer", "sql") {
		t.Error("no-op replay should not register any transform")
	}
}
