package initdb

import (
	"path/filepath"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/wal"
)

// operatorCatalog is a small test helper: the Runtime.Catalog field is the
// catalog.Catalog interface, but the operator registry lives on the concrete
// *catalog.InMemory implementation (mirrors rangeTypeCatalog).
func operatorCatalog(t *testing.T, rt *Runtime) *catalog.InMemory {
	t.Helper()
	im, ok := rt.Catalog.(*catalog.InMemory)
	if !ok {
		t.Fatalf("Runtime.Catalog is not *catalog.InMemory: %T", rt.Catalog)
	}
	return im
}

// TestOperatorDDLRecoveryReplaysCreate confirms the DU-002
// restart-persistence hook (M0119-0004/M0110-0001, discovered while
// verifying the loop #64 CREATE TYPE ... AS RANGE opclass/collation
// follow-up — see ledger): a CREATE OPERATOR WAL record written by a
// pre-crash run is replayed into the catalog's userOperators registry on
// the post-crash Open path, so pg_operator (pg_dump's dumpOpr) round-trips
// after restart even though the goopg process never wrote a JSON snapshot.
// The OID and every cross-reference field survive the restart.
func TestOperatorDDLRecoveryReplaysCreate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	rt1, err := Open(OpenOptions{DataDir: dir, PoolSlots: 4})
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	payload := wal.CreateOperatorPayload{
		OID: 40900, Schema: "public", Name: "===", LeftType: "int4", RightType: "int4",
		FuncOID: 40901, Owner: 10, CommutatorOID: 40900, RestrictOID: 40902, JoinOID: 40903,
		CanMerge: true, CanHash: true,
	}
	if _, _, werr := rt1.WAL.Append(wal.EncodeCreateOperator(payload)); werr != nil {
		_ = rt1.Close()
		t.Fatalf("WAL.Append create-operator: %v", werr)
	}
	if ferr := rt1.WAL.FlushUpTo(rt1.WAL.WrittenLSN()); ferr != nil {
		_ = rt1.Close()
		t.Fatalf("FlushUpTo: %v", ferr)
	}
	if err := rt1.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}

	// Second Open: WAL replay must surface the operator without any JSON
	// snapshot help.
	rt2, err := Open(OpenOptions{DataDir: dir, PoolSlots: 4})
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer rt2.Close()

	found, ok := operatorCatalog(t, rt2).LookupUserOperator("public", "===", "int4", "int4")
	if !ok {
		t.Fatalf("after WAL replay, operator \"===\"(int4,int4) not found")
	}
	if found.OID != payload.OID || found.FuncOID != payload.FuncOID || found.Owner != payload.Owner ||
		found.CommutatorOID != payload.CommutatorOID || found.RestrictOID != payload.RestrictOID ||
		found.JoinOID != payload.JoinOID || !found.CanMerge || !found.CanHash {
		t.Errorf("after WAL replay, operator = %+v, want matching payload %+v", found, payload)
	}
}

// TestOperatorDDLRecoveryReplaysDropAfterCreate confirms a CREATE followed
// by a DROP cancels out — the registry agrees with the most recent durable
// record, not the first one.
func TestOperatorDDLRecoveryReplaysDropAfterCreate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	rt1, err := Open(OpenOptions{DataDir: dir, PoolSlots: 4})
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	if _, _, werr := rt1.WAL.Append(wal.EncodeCreateOperator(wal.CreateOperatorPayload{
		OID: 40910, Schema: "public", Name: "===", LeftType: "int4", RightType: "int4", FuncOID: 40911,
	})); werr != nil {
		_ = rt1.Close()
		t.Fatalf("WAL.Append create: %v", werr)
	}
	if _, _, werr := rt1.WAL.Append(wal.EncodeDropOperator(40910)); werr != nil {
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

	if found, ok := operatorCatalog(t, rt2).LookupUserOperator("public", "===", "int4", "int4"); ok {
		t.Errorf("after CREATE + DROP replay, operator \"===\"(int4,int4) = %+v, want not found", found)
	}
}

// TestReplayOperatorDDLRecordsHandlesMissingWalDir verifies the recovery
// hook is idempotent when invoked against a missing pg_wal directory (brand
// new initdb).
func TestReplayOperatorDDLRecordsHandlesMissingWalDir(t *testing.T) {
	cat := catalog.NewInMemory()
	if err := replayOperatorDDLRecords(filepath.Join(t.TempDir(), "nonexistent"), cat); err != nil {
		t.Fatalf("replay against missing dir: %v", err)
	}
	if len(cat.ListUserOperators()) != 0 {
		t.Errorf("no-op replay should not register anything, got %+v", cat.ListUserOperators())
	}
}

// TestReplayOperatorDDLRecordsHandlesNilCatalog verifies the recovery hook
// tolerates a nil catalog (mirrors the nil-registry guard the range type DDL
// recovery driver has for embedded test setups).
func TestReplayOperatorDDLRecordsHandlesNilCatalog(t *testing.T) {
	if err := replayOperatorDDLRecords(filepath.Join(t.TempDir(), "nonexistent"), nil); err != nil {
		t.Fatalf("replay with nil catalog: %v", err)
	}
}
