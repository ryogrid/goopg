# 07 — WAL Insert Striping + FSM-Driven Page Distribution

This chapter solves two related contention sources that together
caused the 23-minute c=100 simple-update livelock observed in
`analysis/perf-optimize/04-contention.md` §4.4:

1. **`wal.Writer.appendMu sync.Mutex`** — a single lock serialising
   every WAL record append (`internal/wal/writer.go:355`). Replaced by
   an 8-stripe `appendLocks [8]paddedMutex`, matching PG's
   `NUM_XLOGINSERT_LOCKS = 8` (`postgres/src/backend/access/transam/
   xlog.c:151,570`).
2. **Tail-page insert determinism in `pgbench_history`** — the heap-
   insert path (`internal/executor/operators_storage.go:2692-2859
   writeHeapRowReturning`) currently defaults to the relation's tail
   page when FSM returns no candidate. Under c=100 SU, 19 backends
   all converged on the same tail page; one of 128 `bufferPartition.mu`
   was held while the others queued. Replaced by an FSM-first path
   that consults the buf-mapping table ([[06-bufpool-lockfree]]) to
   skip pages with high pin counts, plus batch extension (multiple
   pages added at once) to spread concurrent inserters.

On-disk **WAL record format**, **heap page format**, and **FSM file
format** are all unchanged.

Cross-references: [[04-mvcc-procarray]] (procNum %-striping into the
8 WAL insert locks), [[06-bufpool-lockfree]] (FSM consults
`bufmap.Lookup` for hotspot avoidance).

## 1. Current state

### WAL insert serialisation

Verbatim from `internal/wal/writer.go:355`:

```go
type Writer struct {
    appendMu sync.Mutex
    // ... other fields
}
```

Every WAL record append takes `appendMu`. Even with group commit
(M0098-0002 at `writer.go:611+`) coalescing fdatasync calls, the
**insert** path serialises. At write rates of 350 TPS × ~5 records
per txn = 1 750 inserts/sec, the lock hold time is fine; at higher
rates or larger records the serialisation becomes meaningful.

`analysis/perf-optimize/04-contention.md` §4.7 ranks this as a "minor
(< 3 %)" contention source today, but it becomes load-bearing once
the upstream `mvcc.Manager.mu` is removed ([[04-mvcc-procarray]]) and
write throughput rises. The fix is cheap and pre-emptive.

PG counterpart: `postgres/src/backend/access/transam/xlog.c` defines
`NUM_XLOGINSERT_LOCKS = 8` (around line 151); the static
`WALInsertLocks[]` array is declared a few lines below the constant
(around line 154); the stripe selection (`MyProcNumber %
NUM_XLOGINSERT_LOCKS`) lives inside `WALInsertLockAcquire` /
`WALInsertLockAcquireExclusive`. (Exact line numbers drift between PG
minor versions; the implementer should grep the in-tree `postgres/`
for `NUM_XLOGINSERT_LOCKS` to locate current call sites.)

### Tail-page determinism

Verbatim flow from `internal/executor/operators_storage.go:2778-2831`
(paraphrased; the full function is 167 lines):

```go
// writeHeapRowReturning(...) — current control flow:
//   1. (lines 2706-2724) TOAST + encode + form heap tuple.
//   2. (lines 2778-2793) If fsm != nil, call fsm.GetPageWithFreeSpace.
//      If a candidate page is returned, attempt insert there.
//   3. (lines 2796-2809) Otherwise (or on FSM miss), try the relation's
//      tail page (nBlocks - 1). Retry up to 3 times if PageAddItem
//      returns out-of-space (after PagePruneOpt cleans dead tuples).
//   4. (lines 2811-2831) If tail page is full, acquire the heap-extend
//      lock (heapExtendLocks.LoadOrStore — one lock per relation), re-
//      check tail (another writer may have just extended), then PinNew
//      to allocate a fresh block at the end of the relation.
//   5. (lines 2833-2858) Insert into the newly-extended block; release
//      heap-extend lock; return.
```

The failure mode in the c=100 SU livelock:

- All 100 backends are doing `INSERT INTO pgbench_history`. The
  relation starts empty; after the first few inserts the tail page
  is at block N.
- FSM may or may not have N recorded; in either case, all writers
  target block N.
