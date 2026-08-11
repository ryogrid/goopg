package amcheck

// Coverage for the checkunique tier's POSTING-LIST arm (verify_nbtree_unique.go's
// `it.PostingIndex` handling, fed by btree.IndexFormat.PageLeafItems).
//
// A deduplicated leaf item holds ONE key over many heap TIDs, so a duplicate can
// live entirely inside a single line pointer — upstream's bt_entry_unique_check
// loops over the posting list precisely for that case and bt_report_duplicate
// prints ` posting N` instead of a second `tid=` when both conflicting entries
// share the line pointer (contrib/amcheck/verify_nbtree.c:1690-1730, 1830-1870).
// The arm shipped with the tier but was covered by no test: goopg deduplicates
// only under specific write churn, so driving a real tree cannot be relied on to
// produce a posting list at a known place. These fixtures build the item shape
// directly through btree.IndexFormat.PGBTPostingRaw — the same encoder the tree
// itself writes with — which is also why the encoder is exported.
//
// Both key formats are exercised, because a posting list is where they differ
// most: a blob posting shares one key slice across the run, while a tuple-format
// posting carries the heap TID INSIDE each expanded key, so the tier's equality
// test has to be the TID-blind CompareKeyAttrs (which is what
// executor.btIndexCheckUnique injects for a descriptor-bearing index). M0119-0006.

import (
	"encoding/binary"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/access/btree"
	"github.com/goopg/goopg/internal/storage"
)

// postingTupleDesc / postingTupleKey mirror the one-column int4 key descriptor
// and tuple former the external-package tuple-format test uses (tupleFmtDesc /
// tupleFmtKey in verify_nbtree_tupleformat_test.go, package amcheck_test — this
// file is in-package and cannot see them).
func postingTupleDesc() *btree.PGIndexKeyDesc {
	attr := btree.PGKeyAttr{PGIndexAttr: btree.PGIndexAttr{Len: 4, ByVal: true, AlignBy: 4, Storage: 'p'}}
	attr.Compare = btree.PGCompareInt4
	return &btree.PGIndexKeyDesc{Attrs: []btree.PGKeyAttr{attr}}
}

func postingTupleKey(t *testing.T, desc *btree.PGIndexKeyDesc, v int32, tid storage.ItemPointer) []byte {
	t.Helper()
	val := make([]byte, 4)
	binary.LittleEndian.PutUint32(val, uint32(v))
	raw, err := btree.FormPGIndexTuple(desc.Physical(), [][]byte{val}, []bool{false}, tid)
	if err != nil {
		t.Fatalf("FormPGIndexTuple: %v", err)
	}
	return raw
}

// pl is a posting-list leaf entry for makeLeafPageMixed: one key over several
// heap TIDs (upstream requires at least two).
type pl struct {
	key  []byte
	tids []storage.ItemPointer
}

// htid spells a heap ItemPointer compactly.
func htid(block storage.BlockNumber, offset uint16) storage.ItemPointer {
	return storage.ItemPointer{Block: block, Offset: offset}
}

// makeLeafPageMixed builds a leaf page whose items are given in slot order,
// mixing plain (`le`) and posting-list (`pl`) items, under `fm`'s layout. It
// self-checks the page through fm.PageLeafItems — the reader the tier uses — so a
// layout change fails here rather than as a silent under-report downstream.
func makeLeafPageMixed(t *testing.T, fm btree.IndexFormat, next storage.BlockNumber, items ...any) storage.Page {
	t.Helper()
	p := make(storage.Page, storage.BlockSize)
	pgInitTestPage(t, p, next, 0, btree.BTLeaf)
	wantEntries := 0
	for i, it := range items {
		var raw []byte
		switch e := it.(type) {
		case le:
			raw = btree.PGBTItemRaw(e.key, htid(e.block, e.offset))
			wantEntries++
		case pl:
			if len(e.tids) < 2 {
				t.Fatalf("item %d: a posting list needs at least 2 TIDs, got %d", i, len(e.tids))
			}
			raw = fm.PGBTPostingRaw(e.key, e.tids)
			wantEntries += len(e.tids)
		default:
			t.Fatalf("item %d: unsupported fixture type %T", i, it)
		}
		if _, err := storage.PageAddItemRaw(p, raw); err != nil {
			t.Fatalf("PageAddItemRaw[%d]: %v", i, err)
		}
	}
	got, err := fm.PageLeafItems(p)
	if err != nil {
		t.Fatalf("makeLeafPageMixed PageLeafItems self-check: %v", err)
	}
	if len(got) != wantEntries {
		t.Fatalf("makeLeafPageMixed self-check: page yields %d entries, want %d", len(got), wantEntries)
	}
	return p
}

// Two TIDs of ONE posting list, both visible, are a uniqueness violation — the
// case that cannot occur without deduplication and that a per-line-pointer walk
// would miss entirely. The errdetail must name the shared index tid once and
// distinguish the two entries by posting index, which is bt_report_duplicate's
// spelling when both entries sit at the same (block, offset).
func TestVerifyBtreeUnique_PostingListBothVisible(t *testing.T) {
	pages := map[storage.BlockNumber]storage.Page{
		btree.MetaBlock: makeMetaWithRoot(t, 1),
		1: makeLeafPageMixed(t, blobFmt, none,
			pl{k(1), []storage.ItemPointer{htid(10, 1), htid(10, 7)}},
			le{k(3), 10, 3}),
	}
	got, err := VerifyBtreeUnique(mapSource(pages), "uidx", blobFmt, nil, allVisible())
	if err != nil {
		t.Fatalf("VerifyBtreeUnique: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d findings, want 1: %+v", len(got), got)
	}
	if want := `index uniqueness is violated for index "uidx"`; got[0].Msg != want {
		t.Fatalf("Msg = %q, want %q", got[0].Msg, want)
	}
	// Upstream prints the index tid once and the two posting positions; the
	// second `tid=` appears only when the entries are at different line pointers.
	if want := "Index tid=(1,1) posting 0 and posting 1 (point to heap tid=(10,1) and tid=(10,7))"; !strings.Contains(got[0].Detail, want) {
		t.Fatalf("Detail = %q, want it to contain %q", got[0].Detail, want)
	}
}

