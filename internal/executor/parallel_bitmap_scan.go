package executor

// parallel_bitmap_scan.go — S5.6 of docs/design/0129-q74-fix-and-m0128-followups.md.
//
// The shared work queue for a parallel bitmap heap scan. PG's equivalent
// (ParallelBitmapHeapState) lives in dynamic shared memory under a spinlock;
// goopg's workers share a Go pointer, so the state reduces to a sorted block
// list and an atomic counter.
//
// Lifecycle:
//
//   1. Leader builds the TIDBitmap (runs the BitmapIndexScan child).
//   2. Leader publishes the bitmap and the sorted block list here.
//   3. Workers claim page indices from the atomic counter; each worker gets a
//      disjoint range of blocks.
//   4. Each worker iterates its pages locally — no further synchronisation.

import (
	"sync/atomic"

	"github.com/goopg/goopg/internal/storage"
)

// parallelBitmapState is the shared work queue for one parallel bitmap heap scan
// node. The leader creates and populates it; every worker's bitmapHeapScanOp
// holds a pointer to the same instance.
type parallelBitmapState struct {
	// tbm is the fully-built TIDBitmap, read-only after initialisation.
	// nil until the leader publishes.
	tbm *TIDBitmap

	// blocks is the sorted list of block numbers from tbm.entries.
	// Immutable after initialisation.
	blocks []storage.BlockNumber

	// nextIdx is the next unclaimed index into blocks. Workers claim a range
	// with a single atomic add; the value may run past len(blocks), which is
	// how exhaustion is detected without a second synchronisation point.
	nextIdx atomic.Int64
}

// newParallelBitmapState creates an uninitialised state.
func newParallelBitmapState() *parallelBitmapState {
	return &parallelBitmapState{}
}

// init publishes the bitmap and its sorted block list. Called once by the
// leader after building the bitmap. Idempotent: subsequent calls are no-ops
// (the first write wins).
func (s *parallelBitmapState) init(tbm *TIDBitmap) {
	if s == nil || s.tbm != nil {
		return
	}
	s.tbm = tbm
	if len(tbm.entries) > 0 {
		s.blocks = make([]storage.BlockNumber, 0, len(tbm.entries))
		for block := range tbm.entries {
			s.blocks = append(s.blocks, block)
		}
		// Sort for physical-order I/O.
		sortBlockNumbers(s.blocks)
	}
}

// nextPage claims the next page index for the calling worker, or reports
// exhaustion. Returns the block number and the page entry from the bitmap.
//
// Workers call this in a loop until it returns false. Each worker gets a
// single page per call — the same granularity as the serial iterator, which
// keeps the pin/release cadence identical.
func (s *parallelBitmapState) nextPage() (block storage.BlockNumber, entry *pageEntry, ok bool) {
	if s == nil || s.tbm == nil {
		return 0, nil, false
	}
	idx := s.nextIdx.Add(1) - 1
	if idx >= int64(len(s.blocks)) {
		return 0, nil, false
	}
	block = s.blocks[idx]
	entry = s.tbm.entries[block]
	return block, entry, true
}

// claimed reports how many page indices have been handed out, for tests and
// instrumentation. It may exceed len(blocks) once workers have raced past the
// end.
func (s *parallelBitmapState) claimed() int64 {
	if s == nil {
		return 0
	}
	return s.nextIdx.Load()
}

// sortBlockNumbers sorts a slice of block numbers in ascending order.
// Factored out so init doesn't drag sort into the call site.
func sortBlockNumbers(blocks []storage.BlockNumber) {
	// Simple insertion sort — the slice is small (at most a few thousand
	// pages for a typical bitmap scan).
	for i := 1; i < len(blocks); i++ {
		v := blocks[i]
		j := i - 1
		for j >= 0 && blocks[j] > v {
			blocks[j+1] = blocks[j]
			j--
		}
		blocks[j+1] = v
	}
}