- 19 backends arrive at `Pool.Pin(tag{rel=history, block=N})` more or
  less simultaneously. The bufpool partition lock (today; lock-free
  bufmap post-[[06-bufpool-lockfree]]) serialises lookup, but the
  per-slot **content lock** (the page write lock — required for
  PageAddItem) is held by whichever backend writes first. The other 18
  block on the content lock.
- When the tail page fills (page header signals out-of-space), backends
  take the heap-extend lock to allocate block N+1. Only one extender
  proceeds; the others wait. The new tail is block N+1; all 18 now
  retry against block N+1; back to step 2.
- Under steady write load this should make slow progress, but
  combined with GC stop-the-world windows (`05-runtime-trace.md` §5.3)
  the system stalls.

The two refactors below address both contention layers.

## 2. 8-stripe WAL insert locks

### Data structure

```go
// internal/wal/writer.go (refactor)

type Writer struct {
    appendLocks [8]paddedMutex   // per-stripe; backends hash by procNum % 8
    // ... unchanged fields: groupFlushReq queue, fdatasync amortisation, etc.
}

type paddedMutex struct {
    mu sync.Mutex
    _  [56]byte   // pad to 64 B; prevent false sharing
}
```

`paddedMutex` ensures the 8 mutexes occupy 8 distinct cache lines.
Without the padding, two adjacent mutexes share a cache line and
contending writers cause coherence traffic even when they intend to
lock different stripes.

### Append API

```go
// Append serialises one record's append into the WAL buffer.
// procNum selects the stripe; flush coordination is unchanged
// (the group-commit machinery at writer.go:611+ remains).
func (w *Writer) Append(rec *Record, procNum int32) (LSN, error) {
    stripe := procNum & 0x7   // procNum % 8
    w.appendLocks[stripe].mu.Lock()
    defer w.appendLocks[stripe].mu.Unlock()

    lsn := w.reserveSpace(rec.Size())
    w.writeIntoBuf(lsn, rec.Bytes())
    return lsn + LSN(rec.Size()), nil
}
```

### LSN allocation

The current `reserveSpace` is internal to the locked critical section
— it bumps the next-LSN counter and may rotate the on-disk WAL
segment. Under 8-stripe locking, two stripes might race on `nextLSN`;
we make `nextLSN` an `atomic.Uint64` so the stripe lock guards only
the buffer write, not the LSN counter:

```go
func (w *Writer) reserveSpace(size int) LSN {
    return LSN(w.nextLSN.Add(uint64(size)) - uint64(size))
}
```

This atomic reserve is racy with the segment-rotation logic (because
two writers may reserve LSNs that span a segment boundary). The fix
is the same as PG's: a stripe that reserves space in a new segment
takes a separate `rotateMu sync.Mutex` (added to the Writer struct
alongside `appendLocks [8]paddedMutex`; the lock is coarse but rare —
once per `wal_segment_size`, typically 16 MB).

```go
type Writer struct {
    appendLocks [8]paddedMutex
    rotateMu    sync.Mutex   // covers segment-boundary crossings
    nextLSN     atomic.Uint64
    segSize     uint64       // wal_segment_size, immutable after init
    // ... existing fields (group-commit queue, etc.)
}

// reserveSpaceWithinSegment reserves `size` bytes for one record.
// If the reservation would straddle the next segment boundary, it
// coordinates via rotateMu so segment rotation is serialised even
// while the 8 stripe locks remain non-overlapping.
func (w *Writer) reserveSpaceWithinSegment(size int) LSN {
    for {
        old := w.nextLSN.Load()
        end := old + uint64(size)
        oldSeg := old / w.segSize
        endSeg := (end - 1) / w.segSize
        if oldSeg == endSeg {
            // No boundary crossing; plain CAS.
            if w.nextLSN.CompareAndSwap(old, end) {
                return LSN(old)
            }
            continue
        }
        // Crossing a segment boundary. Take rotateMu, advance LSN to
        // the next segment's start, perform the rotation IO, then
        // reserve within the new segment.
        w.rotateMu.Lock()
        // Recheck: another writer may have rotated while we waited.
        old = w.nextLSN.Load()
        oldSeg = old / w.segSize
        nextStart := (oldSeg + 1) * w.segSize
        if old < nextStart {
            // Advance LSN to nextStart, padding the current segment's tail.
            // (Implementation pads with a NOOP WAL record and bumps nextLSN.)
            w.padToSegmentBoundary(old, nextStart)
            w.nextLSN.Store(nextStart)
        }
        // Now reserve inside the new segment via plain CAS (within rotateMu;
        // contention is bounded by rotation rate, not insert rate).
        old = w.nextLSN.Load()
        w.nextLSN.Store(old + uint64(size))
        w.rotateMu.Unlock()
        return LSN(old)
    }
}
```

