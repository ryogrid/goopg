package initdb

import (
	"path/filepath"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/wal"
)

// findUserAggregate looks up a user aggregate by name in the registry
// snapshot returned by ListUserAggregates.
func findUserAggregate(cat *catalog.InMemory, name string) *catalog.UserAggregate {
	for _, agg := range cat.ListUserAggregates() {
		if agg.Name == name {
			return agg
		}
	}
	return nil
}

// TestAggregateDDLRecoveryReplaysCreate confirms the DU-002
// restart-persistence follow-up (M0119-0004, slice 405 ledger resume point
// (c)): a CREATE AGGREGATE WAL record written by a pre-crash run is replayed
// into the catalog's user-aggregate registry on the post-crash Open path, so
// pg_aggregate/pg_proc (pg_dump's getAggregates/dumpAgg, and the planner's
// isUserAggregateFunc lookup) agree with the pre-crash server even though the
// goopg process never wrote a JSON snapshot. The OID is preserved across the
// restart.
func TestAggregateDDLRecoveryReplaysCreate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	rt1, err := Open(OpenOptions{DataDir: dir, PoolSlots: 4})
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	const wantOID = uint32(40530)
	if _, _, werr := rt1.WAL.Append(wal.EncodeCreateAggregate("newavg", "_avgstate", "avg_transfn", "avg_finalfn", "", "", "", []string{"int4"}, wantOID, true, false)); werr != nil {
		_ = rt1.Close()
		t.Fatalf("WAL.Append create-aggregate: %v", werr)
	}
	if ferr := rt1.WAL.FlushUpTo(rt1.WAL.WrittenLSN()); ferr != nil {
		_ = rt1.Close()
		t.Fatalf("FlushUpTo: %v", ferr)
	}
	if err := rt1.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}

	// Second Open: WAL replay must surface the aggregate in the registry
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
	agg := findUserAggregate(cat, "newavg")
	if agg == nil {
		t.Fatalf("after WAL replay, aggregate \"newavg\" not found; registry = %+v", cat.ListUserAggregates())
	}
	if agg.OID != wantOID || agg.SType != "_avgstate" || agg.SFunc != "avg_transfn" || agg.FinalFunc != "avg_finalfn" || !agg.SFuncStrict || agg.Variadic {
		t.Errorf("after WAL replay, aggregate = %+v, want OID=%d SType=_avgstate SFunc=avg_transfn FinalFunc=avg_finalfn SFuncStrict=true Variadic=false", agg, wantOID)
	}
	if len(agg.ArgTypes) != 1 || agg.ArgTypes[0] != "int4" {
		t.Errorf("after WAL replay, ArgTypes = %+v, want [int4]", agg.ArgTypes)
	}
}

// TestAggregateDDLRecoveryReplaysRenameAfterCreate confirms that a CREATE
// AGGREGATE followed by an ALTER AGGREGATE ... RENAME TO replays in order, so
// the post-restart registry has the aggregate under its new name (not the
// original), with its OID preserved.
func TestAggregateDDLRecoveryReplaysRenameAfterCreate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	rt1, err := Open(OpenOptions{DataDir: dir, PoolSlots: 4})
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	const wantOID = uint32(40540)
	if _, _, werr := rt1.WAL.Append(wal.EncodeCreateAggregate("newavg", "_avgstate", "avg_transfn", "avg_finalfn", "", "", "", []string{"int4"}, wantOID, true, false)); werr != nil {
		_ = rt1.Close()
		t.Fatalf("WAL.Append create: %v", werr)
	}
	if _, _, werr := rt1.WAL.Append(wal.EncodeAlterAggregateRename("newavg", "renamedavg")); werr != nil {
		_ = rt1.Close()
		t.Fatalf("WAL.Append rename: %v", werr)
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
	if agg := findUserAggregate(cat, "newavg"); agg != nil {
		t.Errorf("after CREATE + RENAME replay, old name \"newavg\" = %+v, want nil", agg)
	}
	agg := findUserAggregate(cat, "renamedavg")
	if agg == nil {
		t.Fatalf("after CREATE + RENAME replay, \"renamedavg\" not found; registry = %+v", cat.ListUserAggregates())
	}
	if agg.OID != wantOID {
		t.Errorf("after CREATE + RENAME replay, OID = %d, want %d (renamed aggregate must keep its original OID)", agg.OID, wantOID)
	}
}

