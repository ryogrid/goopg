package initdb

import (
	"path/filepath"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/wal"
)

// rangeTypeCatalog is a small test helper: the Runtime.Catalog field is the
// catalog.Catalog interface, but the range type registry lives on the
// concrete *catalog.InMemory implementation (mirrors accessMethodCatalog).
func rangeTypeCatalog(t *testing.T, rt *Runtime) *catalog.InMemory {
	t.Helper()
	im, ok := rt.Catalog.(*catalog.InMemory)
	if !ok {
		t.Fatalf("Runtime.Catalog is not *catalog.InMemory: %T", rt.Catalog)
	}
	return im
}

// TestRangeTypeDDLRecoveryReplaysCreate confirms the DU-002
// restart-persistence hook (M0110-0001, DU-002 slice 429 ledger resume
// point, sub-item (c)): a CREATE TYPE ... AS RANGE WAL record written by a
// pre-crash run is replayed into the catalog's rangeTypes registry on the
// post-crash Open path, so pg_range/pg_type (pg_dump's dumpRangeType)
// round-trips after restart even though the goopg process never wrote a
// JSON snapshot. Both OIDs and every field survive the restart.
func TestRangeTypeDDLRecoveryReplaysCreate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	rt1, err := Open(OpenOptions{DataDir: dir, PoolSlots: 4})
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	const wantOID = uint32(40900)
	const wantArrayOID = uint32(40901)
	const wantMultirangeOID = uint32(40902)
	const wantMultirangeArrayOID = uint32(40903)
	const wantOpclassOID = uint32(1978)
	const wantCollationOID = uint32(0)
	const wantOwnerOID = uint32(16400)
	if _, _, werr := rt1.WAL.Append(wal.EncodeCreateRangeType("myrange", "int4", "mymultirange", wantOID, wantArrayOID, wantMultirangeOID, wantMultirangeArrayOID, wantOpclassOID, wantCollationOID, wantOwnerOID)); werr != nil {
		_ = rt1.Close()
		t.Fatalf("WAL.Append create-range-type: %v", werr)
	}
	if ferr := rt1.WAL.FlushUpTo(rt1.WAL.WrittenLSN()); ferr != nil {
		_ = rt1.Close()
		t.Fatalf("FlushUpTo: %v", ferr)
	}
	if err := rt1.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}

	// Second Open: WAL replay must surface the range type without any JSON
	// snapshot help.
	rt2, err := Open(OpenOptions{DataDir: dir, PoolSlots: 4})
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer rt2.Close()

	found, ok := rangeTypeCatalog(t, rt2).LookupRangeType("myrange")
	if !ok {
		t.Fatalf("after WAL replay, range type \"myrange\" not found")
	}
	if found.OID != wantOID || found.ArrayOID != wantArrayOID || found.MultirangeOID != wantMultirangeOID ||
		found.MultirangeArrayOID != wantMultirangeArrayOID || found.OpclassOID != wantOpclassOID ||
		found.CollationOID != wantCollationOID || found.Owner != wantOwnerOID ||
		found.SubtypeName != "int4" || found.MultirangeName != "mymultirange" {
		t.Errorf("after WAL replay, range type = %+v, want OID=%d ArrayOID=%d MultirangeOID=%d MultirangeArrayOID=%d OpclassOID=%d CollationOID=%d Owner=%d SubtypeName=int4 MultirangeName=mymultirange",
			found, wantOID, wantArrayOID, wantMultirangeOID, wantMultirangeArrayOID, wantOpclassOID, wantCollationOID, wantOwnerOID)
	}
}