The boundary-crossing case enters the slow path under `rotateMu`; the
common in-segment case is one atomic CAS. Stripe locks order with
`rotateMu` as: stripe.Lock → (rare: rotateMu.Lock → rotateMu.Unlock) →
stripe.Unlock. Two stripes that both straddle the boundary serialise
on `rotateMu`; otherwise they proceed in parallel.

PG counterpart: `xlog.c:1392,1399` does the same — `XLogInsertRecord`
takes the stripe's `WALInsertLock`, calls `ReserveXLogInsertLocation`
which atomically bumps the insertpos, and the stripe lock guards the
record-bytes write into the shared WAL buffer.

### Flush coordination

The flush path (`writer.go FlushUpTo`) — driven by group commit
(M0098-0002) — is **unchanged**. Flush operates on the cumulative
buffer content, not per-stripe; it merges across all 8 stripes
naturally because the buffer is a single byte ring and LSNs are
globally ordered.

The waiter chain that piggybacks on a leader's fdatasync continues
to work: a backend doing `Commit` waits until its LSN is flushed; the
leader writer drains all reservations up to its target.

### Stripe selection

```go
stripe := procNum & 0x7
```

`procNum` is allocated from `mvcc.ProcArray` ([[04-mvcc-procarray]])
at backend start; it ranges over `[0, maxBackends)`. The simple modulo
maps each backend to a fixed stripe; concurrent backends with different
procNum values rarely collide. PG uses identical striping
(`MyProcNumber % NUM_XLOGINSERT_LOCKS`).

For non-backend writers (bgwriter, walwriter, autovacuum), assign a
reserved procNum in the high range (e.g., `procNumWALWriter = max +
1`, `procNumBgwriter = max + 2`). These rarely append; modulo
collisions with backend-procNums are fine because they share a
stripe with at most one such system worker.

## 3. FSM-driven page selection

### Goals

- Avoid concurrent writers converging on the same tail page.
- Use the FSM (`internal/storage/fsm.go`) as the primary source of
  insert pages, falling back to extension only when FSM is exhausted
  or unhelpful.
- When extending, allocate multiple pages at once so different
  backends pick different pages.
- Consult [[06-bufpool-lockfree]]'s `bufmap.Lookup` to read the slot's
  pin count (cheap, lock-free) and skip pages with high pin counts.

### Algorithm