// The same posting list is CLEAN when only one of its heap TIDs is visible: a
// deduplicated entry accumulates dead row versions exactly as separate entries
// do, so the tier must judge the posting list by visibility, not by length.
func TestVerifyBtreeUnique_PostingListOneVisibleClean(t *testing.T) {
	pages := map[storage.BlockNumber]storage.Page{
		btree.MetaBlock: makeMetaWithRoot(t, 1),
		1: makeLeafPageMixed(t, blobFmt, none,
			pl{k(1), []storage.ItemPointer{htid(10, 1), htid(10, 7), htid(10, 9)}}),
	}
	got, err := VerifyBtreeUnique(mapSource(pages), "uidx", blobFmt, nil,
		visibleTIDs(htid(10, 7)))
	if err != nil {
		t.Fatalf("VerifyBtreeUnique: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("dead-version posting list reported %d findings: %+v", len(got), got)
	}
}

// A posting list conflicting with the PLAIN item that follows it: the surviving
// posting entry versus a separate line pointer holding the same key. Here the
// two entries differ in line pointer, so the detail carries the second `tid=`
// AND a posting index on the left side only — the mixed rendering neither of the
// two pure cases produces.
func TestVerifyBtreeUnique_PostingThenPlainDuplicate(t *testing.T) {
	pages := map[storage.BlockNumber]storage.Page{
		btree.MetaBlock: makeMetaWithRoot(t, 1),
		1: makeLeafPageMixed(t, blobFmt, none,
			pl{k(1), []storage.ItemPointer{htid(10, 1), htid(10, 7)}},
			le{k(1), 10, 9}),
	}
	got, err := VerifyBtreeUnique(mapSource(pages), "uidx", blobFmt, nil,
		visibleTIDs(htid(10, 7), htid(10, 9)))
	if err != nil {
		t.Fatalf("VerifyBtreeUnique: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d findings, want 1: %+v", len(got), got)
	}
	if want := "Index tid=(1,1) posting 1 and tid=(1,2) (point to heap tid=(10,7) and tid=(10,9))"; !strings.Contains(got[0].Detail, want) {
		t.Fatalf("Detail = %q, want it to contain %q", got[0].Detail, want)
	}
}

// Posting lists over DISTINCT keys never conflict however visible the heap is:
// the expansion must not merge the runs of two neighbouring posting items. This
// is the no-false-positive gate for the arm — a real deduplicated index is
// mostly adjacent posting lists.
func TestVerifyBtreeUnique_AdjacentPostingListsDistinctKeysClean(t *testing.T) {
	pages := map[storage.BlockNumber]storage.Page{
		btree.MetaBlock: makeMetaWithRoot(t, 1),
		1: makeLeafPageMixed(t, blobFmt, none,
			pl{k(1), []storage.ItemPointer{htid(10, 1), htid(10, 2)}},
			pl{k(2), []storage.ItemPointer{htid(10, 3), htid(10, 4)}}),
	}
	got, err := VerifyBtreeUnique(mapSource(pages), "uidx", blobFmt, nil,
		visibleTIDs(htid(10, 2), htid(10, 3)))
	if err != nil {
		t.Fatalf("VerifyBtreeUnique: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("distinct-key posting lists reported %d findings: %+v", len(got), got)
	}
}

// The tuple format is where a posting list is genuinely harder: each expanded
// entry's key carries its OWN heap TID, so the tier only sees a duplicate under
// the TID-blind CompareKeyAttrs that executor.btIndexCheckUnique injects for a
// descriptor-bearing index. Under the bytewise default the very same page must
// report nothing — which is the under-report this test exists to pin, and the
// proof that the comparator argument is load-bearing here.
func TestVerifyBtreeUnique_PostingListTupleFormat(t *testing.T) {
	desc := postingTupleDesc()
	tupleFmt := btree.IndexFormatFor(desc)
	tids := []storage.ItemPointer{htid(10, 1), htid(10, 7)}
	pages := map[storage.BlockNumber]storage.Page{
		btree.MetaBlock: makeMetaWithRoot(t, 1),
		1: makeLeafPageMixed(t, tupleFmt, none,
			pl{postingTupleKey(t, desc, 42, tids[0]), tids}),
	}
	got, err := VerifyBtreeUnique(mapSource(pages), "uidx", tupleFmt,
		tupleFmt.CompareKeyAttrs, allVisible())
	if err != nil {
		t.Fatalf("VerifyBtreeUnique: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d findings, want 1 under CompareKeyAttrs: %+v", len(got), got)
	}
	if want := "posting 0 and posting 1 (point to heap tid=(10,1) and tid=(10,7))"; !strings.Contains(got[0].Detail, want) {
		t.Fatalf("Detail = %q, want it to contain %q", got[0].Detail, want)
	}
	blind, err := VerifyBtreeUnique(mapSource(pages), "uidx", tupleFmt, nil, allVisible())
	if err != nil {
		t.Fatalf("VerifyBtreeUnique (bytewise): %v", err)
	}
	if len(blind) != 0 {
		t.Fatalf("bytewise comparator reported %d findings on tuple keys; the TID inside the key should hide the duplicate: %+v", len(blind), blind)
	}
}
