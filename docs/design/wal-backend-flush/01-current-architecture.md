# 01 — Current architecture (verified at HEAD, 2026-07-12)

status: draft · date: 2026-07-12

All references are `internal/wal/writer.go` unless another file is named. Line
numbers are from the tree at commit `fedb0eec` (post perf-optimize2 fix-01/03/05);
they will drift — symbol names are the anchors.

## 1.1 The split that already exists: appends are backend-side, I/O is not

A common misreading is "one goroutine owns all WAL work". Since M0026 and the
M0107-0007 slice-B work, the **append fast path already runs on the calling
backend goroutine**: `tryAppend` under `appendMu.RLock` reserves ring space
(`walBuf.tryReserve` CAS), reserves LSN space (`insertPosTracker`, atomic),
memcpys into the ring under one of 8 padded stripe locks
(`stripeWriterCore.AppendXLogPayload`, stripe = `procNum & 7` via the
`internal/gls` backend id since fix-01), publishes via `PublishUpTo`, feeds the
walsender `memRing`, and CAS-max-stores `writeLSNAtomic`.

What remains bound to the single writer goroutine (`state.loop`, spawned in
`NewWriter`, `runtime.LockOSThread()`):

1. **The fdatasync barrier** — `handleGroupFlush` → `flushUpTo` = Stage 1
   `drainBufferUpTo` (ring → segment files via `writeAt` → `openSegment`,
   `f.WriteAt` or `aio.Submit(...).Wait()`) + Stage 2 `doSync` (per dirty
   segment, sorted; `unix.Fdatasync` by default per `wal_sync_method`).
2. **The `commit_delay` sleep** — `handleGroupFlush` sleeps
   `commitDelayUs=1000` µs when `len(queue) >= commitSiblings=5`, **on the loop
   goroutine**, before the shared flush.
3. **Unsynchronized writer-local state** — `files map[uint64]*os.File` (the
   segment FD cache; no mutex), `dirty map[uint64]bool`, plain
   `flushedLSN`/`drainedLSN` fields. Safe today *only* because a single
   goroutine touches them.
4. The slow paths as ops: `opAppend` (ring-overflow/no-buffer fallback),
   `opAppendRaw` (walreceiver), `opRecycle` (segment retirement), `opClose`,
   `opWALBufStat`. (`opFlush` is dead legacy — new code uses the flush-group
   signal.) These need `appendMu.Lock`, not the loop per se; the loop is their
   serializer only incidentally.

## 1.2 The commit path today (the hand-off)

```
backend:  xactMarker hook (initdb/open.go)          loop goroutine:
  Append(commit record)  [fast path, backend-side]
  FlushUpTo(endLSN):
    lsn==0? no-op
    lsn <= flushedLSNAtomic? return   (fix-03)
    OnWALSync hook
    fg.mu.Lock; queue=append; Unlock
    signal <- struct{}{}  (cap-1)   ─────────────►  select: flushSig
    block on <-req.done                             handleGroupFlush:
                                                      swap queue
                                                      if len>=5: sleep 1000µs   ← stalls ALL waiters
                                                      maxLSN across queue
                                                      flushUpTo(maxLSN):
                                                        drain ring → pwrite segs
                                                        fdatasync dirty segs
                                                        flushedLSN(+atomic mirror)
    ◄─────────────────────────────────────────────    close(req.done) each
  clog.SetCommitted(xid)      (still inside the xactMarker hook)
  SyncRep.WaitForLSN(WrittenLSN()) if configured
                              (in operators_tx.go, after CommitTransaction returns)
```

Per-commit costs on top of the raw fdatasync: queue-mutex lock/unlock, a
channel send, goroutine park + unpark, the loop's scheduling latency, and —
whenever ≥5 commits are queued — a 1000 µs sleep taken **once for the whole
queue**, delaying LSNs that were requested *before* the sleep began, plus every
subsequent op behind the loop. The measured shape: block profile 72.9 % under
`CommitTransaction` (`selectgo` 40 % + `chanrecv` 27 %).

Group batching itself works (≈8.9 txns/fdatasync at c=50) — the problem is
the *mechanism around* the batch, not the batching.

## 1.3 The 200 ms walwriter tick — what it is and is not

`internal/initdb/open.go` starts a ticker goroutine (`WalWriterDelay`, default
200 ms) that calls `FlushUpTo(WrittenLSN())`. Two facts the redesign must state
precisely:

- It is **not on the commit latency path**: commits signal the flush group
  directly and are serviced immediately; the tick only covers buffered bytes
  when no commit is in flight (async-commit durability bound, WAL-before-data
  for the clog hook).
- But it is **not PG's walwriter either**: each tick *with newly-appended,
  not-yet-durable bytes* performs a *full flush* (drain + fdatasync to the
  current written LSN) through the same single-goroutine path; an
  already-flushed tick fast-exits with zero I/O (fix-03). PG's walwriter
  mostly only *writes* completed pages to the OS cache and throttles fsync by
  `wal_writer_flush_after`. goopg therefore fsyncs appended-but-uncommitted
  bytes on a 200 ms cadence even when no commit requires their durability yet,
  and conversely never pre-writes in a way that makes a later commit's flush
  cheaper without also paying an fsync.

## 1.4 FlushUpTo / Append caller matrix

| caller | goroutine | purpose / ordering assumption |
|---|---|---|
| xactMarker commit hook (`initdb/open.go`) | backend | THE latency path: Append(commit) → `FlushUpTo(endLSN)` → clog mark (inside the hook) → SyncRep wait (in `operators_tx.go`, after `CommitTransaction` returns) |
| clog flush-WAL hook (`clog.SetFlushWALHook`) | clog writer (backend or checkpointer) | WAL-before-data for async commits; must stay synchronous |
| checkpointer (`checkpointer.go`) | checkpointer | checkpoint marker Append+FlushUpTo; retention via `RemoveOldSegments*` (opRecycle) |
| bufpool WAL-before-data (`storage/bufpool.go`) | evicting backend / bgwriter / checkpointer | page LSN durable before page writeback |
| bg walwriter ticker (`open.go`) | ticker goroutine | §1.3 |
| `Append` | ~100+ executor/DDL sites, all backend context | fast path backend-side; overflow falls back to `opAppend` |
| `AppendRaw` | walreceiver (standby) | position reset; exclusive |

`SyncRep.WaitForLSN(WrittenLSN(), mode)` runs *after* `FlushUpTo` returns
(`executor/operators_tx.go`) — local durability precedes remote wait, same as PG.

## 1.5 Existing primitives the redesign reuses

- `insertionTracker` (`insertion_tracker.go`): per-stripe `insertingAt`
  atomics — goopg's `WALInsertLock.insertingAt` analog, built for exactly the
  "publication walker" role (`lowestActiveLSN`).
- `stripeWriterCore.PublishUpTo` (`stripe_writer_core.go`): publishes
  `min(upperBound, lowestActive)` to the rings and returns the safe contiguous
  frontier — the core of a `WaitXLogInsertionsToFinish` analog.
- Mirrored atomics already exist and are load-bearing: `writeLSNAtomic`
  (`WrittenLSN()`, CAS-max by backends) and `flushedLSNAtomic` (fix-03,
  published by the flusher after fdatasync; the pre-enqueue fast exit reads it).
- `walBuffer` head/base are atomics (0107-0007ai) — designed so a drainer and
  concurrent reserved-space writers can coexist; the current design only ever
  exercises it with the loop as the sole drainer.

## 1.6 Shutdown ordering (must be preserved)

`Runtime.Close` (`initdb/open.go`): final checkpoint → stop bgwriter → stop the
walwriter ticker **before** WAL close (so no FlushUpTo races the final close)
→ `Pool.Close` → `WAL.Close` (today: `opClose` → drain group flush → final
`flushUpTo` → wait `eagerWG` (preallocation workers) → close FDs) →
StorageMgr/AIO.

## 1.7 Supersession map

| existing doc | what changes |
|---|---|
| `0098-0002-wal-group-commit.md` | queue + writer-goroutine group commit → superseded by emergent model (03 §3) |
| `0099-0002-wal-group-commit-batching-policy.md` | hardcoded 1000 µs/5 on the loop → superseded by holder-only sleep, GUC default 0 (00 D2) |
| `wal_fsync_flow_primary.md` | §§ describing loop-owned flush → rewritten around backend-driven flush |
| `0107-0007ai` (atomic head/base, async drain) | single-drainer *assumption* superseded: "the single drainer is the `writeMu` holder" (04 §4.4) |
| `0107-0007ah` (appendMu RLock protocol) | extended: lock-order table gains the `writeMu` tier (04 §4.2) |
| `0013-0001`, `0010-0002`, `0007-0002`, `perf-optimize/07` | unchanged foundations — built upon, not superseded |
