package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/access/transam"
	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/storage"
)

// TestCatalogUpdateExtendsWhenLastPageIsFull pins M0146-0078. A catalog
// UPDATE whose new version fits neither the old tuple's page nor the
// relation's last page must extend the relation. updateHeapRowCanonicalPG
// tried the last block, got ErrNoSpaceInPage, and retried, but the retry
// picked the same full last block again. It then failed `catalog update:
// freshly extended page did not accept tuple` without having extended
// anything. A repeated CREATE OR REPLACE FUNCTION hit this as soon as
// pg_proc's last page filled up.
func TestCatalogUpdateExtendsWhenLastPageIsFull(t *testing.T) {
	dir := t.TempDir()
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: dir})
	defer mgr.Close()
	pool, err := storage.NewPool(mgr, storage.PoolConfig{Slots: 8})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	rel := storage.RelFileNode{DBOid: 1, RelOid: 1255, Fork: storage.MainFork}
	cols := []catalog.Column{{Name: "payload", Type: catalog.Type{Name: "text"}}}
	ctx := &Context{Pool: pool, Tx: transam.Transaction{XID: 42}}

	// Two pages, each holding one 5000-byte tuple: neither has room for
	// another. Block 0 holds the version being updated; block 1 is the last
	// block, which the update tries first.
	var oldOff uint16
	for want := storage.BlockNumber(0); want < 2; want++ {
		tup, _, err := buildCatalogPGHeapTuple(ctx, cols, Row{NewStringDatum(strings.Repeat("o", 5000))})
		if err != nil {
			t.Fatal(err)
		}
		slot, blk, err := pool.PinNew(rel)
		if err != nil {
			t.Fatal(err)
		}
		if blk != want {
			t.Fatalf("seed block = %d, want %d", blk, want)
		}
		slot.Lock()
		if err := storage.InitPage(slot.Page()); err != nil {
			t.Fatal(err)
		}
		off, err := storage.PageAddHeapTuple(slot.Page(), tup)
		if err != nil {
			t.Fatal(err)
		}
		if blk == 0 {
			oldOff = off
		}
		pool.MarkDirty(slot)
		slot.Unlock()
		pool.Unpin(slot)
	}

	newTID, err := updateHeapRowCanonicalPG(ctx, rel, cols, storage.ItemPointer{Block: 0, Offset: oldOff},
		Row{NewStringDatum(strings.Repeat("n", 5000))})
	if err != nil {
		t.Fatalf("updateHeapRowCanonicalPG: %v", err)
	}
	if newTID.Block != 2 {
		t.Fatalf("new version landed on block %d, want the freshly extended block 2", newTID.Block)
	}
	if n, err := pool.NBlocks(rel); err != nil || n != 3 {
		t.Fatalf("relation has %d blocks (err %v), want 3", n, err)
	}
}