```go
// internal/executor/operators_storage.go (refactor of writeHeapRowReturning)

func selectInsertPage(rel relation.Handle, tupleSize int, ctx *Context) (storage.BlockNumber, error) {
    fsm := ctx.FSM(rel)
    proc := ctx.ProcNum

    // 1. Consult FSM for top-N candidate pages with enough free space.
    candidates := fsm.GetCandidates(rel, tupleSize+fsmSlop, candidatesPerInsert)
    // candidatesPerInsert == 4 — see §rationale.

    // 2. Rank candidates by pin count (low is better). The pinCount
    //    extraction goes through Pool.SlotPinCount (defined in
    //    [[06-bufpool-lockfree]]) rather than inlining the bit-mask
    //    arithmetic; keeps this code robust to slotState layout
    //    changes.
    bestBlock := storage.InvalidBlockNumber
    bestPin   := int32(math.MaxInt32)
    for _, blk := range candidates {
        tag := bufferTag(rel, blk)
        pin := ctx.Pool.SlotPinCount(tag)   // returns 0 for unmapped tags
        if pin < bestPin {
            bestPin = pin
            bestBlock = blk
        }
        if pin == 0 {
            break   // perfect candidate; stop searching
        }
    }

    // 3. If best candidate has pin count above the contention
    //    threshold, fall through to extension.
    if bestBlock != storage.InvalidBlockNumber && bestPin < hotPinThreshold {
        return bestBlock, nil
    }

    // 4. Extension path: take the per-relation extend lock (striped).
    stripe := proc & extendLockMask
    el := ctx.ExtendLocks(rel)[stripe]
    el.mu.Lock()
    defer el.mu.Unlock()

    // 5. Re-check FSM (another writer may have freed a page while we waited):
    if blk := fsm.GetPageWithFreeSpace(tupleSize); blk != storage.InvalidBlockNumber {
        return blk, nil
    }

    // 6. Batch-extend: allocate `extendBatchSize` pages at once. The
    //    extra pages immediately register in FSM, so subsequent inserts
    //    from this and other backends will distribute across them.
    firstNew, err := ctx.Pool.ExtendRelationBatch(rel, extendBatchSize)
    if err != nil {
        return storage.InvalidBlockNumber, err
    }
    for i := 1; i < extendBatchSize; i++ {
        fsm.RecordFreeSpace(rel, firstNew+storage.BlockNumber(i), pageSize-pageHeaderSize)
    }
    // Use the first new page for our insert; the others are FSM
    // candidates for the next inserters.
    return firstNew, nil
}

const (
    fsmSlop              = 256   // bytes; bias toward pages with comfortable headroom
    candidatesPerInsert  = 4     // FSM returns up to 4 candidates
    hotPinThreshold      = 4     // pages with > 4 pins are skipped
    extendLockMask       = 0x7   // 8 stripes for per-relation extend lock
    extendBatchSize      = 8     // pages added per extension event
)
```

### Trade-offs

- **`candidatesPerInsert = 4`** — too few and we miss avoiding the
  hot page; too many and we walk too much FSM index. Four matches
  PG's typical "try FSM, then look elsewhere" behaviour.
- **`hotPinThreshold = 4`** — at c=100 with even distribution, 100 /
  N candidates pages averages 4–8 pins per page; the threshold biases
  toward pages with no current writers but not so strictly that the
  system thrashes through FSM candidates.
- **`extendBatchSize = 8`** — each extension event adds 8 pages
  (~32 KiB). Concurrent inserters hitting the hot-pin barrier all
  re-check FSM after extension; they spread across the 8 new pages.
  PG uses similar batched extension (see
  `postgres/src/backend/access/heap/hio.c::RelationGetBufferForTuple`
  and the `RelationExtensionLockWaiterCount` heuristic).
- **8-stripe per-relation extend lock** — replaces the current single
  `heapExtendLocks.LoadOrStore` (`operators_storage.go:2820+`). Eight
  backends can extend in parallel as long as they hash to different
  stripes; PG's `RelationExtensionLock` is similarly contention-light
  after batched extension lands.

### FSM-API changes

The current FSM (`internal/storage/fsm.go:27+`) exposes
`GetPageWithFreeSpace(rel, minBytes)` returning one block. We add:

```go
func (f *FSM) GetCandidates(rel relation.Handle, minBytes int, n int) []storage.BlockNumber
func (f *FSM) RecordFreeSpace(rel relation.Handle, blk storage.BlockNumber, free int)
```

The first returns the top-N pages by free space (≥ minBytes); the
second is unchanged from today. The implementation walks the FSM's
in-memory ranked structure (FSM is a hierarchical tree on PG; our
implementation mirrors that — `fsm.go` already keeps a per-page
free-bytes count). On-disk format unchanged.

## 4. Heap-extend lock striping

```go
// internal/executor/operators_storage.go
type extendLockSet struct {
    locks [8]paddedMutex
}

// Per-relation set, cached in Context:
func (ctx *Context) ExtendLocks(rel relation.Handle) *extendLockSet {
    return ctx.extendLockCache.LoadOrStore(rel, newExtendLockSet)
}
```

A goroutine wanting to extend `rel` picks `locks[procNum % 8]`. Eight
extenders can proceed in parallel; the bufpool extension path
serializes them naturally at the disk-write level (one
`Pool.ExtendRelationBatch` call per stripe acquires the relation's
underlying smgr extend mutex, which is per-fork — beyond this design's
scope).

