# 0005 — Buffer Manager and Storage Manager (v0)

- **Status:** accepted
- **Date:** 2026-04-28
- **Supersedes:** —

## Context

Milestone 5 in `.ralph/fix_plan.md` is the storage layer. The buffer
manager is the foundation: every later piece (heap, btree, MVCC, WAL)
ends up calling `Pin(blockNum) → page; mutate; MarkDirty; Unpin`. This
doc covers the buffer pool plus the `smgr` (storage manager) layer it
sits on. The on-disk page format is in `0006-storage-format.md`.

References into upstream:

- `postgres/src/backend/storage/buffer/bufmgr.c` — `ReadBuffer_common`,
  `BufferAlloc`, `PinBuffer`/`UnpinBuffer`.
- `postgres/src/backend/storage/buffer/freelist.c:196` —
  `StrategyGetBuffer` (the clock-sweep victim picker).
- `postgres/src/backend/storage/smgr/md.c` — magnetic-disk smgr, the
  primary mapping of (relfilenode, fork, blocknum) → file:offset.
- `postgres/src/include/storage/bufpage.h:159` — `PageHeaderData`.
- `postgres/src/include/pg_config_manual.h` — `BLCKSZ` (default 8 KB).

## Decision

### Scope of v0

Land:

1. **smgr layer**: `internal/storage` exposes a typed `RelFileNode`
   (database OID + relation OID + fork number), a per-relation file
   handle that opens with `O_DIRECT | O_DSYNC` for primary data, and
   block-aligned `ReadBlock`/`WriteBlock`/`Extend`/`Sync` operations.
2. **Page**: a fixed 8192-byte aligned slice plus a typed
   `PageHeader` matching upstream's `PageHeaderData` byte-for-byte.
3. **Buffer manager**: a fixed-size pool of pre-allocated, page-
   aligned buffer slots; a (RelFileNode, BlockNumber) → slot hash
   table; pin counts; dirty bit; LSN; and a clock-sweep victim
   selector. `Pin`/`Unpin` are the public API.

Do **not** land yet:

- WAL ordering enforcement (no LSN compare against flushed-WAL
  position; that lands in `0008-wal-and-recovery.md`).
- Background writer / checkpointer goroutines (also milestone-5
  follow-ups).
- Multi-backend concurrent I/O coordination (we do hold the slot's
  content mutex during the disk read, but a v0 single-writer flow is
  acceptable for the unit-test smoke; the lock manager arrives later).
- Replication, WAL-streaming, prefetch.

The seam is small enough that those additions are case-arm/method
extensions, not redesigns.

### Page layout

`Page` is a `[]byte` of length `BlockSize = 8192`, allocated from a
pool that returns blocks aligned to 4096 bytes (the standard Linux
direct-I/O alignment requirement on ext4/xfs). Its leading bytes are
the upstream `PageHeaderData` shape (matching `0006-storage-format.md`).
Buffers are pinned to backing memory by being slices into a single
contiguous arena allocated via `unix.Mmap` (anonymous private), so:

- The arena is naturally page-aligned (the kernel returns 4 KiB
  alignment from anonymous mmap).
- We can carve fixed-size, fixed-position slots that share a single
  backing allocation, keeping GC pressure low.
- The arena is held by the buffer pool for its lifetime; we don't
  munmap until shutdown.

A safety net: if `unix.Mmap` is unavailable for any reason in tests
(e.g. seccomp-restricted CI), the pool falls back to a Go-allocated
arena and trades the alignment guarantee for portability. The fallback
is gated by a build flag we'll wire later; v0 always tries mmap first.

### Storage manager (smgr)

`smgr.Manager` holds a map from `RelFileNode` to `*relFile` (an open
file). `relFile` opens lazily on first access:

- Path: `<DataDir>/base/<dbOid>/<relOid>` for the main fork.
  Additional forks (`fsm`, `vm`) get suffix paths
  (`<relOid>_fsm`, `<relOid>_vm`) — recognised but not used in v0.
- Open flags: `O_RDWR | O_CREATE | O_DIRECT | O_DSYNC`.
- I/O is `pread`/`pwrite` at `int64(blockNum) * BlockSize`, requiring
  the supplied buffer to be 4 KiB-aligned (the buffer pool guarantees
  this).
- `Extend` zero-fills a fresh block at the end of the file, used
  during heap insert and btree split.
- `Sync` issues `fdatasync(2)` on the file. WAL-driven flush ordering
  is the buffer manager / WAL writer's job; smgr just exposes the
  primitive.

Sizes are tracked in-memory after an initial `lseek(SEEK_END)` to
avoid re-stat'ing on every block read. `NBlocks()` returns the cached
size in blocks.

### Buffer manager

The pool is a slice of `slot`s, sized at construction (`shared_buffers`
GUC arrives later; v0 takes a `Slots int` config knob).

```go
type slot struct {
    page     []byte // alias into the mmap'd arena
    tag      bufferTag // (RelFileNode, ForkNumber, BlockNumber)
    valid    bool
    dirty    bool
    pinCount int32 // mutated under poolMu while looking up; atomic for fast paths
    lsn      uint64
    usageCount uint8 // 0..maxUsageCount, decremented by clock sweep
    contentMu sync.RWMutex // held during read/write of page bytes
}
```