// TestRangeTypeDDLRecoveryReplaysDropAfterCreate confirms a CREATE followed
// by a DROP cancels out — the registry agrees with the most recent durable
// record, not the first one.
func TestRangeTypeDDLRecoveryReplaysDropAfterCreate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	rt1, err := Open(OpenOptions{DataDir: dir, PoolSlots: 4})
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	if _, _, werr := rt1.WAL.Append(wal.EncodeCreateRangeType("myrange", "int4", "mymultirange", 40910, 40911, 40912, 40913, 1978, 0, 10)); werr != nil {
		_ = rt1.Close()
		t.Fatalf("WAL.Append create: %v", werr)
	}
	if _, _, werr := rt1.WAL.Append(wal.EncodeDropRangeType("myrange")); werr != nil {
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

	if found, ok := rangeTypeCatalog(t, rt2).LookupRangeType("myrange"); ok {
		t.Errorf("after CREATE + DROP replay, range type \"myrange\" = %+v, want not found", found)
	}
}

// TestRangeTypeDDLRecoveryReplaysRenameAndOwner confirms the M0122-0005
// restart-persistence follow-up (deferral ledger 2026-07-06 row, resume
// point (1)): a post-CREATE `ALTER TYPE ... RENAME TO`/`OWNER TO` on a range
// type survives a restart, mirroring
// TestRangeTypeDDLRecoveryReplaysCreate but for the two ALTER forms instead
// of the initial CREATE.
func TestRangeTypeDDLRecoveryReplaysRenameAndOwner(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	rt1, err := Open(OpenOptions{DataDir: dir, PoolSlots: 4})
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	const wantOwnerOID = uint32(16400)
	if _, _, werr := rt1.WAL.Append(wal.EncodeCreateRangeType("myrange", "int4", "mymultirange", 40920, 40921, 40922, 40923, 1978, 0, 10)); werr != nil {
		_ = rt1.Close()
		t.Fatalf("WAL.Append create: %v", werr)
	}
	if _, _, werr := rt1.WAL.Append(wal.EncodeAlterRangeTypeRename("myrange", "myrange2")); werr != nil {
		_ = rt1.Close()
		t.Fatalf("WAL.Append rename: %v", werr)
	}
	if _, _, werr := rt1.WAL.Append(wal.EncodeAlterRangeTypeOwner("myrange2", wantOwnerOID)); werr != nil {
		_ = rt1.Close()
		t.Fatalf("WAL.Append owner: %v", werr)
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

	cat := rangeTypeCatalog(t, rt2)
	if _, ok := cat.LookupRangeType("myrange"); ok {
		t.Errorf("after rename replay, old name \"myrange\" should no longer resolve")
	}
	found, ok := cat.LookupRangeType("myrange2")
	if !ok {
		t.Fatalf("after rename replay, range type \"myrange2\" not found")
	}
	if found.Owner != wantOwnerOID {
		t.Errorf("after owner replay, Owner = %d, want %d", found.Owner, wantOwnerOID)
	}
}

// TestReplayRangeTypeDDLRecordsHandlesMissingWalDir verifies the recovery
// hook is idempotent when invoked against a missing pg_wal directory (brand
// new initdb).
func TestReplayRangeTypeDDLRecordsHandlesMissingWalDir(t *testing.T) {
	cat := catalog.NewInMemory()
	if err := replayRangeTypeDDLRecords(filepath.Join(t.TempDir(), "nonexistent"), cat); err != nil {
		t.Fatalf("replay against missing dir: %v", err)
	}
	if len(cat.ListRangeTypes()) != 0 {
		t.Errorf("no-op replay should not register anything, got %+v", cat.ListRangeTypes())
	}
}

// TestReplayRangeTypeDDLRecordsHandlesNilCatalog verifies the recovery hook
// tolerates a nil catalog (mirrors the nil-registry guard the access method
// DDL recovery driver has for embedded test setups).
func TestReplayRangeTypeDDLRecordsHandlesNilCatalog(t *testing.T) {
	if err := replayRangeTypeDDLRecords(filepath.Join(t.TempDir(), "nonexistent"), nil); err != nil {
		t.Fatalf("replay with nil catalog: %v", err)
	}
}
