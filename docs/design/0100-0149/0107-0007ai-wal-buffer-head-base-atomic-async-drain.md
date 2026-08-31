# 0107-0007ai — WAL buffer head/base atomic upgrade + async drain (appendPGCompat Path B)

**Milestone**: M0107-0007 Phase D4 — WAL insert striping + FSM page distribution  
**Status**: Landed 2026-05-21

---

## Summary

Two tightly coupled changes that together close the last `Lock()`-on-hot-path
bottleneck in the WAL append flow:

1. **`walBuffer.head` and `walBuffer.base` upgraded to `atomic.Int64`** —
   the equivalent of [[0107-0007r]]'s `tail` upgrade.  Concurrent stripe
   writers' `free()` / `resident()` / `writeReserved` reads are now
   race-free against the drain goroutine's `advanceHead` stores.

2. **`appendPGCompat` Path B drops `appendMu.Lock()`** — drain and the
   stripe append run without any outer lock.  The state-loop goroutine is
   the only caller of `drainBufferBytes → advanceHead` (single writer), so
   no lock is needed to guard the write side.  Stripe writers under
   `appendMu.RLock()` proceed fully in parallel while drain is in progress.

---

## Problem

After [[0107-0007ah]] (RWMutex + parallel tryAppend), `tryAppend` goroutines
under `RLock()` proceed in parallel on different stripes.  However,
`appendPGCompat` Path B — the state-loop's own append path — still acquired
`appendMu.Lock()` for the pre-drain step:

```go
// OLD Path B
s.appendMu.Lock()
defer s.appendMu.Unlock()
need := int64(conservativeSize) - s.walBuf.free()
if need > 0 {
    s.core.PublishUpTo(curr)
    s.drainBufferBytes(need, drainReasonOverflow)  // I/O — can take ms
}
s.core.AppendXLogPayload(...)
```

`drainBufferBytes` performs disk I/O which can take milliseconds.  During
that window all `tryAppend` goroutines block waiting for the `Lock()`.  The
stripe parallelism introduced by [[0107-0007ah]] was nullified whenever a
drain cycle was required.

---

## Solution

### Part 1: atomic.Int64 for head and base

`walBuffer.head` and `walBuffer.base` were plain `int64` fields.  The
original design note (pre-[[0107-0007r]]) marked all methods as
"state-loop-only".  [[0107-0007ah]] introduced concurrent `writeReserved`
calls (from stripe writers under `RLock()`), so `writeReserved` already read
`base` — but since drain still held `Lock()`, there was no concurrent
`advanceHead` write to race against.

With `appendMu.Lock()` removed from Path B, `advanceHead` (called from the
state-loop goroutine without any lock) can race against concurrent
`writeReserved` or `free()` reads under `RLock()`.  Both `head` and `base`
must therefore be atomic.

**Update ordering in `advanceHead`**: base is stored BEFORE head.  A
concurrent reader that observes the new `base` but the old `head` computes
a conservatively small `free()` (fewer available bytes), which causes
`tryAppend` to fall through to the state-loop path — a safe conservative
behaviour.  The reverse ordering (head-before-base) could transiently expose
a range where `writeReserved` sees the new head (suggesting more room) but
the old base (smaller valid window), potentially triggering a false
`errWALBufferReservedOutOfRange`.

### Part 2: lock-free appendPGCompat Path B

Safety argument (why no lock is needed for drain + stripe append):

**Drain window vs stripe-write window are disjoint.**  Drain reads bytes in
`[head, head+need)` from the ring.  Concurrent `tryAppend` goroutines write
at LSNs `≥ tail` (enforced by their `free() ≥ conservativeSize` check under
`RLock()`).  Since `tail = head + resident() ≥ head + need` (drain only
consumes up to `resident` bytes), the two regions are disjoint in the linear
LSN space.

**Physical ring-slot overlap is impossible while drain is running.**  For
stripe-write slot `(lsn % cap)` to equal drain-read slot `(head_x % cap)`,
we need `tail - head ≥ cap - need`, i.e. `free() ≤ need ≤ conservativeSize`.
But then the `free()` check under `RLock()` would return false and
`tryAppend` would fall through.  Therefore no `tryAppend` goroutine writes to
the same physical slot as drain while drain is active.

