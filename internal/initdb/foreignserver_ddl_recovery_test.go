package initdb

import (
	"path/filepath"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/wal"
)

// TestForeignServerDDLRecoveryReplaysCreate confirms the M0122-0007
// foreign-server registry restart-durability fix: a CREATE SERVER WAL record
// written by a pre-crash run is replayed into the catalog's foreign-server
// registry on the post-crash Open path, so `pg_foreign_server` lookups (and
// any dependent user mapping's srvid) resolve after restart even though the
// goopg process never wrote a JSON snapshot. The OID is preserved across the
// restart.
func TestForeignServerDDLRecoveryReplaysCreate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	rt1, err := Open(OpenOptions{DataDir: dir, PoolSlots: 4})
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	const wantOID = uint32(40456)
	options := []string{"host=localhost", "port=5432"}
	if _, _, werr := rt1.WAL.Append(wal.EncodeCreateForeignServer("myserver", "postgres_fdw", "prod", "9.1", options, wantOID, catalog.DefaultDBOid)); werr != nil {
		_ = rt1.Close()
		t.Fatalf("WAL.Append create-foreign-server: %v", werr)
	}
	if ferr := rt1.WAL.FlushUpTo(rt1.WAL.WrittenLSN()); ferr != nil {
		_ = rt1.Close()
		t.Fatalf("FlushUpTo: %v", ferr)
	}
	if err := rt1.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}

	// Second Open: WAL replay must surface myserver in the registry without
	// any JSON snapshot help.
	rt2, err := Open(OpenOptions{DataDir: dir, PoolSlots: 4})
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer rt2.Close()

	cat, ok := rt2.Catalog.(*catalog.InMemory)
	if !ok {
		t.Fatalf("Catalog is %T, expected *catalog.InMemory", rt2.Catalog)
	}
	got := cat.ForeignServerOID("myserver")
	if got == 0 {
		t.Fatalf("after WAL replay, ForeignServerOID(myserver) not found")
	}
	if got != wantOID {
		t.Errorf("after WAL replay, ForeignServerOID(myserver) = %d, want %d (OID not preserved)", got, wantOID)
	}
}

// TestForeignServerDDLRecoveryReplaysDropAfterCreate confirms that a CREATE
// followed by a DROP cancels out — the registry agrees with the most recent
// durable record, not the first one.
func TestForeignServerDDLRecoveryReplaysDropAfterCreate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	rt1, err := Open(OpenOptions{DataDir: dir, PoolSlots: 4})
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	if _, _, werr := rt1.WAL.Append(wal.EncodeCreateForeignServer("scratch", "postgres_fdw", "", "", nil, 40500, catalog.DefaultDBOid)); werr != nil {
		_ = rt1.Close()
		t.Fatalf("WAL.Append create: %v", werr)
	}
	if _, _, werr := rt1.WAL.Append(wal.EncodeDropForeignServer("scratch", catalog.DefaultDBOid)); werr != nil {
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
	if got := cat.ForeignServerOID("scratch"); got != 0 {
		t.Errorf("after CREATE + DROP replay, ForeignServerOID(scratch) = %d, want 0 (gone)", got)
	}
}

// TestForeignServerDDLRecoveryPreservesDistinctDBOidAfterRestart confirms the
// M0122-0007 4e follow-up 36 fix: two same-named CREATE SERVERs under
// distinct dbOids must both survive a restart as independent registry
// entries (the pre-fix bare-name key silently collapsed them onto one OID,
// last-writer-wins).
func TestForeignServerDDLRecoveryPreservesDistinctDBOidAfterRestart(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	rt1, err := Open(OpenOptions{DataDir: dir, PoolSlots: 4})
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	const dbA, dbB = uint32(16401), uint32(16402)
	if _, _, werr := rt1.WAL.Append(wal.EncodeCreateForeignServer("shared", "postgres_fdw", "", "", nil, 40600, dbA)); werr != nil {
		_ = rt1.Close()
		t.Fatalf("WAL.Append create (dbA): %v", werr)
	}
	if _, _, werr := rt1.WAL.Append(wal.EncodeCreateForeignServer("shared", "file_fdw", "", "", nil, 40601, dbB)); werr != nil {
		_ = rt1.Close()
		t.Fatalf("WAL.Append create (dbB): %v", werr)
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
	if got := cat.ForeignServerOID("shared", dbA); got != 40600 {
		t.Errorf("after WAL replay, dbA's ForeignServerOID(shared) = %d, want 40600", got)
	}
	if got := cat.ForeignServerOID("shared", dbB); got != 40601 {
		t.Errorf("after WAL replay, dbB's ForeignServerOID(shared) = %d, want 40601 (must survive dbA's same-named server)", got)
	}
}

// TestReplayForeignServerDDLRecordsHandlesMissingWalDir verifies the recovery
// hook is idempotent when invoked against a missing pg_wal directory (brand
// new initdb).
func TestReplayForeignServerDDLRecordsHandlesMissingWalDir(t *testing.T) {
	cat := catalog.NewInMemory()
	if err := replayForeignServerDDLRecords(filepath.Join(t.TempDir(), "nonexistent"), cat); err != nil {
		t.Fatalf("replay against missing dir: %v", err)
	}
	if got := cat.ForeignServerOID("anything"); got != 0 {
		t.Errorf("no-op replay unexpectedly registered a server: OID=%d", got)
	}
}
