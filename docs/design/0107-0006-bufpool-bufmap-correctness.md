# 0107-0006 — `bufmap` & FPI Correctness Fixes (loop 1)

Milestone: **M0107-0006 — Phase D3: lock-free buffer pool**
Parent design: [`docs/design/perf-optimize/06-bufpool-lockfree.md`](perf-optimize/06-bufpool-lockfree.md)
Status: partial progress — correctness gates unblocked, perf gates pending.

## Background

A previous loop landed the bulk of the Phase D3 rewrite (new
`internal/storage/bufmap.go` open-addressing hash table; pointer-free
64-bit packed `slotState` CAS in `bufpool.go`).  When this loop ran
`go test ./internal/storage/`, three tests hung or failed:

| Test | Symptom | Root cause |
|---|---|---|
| `TestBgwriterDoDDirtyVictimRate` | infinite loop in `pinSlow` | bufmap `packVal` collided with the `bufmapTombstone=1` sentinel |
| `TestScanRingCacheMissNoEviction` | `expected 90 hot pages cached, got 86` | bufmap `Lookup` used Robin-Hood early exit but `Insert` did not displace |
| `TestPoolFPIEmittedOncePerEpoch` | deadlock in `maybeEmitFPI` | `s.contentMu.Lock()` taken while caller already holds it |

This design records the fixes that closed the three issues so that the
remaining work (pgbench TPS targets, 1 000-goroutine stress, mutex
top-20 exit for `bufferPartition.mu`) can run cleanly on top.

## Issue 1 — `packVal` sentinel collision

### Reproduction

`internal/storage/bufmap.go` reserved `0 = empty` and `1 = tombstone`
in the `vals []uint64` array and packed live entries as

```go
return uint64(uint32(slotIdx))<<32 | uint64(gen)
```

For `slotIdx = 0` and `gen = 1` this evaluates to `1`, which is exactly
`bufmapTombstone`.  The author worked around the collision in `Insert`:

```go
val := packVal(slotIdx, gen)
if val < 2 {
    val |= 2 // ensure not a sentinel
}
```

…but `val |= 2` corrupts the low 32 bits (the gen field).  `Lookup`
later returns the corrupted gen (e.g. gen 1 → 3), which never matches
the real slot's gen, so `pinSlow`'s

```go
if stateValid(old) && stateGen(old) == gen { ... }
```

loop runs forever under `pinMu`.  The debug instrumentation captured
the live state:

```
pinSlow stuck: tag={{1 1 0} 0} slotIdx=0 gen=3 slotState=0x280400000
slotTag={{1 1 0} 0} valid=true io=false stateGen=1 pin=0
```

### Fix

Shift `slotIdx` by `+1` inside the packed value so a live entry's
high 32 bits are always non-zero, guaranteeing the packed value
exceeds `UINT32_MAX` and cannot collide with the 0 / 1 sentinels.

```go
func packVal(slotIdx int32, gen uint32) uint64 {
    return uint64(uint32(slotIdx+1))<<32 | uint64(gen)
}
func unpackVal(v uint64) (int32, uint32) {
    return int32(v>>32) - 1, uint32(v)
}
```

The `val |= 2` workaround in `Insert` is now dead code and was
removed.

Regression: `TestBufmapPackUnpackRoundtrip` and
`TestBufmapInsertLookupSlotZeroGenOne` in `bufmap_test.go` pin the
behaviour for all the `(slotIdx, gen)` shapes that exercised the
collision.

## Issue 2 — Lookup's invalid Robin-Hood early exit

### Reproduction

`Lookup` shipped with

```go
residentH := bufTagHash(m.keys[h]) & m.mask
residentDist := (h - residentH) & m.mask
if dist > residentDist {
    return -1, 0   // tag cannot be here
}
```

That early exit is only valid when the table is maintained in
Robin-Hood order — every insert must swap entries so the resident at
each bucket has probe distance ≥ the visitor's.  `Insert` does plain
linear probing (no swap), so under hash-collision sequences a real
entry can sit at a later bucket whose resident has lower probe
distance.  Lookup then incorrectly returns "not found" for entries
that are present.

`TestScanRingCacheMissNoEviction` exposed this with 90 hot pages
pre-loaded: 4 of them missed in `countCachedBlocks`'s `bm.Lookup`
even though their slots were still valid.

### Fix

Drop the early-exit; rely on the empty-bucket terminator plus the
`dist <= size` safety bound.  Comment in the source explains why
Robin-Hood early-exit requires Robin-Hood insertion.

## Issue 3 — FPI emit path deadlocks on contentMu

### Reproduction

The legacy FPI helpers (`maybeEmitFPI`, `markDirtyWithLSNCommon`,
`MarkDirtyChangeRecord`, `MarkDirtyLogicalChange`, `ResetCheckpointEpoch`,
…) wrap reads and writes of `slot.fpiSinceCheckpoint` in
`s.contentMu.Lock()` / `Unlock()`.  Callers of `MarkDirty` use the
documented pattern

```go
s.Lock()           // contentMu
s.Page()[i] = b
pool.MarkDirty(s)  // re-enters contentMu.Lock — deadlock
s.Unlock()
```

`sync.RWMutex` is non-reentrant, so the second `Lock()` blocks
forever.  `TestPoolFPIEmittedOncePerEpoch` hit this exact path.

### Fix

`fpiSinceCheckpoint bool` → `fpiSinceCheckpoint atomic.Bool`.  All
read/write sites now use `Load()` / `Store(true)` directly, with no
`contentMu` involvement.  The slot's page-content lock continues to
guard `page[]` bytes — only the FPI flag drops the lock.

## Scope of this change

| File | Change |
|---|---|
| `internal/storage/bufmap.go` | shift `packVal` by +1; drop Robin-Hood early-exit; drop dead `val \|= 2` |
| `internal/storage/bufpool.go` | `fpiSinceCheckpoint atomic.Bool`; remove all `contentMu` wrappers around it; remove diagnostic counter from `pinSlow` |
| `internal/storage/bufmap_test.go` | new — regression tests for packing/lookup |

No on-disk layout or wire-format bytes changed.

## What this **does not** cover (still M0107-0006 open)

- pgbench `c=100 SU` TPS ≥ 500 (was SKIPPED/DEADLOCK)
- `runtime.futex` cum% at c=100 SO < 8 % (vs 23 %)
- `bufferPartition.mu` absence from mutex top-20 (already gone
  structurally — needs an empirical mutex-profile run to confirm)
- 1 000-goroutine 30 s Pin/Unpin/evict stress (the new bufmap is
  Insert-under-mutex / Lookup-lock-free; the stress test is in
  scope for the next loop)
- `TestE2E_FailoverGoopgToPG/async` PASS

These remain open and will be addressed by subsequent M0107-0006
loops.

## Cross-references

- [[perf-optimize/06-bufpool-lockfree]] — full Phase D3 design
- [[0107-0004-procarray-xidgen-clog-bank-locks]] — sibling Phase D1
- [[0107-0005-activity-registry-per-backend-slots]] — sibling Phase D2
