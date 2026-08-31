# SeqScan Strategy Ring — M0048-0002

| field      | value                         |
|------------|-------------------------------|
| status     | accepted                      |
| date       | 2026-05-05                    |
| supersedes | —                             |

## 1. Problem

A sequential scan over a large relation (> pool capacity) evicts virtually
every hot page from the shared buffer pool, because the clock-sweep eviction
policy considers all cached pages as equal candidates. On a 1 000-slot pool,
scanning a 5 000-block relation exhausts the entire pool 5 times over,
leaving hit rates near 0% for pages loaded before the scan.

PostgreSQL solves this with a *ring buffer strategy*: the SeqScan uses a
small private ring of buffer slots whose pages are recycled within the ring
rather than competing with the global pool for eviction victims.

## 2. Design

### 2.1 ScanRing (`internal/storage/scan_ring.go`)

`ScanRing` has two modes per block:

**Cache hit** (`Pool.TryPin` succeeds): the block is already in the pool.
The pool slot is pinned with `Pool.TryPin`, its shared content lock held
via `Slot.RLock`, and the page returned.  The pool's hot pages are left
untouched.

**Cache miss** (`Pool.TryPin` returns false): the block is not cached.
`Manager.ReadBlock` fills the next of 32 private page buffers (each 8 KB
of heap memory), completely bypassing the pool.  No eviction occurs.

```go
type ScanRing struct {
    pool    *Pool
    rel     RelFileNode
    bufs    [ScanRingSize][]byte // 32 × 8 KiB = 256 KiB
    bufHead int
    activeSlot *Slot   // non-nil → page from pool (RLock held)
    activePage Page
}

func (r *ScanRing) AcquirePage(blk BlockNumber) (Page, error)
func (r *ScanRing) ReleasePage()
func (r *ScanRing) Close()
```

`ScanRingSize = 32` matches PostgreSQL's `NBuffers_QueryRing`.

### 2.2 New Pool methods (`internal/storage/bufpool.go`)

**`Pool.TryPin(tag) (*Slot, bool)`**: Returns the cached slot without
issuing any I/O. Returns `(nil, false)` on a miss.

**`Pool.Capacity() int`**: Returns the total number of pool slots.  Used
by the planner heuristic.

### 2.3 SeqScan integration (`internal/executor/operators_storage.go`)

`seqScanOp` grows two new fields:
- `ring *storage.ScanRing` — non-nil when the ring strategy is active.
- `activePage storage.Page` — the current page bytes regardless of source.

**Activation heuristic** (in `Open`): the ring is created when
`nBlocks > pool.Capacity() / 4`, matching PostgreSQL's rule "use ring when
the relation is larger than one quarter of shared_buffers".

**`Next`** uses the ring when it is active:
```go
if o.ring != nil {
    page, err = o.ring.AcquirePage(o.curBlock)
    o.activePage = page
} else {
    slot, err = pool.Pin(tag)
    slot.RLock(); o.pinned = slot; o.activePage = slot.Page()
}
```

**`releasePinned`** and **`Close`** delegate to the ring's `ReleasePage`
/ `Close` methods when the ring is active, preserving the same interface
for the surrounding loop.

### 2.4 Interaction with existing mechanisms

- **BM_IO_IN_PROGRESS (M0048-0001)**: `TryPin` does not participate in the
  in-flight deduplication; it only returns slots that are already published
  in `byTag`.  Cache misses on ring pages bypass the pool entirely, so
  `ioByTag` is never consulted on the miss path.
- **Prefetch / AIO**: the existing `refillPrefetchWindow` still fires for
  all pool paths; ring cache misses do not use prefetch.
- **HOT / TOAST / FSM / VM**: all read-path hooks in `seqScanOp.Next` operate
  on `o.activePage` which is set identically for both paths.

## 3. Correctness

- **Read-only safety**: `Manager.ReadBlock` reads a consistent on-disk page.
  For a SeqScan, the visible tuple set is the one that existed at snapshot
  time. Pages dirtied by concurrent writers are still in the pool (pool path)
  or will be flushed before the scan reaches them (ring path—same invariant
  as any non-cached read).
- **No lock gap**: pool-sourced pages hold `slot.RLock` throughout tuple
  iteration; ring-sourced pages are private buffers with no concurrent
  writers, so no lock is needed.
- **No memory leak**: `Close` / `releasePinned` always releases pool slots.

## 4. Tests (`internal/storage/scan_ring_test.go`)

| Test | Coverage |
|---|---|
| `TestScanRingBasic` | AcquirePage / ReleasePage / Close lifecycle |
| `TestScanRingCacheHitUsesPool` | Cache hits use pool without extra disk I/O |
| `TestScanRingCacheMissNoEviction` | **DoD**: 500-page scan in 100-slot pool → 100% of 90 hot pages preserved |
| `TestScanRingMultiBlock` | Ring cycles correctly past 100 blocks (> ScanRingSize) |
