package amcheck

import (
	"fmt"

	"github.com/goopg/goopg/internal/access/nbtree"
	"github.com/goopg/goopg/internal/storage"
)

// VerifyBtreeRootDescend is amcheck's `rootdescend` tier: every leaf entry must
// be reachable by an INDEPENDENT search that starts at the root, exactly as
// upstream's bt_rootdescend does (contrib/amcheck/verify_nbtree.c:3029+, driven
// from bt_target_page_check at :1382).
//
// The property is not redundant with the structural tiers. Those verify each
// page against its neighbours — key order within a page, sibling links, parent
// downlinks. This one verifies the tree as a SEARCH STRUCTURE: an entry can sit
// on a correctly-ordered leaf, under a correctly-linked parent, and still be
// unreachable because a separator key on the path routes a searcher down the
// wrong subtree. Upstream's own comment for the tier is that it catches
// "inconsistencies that are only detectable by searching from the root".
//
// Upstream asserts `key->heapkeyspace && key->scantid != NULL` before running,
// and errors outright when the index is not version 4, because the search must
// identify ONE entry rather than the first of a duplicate group. goopg's two key
// formats map onto that gate exactly, and the caller is responsible for it — see
// RootDescendSupported. Running this on a blob-format index would report
// findings on a healthy one, since every duplicate of a key is byte-identical
// there and no probe can distinguish them.
//
// Read-only: it walks page COPIES from the PageSource, like every other tier
// here, so a concurrent split cannot desynchronise the view mid-descent.
func VerifyBtreeRootDescend(src PageSource, root storage.BlockNumber, indexName string, keyFmt nbtree.IndexFormat) ([]BtreeReport, error) {
	var out []BtreeReport
	leaves, err := CollectBtreeLeafEntries(src, keyFmt)
	if err != nil {
		return nil, err
	}
	for _, e := range leaves {
		found, ferr := rootDescendFinds(src, root, keyFmt, e)
		if ferr != nil {
			return nil, ferr
		}
		if found {
			continue
		}
		// Upstream's message verbatim (verify_nbtree.c:1393-1399), with its
		// errdetail_internal's index/heap tid pair.
		out = append(out, BtreeReport{
			Block: e.TID.Block,
			Msg:   fmt.Sprintf("could not find tuple using search from root page in index %q", indexName),
			Detail: fmt.Sprintf("Index tid points to heap tid=(%d,%d).",
				e.TID.Block, e.TID.Offset),
		})
	}
	return out, nil
}

// RootDescendSupported reports whether the tier can run against an index in
// this key format.
//
// The tuple format puts the heap TID inside the key image, which is
// heapkeyspace's final tiebreaker, so a stored entry's key identifies that one
// entry and a descent can confirm exactly it. The blob format carries no TID in
// the key at all (the entry's TID travels beside it in the btree item), so two
// duplicates are byte-identical keys and "did the search find THIS entry" is not
// a question the key can ask. That is the same distinction upstream draws with
// heapkeyspace, and the caller must raise upstream's 0A000 rather than run.
func RootDescendSupported(keyFmt nbtree.IndexFormat) bool {
	return keyFmt.KeyDesc() != nil
}

// rootDescendFinds performs one independent descent from the root for a single
// leaf entry, mirroring upstream's `_bt_search` + `_bt_binsrch_insert` +
// `_bt_compare(...) == 0` sequence: descend to the leaf the entry's key routes
// to, then confirm the entry is actually on it.
//
// The descent compares with the FULL key (keyFmt.Compare, which includes the
// TID tiebreaker) rather than key attributes alone, because that is the
// ordering the tree was built under and therefore the ordering a searcher
// follows. Comparing by attributes here would route to the first leaf of a
// duplicate group and then fail to find entries that legitimately live further
// right — a false finding on a healthy index.
//
// A read error propagates. A structurally broken page (undecodable downlinks,
// an internal page with no children) ends the descent as "not found": the
// per-page tier already reports that damage, and treating it as reachable here
// would suppress a real finding.
func rootDescendFinds(src PageSource, root storage.BlockNumber, keyFmt nbtree.IndexFormat, want nbtree.LeafEntry) (bool, error) {
	seen := make(map[storage.BlockNumber]bool)
	blk := root
	for {
		if seen[blk] {
			// A downlink cycle: corrupt, and not reachable in any useful sense.
			return false, nil
		}
		seen[blk] = true
		p, err := src(blk)
		if err != nil {
			return false, err
		}
		if nbtree.ParseOpaque(p).IsLeaf() {
			return leafHasEntry(p, keyFmt, want)
		}
		dls, derr := keyFmt.PageDownlinks(p)
		if derr != nil || len(dls) == 0 {
			return false, nil
		}
		// Route as a searcher does: the child is the LAST downlink whose
		// separator key is <= the search key. Slot 1 of an internal page is the
		// negative-infinity downlink (its key is not a real bound), so it is the
		// floor when nothing else matches.
		next := dls[0].Child
		for i := 1; i < len(dls); i++ {
			if keyFmt.Compare(dls[i].Key, want.Key) <= 0 {
				next = dls[i].Child
				continue
			}
			break
		}
		blk = next
	}
}

// leafHasEntry reports whether the leaf page carries the exact entry — same key
// image AND same heap TID.
//
// The TID is compared explicitly even though the tuple format already encodes it
// in the key, because CollectBtreeLeafEntries expands a posting list into one
// entry per TID while the stored key image is shared: comparing keys alone would
// call every member of a posting list found as soon as any one of them was.
func leafHasEntry(p storage.Page, keyFmt nbtree.IndexFormat, want nbtree.LeafEntry) (bool, error) {
	entries, err := keyFmt.PageLeafEntries(p)
	if err != nil {
		// An undecodable leaf is damage the per-page tier reports; it is not
		// evidence that this entry is reachable.
		return false, nil
	}
	for _, e := range entries {
		if e.TID == want.TID && keyFmt.Compare(e.Key, want.Key) == 0 {
			return true, nil
		}
	}
	return false, nil
}
