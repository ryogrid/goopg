package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// TestUndoRangeTypeDDLOnRollback verifies that undoEnumDDLFromContext (the
// shared "undo type DDL on ROLLBACK" path) drops range types that were
// created via CREATE TYPE … AS RANGE inside the aborted transaction, while
// leaving range types that pre-existed the transaction (i.e. were NOT
// recorded in PendingCreatedRangeTypes) intact. Range types had no
// rollback-undo tracking at all before this test (mirrors
// TestUndoCompositeDDLOnRollback). M0122-0007 4e follow-up.
func TestUndoRangeTypeDDLOnRollback(t *testing.T) {
	cat := catalog.NewInMemory()

	// `committed` simulates a range type that existed before the
	// transaction: it is registered but NOT tracked in
	// PendingCreatedRangeTypes, so the rollback undo must leave it alone.
	if _, err := cat.RegisterRangeType("committed", "int4", "", "", "", catalog.DefaultDBOid); err != nil {
		t.Fatalf("RegisterRangeType(committed): %v", err)
	}
	// `intx` simulates CREATE TYPE … AS RANGE issued inside the open
	// transaction. The executor records it in PendingCreatedRangeTypes.
	if _, err := cat.RegisterRangeType("intx", "int4", "", "", "", catalog.DefaultDBOid); err != nil {
		t.Fatalf("RegisterRangeType(intx): %v", err)
	}

	ctx := &Context{
		Catalog:                  cat,
		CurrentDatabaseOid:       catalog.DefaultDBOid,
		PendingCreatedRangeTypes: map[string]bool{"intx": true},
	}

	undoEnumDDLFromContext(ctx)

	if got, ok := cat.LookupRangeType("intx", catalog.DefaultDBOid); ok {
		t.Errorf("ROLLBACK should have dropped range type created in-tx; LookupRangeType(intx)=%+v", got)
	}
	if _, ok := cat.LookupRangeType("committed", catalog.DefaultDBOid); !ok {
		t.Error("ROLLBACK dropped a pre-existing range type not created in this transaction")
	}
}

// TestUndoRangeTypeDDLCaseInsensitive verifies that the recorded
// (lowercased) name still matches the catalog's case-insensitive drop, so a
// `CREATE TYPE public.Ival AS RANGE (...)` rolled back leaves no orphan.
// M0122-0007 4e follow-up.
func TestUndoRangeTypeDDLCaseInsensitive(t *testing.T) {
	cat := catalog.NewInMemory()
	if _, err := cat.RegisterRangeType("Ival", "int4", "", "", "", catalog.DefaultDBOid); err != nil {
		t.Fatalf("RegisterRangeType(Ival): %v", err)
	}
	ctx := &Context{
		Catalog:                  cat,
		CurrentDatabaseOid:       catalog.DefaultDBOid,
		PendingCreatedRangeTypes: map[string]bool{"ival": true},
	}
	undoEnumDDLFromContext(ctx)
	if got, ok := cat.LookupRangeType("Ival", catalog.DefaultDBOid); ok {
		t.Errorf("case-insensitive rollback drop failed; LookupRangeType(Ival)=%+v", got)
	}
}
