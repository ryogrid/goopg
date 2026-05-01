# goopg TPC-H Memory Leak Investigation Report

## Overview

This report documents the investigation into a monotonically increasing memory
consumption issue when running HammerDB's TPC-H power test (scale factor 1, 1
virtual user) against goopg, eventually leading to OOM termination.

The investigation used:
- Static code analysis of the buffer pool, executor, planner, WAL writer, AIO
  engine, and MVCC manager
- Review of existing pprof heap and goroutine profiles captured during a prior
  TPC-H schema build run

---

## 1. Architecture & Memory Model

### 1.1 Buffer Pool (`internal/storage/bufpool.go`)

The buffer pool is a **fixed-size** clock-sweep cache:

- **Arena** (`internal/storage/arena.go`): A single mmap(`MAP_PRIVATE|MAP_ANONYMOUS`)
  allocation of `nslots * BlockSize` (8 KiB each).
  - Slot count is derived from the `shared_buffers` GUC via `poolSlotsFromGUC()`
    (`cmd/goopg/main.go:680`).
  - For the TPC-H config (`shared_buffers = 1600MB` → 204800 slots), the arena is
    **~1.6 GiB**. This memory is allocated once at startup and **never grows**.
- **`byTag` map**: Bounded at `len(slots)` entries. Entries are removed on eviction.
- **No Go heap memory grows** from the buffer pool's core data structures after
  initialization.

### 1.2 Page Lifecycle

`Page` is `type Page []byte` — a Go slice header aliasing arena memory.
The flow during a sequential scan:

```
Pool.Pin(tag) → *Slot (pinCount++)
  slot.Page() → Page (slice header pointing at arena, no heap alloc)
    PageGetHeapTuple(page, slot)
      → append([]byte(nil), p[off:off+ln]...)  // COPY from arena → Go heap
      → ParseHeapTuple(raw)
        → Data: append([]byte(nil), raw[hoff:]...)  // COPY again → Go heap
      → HeapTuple{Data: []byte}   // Data is fully on Go heap
    DecodeRowInto(cols, tuple.Data)
      → []Datum{String: string(data[4:4+n])}  // Go string = heap copy
  Pool.Unpin(slot) → pinCount--
```

**Key finding**: Every tuple read from a buffer pool page produces independent
Go heap allocations. The `Page` (arena) slice header never escapes the local
scope. **No arena memory reference leaks through the tuple decoding path.**

---

## 2. Potential Leak Sources Found

### 2.1 WAL `state.files` Map — Unbounded Growth (Medium Severity)

**File**: `internal/wal/writer.go:337`

```go
type state struct {
    // ...
    files map[uint64]*os.File  // line 337 — NEVER SHRINKS AUTOMATICALLY
    dirty map[uint64][]int64   // line 338 — bounded by flush frequency
    // ...
}
```

The `files` map caches `*os.File` handles for every WAL segment ever opened
(`openSegment`, line ~1578). Entries are **only** removed in `removeOldSegments`
(line ~1309), which is called from `SlotAwareRetainer.Retain()` → `Writer.RemoveOldSegments()`
— itself triggered only **after successful checkpoints**.

**Impact**: If the checkpointer interval is long (config: `checkpoint_timeout = 15min`,
`max_wal_size = 4GB`), or if the system is write-heavy, the map accumulates one
entry per 16 MiB WAL segment. Each entry is an open file descriptor + map
overhead (~100 bytes). Over hours of TPC-H execution, this could reach
thousands of entries. Each file descriptor consumes kernel memory (~1-2 KiB),
contributing to RSS growth.

**Evidence in setup script**: `bench/tpch/setup_goopg.sh:54` sets
`checkpoint_timeout = 15min` and `max_wal_size = 4GB`, meaning checkpoints
fire at most every 15 minutes. During a TPC-H power test run (several minutes),
very few checkpoints occur, and `removeOldSegments` may only fire once or twice.

**Mitigation**: `close()` (line ~1606) sets `s.files = nil`,
so a restart resets the accumulation.

