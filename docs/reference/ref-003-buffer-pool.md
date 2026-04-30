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

## PostgreSQL Implementation

PostgreSQL's buffer manager (`bufmgr.c`) is conceptually similar
but more sophisticated:

- **Buffer descriptors** — each buffer has a `BufferDesc` with
  `tag`, `state` flags (dirty, valid, IO-in-progress, etc.),
  `refcount` and `usage_count`. Locked via `buffer_header_lock`
  (atomic operations on state flags).
- **Strategy ring** — sequential scans use a small (256 KB) ring
  of buffers to avoid evicting the entire working set.
- **Bgwriter** — a dedicated background writer (`bgwriter.c`)
  flushes dirty buffers proactively, reducing checkpointer I/O.
- **Checkpointer** — writes all dirty buffers during a checkpoint
  and fsyncs the WAL. Unlike goopg, the checkpointer and bgwriter
  are separate processes.

### Key Differences

| Aspect | goopg | PostgreSQL |
|--------|-------|------------|
| Eviction algorithm | Clock sweep (single hand) | Clock sweep with strategy rings |
| Background flush | `FlushAllPaced` (in-band) | Dedicated bgwriter process |
| Content lock | `sync.Mutex` + `RLock` on each Slot | `content_lock` per BufferDesc |
| IO-in-progress flag | Implicit (slot is not in byTag) | Explicit `BM_IO_IN_PROGRESS` flag |
| Pin count | `int` under poolMu | `refcount` (atomic) |
| Buffer descriptor | `Slot` struct (mutex + page) | `BufferDesc` (atomic flags) + separate `Buffer` pointer |

## Potential Optimisations or Corrections

- **Strategy ring** for sequential scans would prevent a single
  large scan from evicting the entire cache.
- **Explicit IO-in-progress flag** would let concurrent readers
  wait for an in-progress read instead of both reading the same
  page from disk.

## References

- goopg: `internal/storage/bufpool.go`
- PG buffer manager: `postgres/src/backend/storage/buffer/bufmgr.c`
- PG local buffer: `postgres/src/backend/storage/buffer/localbuf.c`
- PG bgwriter: `postgres/src/backend/postmaster/bgwriter.c`
