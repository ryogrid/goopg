package storage

// ScanRingSize is the number of private page buffers in a ScanRing.
// Mirrors PostgreSQL's NBuffers_QueryRing constant (32 pages).
const ScanRingSize = 32

// ScanRing implements a small private ring buffer for sequential scans
// (M0048-0002). It eliminates the cache-pollution problem that occurs
// when a large sequential scan evicts all hot pages from the shared
// buffer pool:
//
//  1. For each block, ScanRing first checks Pool.TryPin (cache hit).
//     If the block is already cached, it is used directly from the pool
//     without any eviction pressure.
//
//  2. On a cache miss, ScanRing reads the page into one of its 32
//     private page buffers using Manager.ReadBlock, bypassing the
//     global pool entirely.  No pool slot is evicted.
//
// The ring is appropriate when the relation being scanned is large enough
// that a full sequential scan would evict most of the shared buffer pool.
// A common heuristic is to use it when nBlocks > pool.Capacity() / 4.
type ScanRing struct {
	pool *Pool
	rel  RelFileNode

	// Private ring page buffers for cache misses.  Pre-allocated to
	// avoid repeated heap allocation.  head cycles through 0..ScanRingSize-1.
	bufs    [ScanRingSize][]byte
	bufHead int

	// activeSlot is non-nil when the current page came from the pool
	// (cache hit path).  Its RLock is held until ReleasePage.
	activeSlot *Slot

	// activePage is the current page bytes (from pool or ring buffer).
	activePage Page
}

// NewScanRing allocates a ScanRing for rel.
func NewScanRing(pool *Pool, rel RelFileNode) *ScanRing {
	r := &ScanRing{pool: pool, rel: rel}
	for i := range r.bufs {
		r.bufs[i] = make([]byte, BlockSize)
	}
	return r
}

// AcquirePage makes the page for blk available and returns it.
//
// Cache hit (block already in pool): the pool slot is pinned and its
// shared content lock held; the caller reads from the returned page while
// it is valid.
//
// Cache miss: the block is read into the next ring buffer; no pool slot
// is touched, so no hot page is evicted.
//
// Callers must call ReleasePage when they are done with the page.
func (r *ScanRing) AcquirePage(blk BlockNumber) (Page, error) {
	r.ReleasePage()

	tag := BufferTag{Rel: r.rel, Block: blk}

	// Cache hit: use the pool slot directly.
	if slot, ok := r.pool.TryPin(tag); ok {
		slot.RLock()
		r.activeSlot = slot
		r.activePage = slot.Page()
		return r.activePage, nil
	}

	// Cache miss: read into the next ring buffer (no pool eviction).
	buf := r.bufs[r.bufHead]
	r.bufHead = (r.bufHead + 1) % ScanRingSize
	if err := r.pool.Manager().ReadBlock(r.rel, blk, Page(buf)); err != nil {
		return nil, err
	}
	r.activeSlot = nil
	r.activePage = Page(buf)
	return r.activePage, nil
}

// ActivePage returns the currently held page, valid between
// AcquirePage and ReleasePage calls.
func (r *ScanRing) ActivePage() Page { return r.activePage }

// ReleasePage releases the currently held page and its pool lock
// (if any).  It is safe to call when no page is held.
func (r *ScanRing) ReleasePage() {
	if r.activeSlot != nil {
		r.activeSlot.RUnlock()
		r.pool.Unpin(r.activeSlot)
		r.activeSlot = nil
	}
	r.activePage = nil
}

// Close releases any held page and returns the ring to an idle state.
func (r *ScanRing) Close() { r.ReleasePage() }
