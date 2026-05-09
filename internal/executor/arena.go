package executor

// Arena is a per-batch growable byte buffer that hands out slices.
// Producers (SeqScan / IndexScan decode paths) write variable-length
// payloads (varchar / bytes / large NUMERIC) into a shared arena
// instead of allocating one Go string per value. Consumers either
// read directly from the arena (within the same Next() call) or
// call slot.Materialize() to copy the payload out before the
// producer calls Reset().
//
// M0072-0004 introduces the Arena type as a standalone foundation;
// Datum integration (replacing `Buf []byte` per-Datum allocation
// with `(arena, offset, length)` tuples) is the risky structural
// piece deferred to M0073 alongside the Q9 virtual-coord
// propagation. The Arena lives here so the M0073 work can wire it
// without re-litigating the type design.
//
// See `docs/design/0068-0003-batch-string-arena.md` for the
// authoritative design and integration plan.
//
// Concurrency: an Arena is NOT thread-safe. Each operator owns its
// arena. Cross-goroutine sharing is a contract violation.
type Arena struct {
	pages    [][]byte
	cur      int // index of the page currently being filled
	pageSize int // each page is allocated at this capacity
}

// arenaPageSize is the default per-page capacity. 64 KiB matches
// the design's "typical scan emits ≤ 1 page per batch of 1024 rows
// × ~50-byte avg string = 50 KB" observation.
const arenaPageSize = 64 * 1024

// NewArena returns an empty arena that will allocate pages of the
// default size on first use. Pass 0 for the default; pass a
// positive value to override (test-only knob).
func NewArena(pageSize int) *Arena {
	if pageSize <= 0 {
		pageSize = arenaPageSize
	}
	return &Arena{pageSize: pageSize}
}

// Allocate returns a writable slice of exactly n bytes inside the
// arena. The returned slice is valid until Reset() is called or
// (for very large n that exceeds pageSize) until the arena is
// dropped — large allocations get their own dedicated page so
// growth doesn't churn smaller pages.
func (a *Arena) Allocate(n int) []byte {
	if n < 0 {
		// Defensive: negative widths are not meaningful; treat
		// as zero so callers don't panic on bogus length input.
		n = 0
	}
	if n == 0 {
		return nil
	}
	if n > a.pageSize {
		// Oversized payload — allocate a private page sized to
		// fit. Append at the cur+1 position so subsequent small
		// allocations continue using the existing cur page.
		page := make([]byte, n)
		if a.cur < len(a.pages) {
			// Insert immediately AFTER the active page so the
			// active small-page tail stays usable.
			a.pages = append(a.pages, nil)
			copy(a.pages[a.cur+2:], a.pages[a.cur+1:])
			a.pages[a.cur+1] = page
		} else {
			a.pages = append(a.pages, page)
		}
		return page
	}
	// Walk forward from cur, allocating new pages as needed
	// until one has enough remaining capacity for n.
	for {
		if a.cur < len(a.pages) {
			p := a.pages[a.cur]
			if cap(p)-len(p) >= n {
				start := len(p)
				a.pages[a.cur] = p[:start+n]
				return a.pages[a.cur][start : start+n]
			}
			// Current page exhausted; advance.
			a.cur++
			continue
		}
		// No more pages — allocate a fresh one and retry.
		a.pages = append(a.pages, make([]byte, 0, a.pageSize))
	}
}

// Reset truncates every page back to zero length and rewinds the
// allocation cursor. Page backing arrays are kept so subsequent
// batches reuse the same memory (amortised zero-alloc steady
// state). Callers who retained references to allocated slices
// MUST have copied the bytes out before Reset; reading after
// Reset returns garbage.
func (a *Arena) Reset() {
	for i := range a.pages {
		a.pages[i] = a.pages[i][:0]
	}
	a.cur = 0
}

// Drop releases the underlying page memory. Use when the arena
// is no longer needed (e.g. operator Close). After Drop, the
// arena is empty but reusable — the next Allocate call will
// re-grow pages from scratch.
func (a *Arena) Drop() {
	a.pages = nil
	a.cur = 0
}

// TotalAllocated returns the sum of len(page) across all pages.
// Test-only inspection helper.
func (a *Arena) TotalAllocated() int {
	n := 0
	for _, p := range a.pages {
		n += len(p)
	}
	return n
}

// PageCount returns the number of pages currently held by the
// arena (independent of how much of each is filled).
// Test-only inspection helper.
func (a *Arena) PageCount() int {
	return len(a.pages)
}
