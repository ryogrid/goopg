# Design: Buffer Pool 128-Partition Locking (M0098-0003)

**Status**: accepted  
**Milestone**: M0098-0003 — Buffer pool 128-partition locking  
**Expected gain**: 3–6× TPS for Select Only at -c 100; 1.5–2× for write workloads

## Problem

The buffer pool uses a single `poolMu sync.Mutex` that guards every `byTag` lookup,
slot metadata mutation, `ioByTag` update, and clock-sweep hand. At -c 100, 100 goroutines
all serialize on this one mutex for every Pin/Unpin operation. `WriteDirtyPages` = 16.67%
CPU in the M0093 pprof profile.

Baseline (M0098-0001): Select Only 6,166 TPS; target 10,000 TPS (1.6× gap).

## Design

### Core idea: 128 independent partition mutexes

Replace the global `poolMu` + `byTag` map with 128 `bufferPartition` structs:

```go
type bufferPartition struct {
    mu      sync.Mutex
    byTag   map[BufferTag]int
    ioByTag map[BufferTag]struct{}
    ioCond  *sync.Cond   // sync.NewCond(&mu)
}
```

A `BufferTag` maps to `partitions[tagHash(tag) & 127]`.  
`tagHash` is a FNV-1a mix of the four fields (DBOid, RelOid, Fork, Block).

### Slot metadata: move under partition lock

All slot metadata currently under `poolMu` (`tag`, `valid`, `dirty`, `pinCount`,
`usageCount`, `fpiSinceCheckpoint`) moves under the partition lock for the slot's
current tag. A slot's partition assignment is stable while it is pinned.

**`pinCount` stays `int32`** (not atomic) — accessed exclusively under partition lock.

### Eviction coordination: separate `evictMu`

The clock-sweep hand (`clockHand`, `bgwriterHand`) and the victim-selection loop move
behind a separate `evictMu sync.Mutex`. This prevents two goroutines from racing to
evict the same slot while still being independent from the partition locks.

Eviction protocol:
1. Lock `evictMu`
2. Scan from `clockHand` forward
3. For each candidate: lock its partition, check pinCount == 0 and usageCount
4. If evictable: remove from old partition's byTag, unlock old partition, do I/O, then lock new partition, publish new tag

### ioCond semantics

Each partition has its own `ioCond = sync.NewCond(&partition.mu)`.  
Callers waiting for in-flight I/O on a tag call `partition.ioCond.Wait()`, which
atomically releases the partition mutex (not a global mutex), matches exactly one
tag's partition.

### Lock ordering (deadlock prevention)

When multiple partition locks are needed (e.g., eviction that changes a slot's tag):
- Always acquire `evictMu` first
- Then acquire partition locks in increasing index order

Pin cold path:
1. Lock partition[i] for tag lookup
2. If miss: add to ioByTag, unlock partition[i]
3. Lock evictMu → evict (locks old partition, removes from old byTag, unlock old partition) → unlock evictMu
4. Do I/O (no locks)
5. Lock partition[i] → check ioByTag/byTag → publish → unlock

## Files changed

| File | Change |
|------|--------|
| `internal/storage/bufpool.go` | Replace poolMu + byTag + ioByTag + ioCond with 128 bufferPartition; add evictMu; update all Pool methods |
| `internal/storage/page.go` | Add tagHash(BufferTag) function |
| `docs/design/README.md` | Index entry |

## Correctness invariants

- A slot with pinCount > 0 is never evicted (partition lock prevents concurrent evict + pin)
- ioByTag prevents two goroutines from racing to load the same page (ioCond.Wait serializes them)
- evictMu prevents two goroutines from evicting the same slot simultaneously
- Lock ordering (evictMu before partitions) prevents deadlock
- contentMu per slot is orthogonal (guards page bytes, not slot metadata)
