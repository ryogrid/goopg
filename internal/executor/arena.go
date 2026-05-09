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

	// M0074-0003 forward-compat (PARTIAL): registryIdx is the
	// arena's slot in arenaRegistry; permanent guards Reset()
	// from being called on the process-global permArena.
	// Populated on NewArena; cleared on Drop(). Today these are
	// dormant — production code paths don't yet read either —
	// but they form the foundation for the deferred Datum struct
	// packed-layout flip (M0075).
	permanent   bool
	registryIdx int32
}

// arenaPageSize is the default per-page capacity. 64 KiB matches
// the design's "typical scan emits ≤ 1 page per batch of 1024 rows
// × ~50-byte avg string = 50 KB" observation.
const arenaPageSize = 64 * 1024

// NewArena returns an empty arena that will allocate pages of the
// default size on first use. Pass 0 for the default; pass a
// positive value to override (test-only knob).
//
// M0074-0003 forward-compat: the arena registers in arenaRegistry
// for future packed-Datum lookup. Today the registration is
// inert — production paths don't yet consult arenaRegistry — but
// the registration runs unconditionally so the M0075 flip lands
// without a cascade of constructor changes.
func NewArena(pageSize int) *Arena {
	if pageSize <= 0 {
		pageSize = arenaPageSize
	}
	a := &Arena{pageSize: pageSize, registryIdx: -1}
	registerArena(a)
	return a
}

// Allocate returns a writable slice of exactly n bytes inside the
// arena plus the absolute offset within the arena's logical byte
// stream (offset 0 is the first byte of page 0; subsequent pages'
// offsets accumulate). The returned slice is valid until Reset()
// is called or (for very large n that exceeds pageSize) until the
// arena is dropped — large allocations get their own dedicated page
// so growth doesn't churn smaller pages.
//
// M0073-0002 returns the offset so DecodeRowInto can encode the
// (offset, length) pair into Datum.Int for arena-backed variants.
func (a *Arena) Allocate(n int) (buf []byte, offset int) {
	if n < 0 {
		// Defensive: negative widths are not meaningful; treat
		// as zero so callers don't panic on bogus length input.
		n = 0
	}
	if n == 0 {
		return nil, 0
	}
	if n > a.pageSize {
		// Oversized payload — allocate a private page sized to
		// fit. Append at the cur+1 position so subsequent small
		// allocations continue using the existing cur page.
		page := make([]byte, n)
		var insertedAt int
		if a.cur < len(a.pages) {
			// Insert immediately AFTER the active page so the
			// active small-page tail stays usable.
			a.pages = append(a.pages, nil)
			copy(a.pages[a.cur+2:], a.pages[a.cur+1:])
			a.pages[a.cur+1] = page
			insertedAt = a.cur + 1
		} else {
			a.pages = append(a.pages, page)
			insertedAt = len(a.pages) - 1
		}
		off := 0
		for i := 0; i < insertedAt; i++ {
			off += a.pageSize
		}
		return page, off
	}
	// Walk forward from cur, allocating new pages as needed
	// until one has enough remaining capacity for n.
	for {
		if a.cur < len(a.pages) {
			p := a.pages[a.cur]
			if cap(p)-len(p) >= n {
				start := len(p)
				a.pages[a.cur] = p[:start+n]
				off := a.cur*a.pageSize + start
				return a.pages[a.cur][start : start+n], off
			}
			// Current page exhausted; advance.
			a.cur++
			continue
		}
		// No more pages — allocate a fresh one and retry.
		a.pages = append(a.pages, make([]byte, 0, a.pageSize))
	}
}

// Bytes resolves an (offset, length) pair returned by Allocate
// back to the underlying byte slice. Cross-page references are
// resolved internally — the offset addresses the arena's logical
// byte stream, where page i starts at offset i*pageSize.
//
// The returned slice aliases the arena page; callers MUST NOT
// mutate it, and MUST treat it as invalid past the arena's next
// Reset() call.
func (a *Arena) Bytes(offset, length int) []byte {
	if length == 0 {
		return nil
	}
	pageIdx := offset / a.pageSize
	pageStart := offset % a.pageSize
	if pageIdx < 0 || pageIdx >= len(a.pages) {
		return nil
	}
	page := a.pages[pageIdx]
	if pageStart < 0 || pageStart+length > cap(page) {
		// Out-of-range — return empty slice rather than panic.
		// The compile-time / test invariant is callers always
		// supply valid (offset, length) pairs from Allocate.
		return nil
	}
	// Use cap(page) so we can read bytes that have been
	// allocated but happen to lie past page's current len —
	// not strictly needed (Allocate extends len before
	// returning), but defensive.
	return page[pageStart : pageStart+length]
}

// Reset truncates every page back to zero length and rewinds the
// allocation cursor. Page backing arrays are kept so subsequent
// batches reuse the same memory (amortised zero-alloc steady
// state). Callers who retained references to allocated slices
// MUST have copied the bytes out before Reset; reading after
// Reset returns garbage.
//
// M0074-0003 forward-compat: permArena (process-global, never
// resets) skips this no-op so the future packed-Datum flip's
// "literal Datums live in permArena" contract holds.
func (a *Arena) Reset() {
	if a.permanent {
		return
	}
	for i := range a.pages {
		a.pages[i] = a.pages[i][:0]
	}
	a.cur = 0
}

// Drop releases the underlying page memory. Use when the arena
// is no longer needed (e.g. operator Close). After Drop, the
// arena is empty but reusable — the next Allocate call will
// re-grow pages from scratch.
//
// M0074-0003 forward-compat: also unregisters the arena from
// arenaRegistry (no-op for permArena which is never dropped).
func (a *Arena) Drop() {
	unregisterArena(a)
	a.pages = nil
	a.cur = 0
	a.registryIdx = -1
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
