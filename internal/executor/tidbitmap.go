package executor

import (
	"math/bits"
	"sort"

	"github.com/goopg/goopg/internal/storage"
)

// TIDBitmap is a sparse, page-keyed set of tuple IDs (PG's TIDBitmap
// analogue in nodes/tidbitmap.c). It stores exact pages (per-offset
// bitmap) and lossy pages (one bit per page, every tuple must be
// re-checked against the original index qual).
//
// Zero value is an empty bitmap ready for use.
type TIDBitmap struct {
	// entries maps BlockNumber → pageEntry. nil until the first insert.
	entries map[storage.BlockNumber]*pageEntry
	// maxEntries is the soft memory ceiling; when exceeded, the heaviest
	// exact page is degraded to lossy. 0 = unlimited.
	maxEntries int
	// npages counts exact entries; nchunks counts lossy entries.
	npages  int
	nchunks int
	// recheckAny is true if any entry has recheck set (fast-path for
	// the heap-scan operator).
	recheckAny bool
}

// MaxOffsetNumber is the maximum tuple offset on a heap page (PG's
// MaxOffsetNumber, itemptr.h).
const MaxOffsetNumber = 2048 // PG default; must be >= the actual max on any page.

// bitmapWords is the number of bytes needed to represent MaxOffsetNumber bits.
const bitmapWords = MaxOffsetNumber / 8

type pageEntry struct {
	block   storage.BlockNumber // block number (key)
	isLossy bool                // true → visited entirely; bitmap ignored
	recheck bool                // true → evaluate original index qual per tuple
	// bitmap[k/8] & (1<<(k%8)) means OffsetNumber (k+1) is present.
	// Only used when isLossy == false. Length = bitmapWords.
	bitmap []byte
}

// tbmAddTuples ORs the given TIDs into the bitmap.
// recheck is set when the index AM cannot guarantee the row satisfies
// the index qual (e.g. prefix scan on composite index, lossy index conditions).
func tbmAddTuples(tbm *TIDBitmap, tids []storage.ItemPointer, recheck bool) {
	if tbm.entries == nil {
		tbm.entries = make(map[storage.BlockNumber]*pageEntry)
	}
	if recheck {
		tbm.recheckAny = true
	}
	for i := range tids {
		tbm.addOne(tids[i].Block, tids[i].Offset, recheck)
	}
}

// addOne inserts a single TID into the bitmap.
func (tbm *TIDBitmap) addOne(block storage.BlockNumber, offset uint16, recheck bool) {
	e, ok := tbm.entries[block]
	if !ok {
		e = &pageEntry{
			block:  block,
			bitmap: make([]byte, bitmapWords),
		}
		tbm.entries[block] = e
		tbm.npages++
	}
	if e.isLossy {
		// Already lossy — every offset on this page is covered.
		if recheck {
			e.recheck = true
		}
		return
	}
	if recheck {
		e.recheck = true
		tbm.recheckAny = true
	}
	// OffsetNumber is 1-based; subtract 1 for bit index.
	off := offset - 1
	if int(off) < len(e.bitmap)*8 {
		e.bitmap[off/8] |= 1 << (off % 8)
	}
}

// tbmUnion ORs another bitmap's entries into this one.
func tbmUnion(tbm, other *TIDBitmap) {
	if other == nil || len(other.entries) == 0 {
		return
	}
	if tbm.entries == nil {
		tbm.entries = make(map[storage.BlockNumber]*pageEntry)
	}
	if other.recheckAny {
		tbm.recheckAny = true
	}
	for block, oe := range other.entries {
		e, ok := tbm.entries[block]
		if !ok {
			nc := *oe // copy
			tbm.entries[block] = &nc
			if nc.isLossy {
				tbm.nchunks++
			} else {
				tbm.npages++
			}
			continue
		}
		if e.isLossy {
			// Already lossy — every offset is covered.
			if oe.recheck {
				e.recheck = true
			}
			continue
		}
		if oe.isLossy {
			// Promote to lossy.
			e.isLossy = true
			e.bitmap = nil
			if oe.recheck {
				e.recheck = true
			}
			tbm.npages--
			tbm.nchunks++
			continue
		}
		// Both exact — OR the bitmaps.
		if oe.recheck {
			e.recheck = true
			tbm.recheckAny = true
		}
		for i := 0; i < len(e.bitmap); i++ {
			e.bitmap[i] |= oe.bitmap[i]
		}
	}
}

