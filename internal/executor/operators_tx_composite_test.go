package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// TestUndoCompositeDDLOnRollback verifies that undoEnumDDLFromContext (the
// shared "undo type DDL on ROLLBACK" path) drops composite types that were
// created via CREATE TYPE … AS (...) inside the aborted transaction, while
// leaving composite types that pre-existed the transaction (i.e. were NOT
// recorded in PendingCreatedComposites) intact. DU-002 slice 244.
func TestUndoCompositeDDLOnRollback(t *testing.T) {
	cat := catalog.NewInMemory()

	// `committed` simulates a composite that existed before the transaction:
	// it is registered but NOT tracked in PendingCreatedComposites, so the
	// rollback undo must leave it alone.
	cat.RegisterCompositeTypeWithFields("committed", []catalog.CompositeField{
		{Name: "a", ColType: "int"},
	}, catalog.DefaultDBOid)
	// `intx` simulates CREATE TYPE … AS (...) issued inside the open
	// transaction. The executor records it in PendingCreatedComposites.
	cat.RegisterCompositeTypeWithFields("intx", []catalog.CompositeField{
		{Name: "street", ColType: "text"},
		{Name: "zip", ColType: "int"},
	}, catalog.DefaultDBOid)

	ctx := &Context{
		Catalog:                  cat,
		CurrentDatabaseOid:       catalog.DefaultDBOid,
		PendingCreatedComposites: map[string]bool{"intx": true},
	}

	undoEnumDDLFromContext(ctx)

	if got := cat.LookupCompositeType("intx", catalog.DefaultDBOid); got != nil {
		t.Errorf("ROLLBACK should have dropped composite type created in-tx; LookupCompositeType(intx)=%+v", got)
	}
	if got := cat.LookupCompositeType("committed", catalog.DefaultDBOid); got == nil {
		t.Error("ROLLBACK dropped a pre-existing composite type not created in this transaction")
	}
}

// TestUndoCompositeDDLCaseInsensitive verifies that the recorded
// (lowercased) name still matches the catalog's case-insensitive drop, so a
// `CREATE TYPE public.Addr AS (...)` rolled back leaves no orphan. DU-002
// slice 244.
func TestUndoCompositeDDLCaseInsensitive(t *testing.T) {
	cat := catalog.NewInMemory()
	cat.RegisterCompositeTypeWithFields("Addr", []catalog.CompositeField{
		{Name: "x", ColType: "int"},
	}, catalog.DefaultDBOid)
	ctx := &Context{
		Catalog:                  cat,
		CurrentDatabaseOid:       catalog.DefaultDBOid,
		PendingCreatedComposites: map[string]bool{"addr": true},
	}
	undoEnumDDLFromContext(ctx)
	if got := cat.LookupCompositeType("Addr", catalog.DefaultDBOid); got != nil {
		t.Errorf("case-insensitive rollback drop failed; LookupCompositeType(Addr)=%+v", got)
	}
}