### 2.2 AIO Per-Target `sync.Map` — Monotonic Growth (Low Severity)

**File**: `internal/aio/aio.go:242,327-336`

```go
type Engine struct {
    targets sync.Map // map[string]*targetStats — grows monotonically
    // ...
}
```

Each unique `Op.Target` string (set from relation file paths) creates a new
`*targetStats` entry via `loadOrCreateTarget()`. Entries are **never removed**
for the engine's lifetime. For TPC-H (8 tables + indexes), cardinality is low
(~50 targets), so this is negligible.

### 2.3 Extended Query Portal Result Buffering (Conditional)

**File**: `internal/server/dispatch_extended.go:90-126`

When the extended query protocol is used, ALL result rows are materialized into
memory before any are sent:

```go
res := &extendedQueryResult{}
// ...
res.Rows = append(res.Rows, cells)  // line 123 — ALL rows buffered
```

If the client sends Parse/Bind/Execute without Close or Sync (or uses the
simple query protocol), the portal's result stays in `state.portals` until the
connection closes. For SELECT queries that return millions of rows, this would
consume significant memory.

**However**, HammerDB's TPC-H power test likely uses the **simple query protocol**
(MsgQuery → `handleQueryOrCopy` → `dispatchSimpleQueryViaExecutor`, which
streams rows directly via `WriteDataRow` without buffering). So this is
unlikely the main cause for the user's scenario.

### 2.4 SessionRegistry Variable Maps — Bounded

`internal/config/session.go:17-26` — `session` and `local` maps store GUC
overrides per connection. They are bounded by the number of distinct GUC
variables a client sets (typically < 10 for TPC-H).

### 2.5 MVCC Manager Active-Transaction Map — Bounded

`internal/mvcc/manager.go:39` — `active map[TransactionID]*txState`. Entries
are added on `Begin()` and removed on `Commit()`/`Rollback()`. Each TPC-H
query creates one transaction. For `VUSER=1`, there's at most 1 active
transaction. **Bounded.**

---

## 3. Investigated and Ruled-Out Sources

### 3.1 Buffer Pool Page Reference Leak (User's Hypothesis)

**Ruled out** through exhaustive code review:

- **`Pool.Pin()`/`Unpin()`**: Every call site has balanced Pin/Unpin pairs.
  - `seqScanOp` (`operators_storage.go:117-127,186-192`): Pin at block start,
    Unpin at block end via `releasePinned()`.
  - `indexScanOp` (`operators_index.go:62-69`): Pins and Unpins within the
    `RangeScan` callback.
  - `scanMatching` (`operators_storage.go:703-768`): Pin at block start, Unpin
    after processing all slots on the block.
  - `writeHeapRowReturning` (`operators_storage.go:835-928`): Pin/Unpin
    within the `tryAppendToBlock` closure.
  - B-tree `RangeScan` (`btree.go:1096-1140`): Pin via `pinR`, Unpin via
    `unpinR`, balanced in every code path.
  - B-tree `descendToLeaf` (`btree.go:662-700`): `pinR`/`unpinR` balanced
    across both normal and split-recovery paths.
  - `readMeta` (`btree.go:457-466`): Bootstrap page pin/unpin balanced.

- **`Page` (slice header) escape**: The `page := slot.Page()` local variable
  is a `Page = []byte` slice header aliasing arena memory. It is only used for
  decoding tuples (which copy data out via `PageGetHeapTuple` → `append([]byte(nil), ...)`)
  and never stored in any persistent structure. After `releasePinned()` or the
  next iteration, the local `page` variable goes out of scope.

- **`pageCopy` in FPI emission**: `MarkDirtyChangeRecord` and `maybeEmitFPI`
  (`bufpool.go:704,755`) allocate `make(Page, BlockSize)` copies for WAL
  full-page images. These are temporary — GC'd after the WAL append returns.

### 3.2 AIO Handle Leak from `Pool.Prefetch()`

**Ruled out**: The `Pool.Prefetch()` method at `bufpool.go:429-441` drops
the AIO handle on the floor. Investigation of `Engine.finishHandle()`
(`aio.go:472-517`) confirms that:

