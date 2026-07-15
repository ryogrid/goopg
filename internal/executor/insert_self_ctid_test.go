package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/storage"
)

// TestFreshInsertTupleIsSelfPointing verifies the PG t_ctid convention adopted
// in markHeapInsertDirty: a freshly inserted heap tuple stores a self-pointing
// t_ctid {block, lineSlot} rather than goopg's legacy {InvalidBlockNumber, 0}
// sentinel. Covers both a plain row and a NULL-bearing row (null-bitmap tuple),
// since t_ctid sits in the fixed header ahead of the bitmap. This is the
// primary-side half of record-content parity: goopg's stored page now matches
// what a PG xl_heap_insert record reconstructs on replay (which carries no
// t_ctid and rebuilds it self-pointing).
func TestFreshInsertTupleIsSelfPointing(t *testing.T) {
	ctx, cat, cleanup := newHOTFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE items (id int, label text)"); err != nil {
		t.Fatal(err)
	}
	if err := runDDL(t, ctx, "INSERT INTO items VALUES (1, 'a')"); err != nil {
		t.Fatal(err)
	}
	// NULL label → HEAP_HASNULL + a null bitmap between the fixed header and data.
	if err := runDDL(t, ctx, "INSERT INTO items VALUES (2, NULL)"); err != nil {
		t.Fatal(err)
	}

	tbl, _ := cat.LookupTable(parser.ObjectName{Name: "items"})
	heapRel := ctx.Catalog.RelFileNode(tbl)
	page := readPageViaPool(t, ctx.Pool, heapRel, 0)

	for slot := uint16(1); slot <= 2; slot++ {
		tup, err := storage.PageGetHeapTuple(page, slot)
		if err != nil {
			t.Fatalf("PageGetHeapTuple(slot=%d): %v", slot, err)
		}
		want := storage.ItemPointer{Block: 0, Offset: slot}
		if tup.Header.CTID != want {
			t.Fatalf("slot %d t_ctid = %+v, want self-pointing %+v", slot, tup.Header.CTID, want)
		}
		// It must NOT be the legacy sentinel any more.
		if tup.Header.CTID.Block == storage.InvalidBlockNumber {
			t.Fatalf("slot %d still uses the legacy {Invalid,0} sentinel", slot)
		}
	}
}