// TestAggregateDDLRecoveryReplaysDropAfterCreate confirms that a CREATE
// AGGREGATE followed by a DROP AGGREGATE replays in order, so the
// post-restart registry no longer has the aggregate at all (loop #56 ledger
// resume point: DROP AGGREGATE restart persistence).
func TestAggregateDDLRecoveryReplaysDropAfterCreate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	rt1, err := Open(OpenOptions{DataDir: dir, PoolSlots: 4})
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	const wantOID = uint32(40550)
	if _, _, werr := rt1.WAL.Append(wal.EncodeCreateAggregate("newavg", "_avgstate", "avg_transfn", "avg_finalfn", "", "", "", []string{"int4"}, wantOID, true, false)); werr != nil {
		_ = rt1.Close()
		t.Fatalf("WAL.Append create: %v", werr)
	}
	if _, _, werr := rt1.WAL.Append(wal.EncodeDropAggregate("newavg")); werr != nil {
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
	if agg := findUserAggregate(cat, "newavg"); agg != nil {
		t.Errorf("after CREATE + DROP replay, \"newavg\" = %+v, want nil (dropped)", agg)
	}
}

// TestAggregateDDLRecoveryReplaysOwnerAfterCreate confirms that a CREATE
// AGGREGATE followed by an ALTER AGGREGATE ... OWNER TO replays in order, so
// the post-restart registry reports the new owner (loop #57 ledger resume
// point: ALTER AGGREGATE OWNER TO restart persistence).
func TestAggregateDDLRecoveryReplaysOwnerAfterCreate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	rt1, err := Open(OpenOptions{DataDir: dir, PoolSlots: 4})
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	const wantOID = uint32(40560)
	const wantOwnerOID = uint32(16401)
	if _, _, werr := rt1.WAL.Append(wal.EncodeCreateAggregate("newavg", "_avgstate", "avg_transfn", "avg_finalfn", "", "", "", []string{"int4"}, wantOID, true, false)); werr != nil {
		_ = rt1.Close()
		t.Fatalf("WAL.Append create: %v", werr)
	}
	if _, _, werr := rt1.WAL.Append(wal.EncodeAlterAggregateOwner("newavg", wantOwnerOID)); werr != nil {
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

	cat := rt2.Catalog.(*catalog.InMemory)
	agg := findUserAggregate(cat, "newavg")
	if agg == nil {
		t.Fatalf("after CREATE + OWNER replay, \"newavg\" not found; registry = %+v", cat.ListUserAggregates())
	}
	if agg.OID != wantOID {
		t.Errorf("after CREATE + OWNER replay, OID = %d, want %d (owner change must keep the original OID)", agg.OID, wantOID)
	}
	if got := agg.OwnerOrDefault(); got != wantOwnerOID {
		t.Errorf("after CREATE + OWNER replay, owner = %d, want %d", got, wantOwnerOID)
	}
}

// TestReplayAggregateDDLRecordsHandlesMissingWalDir verifies the recovery
// hook is idempotent when invoked against a missing pg_wal directory (brand
// new initdb).
func TestReplayAggregateDDLRecordsHandlesMissingWalDir(t *testing.T) {
	cat := catalog.NewInMemory()
	if err := replayAggregateDDLRecords(filepath.Join(t.TempDir(), "nonexistent"), cat); err != nil {
		t.Fatalf("replay against missing dir: %v", err)
	}
	if len(cat.ListUserAggregates()) != 0 {
		t.Errorf("no-op replay should not register any aggregate, got %+v", cat.ListUserAggregates())
	}
}