// tbmIntersect ANDs another bitmap's entries with this one.
// An exact page intersecting a lossy page → lossy result.
func tbmIntersect(tbm, other *TIDBitmap) {
	if other == nil || len(other.entries) == 0 {
		// Intersection with empty = empty.
		tbm.entries = nil
		tbm.npages = 0
		tbm.nchunks = 0
		tbm.recheckAny = false
		return
	}
	if len(tbm.entries) == 0 {
		return // already empty
	}
	for block, e := range tbm.entries {
		oe, ok := other.entries[block]
		if !ok {
			// Other doesn't have this page → remove it.
			if e.isLossy {
				tbm.nchunks--
			} else {
				tbm.npages--
			}
			delete(tbm.entries, block)
			continue
		}
		if e.isLossy {
			// Lossy stays lossy — other MUST have this page.
			if oe.recheck {
				e.recheck = true
			}
			continue
		}
		if oe.isLossy {
			// Exact ∩ lossy → lossy.
			e.isLossy = true
			e.bitmap = nil
			tbm.npages--
			tbm.nchunks++
			if oe.recheck {
				e.recheck = true
			}
			continue
		}
		// Both exact — AND the bitmaps.
		if oe.recheck {
			e.recheck = true
		}
		anySet := false
		for i := 0; i < len(e.bitmap); i++ {
			e.bitmap[i] &= oe.bitmap[i]
			if e.bitmap[i] != 0 {
				anySet = true
			}
		}
		if !anySet {
			// No common TIDs on this page.
			tbm.npages--
			delete(tbm.entries, block)
		}
	}
}

// tbmIsEmpty returns true if the bitmap contains no entries.
func tbmIsEmpty(tbm *TIDBitmap) bool {
	return len(tbm.entries) == 0
}

// popcount counts the number of set bits in a byte slice.
func popcount(b []byte) int {
	n := 0
	for _, v := range b {
		n += bits.OnesCount8(v)
	}
	return n
}

// tbmLossify degrades the heaviest exact pages to lossy until the
// entry count is under maxEntries. maxEntries of 0 means unlimited.
// Lossy pages save memory (no bitmap), so converting exact→lossy
// reduces the effective entry count: we count exact pages as 5 units
// (bitmapWords bytes + overhead) and lossy pages as 1 unit (overhead only).
func tbmLossify(tbm *TIDBitmap) {
	if tbm.maxEntries == 0 {
		return
	}
	// Effective cost: exact pages are ~5× lossy pages in memory.
	effectiveEntries := tbm.npages*5 + tbm.nchunks
	for effectiveEntries > tbm.maxEntries && tbm.npages > 0 {
		// Find the exact page with the most tuple bits set.
		var worstBlock storage.BlockNumber
		worstFound := false
		worstCount := -1
		for block, e := range tbm.entries {
			if e.isLossy {
				continue
			}
			c := popcount(e.bitmap)
			if c > worstCount {
				worstCount = c
				worstBlock = block
				worstFound = true
			}
		}
		if !worstFound {
			break // no exact pages left
		}
		e := tbm.entries[worstBlock]
		e.isLossy = true
		e.bitmap = nil
		tbm.npages--
		tbm.nchunks++
		effectiveEntries = tbm.npages*5 + tbm.nchunks
	}
}

// tbmCalculateMaxEntries converts workMem (bytes) to a max entry count.
// PG derives maxbytes from work_mem * 1024; goopg uses the same convention.
// Each exact entry costs ~ bitmapWords bytes + overhead.
func tbmCalculateMaxEntries(workMemBytes int64) int {
	if workMemBytes <= 0 {
		return 0 // unlimited
	}
	// Conservative estimate: each entry costs bitmapWords + map overhead (~64 bytes).
	entryBytes := int64(bitmapWords + 64)
	max := int(workMemBytes / entryBytes)
	if max < 16 {
		max = 16 // minimum sensible value
	}
	return max
}

// ---------------------------------------------------------------------------
// Iterator
// ---------------------------------------------------------------------------

// BitmapPageResult holds the result of a page-level iteration step.
// This mirrors PG's TBMIterateResult (tidbitmap.c).
// For lossy pages, the caller visits every tuple on the page.
// For exact pages, the caller extracts offsets via tbmExtractPageTuple.
type BitmapPageResult struct {
	Block   storage.BlockNumber
	Lossy   bool
	Recheck bool

	// internalPage points to the pageEntry for exact pages (nil for lossy).
	// Callers extract offsets via tbmExtractPageTuple.
	internalPage *pageEntry
}

