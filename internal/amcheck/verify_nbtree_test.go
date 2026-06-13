package amcheck

import (
	"encoding/binary"
	"testing"

	"github.com/goopg/goopg/internal/access/btree"
	"github.com/goopg/goopg/internal/storage"
)

// btSpecial returns the byte offset where the B-tree opaque special area
// begins, mirroring btree.go's btSpecialOffset (BlockSize - SizeOfBTPageOpaque).
func btSpecial() int { return storage.BlockSize - btree.SizeOfBTPageOpaque }

// makeMetaPage builds a metapage (block 0) carrying the given magic and
// version, mirroring btree.writeMeta's layout (payload at SizeOfPageHeaderData:
// magic, version, ...). The remaining metadata fields are left zero — the
// verify tier only inspects magic and version. It self-checks the bytes through
// the real decoder so a future layout change fails loudly here rather than
// silently exercising garbage.
func makeMetaPage(t *testing.T, magic, version uint32) storage.Page {
	t.Helper()
	p := make(storage.Page, storage.BlockSize)
	if err := storage.InitPage(p); err != nil {
		t.Fatalf("InitPage: %v", err)
	}
	off := storage.SizeOfPageHeaderData
	binary.LittleEndian.PutUint32(p[off:off+4], magic)
	binary.LittleEndian.PutUint32(p[off+4:off+8], version)
	if got := btree.ParseMeta(p); got.Magic != magic || got.Version != version {
		t.Fatalf("makeMetaPage self-check: ParseMeta=%+v, want magic=%#x version=%d", got, magic, version)
	}
	return p
}

// makeDataPage builds a non-meta B-tree page with the given opaque flags and
// level, mirroring btree.writeOpaque's layout (Prev,Next,Level,Flags,HighKeyLen
// in the special area). Self-checks through btree.ParseOpaque.
func makeDataPage(t *testing.T, flags uint16, level uint32) storage.Page {
	t.Helper()
	p := make(storage.Page, storage.BlockSize)
	if err := storage.InitPage(p); err != nil {
		t.Fatalf("InitPage: %v", err)
	}
	off := btSpecial()
	binary.LittleEndian.PutUint32(p[off+8:off+12], level) // Level
	binary.LittleEndian.PutUint16(p[off+12:off+14], flags)
	op := btree.ParseOpaque(p)
	if op.Flags != flags || op.Level != level {
		t.Fatalf("makeDataPage self-check: ParseOpaque flags=%#x level=%d, want flags=%#x level=%d", op.Flags, op.Level, flags, level)
	}
	return p
}

func TestVerifyBtreePage_MetaPageClean(t *testing.T) {
	p := makeMetaPage(t, btree.BTreeMagic, btree.BTreeVersion)
	if rs := VerifyBtreePage(p, btree.MetaBlock, "ix"); len(rs) != 0 {
		t.Fatalf("clean metapage reported %d: %+v", len(rs), rs)
	}
}

func TestVerifyBtreePage_MetaPageBadMagic(t *testing.T) {
	p := makeMetaPage(t, btree.BTreeMagic^0xdead, btree.BTreeVersion)
	rs := VerifyBtreePage(p, btree.MetaBlock, "ix")
	if len(rs) != 1 {
		t.Fatalf("bad-magic metapage reported %d, want 1: %+v", len(rs), rs)
	}
	want := `index "ix" meta page is corrupt`
	if rs[0].Msg != want {
		t.Fatalf("msg = %q, want %q", rs[0].Msg, want)
	}
	if rs[0].Block != btree.MetaBlock {
		t.Fatalf("block = %d, want %d", rs[0].Block, btree.MetaBlock)
	}
}

func TestVerifyBtreePage_MetaPageBadVersion(t *testing.T) {
	bad := btree.BTreeVersion + 99
	p := makeMetaPage(t, btree.BTreeMagic, bad)
	rs := VerifyBtreePage(p, btree.MetaBlock, "ix")
	if len(rs) != 1 {
		t.Fatalf("bad-version metapage reported %d, want 1: %+v", len(rs), rs)
	}
	want := "version mismatch in index \"ix\": file version 103, current version 4, minimum supported version 4"
	if rs[0].Msg != want {
		t.Fatalf("msg = %q, want %q", rs[0].Msg, want)
	}
}

// A bad magic masks a bad version: upstream returns after the first conclusive
// metapage problem, so only the magic finding surfaces.
func TestVerifyBtreePage_MetaPageMagicMasksVersion(t *testing.T) {
	p := makeMetaPage(t, btree.BTreeMagic^1, btree.BTreeVersion+7)
	rs := VerifyBtreePage(p, btree.MetaBlock, "ix")
	if len(rs) != 1 || rs[0].Msg != `index "ix" meta page is corrupt` {
		t.Fatalf("want single meta-corrupt finding, got %+v", rs)
	}
}

func TestVerifyBtreePage_LeafLevelZeroClean(t *testing.T) {
	p := makeDataPage(t, btree.BTLeaf, 0)
	if rs := VerifyBtreePage(p, 1, "ix"); len(rs) != 0 {
		t.Fatalf("clean leaf reported %d: %+v", len(rs), rs)
	}
}

func TestVerifyBtreePage_InternalNonZeroClean(t *testing.T) {
	p := makeDataPage(t, 0, 2) // not leaf, level 2
	if rs := VerifyBtreePage(p, 5, "ix"); len(rs) != 0 {
		t.Fatalf("clean internal reported %d: %+v", len(rs), rs)
	}
}

func TestVerifyBtreePage_LeafBadLevel(t *testing.T) {
	p := makeDataPage(t, btree.BTLeaf, 3)
	rs := VerifyBtreePage(p, 7, "ix")
	if len(rs) != 1 {
		t.Fatalf("bad leaf level reported %d, want 1: %+v", len(rs), rs)
	}
	want := `invalid leaf page level 3 for block 7 in index "ix"`
	if rs[0].Msg != want {
		t.Fatalf("msg = %q, want %q", rs[0].Msg, want)
	}
	if rs[0].Block != 7 {
		t.Fatalf("block = %d, want 7", rs[0].Block)
	}
}

func TestVerifyBtreePage_InternalLevelZero(t *testing.T) {
	p := makeDataPage(t, 0, 0) // not leaf, level 0 == corrupt
	rs := VerifyBtreePage(p, 9, "ix")
	if len(rs) != 1 {
		t.Fatalf("internal level-0 reported %d, want 1: %+v", len(rs), rs)
	}
	want := `invalid internal page level 0 for block 9 in index "ix"`
	if rs[0].Msg != want {
		t.Fatalf("msg = %q, want %q", rs[0].Msg, want)
	}
}

// A fully deleted page type-puns its level field, so the level checks are
// suppressed even when leaf-with-nonzero-level would otherwise fire.
func TestVerifyBtreePage_DeletedPageSuppressesLevelCheck(t *testing.T) {
	p := makeDataPage(t, btree.BTLeaf|btree.BTDeleted, 42)
	if rs := VerifyBtreePage(p, 11, "ix"); len(rs) != 0 {
		t.Fatalf("deleted page reported %d, want 0: %+v", len(rs), rs)
	}
}

// A root page that is also a leaf (single-page tree) sits at level 0 and is
// clean — guards against the leaf check misfiring on the common new-tree shape.
func TestVerifyBtreePage_RootLeafClean(t *testing.T) {
	p := makeDataPage(t, btree.BTLeaf|btree.BTRoot, 0)
	if rs := VerifyBtreePage(p, 1, "ix"); len(rs) != 0 {
		t.Fatalf("root+leaf page reported %d, want 0: %+v", len(rs), rs)
	}
}
