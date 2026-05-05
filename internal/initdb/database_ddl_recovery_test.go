package initdb

import (
	"path/filepath"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/wal"
)

// TestDatabaseDDLRecoveryReplaysCreate confirms the M0054-0001 hook:
// a CREATE DATABASE WAL record written by a pre-crash run is replayed
// into the catalog's database registry on the post-crash Open path,
// so `pg_database` reports the user database after restart even
// though the goopg process never wrote a JSON snapshot.
func TestDatabaseDDLRecoveryReplaysCreate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	// First Open: simulate a CREATE DATABASE tpch by appending a WAL
	// record through the runtime's writer, then close cleanly.
	rt1, err := Open(OpenOptions{DataDir: dir, PoolSlots: 4})
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	if _, _, werr := rt1.WAL.Append(wal.EncodeCreateDatabase("tpch")); werr != nil {
		_ = rt1.Close()
		t.Fatalf("WAL.Append create-database: %v", werr)
	}
	if ferr := rt1.WAL.FlushUpTo(rt1.WAL.WrittenLSN()); ferr != nil {
		_ = rt1.Close()
		t.Fatalf("FlushUpTo: %v", ferr)
	}
	// Mutate the catalog so Close() doesn't snapshot the change to
	// JSON — we want the second Open to reconstruct via WAL replay
	// alone, mirroring the post-crash scenario.
	if err := rt1.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}

	// Second Open: WAL replay must surface tpch in the registry
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
	if !cat.HasDatabase("tpch") {
		t.Errorf("after WAL replay, HasDatabase(tpch) = false; databases = %v", cat.ListDatabases())
	}
}

// TestDatabaseDDLRecoveryReplaysDropAfterCreate confirms that a
// CREATE followed by a DROP cancels out — the registry agrees with
// the most recent durable record, not the first one.
func TestDatabaseDDLRecoveryReplaysDropAfterCreate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	rt1, err := Open(OpenOptions{DataDir: dir, PoolSlots: 4})
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	if _, _, werr := rt1.WAL.Append(wal.EncodeCreateDatabase("scratch")); werr != nil {
		_ = rt1.Close()
		t.Fatalf("WAL.Append create: %v", werr)
	}
	if _, _, werr := rt1.WAL.Append(wal.EncodeDropDatabase("scratch")); werr != nil {
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
	if cat.HasDatabase("scratch") {
		t.Errorf("after CREATE + DROP replay, HasDatabase(scratch) = true; databases = %v", cat.ListDatabases())
	}
	if !cat.HasDatabase("postgres") {
		t.Error("seed postgres database should still be present")
	}
}

// TestReplayDatabaseDDLRecordsHandlesMissingWalDir verifies the
// recovery hook is idempotent when invoked against a missing pg_wal
// directory (e.g. brand new initdb).
func TestReplayDatabaseDDLRecordsHandlesMissingWalDir(t *testing.T) {
	cat := catalog.NewInMemory()
	if err := replayDatabaseDDLRecords(filepath.Join(t.TempDir(), "nonexistent"), cat); err != nil {
		t.Fatalf("replay against missing dir: %v", err)
	}
	if !cat.HasDatabase("postgres") {
		t.Error("postgres seed should still be present after no-op replay")
	}
}
