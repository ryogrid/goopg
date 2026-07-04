package initdb

import (
	"path/filepath"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/wal"
)

// statisticsCatalog is a small test helper: the Runtime.Catalog field is the
// catalog.Catalog interface, but the statistics registry lives on the
// concrete *catalog.InMemory implementation (mirrors accessMethodCatalog).
func statisticsCatalog(t *testing.T, rt *Runtime) *catalog.InMemory {
	t.Helper()
	im, ok := rt.Catalog.(*catalog.InMemory)
	if !ok {
		t.Fatalf("Runtime.Catalog is not *catalog.InMemory: %T", rt.Catalog)
	}
	return im
}

// TestStatisticsDDLRecoveryReplaysCreate confirms the DU-002
// restart-persistence hook (slice 441's own resume point): a CREATE
// STATISTICS WAL record written by a pre-crash run is replayed into the
// catalog's statisticsObjs registry on the post-crash Open path, so
// pg_statistic_ext (pg_dump's dumpStatisticsExt) round-trips after restart.
func TestStatisticsDDLRecoveryReplaysCreate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	rt1, err := Open(OpenOptions{DataDir: dir, PoolSlots: 4})
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	const wantOID = uint32(40950)
	const wantTableOID = uint32(16500)
	const wantOwnerOID = uint32(16384)
	wantKinds := []string{"ndistinct", "dependencies"}
	wantColumns := []string{"a", "b"}
	if _, _, werr := rt1.WAL.Append(wal.EncodeCreateStatistics("mystat", "public", wantOID, wantTableOID, wantOwnerOID, wantKinds, wantColumns, nil, false)); werr != nil {
		_ = rt1.Close()
		t.Fatalf("WAL.Append create-statistics: %v", werr)
	}
	if ferr := rt1.WAL.FlushUpTo(rt1.WAL.WrittenLSN()); ferr != nil {
		_ = rt1.Close()
		t.Fatalf("FlushUpTo: %v", ferr)
	}
	if err := rt1.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}

	// Second Open: WAL replay must surface the statistics object without any
	// JSON snapshot help.
	rt2, err := Open(OpenOptions{DataDir: dir, PoolSlots: 4})
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer rt2.Close()

	found, ok := statisticsCatalog(t, rt2).LookupStatistics("public.mystat")
	if !ok {
		t.Fatalf("after WAL replay, statistics object \"mystat\" not found")
	}
	if found.OID != wantOID || found.TableOID != wantTableOID || found.Owner != wantOwnerOID {
		t.Errorf("after WAL replay, statistics object = %+v, want OID=%d TableOID=%d Owner=%d", found, wantOID, wantTableOID, wantOwnerOID)
	}
	if len(found.Kinds) != len(wantKinds) || len(found.Columns) != len(wantColumns) {
		t.Errorf("after WAL replay, statistics object = %+v, want Kinds=%v Columns=%v", found, wantKinds, wantColumns)
	}
}

// TestStatisticsDDLRecoveryReplaysDropAfterCreate confirms a CREATE followed
// by a DROP cancels out — the registry agrees with the most recent durable
// record, not the first one.
func TestStatisticsDDLRecoveryReplaysDropAfterCreate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	rt1, err := Open(OpenOptions{DataDir: dir, PoolSlots: 4})
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	if _, _, werr := rt1.WAL.Append(wal.EncodeCreateStatistics("mystat", "public", 40960, 16501, 0, nil, []string{"a"}, nil, false)); werr != nil {
		_ = rt1.Close()
		t.Fatalf("WAL.Append create: %v", werr)
	}
	if _, _, werr := rt1.WAL.Append(wal.EncodeDropStatistics("mystat", "public")); werr != nil {
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

	if _, ok := statisticsCatalog(t, rt2).LookupStatistics("public.mystat"); ok {
		t.Error("after CREATE + DROP replay, statistics object \"mystat\" found, want not found")
	}
}

// TestReplayStatisticsDDLRecordsHandlesMissingWalDir verifies the recovery
// hook is idempotent when invoked against a missing pg_wal directory (brand
// new initdb).
func TestReplayStatisticsDDLRecordsHandlesMissingWalDir(t *testing.T) {
	cat := catalog.NewInMemory()
	if err := replayStatisticsDDLRecords(filepath.Join(t.TempDir(), "nonexistent"), cat); err != nil {
		t.Fatalf("replay against missing dir: %v", err)
	}
	if len(cat.AllStatistics()) != 0 {
		t.Errorf("no-op replay should not register anything, got %+v", cat.AllStatistics())
	}
}

// TestReplayStatisticsDDLRecordsHandlesNilCatalog verifies the recovery hook
// tolerates a nil catalog (mirrors the nil-registry guard the other DU-002
// DDL recovery drivers have for embedded test setups).
func TestReplayStatisticsDDLRecordsHandlesNilCatalog(t *testing.T) {
	if err := replayStatisticsDDLRecords(filepath.Join(t.TempDir(), "nonexistent"), nil); err != nil {
		t.Fatalf("replay with nil catalog: %v", err)
	}
}
