package amcheck

import (
	"fmt"

	"github.com/goopg/goopg/internal/access/nbtree"
	"github.com/goopg/goopg/internal/storage"
)

// This file ports the heap-side relation walk of the heapallindexed tier — the
// "probe phase" driver of upstream amcheck's bt_index_check(heapallindexed =>
// true): the table_index_build_scan that visits every live heap tuple and, via
// bt_tuple_present_callback, forms the IndexTuple a fresh CREATE INDEX would emit
// and probes the Bloom filter with it (postgres/contrib/amcheck/verify_nbtree.c:
// 2776-2839, heapallindexedCallback / bt_tuple_present_callback).
//
// It is the heap-side counterpart to the index-side fingerprint driver
// (CollectBtreeLeafEntries in heapallindexed_relation.go): that walks the index
// leaf level to produce the fingerprint set; this walks the heap to produce the
// probe set. With both producers in the engine, the heapallindexed SQL slice
// (docs/design/0110-0007) reduces to a thin adapter — fill one PageSource from
// the index's smgr and one from the heap's, supply the TupleDesc-coupled entry
// former, and compose through VerifyBtreeHeapAllIndexedRelation /
// VerifyBtreeHeapAllIndexed.
//
// Scope boundary, mirroring where upstream draws the line in
// table_index_build_scan: deciding whether a given heap tuple should be indexed
// (the visibility / HOT-chain-root determination, which needs an MVCC snapshot
// and clog) and forming its would-be index entry (index_form_tuple, which needs
// the index's key columns, expressions, and collations — i.e. the TupleDesc) are
// catalog- and execution-context coupled, so they stay at the wire layer and are
// injected here as a single HeapEntryFormer callback. The callback is upstream's
// per-tuple callback by another name: it returns the formed entry plus whether to
// include it, exactly as heapallindexedCallback either fingerprint-probes the
// tuple or skips it. What this file ports is precisely the part that is a
// deterministic function of the heap's page bytes plus the block range: the block
// loop, the per-page LP_NORMAL line-pointer iteration, and the (block, offset)
// TID each tuple carries.

// HeapEntryFormer forms the B-tree leaf entry that a live heap tuple at the given
// heap TID would produce in a fresh CREATE INDEX — upstream's index_form_tuple +
// the normalization the probe side applies — or returns include=false to skip the
// tuple (it is dead, not visible to the build snapshot, or a non-root HOT-chain
// member that contributes no index entry). It is TupleDesc- and clog-coupled, so
// the wire layer supplies it; tests supply a deterministic stub.
//
// tid is the tuple's heap location (the block being scanned plus the 1-based
// line-pointer offset); tuple is the raw on-disk tuple bytes
// (page[lp_off : lp_off+lp_len], header included). A former that cannot decode a
// tuple it was asked to form returns an error, which CollectHeapIndexEntries
// surfaces rather than silently dropping the entry — a truncated probe set would
// manufacture spurious "lacks matching index tuple" reports (the heapallindexed
// soundness invariant: the probe set must be complete or the check is unsound).
type HeapEntryFormer func(tid storage.ItemPointer, tuple []byte) (entry nbtree.LeafEntry, include bool, err error)

