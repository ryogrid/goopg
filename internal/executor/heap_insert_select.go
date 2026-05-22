package executor

import (
	"math"

	"github.com/goopg/goopg/internal/storage"
)

// candidatesPerInsert is the FSM top-K width per insert: the heap-insert
// path asks the FSM for up to this many pages with sufficient free space,
// then ranks them by current pin count to bias away from a hot tail page.
// Four matches the design at docs/design/perf-optimize/07-wal-fsm-insert.md §3.
const candidatesPerInsert = 4

// hotPinThreshold rejects candidates whose live pin count is at or above
// this value: under high concurrency every backend converging on one tail
// page would otherwise serialise on that page's content lock. The bound
// is intentionally inclusive on the reject side (pin >= 4 falls through)
// so the caller's extension path can spread inserters across freshly
// added pages instead. Mirrors the design at
// docs/design/perf-optimize/07-wal-fsm-insert.md §3.
const hotPinThreshold = 4

// extendBatchSize is the number of pages appended per heap-extension
// event. One smgr-level call materialises this many empty pages on disk;
// the first becomes the caller's insert target and the remaining N-1 are
// FSM-registered so concurrent inserters spreading across stripes find
// fresh, low-pin pages on their next FSM consultation instead of
// converging on a hot tail page. Matches the design at
// docs/design/perf-optimize/07-wal-fsm-insert.md §3
// (extendBatchSize == 8 — roughly the 8-stripe parallelism budget).
const extendBatchSize = 8

// batchExtendAndRegisterFSM appends extendBatchSize empty pages to rel in
// one smgr-level call and FSM-registers blocks
// [firstBlk+1 .. firstBlk+extendBatchSize-1] at empty-page free space, so
// concurrent inserters that re-consult the FSM after this stripe extends
// land on the extras instead of re-extending. The first block is
// returned so the caller can pin and insert into it; firstBlk is *not*
// FSM-registered here — the caller's subsequent
// markHeapInsertDirty → FSM.RecordFreeSpaceForPage path records the
// post-insert free space, which is the same path every other insert
// already takes.
//
// Nil-FSM safe (skips registration). A nil pool would have aborted
// before reaching the heap-extend cascade; we do not re-check it here.
//
// M0107-0007 slice C — batched-extend half of the call-site rewrite.
// Replaces the single-page PinNew tail in writeHeapRowReturning /
// writeHeapRowReturningPG so each extend event leaves seven fresh
// candidates for the next inserter wave.
func batchExtendAndRegisterFSM(pool *storage.Pool, fsm *storage.FSM, rel storage.RelFileNode) (storage.BlockNumber, error) {
	first, err := pool.ExtendRelationBatch(rel, extendBatchSize)
	if err != nil {
		return 0, err
	}
	if fsm != nil {
		// An InitPage'd empty page has Upper=BlockSize and
		// Lower=SizeOfPageHeaderData, so FreeSpace = BlockSize -
		// SizeOfPageHeaderData. Use the constant rather than reading
		// each page back through the buffer pool — the extras are
		// guaranteed-fresh by ExtendRelationBatch's pre-init buffer.
		emptyFree := uint16(storage.BlockSize - storage.SizeOfPageHeaderData)
		for i := storage.BlockNumber(1); i < storage.BlockNumber(extendBatchSize); i++ {
			fsm.RecordFreeSpace(rel, first+i, emptyFree)
		}
	}
	return first, nil
}

// selectFSMCandidatePage queries the FSM for up to candidatesPerInsert
// pages with at least minFreeBytes free, ranks them by current pin count
// (lower is better), and returns the best block. Returns (0, false) when
// the FSM has no qualifying candidates or every candidate's pin count is
// at or above hotPinThreshold — the caller falls through to the heap-
// extend path.
//
// Read-only and lock-free: FSM.GetCandidates takes its own RLock for the
// scan, and Pool.SlotPinCount probes the lock-free bufmap (M0107-0006).
// No buffer pin is held; the returned block may be stale (another writer
// could fill it between selection and the caller's Pin), which the
// caller already tolerates via PageAddItem retry plus FSM invalidation.
//
// M0107-0007 slice C — executor consumer foundation. The full
// writeHeapRowReturning rewrite — replacing the FSM/tail/extend cascade
// with this helper plus Pool.ExtendRelationBatch — lands in a follow-up
// loop with the PG-compat WAL byte-diff gate from the parent milestone.
func selectFSMCandidatePage(
	fsm *storage.FSM,
	pool *storage.Pool,
	rel storage.RelFileNode,
	minFreeBytes uint16,
) (storage.BlockNumber, bool) {
	if fsm == nil || pool == nil {
		return 0, false
	}
	candidates := fsm.GetCandidates(rel, minFreeBytes, candidatesPerInsert)
	if len(candidates) == 0 {
		return 0, false
	}
	var (
		best     storage.BlockNumber
		bestPin  int32 = math.MaxInt32
		haveBest bool
	)
	for _, blk := range candidates {
		pin := pool.SlotPinCount(storage.BufferTag{Rel: rel, Block: blk})
		if pin < bestPin {
			bestPin = pin
			best = blk
			haveBest = true
			if pin == 0 {
				break
			}
		}
	}
	if !haveBest || bestPin >= hotPinThreshold {
		return 0, false
	}
	return best, true
}
