package storage

import (
	"fmt"
	"os"
	"sync"
)

// TEMPORARY diagnostic for M0131-S30.3 (docs/design/0131-0021): replay and the
// runtime disagree about which physical page a HOT update touched. The crashed
// cluster's WAL carries `HOT_UPDATE rel 1663/5/16407 blk 130, old_off 1,
// new_off 2` while block 130 on disk holds 185 line pointers — so at emit time
// the runtime was looking at a page with exactly ONE line pointer under the tag
// {16407, 130}.
//
// The probe exploits a pair of hard invariants of goopg's page primitives:
//
//   - PageAddHeapTuple (heap.go) ALWAYS appends at count+1 and never reuses a
//     free slot, and neither PagePruneOpt nor VacuumHeapPageBySlots shrinks the
//     line-pointer array. Therefore, for a given (rel, block), the line-pointer
//     COUNT is monotonically non-decreasing for as long as that block exists.
//   - A block number is handed out by extend/extendBatch exactly once per
//     relfilenode lifetime (relFile.nblocks only grows outside TRUNCATE).
//
// So two events are impossible in a healthy run and each pinpoints a different
// mechanism for the divergence:
//
//	PAGEIDENT-REGRESS   a page observed under tag T has FEWER line pointers
//	                    than the high-water mark previously observed under the
//	                    same tag  =>  buffer tag/content aliasing (the slot's
//	                    bytes belong to a different block than its tag says).
//	PAGEIDENT-REEXTEND  extend handed out a block number that already carried
//	                    line pointers  =>  nblocks regressed / double hand-out.
//
// Gated on GOOPG_PAGEIDENT_PROBE=1 (read once at package init, so it costs one
// predictable branch when unset). Not permanent instrumentation — remove once
// S30.3 is closed.
var pageIdentProbeEnabled = os.Getenv("GOOPG_PAGEIDENT_PROBE") == "1"

// PageIdentityProbeEnabled reports whether the S30.3 page-identity probe is
// active, so callers can skip building probe arguments when it is not.
func PageIdentityProbeEnabled() bool { return pageIdentProbeEnabled }

var (
	pageIdentMu   sync.Mutex
	pageIdentHigh = map[BufferTag]int{}
)

// PageIdentityObserve records the line-pointer count of page under tag and
// reports (on stderr) any regression below the high-water mark seen so far for
// that tag. note identifies the observation site.
func PageIdentityObserve(tag BufferTag, page []byte, note string) {
	if !pageIdentProbeEnabled {
		return
	}
	// Only heap/main-fork pages obey the append-only line-pointer invariant;
	// btree pages compact their line-pointer array on split and would produce
	// meaningless regressions.
	if tag.Rel.Fork != MainFork {
		return
	}
	cnt, err := PageLinePointerCount(page)
	if err != nil {
		return
	}
	h, herr := Header(page)
	if herr != nil {
		return
	}
	// Heap pages only. An index relation also lives on MainFork, and a btree
	// page LEGITIMATELY loses line pointers (a split moves half its items to
	// the right sibling and compacts the array) — those are the benign hits
	// this filter removes. A page with no special space is a heap page;
	// pd_special == BlockSize is PG's own encoding of "no special area"
	// (bufpage.h PageGetSpecialSize).
	if int(h.Special()) != BlockSize {
		return
	}
	lsn := h.LSN()
	pageIdentMu.Lock()
	high, seen := pageIdentHigh[tag]
	if !seen || cnt > high {
		pageIdentHigh[tag] = cnt
	}
	pageIdentMu.Unlock()
	if seen && cnt < high {
		fmt.Fprintf(os.Stderr,
			"PAGEIDENT-REGRESS rel=%d/%d/%d blk=%d lp=%d high=%d pd_lsn=%d note=%s\n",
			tag.Rel.TblOid, tag.Rel.DBOid, tag.Rel.RelOid, tag.Block, cnt, high, lsn, note)
	}
}

// PageIdentityExtend records that blk was just handed out by extend/extendBatch
// for rel, and reports a hand-out of a block that already carried tuples.
func PageIdentityExtend(tag BufferTag) {
	if !pageIdentProbeEnabled {
		return
	}
	if tag.Rel.Fork != MainFork {
		return
	}
	pageIdentMu.Lock()
	high, seen := pageIdentHigh[tag]
	pageIdentHigh[tag] = 0
	pageIdentMu.Unlock()
	if seen && high > 0 {
		fmt.Fprintf(os.Stderr,
			"PAGEIDENT-REEXTEND rel=%d/%d/%d blk=%d prevHigh=%d\n",
			tag.Rel.TblOid, tag.Rel.DBOid, tag.Rel.RelOid, tag.Block, high)
	}
}
