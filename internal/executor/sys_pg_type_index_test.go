package executor

// B2.1a pins: pg_type index maintenance (2703 oid / 2704 typname+nsp) via
// writeTypeHeapRowWithIndexes, including the lazy leaf-root allocation on
// 2704's empty metapage-only bootstrap placeholder.

import (
	"encoding/binary"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/storage"
)

// setupStubEmptyMetapageBtree writes ONLY block 0 — a valid empty btree
// metapage with btm_root=P_NONE — mirroring initdb's makeBtreeRootPage
// placeholder shape (what base/1/2704 looks like on a real cluster).
func setupStubEmptyMetapageBtree(ctx *Context, indexOID uint32) error {
	rel := storage.RelFileNode{
		DBOid:  catalog.DefaultDBOid,
		RelOid: indexOID,
		Fork:   storage.MainFork,
	}
	slot, _, err := ctx.Pool.PinNew(rel)
	if err != nil {
		return err
	}
	slot.Lock()
	if err := storage.InitPage(slot.Page()); err != nil {
		slot.Unlock()
		ctx.Pool.Unpin(slot)
		return err
	}
	if err := writeSysBtreeMetapageInPlace(slot.Page(), 0 /* btm_root=P_NONE */, 0); err != nil {
		slot.Unlock()
		ctx.Pool.Unpin(slot)
		return err
	}
	ctx.Pool.MarkDirty(slot)
	slot.Unlock()
	ctx.Pool.Unpin(slot)
	return nil
}

func TestWriteTypeHeapRowMaintainsPgTypeIndexes(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := setupStubSysBtree(ctx, pgTypeOidIndexOID, nil); err != nil {
		t.Fatalf("stub 2703: %v", err)
	}
	// 2704 ships as an EMPTY metapage-only placeholder — the first insert
	// must lazily allocate the leaf-root.
	if err := setupStubEmptyMetapageBtree(ctx, pgTypeTypnameNspIndexOID); err != nil {
		t.Fatalf("stub 2704: %v", err)
	}

	et := &catalog.EnumType{OID: 16600, Name: "mood", ArrayOID: 16601}
	if err := writeTypeHeapRowWithIndexes(ctx, buildUserPGTypeRowForEnum(et)); err != nil {
		t.Fatalf("write enum row: %v", err)
	}
	if err := writeTypeHeapRowWithIndexes(ctx, buildUserPGTypeRowForEnumArray(et)); err != nil {
		t.Fatalf("write enum array row: %v", err)
	}

	le := binary.LittleEndian

	// 2703: two oid-keyed entries, sorted (16600 < 16601).
	oidTuples := readSysBtreeLeaf(t, ctx, pgTypeOidIndexOID)
	if len(oidTuples) != 2 {
		t.Fatalf("2703: got %d tuples, want 2", len(oidTuples))
	}
	for i, want := range []uint32{16600, 16601} {
		if got := le.Uint32(oidTuples[i][sysIndexTupleHoff : sysIndexTupleHoff+4]); got != want {
			t.Errorf("2703 tuple %d oid = %d, want %d", i, got, want)
		}
	}

	// 2704: lazy root allocated (meta names block 1, level 0), two 80-byte
	// name+nsp entries sorted by name ("_mood" < "mood").
	rel2704 := storage.RelFileNode{DBOid: catalog.DefaultDBOid, RelOid: pgTypeTypnameNspIndexOID, Fork: storage.MainFork}
	rootBlk, level, err := readSysBtreeMeta(ctx, rel2704)
	if err != nil {
		t.Fatalf("2704 meta: %v", err)
	}
	if rootBlk != 1 || level != 0 {
		t.Fatalf("2704 meta = (root %d, level %d), want (1, 0)", rootBlk, level)
	}
	nameTuples := readSysBtreeLeaf(t, ctx, pgTypeTypnameNspIndexOID)
	if len(nameTuples) != 2 {
		t.Fatalf("2704: got %d tuples, want 2", len(nameTuples))
	}
	for i, want := range []string{"_mood", "mood"} {
		if len(nameTuples[i]) != 80 {
			t.Fatalf("2704 tuple %d size = %d, want 80", i, len(nameTuples[i]))
		}
		if got := trimNameDataBytes(nameTuples[i][sysIndexTupleHoff : sysIndexTupleHoff+64]); got != want {
			t.Errorf("2704 tuple %d name = %q, want %q", i, got, want)
		}
		if nsp := le.Uint32(nameTuples[i][sysIndexTupleHoff+64 : sysIndexTupleHoff+68]); nsp != catalog.PublicNamespaceOID {
			t.Errorf("2704 tuple %d nsp = %d, want %d", i, nsp, catalog.PublicNamespaceOID)
		}
	}
}