**`advanceHead` is single-writer.**  Only `drainBufferBytes` calls
`advanceHead`; `drainBufferBytes` is only called from the state-loop
goroutine (via `appendPGCompat` or `drainBufferUpTo`).  No CAS or mutex is
needed on the write side — `atomic.Int64.Store` provides the sequencing
guarantee readers need.

**State-loop bookkeeping fields are state-loop-only.**  `s.writePos`,
`s.writeLSN`, `s.drainedLSN` are accessed exclusively on the state-loop
goroutine (sequential).  `s.writeLSNMirror` is shared with `tryAppend`
goroutines; Path B uses `storeMaxLSN` (CAS-max loop) to update it,
consistent with [[0107-0007ah]].

### New Path B (no outer lock)

```
Phase 1: drain if needed (lock-free)
    need = conservativeSize − walBuf.free()
    if need > 0:
        core.PublishUpTo(curr)      // make pending bytes drainable
        drainBufferBytes(need, …)   // I/O; advances head atomically

Phase 2: stripe-locked append (AppendXLogPayload takes its own stripe lock)
    core.AppendXLogPayload(procNum, payload, …)
    core.PublishUpTo(start0 + total)

Phase 3: update state-loop bookkeeping (no lock; state-loop-only fields)
    writePos, writeLSN = end
    storeMaxLSN(writeLSNMirror, end)   // CAS-max; concurrent tryAppend safe
```

---

## PG counterpart

`XLogInsertRecord` in `postgres/src/backend/access/transam/xlog.c` holds the
per-stripe WAL insert lock while writing record bytes into `XLogCtl->pages`,
but never holds that lock during any segment-rotation fsync.  Drain I/O in
goopg is the analogous slow I/O step; the design aligns with PG by not
serialising peer writers behind it.

---

## Lock-ordering tier (updated)

```
(stripe writer, tryAppend):
  appendMu.RLock
    → stripe lock (appendLockSet[i])
      → insertPosTracker.posMu
        → (rare) emitSegmentPad
      → walBuffer.writeReserved  (base.Load — atomic read, no lock)
      → MemRing.mu (read-lock)
    → walBuffer.publishTail       (tail.Store — atomic)
    → MemRing.mu (write-lock)
  appendMu.RUnlock

(state loop, appendPGCompat Path B — no Lock() around drain):
  drainBufferBytes:
    walBuffer.readForDrain        (head.Load, base.Load — atomic reads)
    writeAt(disk I/O)
    walBuffer.advanceHead         (base.Store, head.Store — atomic, base-first)
  stripe lock (appendLockSet[i])  (via AppendXLogPayload)
    → (as above)
  walBuffer.publishTail           (atomic)
  MemRing.PublishUpTo             (MemRing.mu write-lock)

(state loop, appendPGCompat Path A — Lock() retained):
  appendMu.Lock
    drainBufferBytes
    writeAt
    walBuffer.reset
    core.resetPosition
  appendMu.Unlock
```

---

## Files changed

- `internal/wal/wal_buffer.go`: `head` / `base` → `atomic.Int64`; all
  affected methods updated (`reset`, `resident`, `append`, `readForDrain`,
  `advanceHead`, `readAt`, `writeReserved`).

- `internal/wal/writer.go` `state.appendPGCompat`: Path B loses
  `appendMu.Lock()/Unlock()`; Path A unchanged.

- Test updates: `wal_buffer_write_reserved_test.go`,
  `wal_buffer_publish_tail_test.go`, `segment_pad_emit_test.go` — `.head`/
  `.base` direct reads → `.head.Load()` / `.base.Load()`.

- New: `internal/wal/wal_buffer_head_base_atomic_test.go` (4 tests),
  `internal/wal/append_pgcompat_async_drain_test.go` (2 tests).

---

## Verification

```
go test -race -count=1 ./internal/wal/                                PASS
go test -race -count=1 ./internal/executor/ ./internal/storage/ \
    ./internal/mvcc/ ./internal/server/ ./internal/access/btree/      PASS
```
