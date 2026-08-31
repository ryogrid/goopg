# 0107-0008q — Buffer Pool Per-Slot Semaphore Wait Caller

**Status**: accepted  
**Milestone**: M0107-0008 (Phase D5: runtime internals)  
**Date**: 2026-05-21  
**Depends on**: [[0107-0006]] (lock-free bufmap), [[0107-0008c]] (runtimeshim.SemaAcquire/Release)

## Problem

`Pool.pinSlow` previously used a pool-wide `pinCond *sync.Cond` to wait for
in-flight IO to complete. When any slot's IO completed, `pinCond.Broadcast()`
woke ALL waiting goroutines across ALL slots. Under high concurrency (c=100
pgbench-standard) with many concurrent Pin calls, this produced a thundering
herd: all waiters re-acquired `pinMu`, re-checked their individual slots, and
most immediately went back to sleep.

The pool-wide `sync.Cond` also required `pinMu` to be held during `Wait`, which
limited parallel wakeup throughput.

## Solution

Replace the pool-wide `pinCond *sync.Cond` with:

1. **`slotSema []uint32`** — parallel array of runtime semaphore cells, one per
   slot. Indexed by slot index. Used via `runtimeshim.SemaAcquire` /
   `runtimeshim.SemaRelease` ([[0107-0008c]]).

2. **`slotWaiters []atomic.Int32`** — parallel array tracking how many goroutines
   are currently waiting on each slot's IO. Written/read under `pinMu` so the
   loader always sees an exact count before releasing.

### Protocol

**Waiter** (`pinSlow`, when `ioInflightBit` is set on slot i):
```
p.slotWaiters[i].Add(1)      // under pinMu: register before releasing
p.pinMu.Unlock()
if p.OnBufferIOWait != nil { p.OnBufferIOWait() }
runtimeshim.SemaAcquire(&p.slotSema[i])   // park until loader wakes us
p.slotWaiters[i].Add(-1)     // cleanup
p.pinMu.Lock()
continue                      // re-check slot state
```

**Loader** (`pinLoad`, success path, under pinMu):
```
n := p.slotWaiters[victimIdx].Load()     // exact count: under pinMu, no new waiters can arrive
s.state.Store(validSt)                   // clear ioInflightBit
for i := int32(0); i < n; i++ {
    runtimeshim.SemaRelease(&p.slotSema[victimIdx])   // wake exactly n waiters
}
```

**Loader** (`releaseVictimSlot`, on failed IO, under pinMu):
```
n := p.slotWaiters[victimIdx].Load()
p.slots[victimIdx].state.Store(0)
for i := int32(0); i < n; i++ {
    runtimeshim.SemaRelease(&p.slotSema[victimIdx])
}
```

### Correctness argument

The key safety property is: **between the loader reading `n` and clearing
`ioInflightBit`, no new waiter can increment `slotWaiters[i]`**. This holds
because both the waiter's increment and the loader's read occur while holding
`pinMu`. After the loader clears `ioInflightBit` (still under `pinMu`), any
subsequent Pin caller sees `valid && !ioInflight` and succeeds via the fast
path without entering the wait path.

After the loader releases `pinMu`, the `n` previously-waiting goroutines wake
from `SemaAcquire`, each decrement their count, and re-acquire `pinMu` to
re-check. They find the slot valid and proceed.

### Changes

- **`slotIOCond` struct** — removed (dead code placeholder; was never used).
- **`pinCond *sync.Cond`** — removed from `Pool` struct.
- **`NewPool`** — initialises `slotSema` and `slotWaiters` (both `len = cfg.Slots`); removes `sync.NewCond` call.
- **`releaseVictimSlot`** — reads `slotWaiters[i]`, stores state 0, releases sema N times.
- **`PinNew`** — two `pinCond.Broadcast()` calls removed (no per-slot waiters on brand-new slots).
- **`pinSlow`** — `pinCond.Wait()` replaced by increment + unlock + `SemaAcquire` + decrement + lock.
- **`pinLoad`** — `pinCond.Broadcast()` replaced by load `n` + N × `SemaRelease`.

## Regression Pins

Four tests added in `internal/storage/bufpool_sema_test.go`:

- `TestSlotSemaArraysInitializedCorrectly` — verifies len and zero values at construction.
- `TestSlotSemaConcurrentPinSameBlock` — 8 goroutines Pin the same evicted block; all must complete within 10 s (deadlock detection).
- `TestSlotSemaWaiterCountReturnsToZero` — single goroutine IO-wait cycle; all slotWaiters and slotSema back to 0 after Pin.
- `TestSlotSemaNoPinCondInPool` — documents pinCond removal; fails if sema arrays missing.

## Verification

```
go test -race -count=1 ./internal/storage/                          # 5.39 s PASS
go test -race -count=1 ./internal/mvcc/ ./internal/wal/             # PASS
go test -race -count=1 ./internal/executor/ ./internal/server/       # PASS
go test -race -count=1 ./internal/access/btree/                      # PASS
make ralph-state-guard                                                # PASS
```

## Closes

M0107-0008 remaining open item: "bufpool per-slot Sema wait caller (consumes
[[0107-0008c]]; blocked on M0107-0006 lock-free bufpool)" — M0107-0006 is
complete (loop 8), unblocking this caller.

M0107-0008 is now **feature-complete**: all three `runtimeshim` primitives
(Nanotime, PinP, SemaAcquire/Release) are wired to their production callers
(ActivityRegistry, stats.Counter, Pool per-slot IO wait).