// CollectHeapIndexEntries walks blocks [0, nblocks-1] of a heap relation and
// returns the B-tree leaf entry each live heap tuple would form, in
// (block, offset) order — the probe set the heapallindexed tier feeds to
// VerifyBtreeHeapAllIndexed. src yields each block's raw 8 KiB bytes by number
// (the SQL surface fills it from the buffer manager, tests from a map); form is
// the wire-layer callback that decides per-tuple inclusion and forms the entry
// (see HeapEntryFormer).
//
// It mirrors table_index_build_scan's heap walk: for each block it iterates the
// LP_NORMAL line pointers (unused / dead / redirect items carry no tuple body and
// are skipped — a redirect's target is itself an LP_NORMAL item that the loop
// reaches on its own offset, so following redirects here would double-count), and
// hands each tuple's bytes + TID to form. An empty relation (nblocks == 0) yields
// no entries. A brand-new (all-zero) page contributes nothing, exactly as the
// per-page heap checker short-circuits it.
//
// Like CollectBtreeLeafEntries it returns an error rather than a truncated set on
// any structural problem: a read error, an unparseable page header, a line
// pointer whose offset/length falls outside the page, or a former error. A
// truncated probe set would silently mask missing index entries (the entries that
// would have been probed are simply never checked), so completeness is enforced
// by surfacing the error to the SQL surface.
func CollectHeapIndexEntries(src PageSource, nblocks storage.BlockNumber, form HeapEntryFormer) ([]nbtree.LeafEntry, error) {
	if src == nil {
		return nil, fmt.Errorf("amcheck: nil page source")
	}
	if form == nil {
		return nil, fmt.Errorf("amcheck: nil heap entry former")
	}
	if nblocks == 0 {
		return nil, nil
	}

	var entries []nbtree.LeafEntry
	lastBlock := nblocks - 1
	for blkno := storage.BlockNumber(0); ; blkno++ {
		p, err := src(blkno)
		if err != nil {
			return nil, fmt.Errorf("amcheck: reading block %d: %w", blkno, err)
		}
		blockEntries, err := collectHeapPageEntries(p, blkno, form)
		if err != nil {
			return nil, err
		}
		entries = append(entries, blockEntries...)

		// lastBlock is the final iteration; break here rather than in the loop
		// condition so a relation ending at BlockNumber (uint32) max does not wrap.
		if blkno == lastBlock {
			break
		}
	}
	return entries, nil
}

// collectHeapPageEntries forms the leaf entries for every includable LP_NORMAL
// tuple on a single heap page, in offset order. A new (all-zero) page yields
// nothing. It is the per-page body of CollectHeapIndexEntries, factored out so the
// line-pointer iteration is unit-testable in isolation.
func collectHeapPageEntries(p storage.Page, blkno storage.BlockNumber, form HeapEntryFormer) ([]nbtree.LeafEntry, error) {
	if len(p) != storage.BlockSize {
		return nil, fmt.Errorf("amcheck: block %d is %d bytes, want %d", blkno, len(p), storage.BlockSize)
	}
	if storage.IsNew(p) {
		return nil, nil
	}

	maxoff, err := storage.PageLinePointerCount(p)
	if err != nil {
		return nil, fmt.Errorf("amcheck: block %d: cannot determine line pointer count: %w", blkno, err)
	}

	var entries []nbtree.LeafEntry
	for off := firstOffsetNumber; off <= maxoff; off++ {
		offnum := uint16(off)
		item, err := storage.PageGetItemID(p, offnum)
		if err != nil {
			return nil, fmt.Errorf("amcheck: block %d: reading line pointer %d: %w", blkno, offnum, err)
		}
		// Only LP_NORMAL items carry a tuple body. Unused / dead items have
		// none, and a redirect's target is itself an LP_NORMAL item reached on
		// its own offset, so the redirect entry is skipped to avoid forming the
		// same tuple twice.
		if item.Flags != storage.ItemIDNormal {
			continue
		}

		lpOff := int(item.Offset)
		lpLen := int(item.Length)
		// Bound-check before slicing: a corrupt line pointer that points outside
		// the page would otherwise panic. Surface it as an error (a truncated
		// probe set is unsound — see CollectHeapIndexEntries) rather than silently
		// dropping the tuple.
		if lpOff < 0 || lpLen < 0 || lpOff+lpLen > storage.BlockSize {
			return nil, fmt.Errorf(
				"amcheck: block %d offset %d: line pointer (off=%d len=%d) falls outside the page",
				blkno, offnum, lpOff, lpLen)
		}

		tid := storage.ItemPointer{Block: blkno, Offset: offnum}
		entry, include, err := form(tid, p[lpOff:lpOff+lpLen])
		if err != nil {
			return nil, fmt.Errorf("amcheck: block %d offset %d: forming index entry: %w", blkno, offnum, err)
		}
		if include {
			entries = append(entries, entry)
		}
	}
	return entries, nil
}
