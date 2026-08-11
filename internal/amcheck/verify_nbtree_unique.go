package amcheck

// verify_nbtree_unique.go ports the `checkunique` tier of upstream amcheck's
// bt_index_check()/bt_index_parent_check() — contrib/amcheck/verify_nbtree.c's
// bt_entry_unique_check / bt_report_duplicate, driven from bt_target_page_check
// (verify_nbtree.c:1650-1815).
//
// What the tier asserts: for a UNIQUE index, no two index entries that compare
// equal on the *key alone* may both point at a heap tuple that is visible under
// the checker's snapshot. A unique index legitimately holds many entries for one
// key — every dead-but-not-yet-vacuumed version of an updated row leaves one —
// so the check is not "duplicate keys exist" but "duplicate keys whose heap
// tuples are simultaneously live", which is exactly what the constraint forbids.
// Detecting it therefore needs heap visibility, supplied by the caller through
// HeapVisibilityFunc (the SQL surface's MVCC snapshot; tests a stub). That is
// the same engine-first/wire-later seam the heapallindexed tier uses.
//
// Faithfulness notes for goopg's key-encoding B-tree:
//
//   - Key-only equality. Upstream must explicitly blank `skey->scantid` before
//     _bt_compare so that heap TID does not break the tie (verify_nbtree.c:1666).
//     goopg's comparator (btree.CompareKeys, and the opclass comparator injected
//     through KeyComparator) is key-only by construction — the engine has no TID
//     tiebreak anywhere — so equality here is already the "keys only" comparison
//     upstream has to arrange for.
//
//   - NULL keys. Upstream skips the check when `skey->anynullkeys`, because a
//     NULL never conflicts with anything under the default NULLS DISTINCT rule.
//     goopg's byte-key B-tree has no per-attribute null bitmap and therefore
//     never stores a NULL-keyed row at all (encodeCompositeBTreeKey's
//     hasNullKey path, design 0119-0004 §3), so no null-bearing entry can reach
//     this walk and the gate needs no port.
//
//   - Posting lists. goopg has them (M0047-0003) and they are expanded here to
//     one entry per heap TID with its posting position retained, mirroring
//     upstream's per-posting bt_entry_unique_check loop and its `posting N`
//     errdetail.
//
//   - Cross-page pairs. Upstream carries its BtreeLastVisibleEntry across the
//     page boundary while walking a level (the last item of one leaf versus the
//     first of its right sibling). This walk follows btpo_next across the whole
//     leaf level with the same carried state, so a duplicate split across two
//     leaves is found identically.
//
// Like the other tiers, a corruption finding is returned, never raised, and the
// first one is conclusive (upstream ereport(ERROR)s on it). M0119-0006.

import (
	"fmt"

	"github.com/goopg/goopg/internal/access/btree"
	"github.com/goopg/goopg/internal/storage"
)

// HeapVisibilityFunc reports whether the heap tuple at tid is visible under the
// checker's snapshot. A TID that cannot be read (missing block, dead line
// pointer) must report false: an unreadable tuple is not a live duplicate, and
// heap damage is the heap checker's (verify_heapam) business, not this tier's.
type HeapVisibilityFunc func(tid storage.ItemPointer) bool

// lastVisibleEntry is upstream's BtreeLastVisibleEntry: the most recent index
// entry seen whose heap tuple was visible, together with the key it carried.
type lastVisibleEntry struct {
	valid   bool
	key     []byte
	blk     storage.BlockNumber
	slot    uint16
	posting int
	tid     storage.ItemPointer
	lsn     storage.LSN
}

