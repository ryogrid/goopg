package nbtree

import (
	"bytes"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// TestPageHighKeyNoCopyMatchesCopy pins M0122-0012 slice 54. The move-right
// tests of a descent (keyExceedsHighKey, keyAtOrPastHighKey,
// itemOvershootsHighKey) read the high key through pageHighKeyNoCopy, which
// aliases the page; every other caller keeps pageHighKey, which must still
// return a copy that outlives the page bytes. On every page of a blob-format
// and a tuple-format tree both must return the same key and presence, the
// copy must survive the page being overwritten, and the alias must not.
func TestPageHighKeyNoCopyMatchesCopy(t *testing.T) {
	t.Run("blob", func(t *testing.T) {
		bt, pool, cleanup := newTestTree(t)
		defer cleanup()
		for i := 0; i < 3000; i++ {
			ptr := storage.ItemPointer{Block: storage.BlockNumber(i/100 + 1), Offset: uint16(i%100 + 1)}
			if err := bt.Insert(EncodeInt4(int32(i)), ptr); err != nil {
				t.Fatalf("Insert(%d): %v", i, err)
			}
		}
		checkHighKeyNoCopy(t, bt, pool)
	})
	t.Run("tuple", func(t *testing.T) {
		desc := int4x2Desc()
		mgr := storage.NewManager(storage.ManagerConfig{DataDir: t.TempDir()})
		pool, err := storage.NewPool(mgr, storage.PoolConfig{Slots: 64})
		if err != nil {
			t.Fatalf("NewPool: %v", err)
		}
		defer func() { _ = pool.Close(); _ = mgr.Close() }()
		rel := storage.RelFileNode{DBOid: 1, RelOid: 9131, Fork: storage.MainFork}
		bt, err := CreateWithOptions(pool, rel, Options{KeyDesc: desc})
		if err != nil {
			t.Fatalf("CreateWithOptions: %v", err)
		}
		for a := int32(0); a < 100; a++ {
			for b := int32(0); b < 30; b++ {
				tid := storage.ItemPointer{Block: storage.BlockNumber(a + 1), Offset: uint16(b + 1)}
				raw := tup(t, desc.Attrs, [][]byte{int4Val(a), int4Val(b)}, tid)
				if err := bt.Insert(raw, tid); err != nil {
					t.Fatalf("Insert(%d,%d): %v", a, b, err)
				}
			}
		}
		checkHighKeyNoCopy(t, bt, pool)
	})
}

func checkHighKeyNoCopy(t *testing.T, bt *BTree, pool *storage.Pool) {
	t.Helper()
	nblocks, err := pool.NBlocks(bt.rel)
	if err != nil {
		t.Fatalf("NBlocks: %v", err)
	}
	f := bt.format()
	withHK := 0
	for blk := rootStart; blk < nblocks; blk++ {
		slot, err := pool.Pin(storage.BufferTag{Rel: bt.rel, Block: blk})
		if err != nil {
			t.Fatalf("pin block %d: %v", blk, err)
		}
		slot.RLock()
		p := make(storage.Page, storage.BlockSize)
		copy(p, slot.Page())
		slot.RUnlock()
		pool.Unpin(slot)

		cp, okCopy, errCopy := f.pageHighKey(p)
		al, okAlias, errAlias := f.pageHighKeyNoCopy(p)
		if errCopy != nil || errAlias != nil {
			t.Fatalf("block %d: pageHighKey err %v, pageHighKeyNoCopy err %v", blk, errCopy, errAlias)
		}
		if okCopy != okAlias || !bytes.Equal(cp, al) {
			t.Fatalf("block %d: pageHighKey (%v, %x) != pageHighKeyNoCopy (%v, %x)", blk, okCopy, cp, okAlias, al)
		}
		if !okCopy {
			continue
		}
		withHK++
		want := append([]byte(nil), cp...)
		clear(p)
		if !bytes.Equal(cp, want) {
			t.Fatalf("block %d: pageHighKey's key changed with the page — it must be a copy", blk)
		}
		if bytes.Equal(al, want) {
			t.Fatalf("block %d: pageHighKeyNoCopy's key survived the page being cleared — it is not an alias", blk)
		}
	}
	if withHK < 2 {
		t.Fatalf("only %d pages carry a high key over %d blocks — the fixture checked nothing", withHK, nblocks)
	}
}
