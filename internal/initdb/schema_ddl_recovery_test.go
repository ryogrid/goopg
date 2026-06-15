package initdb

import (
	"path/filepath"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/wal"
)

// TestSchemaDDLRecoveryReplaysCreate confirms the M0110-0003 hook: a CREATE
// SCHEMA WAL record written by a pre-crash run is replayed into the catalog's
// schema registry on the post-crash Open path, so schema-qualified lookups
// (e.g. pg_amcheck --schema s1) resolve after restart even though the goopg
// process never wrote a JSON snapshot. The OID is preserved across the restart.
func TestSchemaDDLRecoveryReplaysCreate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	rt1, err := Open(OpenOptions{DataDir: dir, PoolSlots: 4})
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	const wantOID = uint32(40123)
	if _, _, werr := rt1.WAL.Append(wal.EncodeCreateSchema("s1", wantOID)); werr != nil {
		_ = rt1.Close()
		t.Fatalf("WAL.Append create-schema: %v", werr)
	}
	if ferr := rt1.WAL.FlushUpTo(rt1.WAL.WrittenLSN()); ferr != nil {
		_ = rt1.Close()
		t.Fatalf("FlushUpTo: %v", ferr)
	}
	if err := rt1.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}

	// Second Open: WAL replay must surface s1 in the registry without any
	// JSON snapshot help.
	rt2, err := Open(OpenOptions{DataDir: dir, PoolSlots: 4})
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer rt2.Close()

	cat, ok := rt2.Catalog.(*catalog.InMemory)
	if !ok {
		t.Fatalf("Catalog is %T, expected *catalog.InMemory", rt2.Catalog)
	}
	if !cat.SchemaExists("s1") {
		t.Errorf("after WAL replay, SchemaExists(s1) = false")
	}
	if got := cat.SchemaOID("s1"); got != wantOID {
		t.Errorf("after WAL replay, SchemaOID(s1) = %d, want %d (OID not preserved)", got, wantOID)
	}
}

// TestSchemaDDLRecoveryReplaysDropAfterCreate confirms that a CREATE followed
// by a DROP cancels out — the registry agrees with the most recent durable
// record, not the first one.
func TestSchemaDDLRecoveryReplaysDropAfterCreate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	rt1, err := Open(OpenOptions{DataDir: dir, PoolSlots: 4})
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	if _, _, werr := rt1.WAL.Append(wal.EncodeCreateSchema("scratch", 40200)); werr != nil {
		_ = rt1.Close()
		t.Fatalf("WAL.Append create: %v", werr)
	}
	if _, _, werr := rt1.WAL.Append(wal.EncodeDropSchema("scratch")); werr != nil {
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
	if cat.SchemaExists("scratch") {
		t.Errorf("after CREATE + DROP replay, SchemaExists(scratch) = true")
	}
	// A standard system schema must still be present (replay only touches
	// user schemas it has records for).
	if !cat.SchemaExists("public") {
		t.Error("seed public schema should still be present")
	}
}

// TestReplaySchemaDDLRecordsHandlesMissingWalDir verifies the recovery hook is
// idempotent when invoked against a missing pg_wal directory (brand new initdb).
func TestReplaySchemaDDLRecordsHandlesMissingWalDir(t *testing.T) {
	cat := catalog.NewInMemory()
	if err := replaySchemaDDLRecords(filepath.Join(t.TempDir(), "nonexistent"), cat); err != nil {
		t.Fatalf("replay against missing dir: %v", err)
	}
	if !cat.SchemaExists("public") {
		t.Error("public seed schema should still be present after no-op replay")
	}
}