The pool maintains:

- `poolMu sync.Mutex` — guards `slots[*].tag/valid/dirty` and the
  `byTag` lookup map.
- `byTag map[bufferTag]int` — slot index for each loaded page.
- `clockHand int` — clock-sweep cursor.

The public API:

```go
func (b *Pool) Pin(tag BufferTag) (*Slot, error)
func (b *Pool) Unpin(s *Slot)
func (b *Pool) MarkDirty(s *Slot)
func (b *Pool) FlushAll() error
```

`Pin` looks up `byTag`. On hit, increment pinCount + bump usageCount,
return the slot. On miss, run clock-sweep until a slot with
`pinCount==0 && usageCount==0` is found; if dirty, write it out
through smgr; reassign its tag; read the requested page from smgr
into the slot; mark valid; pinCount=1; usageCount=1; return.

The clock sweep advances `clockHand`, and decrements usageCount for
unpinned slots until it finds one at zero. Bounded retry: a fully
pinned pool returns `ErrNoBuffer` rather than spinning. Upstream uses
the same algorithm (`freelist.c:196`).

`Unpin` decrements pinCount; if it would go negative we panic
(programming bug, never recoverable).

`MarkDirty` flips the dirty bit. The actual flush happens when the
slot is evicted, when `FlushAll` is called (checkpoint), or when an
explicit `Sync(tag)` is requested.

### Concurrency model

- The pool mutex guards lookup, pin-count adjustments during lookup,
  and slot reassignment. It is held briefly — read/write I/O happens
  outside it under `slot.contentMu`.
- Once a slot is pinned, the slot's content mutex is the right lock
  for read/write of the page bytes. Heap and index access methods
  layer their own row-level locking on top.
- Pin/unpin is goroutine-safe; multiple goroutines may pin the same
  page simultaneously and read it concurrently.

### Sizing and alignment

- `BlockSize = 8192` (fixed at compile time, matching upstream's
  default).
- Mmap arena size = `Slots * BlockSize`. With the upstream
  `shared_buffers` default of 128 MB the arena is 16 384 slots —
  large but unremarkable for an mmap.
- Direct I/O alignment requirement on Linux ext4/xfs: file offset,
  buffer address, and transfer size must each be multiples of the
  device's logical block size. 4 KiB is the universal safe value;
  ours is 8 KiB, which is always a superset.

### Failure modes

- **mmap fails at startup** — return an error from `NewPool`. Server
  refuses to start.
- **smgr read short / EOF** — the page table holds an invalid slot;
  the slot is reassigned. Caller sees `ErrShortRead`.
- **smgr write short** — return an error from `MarkDirty`'s eventual
  flush; caller of `FlushAll` propagates. We don't auto-retry.
- **dirty page lost during eviction** — never. Eviction synchronously
  writes-out before reassignment. The cost is paid by the evicting
  goroutine, matching upstream.

### What this doc does NOT cover

- The page-format details (header fields, line pointers, free space).
  → `0006-storage-format.md`.
- Tuple visibility / `xmin`/`xmax` semantics. → `0007-mvcc-and-snapshots.md`.
- WAL ordering (the "thou shalt write xlog before data" rule).
  → `0008-wal-and-recovery.md`.
- Background writer / checkpointer goroutines.
- Async I/O / buffered prefetch.
- The buffer-access strategy ring used by sequential scans, vacuum,
  copy. Punted until we have queries that benefit; the contract is
  built so the strategy can wrap `Pin` later.

## Alternatives Considered

- **Use a third-party page cache (`bbolt`, `pebble`, etc.).**
  Rejected: those carry their own page formats and durability
  contracts. We need PostgreSQL's page format, ACID semantics, and
  WAL discipline; building those on top of someone else's cache
  gains nothing and obscures the seam.
- **Skip O_DIRECT for v0; use the OS page cache.** Rejected: the
  spec (§5.2) requires `O_DIRECT` for primary data files. Building
  a buffer pool that doesn't enforce alignment now means
  retrofitting every later test once we flip the flag. Cheaper to
  start aligned.
- **Fixed-size slot table vs. dynamic.** A dynamic table would let
  us grow `shared_buffers` at runtime, which upstream doesn't allow
  either. Fixed-size also keeps the slot index a stable `int`
  rather than a pointer that can move. Pin everything to a single
  preallocated arena.
- **Skip the clock-sweep; use plain LRU.** Clock-sweep is what
  every libpq client / pgbench tuning guide assumes is happening
  underneath, and it composes well with the access-strategy ring
  we'll add later. LRU would have to be replaced by clock-sweep
  before milestone 5's tail anyway.

## Consequences

- Every later storage piece pins through this manager. Heap inserts
  pin the target block, mutate, MarkDirty, unpin. Btree descent
  pins one page at a time. Recovery pins/unpins as it replays.
- The mmap-backed arena means GC pressure from page bytes is zero;
  only the slot metadata and the lookup map churn.
- Direct I/O ties us to ext4/xfs (and similar) on Linux. macOS /
  Windows don't honour O_DIRECT — out of scope by spec §3.4.