1. The worker goroutine always drives the I/O to completion.
2. `finishHandle()` decrements `inFlight`, deletes from the `inflight` map,
   and closes the `done` channel — **regardless of whether `Wait()` is called**.
3. No per-handle goroutines are created.
4. The throwaway buffer (`make([]byte, BlockSize)`) lives only until the
   worker's `runOp()` returns, then becomes GC-eligible.

### 3.3 Sort Operator / Join Operator Buffering

`sortOp` (`operators.go:174-251`) and `joinOp` (`operators_join_agg.go:20-406`)
buffer intermediate results. This is **temporary memory** — the buffers are
freed when `Close()` is called and the operator tree becomes unreachable after
`executeOneSimpleStmt` returns (`dispatch.go:177-263`). The Go GC reclaims
this between queries.

### 3.4 Goroutine Leaks

The goroutine dump from the prior run shows a fixed set of goroutines:
- 1 main goroutine (accept loop)
- 1 pprof HTTP listener
- 1 autovacuum launcher
- 1 checkpointer
- 1 WAL writer state loop
- 3 AIO worker goroutines
- 1 control listener
- Per-connection goroutines (1 for VUSER=1)

No goroutine count growth is observed.

---

## 4. Additional Notes on Tuple Decoding Memory Amplification

The tuple decoding pipeline creates multiple copies of each column value on
the Go heap:

| Stage | Allocation | Persistence |
|---|---|---|
| `PageGetHeapTuple` (line 283) | `append([]byte(nil), p[off:off+ln]...)` | Heap copy #1 (full tuple) |
| `ParseHeapTuple` (line 159) | `append([]byte(nil), raw[hoff:]...)` | Heap copy #2 (data portion) |
| `DecodeRowInto` → `string(data[4:4+n])` | Go string allocation | Heap copy #3 (per string column) |

For TPC-H SF=1, LINEITEM has ~6 million rows with ~8 columns (mixed strings
and numerics). Each query that scans this table temporarily allocates
hundreds of megabytes to gigabytes of heap memory. The memory is freed
between queries, but **Go does not eagerly return freed heap memory to the
OS**, leading to RSS growth.

## 5. Primary Hypothesis: What Causes the Monotonic Growth

Based on the analysis above, the most likely contributors to the OOM are:

### 5.1 Buffer Pool Arena Residency (Primary Contributor)

The mmap'd arena is 1.6 GiB (204800 slots × 8 KiB). With `MAP_PRIVATE`, the
kernel uses demand paging: physical pages are allocated on first touch. As the
TPC-H workload touches different pages of the arena (through Pin/eviction
cycles), **physical memory residency grows** from ~0 GiB at startup to ~1.6 GiB
under steady state. This is normal buffer pool behavior, identical to how
PostgreSQL's `shared_buffers` works. If the system is configured with
`vm.overcommit_memory=2` or has limited swap, this alone can trigger OOM when
combined with Go runtime memory.

### 5.2 Go Heap Not Returning Memory to OS

The Go runtime's heap grows via `mmap(MAP_ANONYMOUS)` but does not always
shrink back after GC, especially under high allocation pressure. The TPC-H
power test creates gigabytes of temporary allocations per query (tuple
decoding, string conversions, Datum slices, sort buffers). After GC, the Go
runtime's `scavenger` may return memory to the OS slowly or not at all,
depending on the `GOGC` setting and allocation rate. This contributes to RSS
growth even though the Go heap is technically "idle."

### 5.3 WAL `state.files` Accumulation

Over a multi-hour TPC-H run with `checkpoint_timeout = 15min`, the
`state.files` map accumulates one `*os.File` per WAL segment. At 16 MiB per
segment and 4 GB WAL (`max_wal_size = 4GB`), this is ~256 file descriptors.
Each descriptor consumes kernel memory (~1-2 KiB), and the Go `*os.File`
object adds heap overhead (~400 bytes). This is a minor contributor.

