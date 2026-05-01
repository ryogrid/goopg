# REF-003: Buffer Pool

## Overview

The buffer pool caches heap and index pages in memory, providing
concurrent access via pin/unpin with per-page content locks. It
handles page eviction (clock-sweep), prefetching, dirty-page
tracking, and WAL-before-data ordering.

## goopg Implementation

**Package:** `internal/storage/bufpool.go`

### Key Types

- `Pool` — the central buffer manager. Holds an array of `Slot`s,
  a hash table mapping `BufferTag` → slot index, and an eviction
  clock hand.
- `Slot` — a single buffer. Contains `Page` (8 KiB byte array),
  `tag` (relation+block), `pinCount`, `usageCount`, `dirty` flag,
  and `contentMu` (sync.Mutex + RWMutex).
- `BufferTag` — identifies a page: `{DB, Rel, Block}`.
- `WALFlusher` interface — abstracts `FlushUpTo` for WAL ordering.

### Data Flow (Page Read)

```
Pool.Pin(tag)
  ├─ Lookup byTag map (poolMu)
  ├─ if hit: pinCount++, usageCount++, return slot
  └─ if miss:
       ├─ evictLocked() — clock-sweep with usageCount
       ├─ if victim is dirty: flushSlot(oldTag)
       ├─ readBlock from disk into slot
       └─ register in byTag, pinCount=1
```

### Eviction (Clock Sweep)

`evictLocked()` walks slots sequentially (`clockHand`). For each
slot with `pinCount == 0`:
- If `usageCount > 0`: decrement usageCount, continue.
- If `usageCount == 0`: evict this slot.

### Dirty Page Tracking

- `MarkDirty(slot)` — sets `dirty = true`.
- `MarkDirtyChangeRecord(slot, emitter)` — emits a WAL change
  record via the emitter callback, then marks dirty.
- `FlushAllPaced(notify)` — writes all dirty slots through
  `smgr.WriteBlockAIO` (batch AIO submit + wait), then calls
  `FlushUpTo` for WAL durability.

### Concurrency Model

- `poolMu` — guards byTag map, slot metadata (tag, valid, dirty,
  pinCount, usageCount), and clockHand.
- `Slot.contentMu` — `sync.Mutex` for exclusive page access
  (write), `RLock` for shared access (read). Writers serialise on
  the same page; readers can proceed in parallel.
- `Pin` drops `poolMu` before acquiring `contentMu` to avoid
  lock-order inversions.

## PostgreSQL Implementation (Deep Dive)

PostgreSQL's buffer manager (`bufmgr.c`) is substantially more
sophisticated than goopg's.

### Buffer Descriptors with Atomic Flags

Each buffer is described by a `BufferDesc` struct containing:

- `tag` — the `BufferTag` (relation + block).
- `state` — a `pg_atomic_uint32` bitfield encoding:
  - `BM_DIRTY` (bit 0) — page is dirty.
  - `BM_VALID` (bit 1) — page content is valid.
  - `BM_TAG_VALID` (bit 2) — tag is set.
  - `BM_IO_IN_PROGRESS` (bit 3) — I/O is in progress.
  - `BM_JUST_DIRTIED` (bit 4) — dirtied during I/O.
  - `BM_PIN_COUNT_WAITER` (bit 5) — someone is waiting for pin.
  - `BM_CHECKPOINT_NEEDED` (bit 6) — checkpoint needed.
  - `refcount` — number of pins (atomic, no mutex).
  - `usage_count` — clock-sweep counter.
  - `content_lock` — per-buffer LWLock (lightweight lock) for
    exclusive/shared page access.

The atomic `state` field lets PostgreSQL check and set flags
without acquiring a global mutex. In goopg, `poolMu` must be
held to read or write any slot metadata.

### IO_IN_PROGRESS Flag

When a backend needs to read a page from disk, it sets
`BM_IO_IN_PROGRESS` atomically _before_ releasing the buffer
mapping lock. Other backends that need the same page will see
the flag and wait on the buffer's `content_lock` instead of
issuing a duplicate read. This eliminates the "thundering herd"
problem. goopg does not have this flag — two concurrent pins for
the same missing page both issue disk reads.

### Strategy Ring

Sequential scans use a **strategy ring** — a small set of buffers
(typically 256 KB) that are recycled within the scan without
affecting the main buffer pool. This prevents a single large
sequential scan from evicting the entire working set. goopg's
SeqScan uses the main pool and can evict frequently accessed
pages.

### Bgwriter / Checkpointer Split

PostgreSQL splits background dirty-page writing into two processes:

1. **Bgwriter** (`bgwriter.c`) — continuously writes dirty buffers
   to disk at a moderate pace. This smooths I/O and keeps the
   checkpointer's work to a minimum.
2. **Checkpointer** (`checkpointer.c`) — runs periodically (default
   5 min) and writes all remaining dirty buffers. Because the
   bgwriter has been flushing continuously, the checkpointer's
   I/O burst is small.

goopg's `FlushAllPaced` combines both roles — it writes dirty
buffers during eviction and during checkpoints. There is no
continuous background writer.

### Buffer Access Rules

PostgreSQL enforces strict rules for buffer access:

- **Pin must be held** before accessing page content. goopg does
  the same.
- **content_lock (LWLock)** must be held in shared mode for
  reading and exclusive mode for writing. Multiple readers can
  hold the same page's content_lock simultaneously. goopg's
  `slot.RLock()` provides the same semantics.
- **Buffer descriptor lock** — a separate LWLock protects the
  descriptor's metadata (tag, flags). In goopg, `poolMu` protects
  all descriptors.

## goopg Improvement Analysis

### P1: IO_IN_PROGRESS Flag

Adding an `ioInProgress` flag (atomic bit on the Slot or a
separate flag) would prevent duplicate reads for the same page:

```go
if s.ioInProgress.CompareAndSwap(0, 1) {
    // We are the first — issue the read
    err = readFromDisk(s)
    s.ioInProgress.Store(0)
    s.contentMu.Unlock()
} else {
    // Someone else is reading — wait and retry
    s.contentMu.Unlock()
    s.contentMu.Lock() // will block until the reader finishes
    // page is now valid
}
```

**Impact:** Eliminates redundant disk reads under high contention
for the same page (e.g., multiple backends updating the same index
page).

### P1: Strategy Ring for Sequential Scans

Add a "small scan" mode to `SeqScan` that uses a dedicated ring
of 32 buffers instead of the main pool. Pages in the ring are
not added to the main `byTag` map, so they don't evict hot pages.

**Impact:** Prevents sequential scans (VACUUM, ANALYZE, COUNT(*))
from flushing the entire buffer pool.

### P2: Atomic Buffer Descriptor Flags

Replace `poolMu` for metadata operations with atomic bit
operations on each `Slot`. This would allow concurrent metadata
updates (e.g., pin and unpin) without a global lock.

**Impact:** Reduces `poolMu` contention under high concurrency.

### P2: Dedicated Bgwriter

Add a background goroutine that periodically flushes a small
number of dirty buffers (e.g., `FlushAllPaced` with a batch
limit of 16). This would smooth I/O and reduce checkpoint bursts.

## References

- goopg: `internal/storage/bufpool.go`
- PG buffer manager: `postgres/src/backend/storage/buffer/bufmgr.c`
- PG local buffer: `postgres/src/backend/storage/buffer/localbuf.c`
- PG bgwriter: `postgres/src/backend/postmaster/bgwriter.c`
- PG buf header: `postgres/src/include/storage/buf_internals.h`
