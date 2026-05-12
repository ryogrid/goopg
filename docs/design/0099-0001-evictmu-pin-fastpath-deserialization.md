# Design: evictMu Pin Fast-Path Deserialization (M0099-0002)

**Status**: draft  
**Milestone**: M0099-0002  
**Filed**: 2026-05-12

## Background

`internal/storage/bufpool.go` introduced 128 hash-partitioned buffer-tag maps
(M0098-0003) so concurrent `Pin` calls for different pages contend on different
`bufferPartition.mu` locks. However, every `Pin` call — cache hit or miss — must
still acquire the global `evictMu` mutex to safely increment `Slot.pinCount`.
With 100 concurrent goroutines (pgbench `-c 100 -j 100`) all pinning pages for
their queries, `evictMu` becomes the dominant serialization point and the primary
remaining bottleneck for all three pgbench workloads.

## Problem

`evictMu` currently protects all per-slot mutable state:
`pinCount`, `usageCount`, `valid`, `dirty`, and `tag` (on eviction).
It also protects the clock-sweep cursors `clockHand` and `bgwriterHand`.

A cache-hit `Pin` call today:

1. Acquires `part.mu` → looks up slot index in `part.byTag` → releases `part.mu`
2. Acquires `evictMu` → increments `slot.pinCount` → releases `evictMu`
3. Returns the slot

Step 2 serializes all concurrent `Pin` calls regardless of which page they target.
At 100 concurrent goroutines each pinning at ~1 ms/query, `evictMu` saturates at
~1,000 acquisitions/second with near-zero useful work per acquisition.

## Proposed Solution

### Option A: RWMutex for evictMu

Replace `evictMu sync.Mutex` with `evictMu sync.RWMutex`. Cache-hit `Pin`
takes `RLock()` (shared) to increment `pinCount`; eviction and clock-sweep
take `Lock()` (exclusive).

**Pros**: Minimal code change; allows N concurrent cache-hit Pins.  
**Cons**: `RLock` still has contention overhead under very high concurrency;
`sync.RWMutex` has writer-starvation behaviour that can delay evictions under
sustained read pressure.

### Option B: Atomic pinCount + CAS-based eviction claim (chosen)

Make `Slot.pinCount` an `atomic.Int32`. Remove `evictMu` from the cache-hit
path entirely. Use compare-and-swap (CAS) in the eviction path to atomically
claim a victim slot before the clock-sweep modifies its metadata.

**Cache-hit Pin fast path** (no evictMu):

```
1. part.mu.Lock()
2. idx, ok = part.byTag[tag]
3. if ok:
     sl = &pool.slots[idx]
     sl.pinCount.Add(1)      // atomic, no evictMu needed
     part.mu.Unlock()
     // Validate: slot was not evicted between lookup and Add
     if sl.tag != tag || !sl.valid {
         sl.pinCount.Add(-1)
         goto cache_miss
     }
     return sl, nil          // fast path done
4. else: goto cache_miss
```

**Cache-miss / eviction path** (evictMu for clock-sweep only):

```
evictMu.Lock()
for clock-sweep:
    sl = &pool.slots[clockHand]
    // Attempt to claim: CAS pinCount 0 → -1 (sentinel "evicting")
    if sl.pinCount.CompareAndSwap(0, -1):
        evictMu.Unlock()
        // Evict: flush if dirty, remove from old partition, do I/O
        // After I/O complete: set sl.tag, sl.valid = true, sl.pinCount = 1
        part.mu.Lock(); part.byTag[newTag] = idx; part.mu.Unlock()
        return sl, nil
    // Slot is pinned or being evicted; continue clock-sweep
    clockHand = (clockHand + 1) % capacity
evictMu.Unlock()
```

**pinCount sentinel semantics**:

| Value | Meaning |
|-------|---------|
| > 0   | Pinned by N readers; cannot evict |
| 0     | Unpinned; eligible for eviction |
| -1    | Being evicted; no new pins allowed until write completes |

The validation step (step 3, check tag after atomic Add) ensures that if the
eviction path CAS'd the slot to -1 between our byTag lookup and our Add, we see
pinCount == -1 (not 0) and the tag mismatch, so we safely back off and retry.

**Unpin** simply does `sl.pinCount.Add(-1)` (atomic, no evictMu needed).

**usageCount** remains guarded by evictMu since it is only touched during
eviction clock-sweep, not in the hot pin path.

## correctness invariants

1. A slot with `pinCount > 0` is never selected as an eviction victim (CAS from 0 to -1 fails).
2. A slot with `pinCount == -1` is invisible to concurrent Pins (tag mismatch or pinCount -1 check).
3. The eviction path always sets `pinCount = 1` (not 0) when it republishes the new slot, preventing a race between "eviction completes" and "caller returns pinned slot."
4. `WriteDirtyPages` / bgwriter only flushes pages where `pinCount == 0` and `dirty == true`; this is unchanged since dirty/valid remain under evictMu during their eviction-path modifications.

## Implementation Plan (M0099-0002)

1. Change `Slot.pinCount` field: `int` → `atomic.Int32` in `internal/storage/bufpool.go`.
2. Update `Unpin` to use `sl.pinCount.Add(-1)`.
3. Refactor `Pin`/`TryPin` cache-hit branch to use atomic Add + tag validation, removing the `evictMu` acquire.
4. Refactor `evictLocked` to use `CAS(0, -1)` for victim selection; on eviction completion set `pinCount = 1` before publishing the new tag.
5. Remove `evictMu.Lock/Unlock` from `Pin`'s cache-hit branch; keep only in `evictLocked`, `WriteDirtyPages`, and `bgwriterFlush`.
6. Update tests: `TestPinUnpinRace`, `TestConcurrentPinSamePage`, `TestEvictLocked*`.
7. Run `go test -race ./internal/storage/...` and `go test -race ./internal/...`.

## Expected Impact

- 100 concurrent `Pin` cache-hit calls can now proceed in parallel (only partition lock + atomic Add).
- evictMu contention drops to eviction-only traffic (rare relative to total Pin rate on a warm cache).
- Expected: 30–60% TPS improvement for all three pgbench workloads at `-c 100`.
- Select-Only: from ~5,000 TPS toward the 10,000 TPS target.
- Standard + Simple Update: secondary benefit (write path still WAL-bottlenecked, but pin contention is reduced).

## Files to Modify

| File | Change |
|------|--------|
| `internal/storage/bufpool.go` | `Slot.pinCount` atomic; Pin/TryPin/Unpin/evictLocked refactor |
| `internal/storage/bufpool_test.go` | Race and eviction tests updated |
| `docs/design/README.md` | Index entry |