PG counterpart: PG uses a single `RelationExtensionLock` per relation;
its insert-distribution comes from FSM and batched extension only.
Goopg goes one step further with 8-way striping because Go mutexes
are more expensive than PG LWLocks; the cost is acceptable because
the lock count per relation is one-time at relation init.

## 5. Verification of design choices

A unit test under `internal/executor/insert_distribution_test.go`
synthesises a 100-goroutine race inserting into a fresh relation:
- Before refactor (with `wal.appendMu` + tail-page determinism), the
  insert latency P99 exceeds 1 s under the synthetic load.
- After refactor, P99 < 50 ms; all goroutines complete in < 5 s.

A pgbench c=100 simple-update rerun is the integration verification
(§verification below).

## 6. WAL record format guarantee

This chapter changes **only** the in-memory contention structure
around WAL appending. The on-disk WAL record (`Record` type in
`internal/wal/records.go`, including headers, CRC, payload encoding)
is byte-identical to pre-refactor. A pre-refactor goopg cluster shut
down cleanly, upgraded to the post-refactor binary, and started
fresh will replay its existing WAL without error. PG-compat is
preserved.

## 7. PG counterparts

| goopg concept                     | PG counterpart                                              |
|-----------------------------------|-------------------------------------------------------------|
| 8-stripe `appendLocks`            | `WALInsertLocks[NUM_XLOGINSERT_LOCKS]` in `xlog.c:151,570`   |
| Atomic LSN reserve                | `ReserveXLogInsertLocation` in `xlog.c` (locate via grep on the symbol; line drifts) |
| Stripe selection by procNum       | `MyProcNumber % NUM_XLOGINSERT_LOCKS` in `xlog.c:1392`      |
| FSM `GetCandidates`               | `GetPageWithFreeSpace` family in `freespace.c`              |
| Pin-count aware page selection    | (PG does not have this — it relies on FSM staleness + retry)|
| Batch relation extension          | `ExtendBufferedRelTo` / `RelationGetBufferForTuple`         |
| 8-stripe extend lock              | (PG uses 1 per relation; this is goopg-specific extension)  |

The pin-count aware selection and 8-stripe extend lock are
goopg-specific. PG handles tail-page hot spots primarily via FSM
freshness (autovacuum updates FSM frequently) and batched extension;
we add the pin-count read because the buf-mapping table is lock-free
(thanks to [[06-bufpool-lockfree]]) and cheap to consult — and because
goopg's autovacuum is less mature than PG's, so FSM may be staler.

## 8. Verification

After Phase D4 of [[09-migration-and-rollout]] ships:

- **Compile-time** — `grep -RIn 'appendMu' internal/wal/` returns
  zero. `Writer` struct contains `appendLocks [8]paddedMutex`.
  `unsafe.Sizeof(paddedMutex{}) == 64`.
- **Mutex pprof** under c=50 simple-update — the 8 stripes appear
  in `wal.Writer.Append` with roughly balanced wait times (no
  single stripe dominating).
- **c=100 simple-update no longer SKIPPED** — combined with
  [[04-mvcc-procarray]], [[06-bufpool-lockfree]], the c=100 SU run
  completes with measured TPS ≥ 500. The 19-goroutine deadlock at
  `bufpool.go:927` does not reproduce.
- **c=100 standard no longer SKIPPED** — same.
- **TPC-H regression** — q1/q4/q5 wall-clock within ±10 % of
  pre-refactor (the regression risk is the FSM consultation taking
  one bufmap.Lookup per candidate, but the lookup is ~5 ns and runs
  at most 4× per insert).
- **WAL replay** — an integration test starts goopg pre-refactor,
  loads pgbench scale 10, shuts down, switches to the post-refactor
  binary, restarts; data is identical to pre-refactor (same row
  counts and contents after replay).
- **FSM ranking correctness** — unit test inserts 10 000 tuples into
  a freshly-extended relation with `extendBatchSize = 8` and asserts
  the per-page tuple counts have stddev < N (vs all-on-tail-page
  pre-refactor); confirms distribution.

This chapter, combined with the four upstream chapters (memory
context, pointer-free Datum, concrete executor, ProcArray, lock-free
bufpool), closes the OLTP-write bottleneck story end-to-end.
