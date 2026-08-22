# Module: `internal/storage`

The buffer-manager + storage-manager layer — a faithful Go port of PostgreSQL's
`src/backend/storage/{buffer,smgr,page,lmgr}`. It owns the buffer pool
(lock-free 8 KiB page cache with clock-sweep eviction and WAL-before-data
flushing), the storage manager (one `os.File` per relation fork, with an
optional io_uring AIO seam), the PG-18 byte-identical heap page/tuple layer, the
visibility-map and free-space-map forks, and the lock manager (`lmgr`).

## Key Files

- `bufpool.go` (2,778) — `Pool`/`Slot`: lock-free pinning, clock-sweep eviction,
  `MarkDirty*` family, FPI emission, WAL flush barrier, flush batches.
- `smgr.go` (1,012) — `Manager`/`relFile`: fork path resolution, read/write/
  extend/sync, checksums, AIO seam.
- `heap.go` (2,238) — heap tuple marshal/parse, line-pointer ops,
  `PageAdd/Get/Remove*`, infomask mutators, HOT stamps, WAL-redo helpers.
- `page.go` — `PageHeader` accessors (LSN, flags, free space), `RelFileNode`/`BufferTag`.
- `fsm.go` / `fsm_fork.go` — in-memory free-space map + PG-format fork persistence.
- `vm.go` / `vm_fork.go` / `vm_redo.go` — visibility map (ALL_VISIBLE) + fork save/load.
- `prune.go` / `freeze.go` — page prune (HOT redirects, LP_DEAD) and tuple freeze.
- `checksum.go` — FNV-1a page checksum (`checksum_impl.h` port).
- `bgwriter.go` / `scan_ring.go` — dirty-page background flusher; bulk-read scan ring.
- `lmgr/lockmgr.go` / `lmgr/deadlock.go` — 8-mode lock manager + wait-for-graph detection.
- `aio/aio.go` / `aio/method_iouring_linux.go` / `aio/read_stream.go` — async-I/O
  engine (raw io_uring syscalls + worker-thread fallback) and prefetch streams.
- `file/pgtemp.go` — `pgsql_tmp` spill-file directory convention + crash sweep.

## Public API

Buffer pool (`bufpool.go`):

```go
func NewPool(mgr *Manager, cfg PoolConfig) (*Pool, error)
func (p *Pool) Pin(tag BufferTag) (*Slot, error)
func (p *Pool) PinNew(rel RelFileNode) (*Slot, BlockNumber, error)
func (p *Pool) Unpin(s *Slot)
func (p *Pool) MarkDirty(s *Slot) / MarkDirtyWithLSN(...) / MarkDirtyLogicalChange(...)
func (p *Pool) FlushAll() error / FlushRel(rel) error
func (p *Pool) WriteDirtyPages(maxPages int) int
type WALFlusher interface { FlushUpTo(lsn uint64) error; WalRecords() int64; WalBytes() int64 }
```

Storage manager (`smgr.go`):

```go
func NewManager(cfg ManagerConfig) *Manager
func (m *Manager) ReadBlock(rel, blk, buf) error
func (m *Manager) WriteBlock(rel, blk, buf) error
func (m *Manager) Extend(rel, buf) (BlockNumber, error)
func (m *Manager) NBlocks(rel) (BlockNumber, error)
func (m *Manager) Sync(rel) / SyncAll() / DropRelation(rel) / TruncateRelation(rel, n)
type AIOEngine interface { Submit(op AIOSubmitOp) AIOHandle }
```

Heap/tuple (`heap.go`):

```go
func NewHeapTuple(xmin, xmax TransactionID, data []byte) HeapTuple
func ParseHeapTuple(raw []byte) (HeapTuple, error)
func PageAddHeapTuple(p Page, t HeapTuple) (uint16, error)
func PageGetHeapTuple(p Page, slot uint16) HeapTuple
func PageRemoveHeapTuple(p Page, slot uint16)
func PageSetHeapTupleXmax(p, slot, xmax) / PageSetHeapTupleLockOnly(...)
func PageStampHotOldTuple(...) / PageSetItemIDRedirect(p, slot, target)
```

Lock manager (`lmgr/lockmgr.go`):

```go
func New() *LockManager
func (lm *LockManager) Acquire(ctx, b BackendID, t LockTag, m Mode) error
func (lm *LockManager) TryAcquire(...) / Release(b, t, m) / ReleaseAll(b)
func ParseMode(name string) (Mode, bool)
func ConflictsWith(m Mode, held Mask) bool
```

## Internal structure

`Pool` owns a 4 KiB-aligned `arena` carved into `BlockSize` slices aliased by
`Slot.page`. Each `Slot` packs `pinCount(22b) | usageCount(8b) | dirty | valid |
ioInflight | gen(15b)` into one atomic word — pin/unpin/usage CAS are fully
lock-free. Tag→slot lookup goes through `bufmap`, a lock-free open-addressing
hash table with tombstone slots and generation counters (ABA defense). The
cache-miss path takes `pinMu`, claims a victim via the clock hand, flushes dirty
victims, then reads under `contentMu`. `aio` is self-contained; `lmgr` holds a
`lockState` per `LockTag` with a FIFO waiter queue and a per-backend deadlock
timer.

## Dependencies

- **Uses** `internal/access/transam/xlog`, `internal/port/runtimeshim`,
  `internal/utils/activity/stats`, `internal/utils/misc`; `lmgr` uses
  `lmgr/lockwait`.
- **Used by** ~148 files: `executor` (every DML/DDL operator), `access/nbtree`,
  `access/transam`, `access/amcheck`, `catalog`, `commands/vacuum`, `initdb`,
  `postmaster`. Storage does **not** import `aio` — it consumes it via the narrow
  `AIOEngine` interface.

## Notable patterns / gotchas

- **Clock-sweep eviction with second-chance** — dirty-victim flush happens
  *before* `bmDelete`, so a concurrent `Pin(oldTag)` can't race a fresh disk read.
- **WAL-before-data at write time** — `flushSlot` flushes WAL to
  `max(pd_lsn, hintFlushBarrier)` before every data write.
- **FPI storm suppression** — the FPI watermark lives on `Slot.nativeImageLSN`
  (not `pd_lsn`), and `evictedImageLSN` preserves it across eviction; without it
  a hot catalog page re-imaged 19k times in one run (97.5% of WAL).
- **PG byte-compat is load-bearing** — `SizeOfPageHeaderData=24`, `BlockSize=8192`,
  tuple header 23 B, `DefaultHeapTupleHoff=24`, LSN high-32 at offset 0 /
  low-32 at offset 4, FNV-1a checksum with `+1` so zero means "no checksum".
- **FSM fork format** — PG `fsm_internals.h` binary tree (256 categories × 32 B);
  written atomically (temp+rename), re-loaded at startup.
- **VM fork** — 2 bits/heap page (only ALL_VISIBLE is set), redo mutates the
  on-disk fork directly because recovery runs before `Runtime.VM` is populated.
- **`pd_lsn` completeness guard** — plain `MarkDirty` is report-only under
  `GOOPG_PDLSN_ASSERT=1`; every logged mutation must use a stamping variant.
- **Sibling twins to keep in sync** — heap encode ↔ decode; FSM/VM runtime ↔
  fork persistence ↔ redo; `PagePruneOpt` ↔ `PageVacuumPrune`; sync I/O ↔ AIO.