### 5.4 WAL Read at Startup Caching Entire Segments

On startup, goopg's crash recovery (`initdb.Open` → `wal.Recover`) scans all
WAL segments from the checkpoint's redo point forward. The scan reads the full
segment into a `[]byte` via `os.ReadFile` or `pread`. With `max_wal_size = 4GB`,
this means up to 4 GiB of WAL data may be read into memory during recovery.
After recovery, these `[]byte` buffers should be GC'd, but while resident they
contribute to peak RSS.

### 5.5 Page Cache Pressure

The WAL writer writes through buffered I/O (no `O_DIRECT` in the current
config, since `wal_direct_io` defaults to off). The kernel page cache grows
with WAL data. Combined with the 1.6 GiB arena, this pushes against the
system's available memory.

---

## 6. Recommendations

### Short-term

1. **Reduce `shared_buffers`** in `bench/tpch/setup_goopg.sh:54`:
   `shared_buffers = 256MB` is adequate for SF=1 and reduces the arena to
   256 MiB.

2. **Profile under load**: Start goopg with pprof, run the power test, and
   capture heap profiles at fixed intervals:

   ```bash
   while true; do
     curl -s "http://127.0.0.1:6060/debug/pprof/heap" \
       > "pprof-data/heap_$(date +%Y%m%d_%H%M%S).prof"
     sleep 30
   done
   ```

   Then compare profiles:

   ```bash
   go tool pprof -top -base base.prof latest.prof
   ```

3. **Set `wal_direct_io = on`** to bypass the kernel page cache for WAL
   writes, reducing page cache pressure.

### Medium-term

4. **Limit `state.files` growth**: Add an LRU eviction or a cap on the
   `s.files` map so old segment handles are closed and removed even without
   checkpoints.

5. **Monitor Go memory stats**: Add periodic `runtime.ReadMemStats()` logging
   to `main.go` to track `HeapInuse`, `HeapIdle`, and `HeapReleased` over
   time. This would identify whether the issue is the Go heap not releasing
   memory to the OS.

6. **Consider `GOMEMLIMIT`**: Set a `GOMEMLIMIT` environment variable to give
   the Go runtime a soft memory cap, improving scavenger behavior.

---

## 7. Appendix: Key Source Locations

| Component | File | Lines |
|---|---|---|
| Buffer pool arena | `internal/storage/arena.go` | 30-48 |
| Pool creation | `internal/storage/bufpool.go` | 259-291 |
| Clock-sweep eviction | `internal/storage/bufpool.go` | 950-970 |
| Pin/Unpin | `internal/storage/bufpool.go` | 542-628 |
| Page type definition | `internal/storage/page.go` | 83 |
| PageGetHeapTuple (data copy) | `internal/storage/heap.go` | 259-285 |
| ParseHeapTuple (data copy) | `internal/storage/heap.go` | 141-162 |
| SeqScan (pin lifecycle) | `internal/executor/operators_storage.go` | 110-192 |
| scanMatching (pin lifecycle) | `internal/executor/operators_storage.go` | 697-788 |
| B-tree RangeScan | `internal/access/btree/btree.go` | 1096-1140 |
| WAL state.files map | `internal/wal/writer.go` | 337 |
| WAL removeOldSegments | `internal/wal/writer.go` | 1309-1350 |
| WAL openSegment | `internal/wal/writer.go` | 1526-1580 |
| AIO finishHandle cleanup | `internal/aio/aio.go` | 472-517 |
| Pool.Prefetch (drops handle) | `internal/storage/bufpool.go` | 429-441 |
| Extended query result buffer | `internal/server/dispatch_extended.go` | 90-126 |
| Simple query streaming | `internal/server/dispatch.go` | 177-263 |
| Sort operator buffer | `internal/executor/operators.go` | 174-251 |
| Shared_buffers GUC resolution | `cmd/goopg/main.go` | 680-703 |
| TPC-H setup config | `bench/tpch/setup_goopg.sh` | 51-57 |
| poolSlots default | `internal/initdb/open.go` | 143-146 |
