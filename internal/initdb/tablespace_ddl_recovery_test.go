package initdb

import (
	"path/filepath"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/wal"
)

// TestTablespaceDDLRecoveryReplaysCreate confirms the M0122-0007
// tablespace-registry restart-durability fix: a CREATE TABLESPACE WAL record
// written by a pre-crash run is replayed into the catalog's tablespace
// registry on the post-crash Open path, so `pg_tablespace` lookups (and any
// table/index whose durable `reltablespace` OID points at it) resolve after
// restart even though the goopg process never wrote a JSON snapshot. The OID
// is preserved across the restart.
func TestTablespaceDDLRecoveryReplaysCreate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	rt1, err := Open(OpenOptions{DataDir: dir, PoolSlots: 4})
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	const wantOID = uint32(40456)
	if _, _, werr := rt1.WAL.Append(wal.EncodeCreateTablespace("ts1", "postgres", "", wantOID)); werr != nil {
		_ = rt1.Close()
		t.Fatalf("WAL.Append create-tablespace: %v", werr)
	}
	if ferr := rt1.WAL.FlushUpTo(rt1.WAL.WrittenLSN()); ferr != nil {
		_ = rt1.Close()
		t.Fatalf("FlushUpTo: %v", ferr)
	}
	if err := rt1.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}

	// Second Open: WAL replay must surface ts1 in the registry without any
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
	got, ok := cat.LookupTablespaceOID("ts1")
	if !ok {
		t.Fatalf("after WAL replay, LookupTablespaceOID(ts1) not found")
	}
	if got != wantOID {
		t.Errorf("after WAL replay, LookupTablespaceOID(ts1) = %d, want %d (OID not preserved)", got, wantOID)
	}
}

// TestTablespaceDDLRecoveryReplaysDropAfterCreate confirms that a CREATE
// followed by a DROP cancels out — the registry agrees with the most recent
// durable record, not the first one.
func TestTablespaceDDLRecoveryReplaysDropAfterCreate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	rt1, err := Open(OpenOptions{DataDir: dir, PoolSlots: 4})
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	if _, _, werr := rt1.WAL.Append(wal.EncodeCreateTablespace("scratch", "postgres", "", 40500)); werr != nil {
		_ = rt1.Close()
		t.Fatalf("WAL.Append create: %v", werr)
	}
	if _, _, werr := rt1.WAL.Append(wal.EncodeDropTablespace("scratch")); werr != nil {
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
	if _, ok := cat.LookupTablespaceOID("scratch"); ok {
		t.Errorf("after CREATE + DROP replay, LookupTablespaceOID(scratch) found, want gone")
	}
	// A bootstrap tablespace must still resolve (replay only touches user
	// tablespaces it has records for).
	if _, ok := cat.LookupTablespaceOID("pg_default"); !ok {
		t.Error("bootstrap pg_default tablespace should still resolve")
	}
}

// TestReplayTablespaceDDLRecordsHandlesMissingWalDir verifies the recovery
// hook is idempotent when invoked against a missing pg_wal directory (brand
// new initdb).
func TestReplayTablespaceDDLRecordsHandlesMissingWalDir(t *testing.T) {
	cat := catalog.NewInMemory()
	if err := replayTablespaceDDLRecords(filepath.Join(t.TempDir(), "nonexistent"), cat); err != nil {
		t.Fatalf("replay against missing dir: %v", err)
	}
	if _, ok := cat.LookupTablespaceOID("pg_default"); !ok {
		t.Error("bootstrap pg_default tablespace should still resolve after no-op replay")
	}
}