// tbmIterator walks a TIDBitmap in block-number order.
// For lossy pages, it yields one entry with offset=0 (caller visits
// the whole page). For exact pages, it yields each set offset.
type tbmIterator struct {
	tbm    *TIDBitmap
	blocks []storage.BlockNumber // sorted block numbers, set on first call

	idx int // current index into blocks

	// For the current exact page (per-offset iteration):
	offsets []uint16 // sorted offsets extracted from bitmap
	offIdx  int
}

// tbmBeginIterate creates an iterator over the bitmap.
// The iterator yields entries in block-number order.
func tbmBeginIterate(tbm *TIDBitmap) *tbmIterator {
	it := &tbmIterator{tbm: tbm}
	if len(tbm.entries) > 0 {
		it.blocks = make([]storage.BlockNumber, 0, len(tbm.entries))
		for block := range tbm.entries {
			it.blocks = append(it.blocks, block)
		}
		sort.Slice(it.blocks, func(i, j int) bool {
			return it.blocks[i] < it.blocks[j]
		})
	}
	return it
}

// tbmExtractPageTuple extracts all tuple offsets from an exact page result
// into the caller-provided buffer. It returns the number of offsets that
// would be extracted (even if the buffer is too small to hold them all).
//
// This mirrors PG's tbm_extract_page_tuple (tidbitmap.c:900).
// The caller pre-allocates the buffer once and reuses it across pages
// to avoid per-page allocations.
func tbmExtractPageTuple(e *pageEntry, offsets []uint16) int {
	total := 0
	for i := 0; i < len(e.bitmap); i++ {
		w := e.bitmap[i]
		for w != 0 {
			bit := bits.TrailingZeros8(w)
			w &= w - 1 // clear lowest set bit
			off := uint16(i*8 + bit + 1) // OffsetNumber is 1-based
			if total < len(offsets) {
				offsets[total] = off
			}
			total++
		}
	}
	return total
}

// nextPage advances the iterator to the next page and fills the
// BitmapPageResult. It returns false when the iterator is exhausted.
//
// This mirrors PG's tbm_private_iterate (tidbitmap.c:976): pages are
// delivered in block-number order, and lossy/exact semantics are
// preserved at the page level. The caller then extracts offsets for
// exact pages via tbmExtractPageTuple.
func (it *tbmIterator) nextPage(result *BitmapPageResult) bool {
	for {
		if it.idx >= len(it.blocks) {
			return false
		}

		block := it.blocks[it.idx]
		e := it.tbm.entries[block]
		it.idx++

		if e.isLossy {
			result.Block = block
			result.Lossy = true
			result.Recheck = e.recheck
			result.internalPage = nil
			return true
		}

		// Exact page.
		result.Block = block
		result.Lossy = false
		result.Recheck = e.recheck
		result.internalPage = e
		return true
	}
}

// next advances the iterator and returns the next TID.
// ok is false when the iterator is exhausted.
func (it *tbmIterator) next() (block storage.BlockNumber, offset uint16, lossy, recheck bool, ok bool) {
	for {
		// If we have exact offsets queued, emit the next one.
		if it.offIdx < len(it.offsets) {
			off := it.offsets[it.offIdx]
			it.offIdx++
			e := it.tbm.entries[it.blocks[it.idx-1]]
			return it.blocks[it.idx-1], off, false, e.recheck, true
		}

		// Advance to the next block.
		if it.idx >= len(it.blocks) {
			return 0, 0, false, false, false
		}

		block := it.blocks[it.idx]
		e := it.tbm.entries[block]
		it.idx++

		if e.isLossy {
			// For lossy pages: yield (block, 0, lossy=true).
			// The caller iterates all offsets on the page.
			return block, 0, true, e.recheck, true
		}

		// Exact page: extract sorted offsets from bitmap.
		it.offsets = it.offsets[:0]
		n := tbmExtractPageTuple(e, it.offsets[:cap(it.offsets)])
		// Grow if needed (first use or undersized buffer).
		if n > cap(it.offsets) {
			it.offsets = make([]uint16, n)
			tbmExtractPageTuple(e, it.offsets)
		} else {
			it.offsets = it.offsets[:n]
		}
		it.offIdx = 0

		if len(it.offsets) == 0 {
			continue // empty page entry (should not happen, but be safe)
		}
		// Emit first offset.
		off := it.offsets[it.offIdx]
		it.offIdx++
		return block, off, false, e.recheck, true
	}
}
