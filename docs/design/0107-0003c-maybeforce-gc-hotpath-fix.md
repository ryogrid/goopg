# 0107-0003c — `maybeForceGCAfterCommit` Hot-Path Fix

**Status:** accepted  
**Loop:** M0107-0003 TPS gate verification (2026-05-21)

## Problem

`maybeForceGCAfterCommit` in `internal/server/dispatch.go` was called after
every committed query in both the simple-query and extended-query paths.
Its body was:

```go
func maybeForceGCAfterCommit() {
    var ms runtime.MemStats
    runtime.ReadMemStats(&ms)          // STW world-stop on EVERY query
    n := atomic.AddInt64(&queriesWithoutFreeCounter, 1)
    if ms.HeapInuse < heapReleaseThresholdBytes && n < queriesPerForcedFree {
        return
    }
    atomic.StoreInt64(&queriesWithoutFreeCounter, 0)
    runtime.GC()
    debug.FreeOSMemory()
}
```

Two bugs compounded each other:

1. **`runtime.ReadMemStats` on every query** — Go's documentation states
   ReadMemStats causes a brief stop-the-world to produce consistent stats.
   Every pgbench SO query paid this STW cost regardless of whether a GC was
   needed.

2. **`queriesPerForcedFree = 8`** — After 8 queries, the function would call
   `runtime.GC() + debug.FreeOSMemory()` unconditionally (once the heap check
   was confirmed cheap by the ReadMemStats call).  At 40 000 TPS this triggered
   ~5 000 full GC rounds per second.

Combined effect: `gcBgMarkWorker` consumed **43 %** of CPU at c=10 SO, and the
effective TPS was only **4 131** (vs 2 307 pre-M0107 baseline — i.e. Phase C
improvements were largely hidden by the GC thrash).

The original design comment noted "at our query granularity (seconds) that is
negligible" — this was correct for TPC-H queries but completely wrong for
high-frequency pgbench workloads.

## Fix

Two changes to `internal/server/dispatch.go`:

### 1. Counter check before `ReadMemStats`

```go
func maybeForceGCAfterCommit() {
    n := atomic.AddInt64(&queriesWithoutFreeCounter, 1)
    if n < queriesPerForcedFree {
        return // fast path: single atomic add, no STW
    }
    var ms runtime.MemStats
    runtime.ReadMemStats(&ms)
    atomic.StoreInt64(&queriesWithoutFreeCounter, 0)
    if ms.HeapInuse < heapReleaseThresholdBytes {
        return
    }
    runtime.GC()
    debug.FreeOSMemory()
}
```

On the common path (n < 10 000), the function returns after one atomic
`AddInt64` — no STW, no heap allocation.

### 2. `queriesPerForcedFree` 8 → 10 000

- Old value (8) was sized for TPC-H where queries take seconds each.
- New value (10 000) still protects against long-running TPC-H sweeps: even a
  continuous 22-query TPC-H run does << 10 000 queries before GC fires.
- For pgbench SO at 40 000 TPS, GC fires at most every 10 000 / 40 000 s =
  0.25 s instead of every 8 / 40 000 s = 0.2 ms.

## Results (scale=100, GOMEMLIMIT=18GiB, 120 s runs)

| Metric | Before fix | After fix | Target |
|--------|-----------|-----------|--------|
| c=10 SO TPS | 4 131 | **41 944** | ≥ 8 000 |
| c=50 SO TPS | — | **86 495** | ≥ 18 000 |
| c=100 SO TPS | — | **83 149** | ≥ 12 000 (milestone-close) |
| gcBgMarkWorker cum% c=10 | 43.46 % | **0.82 %** | < 15 % |
| dispatchSimpleQuery flat% c=10 | — | **0.4 %** | < 10 % |
| runtime.itabHashFunc | in top-40 | **absent** | out of top-40 |

## Affected Files

- `internal/server/dispatch.go` — `queriesPerForcedFree` constant and
  `maybeForceGCAfterCommit` function body
