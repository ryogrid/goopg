package amcheck_test

import (
	"encoding/binary"
	"testing"

	"github.com/goopg/goopg/internal/access/btree"
	"github.com/goopg/goopg/internal/amcheck"
	"github.com/goopg/goopg/internal/storage"
)

// M0130-S11.4 slice 3b-2c-ii-B2-b-iii guard.
//
// Until this slice every amcheck tier decoded pages with the blob format,
// named once as the package-level `blobIndexFormat`. That is a CLAIM about the
// index, not a property of the bytes: an index opened with a key descriptor
// stores each item as a whole nbtree index tuple, and a blob parse strips the
// header (t_info, t_tid) and hands the tiers the payload as if it were the key.
// The tiers now take a btree.IndexFormat from their caller — internal/executor's
// bt_index_check resolves it from ctx.pgIndexKeyDesc(idx), the same catalog
// entry the sibling per-index property (the opclass comparator) comes from.
//
// This test pins that the parameter is load-bearing rather than decorative: it
// builds a REAL tuple-format tree with splits and asserts (a) the leaf-walk
// collector under the resolved format returns whole index tuples that round-trip
// through the tuple accessors, (b) the same bytes under the blob format return
// something strictly different, and (c) the item-order tier finds a
// freshly-built tuple-format tree clean when it is given the format and the
// descriptor's comparator.
func TestAmcheckTiersFollowTheIndexFormat(t *testing.T) {
	desc := tupleFmtDesc()
	dir := t.TempDir()
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: dir})
	pool, err := storage.NewPool(mgr, storage.PoolConfig{Slots: 64})
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	defer func() { _ = pool.Close(); _ = mgr.Close() }()

	rel := storage.RelFileNode{DBOid: 1, RelOid: 9301, Fork: storage.MainFork}
	bt, err := btree.CreateWithOptions(pool, rel, btree.Options{KeyDesc: desc})
	if err != nil {
		t.Fatalf("CreateWithOptions: %v", err)
	}

	// Enough inserts to split, so the tree has an internal level and
	// non-rightmost pages carrying high keys.
	const n = 400
	tids := make(map[int32]storage.ItemPointer, n)
	for i := range n {
		v := int32((i*397)%n) - n/2
		tid := storage.ItemPointer{Block: storage.BlockNumber(i/50 + 1), Offset: uint16(i%50 + 1)}
		tids[v] = tid
		if err := bt.Insert(tupleFmtKey(t, desc, v, tid), tid); err != nil {
			t.Fatalf("Insert(%d): %v", v, err)
		}
	}

	src := realPageSource(t, pool, rel)
	tupleFmt := btree.IndexFormatFor(desc)

	// (a) The leaf walk under the resolved format yields whole index tuples.
	entries, err := amcheck.CollectBtreeLeafEntries(src, tupleFmt)
	if err != nil {
		t.Fatalf("CollectBtreeLeafEntries(tuple format): %v", err)
	}
	if len(entries) != n {
		t.Fatalf("tuple-format leaf walk collected %d entries, want %d", len(entries), n)
	}
	for _, e := range entries {
		if got := btree.PGIndexTupleSize(e.Key); got != len(e.Key) {
			t.Fatalf("leaf key is not a whole index tuple: t_info size %d, len %d", got, len(e.Key))
		}
		if got := btree.PGIndexTupleTID(e.Key); got != e.TID {
			t.Fatalf("tuple TID %v disagrees with the reported entry TID %v", got, e.TID)
		}
	}

	// (b) The SAME bytes under the blob format decode to something else — the
	// header-stripped payload. If both formats agreed, the parameter would be
	// decorative and the flip (B2-c) would silently verify under the wrong
	// decoder.
	blobEntries, err := amcheck.CollectBtreeLeafEntries(src, btree.IndexFormat{})
	if err != nil {
		t.Fatalf("CollectBtreeLeafEntries(blob format): %v", err)
	}
	if len(blobEntries) != len(entries) {
		t.Fatalf("blob walk collected %d entries, want %d", len(blobEntries), len(entries))
	}
	same := 0
	for i := range entries {
		if string(entries[i].Key) == string(blobEntries[i].Key) {
			same++
		}
	}
	if same != 0 {
		t.Fatalf("%d of %d leaf keys decoded identically under both formats — the format argument is not load-bearing", same, len(entries))
	}

	// (c) A freshly built tuple-format tree is clean under its own format and
	// its descriptor's comparator (nil would mean btree.CompareKeys, which is
	// the wrong order over tuple bytes — the comparator and the format are the
	// same per-index catalog resolution and must be supplied together).
	cmp := func(a, b []byte) int {
		c, cerr := btree.ComparePGIndexTuples(desc, a, b)
		if cerr != nil {
			t.Fatalf("ComparePGIndexTuples: %v", cerr)
		}
		return c
	}
	nblocks, err := pool.NBlocks(rel)
	if err != nil {
		t.Fatalf("NBlocks: %v", err)
	}
	for blk := range nblocks {
		p, perr := src(blk)
		if perr != nil {
			t.Fatalf("read block %d: %v", blk, perr)
		}
		if reps := amcheck.VerifyBtreeItemOrderCmp(p, blk, "ix_tuplefmt", tupleFmt, cmp); len(reps) != 0 {
			t.Fatalf("tuple-format item-order tier reported %q on a clean tree at block %d", reps[0].Msg, blk)
		}
	}
}

// tupleFmtDesc is a one-column int4 key descriptor with the int4 opclass
// comparator — the shape internal/executor's buildPGIndexKeyDesc produces for a
// single-int4-column index.
func tupleFmtDesc() *btree.PGIndexKeyDesc {
	attr := btree.PGKeyAttr{PGIndexAttr: btree.PGIndexAttr{Len: 4, ByVal: true, AlignBy: 4, Storage: 'p'}}
	attr.Compare = btree.PGCompareInt4
	return &btree.PGIndexKeyDesc{Attrs: []btree.PGKeyAttr{attr}}
}

// tupleFmtKey forms the whole nbtree index tuple that a tuple-format index
// stores as its item key (PG's index_form_tuple).
func tupleFmtKey(t *testing.T, desc *btree.PGIndexKeyDesc, v int32, tid storage.ItemPointer) []byte {
	t.Helper()
	val := make([]byte, 4)
	binary.LittleEndian.PutUint32(val, uint32(v))
	raw, err := btree.FormPGIndexTuple(desc.Physical(), [][]byte{val}, []bool{false}, tid)
	if err != nil {
		t.Fatalf("FormPGIndexTuple: %v", err)
	}
	return raw
}