// VerifyBtreeUnique runs the checkunique tier over the whole leaf level of the
// index rooted at src's metapage. keyFmt is the index's on-page key format (the
// zero value is the blob format); cmpKeys selects the ordering the equality test
// is performed under (nil ⇒ btree.CompareKeys, the built-in operator-class
// order); visible supplies heap visibility. Callers must invoke it only for an
// index that actually declares UNIQUE — upstream gates on
// `state->indexinfo->ii_Unique` and a non-unique index has nothing to violate.
//
// It returns at most one finding (the first duplicate is conclusive) and a Go
// error only for a genuine read/decode failure of the leaf level, which the SQL
// surface reports rather than silently treating as a clean index.
func VerifyBtreeUnique(src PageSource, indexName string, keyFmt btree.IndexFormat, cmpKeys KeyComparator, visible HeapVisibilityFunc) ([]BtreeReport, error) {
	if src == nil {
		return nil, fmt.Errorf("amcheck: nil page source")
	}
	if visible == nil {
		// Without heap visibility every entry would look invisible and the tier
		// would vacuously pass — report the missing dependency instead.
		return nil, fmt.Errorf("amcheck: checkunique requires a heap visibility function")
	}
	if cmpKeys == nil {
		cmpKeys = btree.CompareKeys
	}

	metaPage, err := src(btree.MetaBlock)
	if err != nil {
		return nil, fmt.Errorf("amcheck: reading metapage: %w", err)
	}
	meta := btree.ParseMeta(metaPage)
	if meta.Root == btree.MetaBlock || meta.Root == storage.InvalidBlockNumber {
		return nil, nil // no key level: vacuously unique
	}
	leftmost, err := leftmostLeafBlock(src, meta.Root, keyFmt)
	if err != nil {
		return nil, err
	}

	var lVis lastVisibleEntry
	visited := make(map[storage.BlockNumber]bool)
	for current := leftmost; current != storage.InvalidBlockNumber; {
		if visited[current] {
			return nil, fmt.Errorf("amcheck: circular leaf sibling chain at block %d", current)
		}
		visited[current] = true

		p, err := src(current)
		if err != nil {
			return nil, fmt.Errorf("amcheck: reading leaf block %d: %w", current, err)
		}
		opaque := btree.ParseOpaque(p)
		if !opaque.IsLeaf() {
			return nil, fmt.Errorf("amcheck: block %d on leaf walk is not a leaf page", current)
		}
		if opaque.IsDeleted() {
			// Unlinked page: no live items, and its type-punned fields must not
			// be decoded (same exemption the per-page tiers apply).
			current = opaque.Next
			continue
		}
		items, err := keyFmt.PageLeafItems(p)
		if err != nil {
			return nil, fmt.Errorf("amcheck: decoding leaf block %d: %w", current, err)
		}
		lsn := storage.MustHeader(p).LSN()

		for _, it := range items {
			// A new key ends the current run of equal keys: nothing before it
			// can conflict with anything after it.
			if lVis.valid && cmpKeys(lVis.key, it.Key) != 0 {
				lVis = lastVisibleEntry{}
			}
			if !visible(it.TID) {
				continue
			}
			if lVis.valid {
				return []BtreeReport{{
					Block: current,
					Msg: fmt.Sprintf("index uniqueness is violated for index \"%s\"",
						indexName),
					Detail: duplicateDetail(&lVis, current, it, lsn),
				}}, nil
			}
			lVis = lastVisibleEntry{
				valid: true, key: it.Key, blk: current,
				slot: it.Slot, posting: it.PostingIndex, tid: it.TID, lsn: lsn,
			}
		}
		current = opaque.Next
	}
	return nil, nil
}

// duplicateDetail renders upstream bt_report_duplicate's errdetail:
//
//	Index tid=(b,o)[ posting N] and[ tid=(b,o)][ posting N] (point to heap
//	tid=(b,o) and tid=(b,o)) page lsn=%X/%X.
//
// The second index tid is emitted only when it differs from the first's
// (block, offset) — i.e. when the two conflicting entries are two postings of
// the *same* line pointer, upstream prints the tid once and distinguishes them
// by posting index alone.
func duplicateDetail(lVis *lastVisibleEntry, blk storage.BlockNumber, it btree.LeafItem, lsn storage.LSN) string {
	posting := func(i int) string {
		if i < 0 {
			return ""
		}
		return fmt.Sprintf(" posting %d", i)
	}
	nitid := ""
	if blk != lVis.blk || it.Slot != lVis.slot {
		nitid = fmt.Sprintf(" tid=(%d,%d)", blk, it.Slot)
	}
	return fmt.Sprintf(
		"Index tid=(%d,%d)%s and%s%s (point to heap tid=(%d,%d) and tid=(%d,%d)) page lsn=%X/%08X.",
		lVis.blk, lVis.slot, posting(lVis.posting),
		nitid, posting(it.PostingIndex),
		lVis.tid.Block, lVis.tid.Offset, it.TID.Block, it.TID.Offset,
		uint32(uint64(lsn)>>32), uint32(uint64(lsn)))
}
