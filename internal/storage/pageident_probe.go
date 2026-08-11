package storage

import (
	"fmt"
	"os"
	"runtime/debug"
	"sync"
	"sync/atomic"
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
		pageIdentStack(note)
	}
}

// pageIdentStack prints the goroutine stack of the first few regressions so the
// MUTATION site is named, not just the site that later noticed the smaller page.
// Bounded because a regressing page tends to be observed repeatedly (write,
// read, mutate) and an unbounded dump would bury the log.
var pageIdentStacks atomic.Int32

func pageIdentStack(note string) {
	if pageIdentStacks.Add(1) > 4 {
		return
	}
	fmt.Fprintf(os.Stderr, "PAGEIDENT-STACK note=%s\n%s\n", note, debug.Stack())
}

// PageIdentityAssertEmit is the EMIT-side half of the probe (M0131-S30.3 step
// (a)). PageIdentityObserve catches a page whose CONTENT regressed; this
// catches the complementary case where the content is fine but the value the
// runtime writes into the WAL record does not describe the page it just
// mutated.
//
// Two invariants are checked at the moment the HOT-update record's `new_off`
// is handed to the WAL:
//
//   - PageAddHeapTuple always APPENDS, so immediately after it the new slot is
//     the last line pointer: newOff == PageLinePointerCount(page). A mismatch
//     means the page changed identity between the add and the emit, or that
//     newOff was carried over from a different page entirely.
//   - newOff must be at least the high-water line-pointer count seen for this
//     tag. The S30.3 record's `new_off: 2` against a 185-line-pointer block 130
//     is exactly this violation, so it fires even if the page under the pointer
//     has meanwhile been replaced.
//
// Caller must hold the page's content lock (the counts are only stable there).
func PageIdentityAssertEmit(tag BufferTag, page []byte, newOff uint16, note string) {
	if !pageIdentProbeEnabled {
		return
	}
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
	if int(h.Special()) != BlockSize { // heap pages only — see PageIdentityObserve
		return
	}
	pageIdentMu.Lock()
	high, seen := pageIdentHigh[tag]
	if !seen || cnt > high {
		pageIdentHigh[tag] = cnt
	}
	pageIdentMu.Unlock()
	if int(newOff) != cnt {
		fmt.Fprintf(os.Stderr,
			"PAGEIDENT-EMIT-OFF rel=%d/%d/%d blk=%d new_off=%d lp=%d high=%d pd_lsn=%d note=%s\n",
			tag.Rel.TblOid, tag.Rel.DBOid, tag.Rel.RelOid, tag.Block, newOff, cnt, high, h.LSN(), note)
	}
	if seen && int(newOff) < high {
		fmt.Fprintf(os.Stderr,
			"PAGEIDENT-EMIT-REGRESS rel=%d/%d/%d blk=%d new_off=%d lp=%d high=%d pd_lsn=%d note=%s\n",
			tag.Rel.TblOid, tag.Rel.DBOid, tag.Rel.RelOid, tag.Block, newOff, cnt, high, h.LSN(), note)
	}
}

// PageIdentityAssertCount reports when a page's line-pointer count differs from
// want. Used to prove the unlogged add/remove pair of the HOT orphan-cleanup
// arm (M0131-S30.5) really is a page no-op: after PageRemoveHeapTuple the count
// must be back to what it was before PageAddHeapTuple, or the buffer that
// reaches disk disagrees with the page replay rebuilds.
func PageIdentityAssertCount(tag BufferTag, page []byte, want int, note string) {
	if !pageIdentProbeEnabled {
		return
	}
	cnt, err := PageLinePointerCount(page)
	if err != nil || cnt == want {
		return
	}
	fmt.Fprintf(os.Stderr,
		"PAGEIDENT-COUNT rel=%d/%d/%d blk=%d lp=%d want=%d note=%s\n",
		tag.Rel.TblOid, tag.Rel.DBOid, tag.Rel.RelOid, tag.Block, cnt, want, note)
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

// PageIdentityReportDupSlot reports a slot mutating a page under a tag that the
// bufmap associates with a DIFFERENT slot — two live buffers for one block.
// See Pool.probeAssertSlotIsMapped (M0131-S30.3).
func PageIdentityReportDupSlot(tag BufferTag, slotIdx, mappedIdx int32, note string) {
	if !pageIdentProbeEnabled {
		return
	}
	fmt.Fprintf(os.Stderr,
		"PAGEIDENT-DUPSLOT rel=%d/%d/%d blk=%d slot=%d mapped=%d note=%s\n",
		tag.Rel.TblOid, tag.Rel.DBOid, tag.Rel.RelOid, tag.Block, slotIdx, mappedIdx, note)
	pageIdentStack("dupslot:" + note)
}
