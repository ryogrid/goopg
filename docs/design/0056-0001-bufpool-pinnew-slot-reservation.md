# 0056-0001 — Bufpool PinNew Slot Reservation

| field      | value |
|------------|-------|
| status     | draft |
| date       | 2026-05-06 |
| supersedes | — |

## 1. Problem

`internal/storage/bufpool.go::(*Pool).PinNew` releases `poolMu` mid-flow
to perform the page-extend I/O. During that window, the just-evicted
slot has the following observable state:

- `s.valid = false`
- `s.dirty = false`
- `s.tag = BufferTag{}` (zero, but no entry in `byTag` references this slot)
- `s.pinCount = 0`
- `s.usageCount = 0`

A concurrent `Pool.Pin(tagX)` call (any other goroutine wanting any
unrelated tag) acquires poolMu, runs `evictLocked`, and finds this
exact slot eligible for eviction (the eviction predicate is
`pinCount == 0 && usageCount == 0` per the clock-sweep at
`evictLocked` lines 1140-1146). The concurrent Pin reserves the slot
for `tagX`, sets `s.pinCount = 1`, publishes `byTag[tagX] = slotIdx`,
and releases poolMu.

When PinNew re-acquires poolMu (line 617) and runs:

```go
s.tag = tag
s.valid = true
s.dirty = true
s.pinCount = 1   // line 638
s.usageCount = 1
p.byTag[tag] = slotIdx
```

it tramples the concurrent Pin's tag/pinCount/byTag entry. From this
point both the original Pin caller and PinNew caller hold a `*Slot`
reference to the same slot, but the slot's `byTag` entry only points
back via PinNew's tag. The original Pin caller's eventual `Unpin`
sees `pinCount = 1`, decrements to 0. The PinNew caller's Unpin then
panics with `unpin underflow on tag {…}` because pinCount is already 0.

The bug pre-dates M0055 but is masked in single-writer-split workloads
because PinNew is called only from the B-tree split path which was
serialised by `splitMu`. Removing `splitMu` (M0055-0004-followup-stage2)
re-exposed the race, surfaced under `-race`'s aggressive scheduling.

## 2. Fix

Reserve the PinNew slot BEFORE releasing poolMu, so concurrent
`evictLocked` skips it (its `pinCount > 0` check at line 1140 will
match).

```go
func (p *Pool) PinNew(rel RelFileNode) (*Slot, BlockNumber, error) {
    p.poolMu.Lock()
    slotIdx, err := p.evictLocked()
    if err != nil {
        p.poolMu.Unlock()
        return nil, InvalidBlockNumber, err
    }
    s := p.slots[slotIdx]

    if s.valid && s.dirty {
        ... // flush victim under contentMu
    }
    delete(p.byTag, s.tag)
    s.valid = false
    s.dirty = false
    s.tag = BufferTag{}      // reserved-but-not-yet-published
    s.pinCount = 1           // <-- THE FIX: reserve before unlocking
    s.usageCount = 1
    p.poolMu.Unlock()

    s.contentMu.Lock()
    if err := InitPage(s.page); err != nil {
        s.contentMu.Unlock()
        // Roll back the reservation — slot returns to the free pool.
        p.poolMu.Lock()
        s.pinCount = 0
        s.usageCount = 0
        p.poolMu.Unlock()
        return nil, InvalidBlockNumber, err
    }
    blk, err := p.mgr.Extend(rel, s.page)
    s.contentMu.Unlock()
    if err != nil {
        p.poolMu.Lock()
        s.pinCount = 0
        s.usageCount = 0
        p.poolMu.Unlock()
        return nil, InvalidBlockNumber, err
    }

    tag := BufferTag{Rel: rel, Block: blk}
    p.poolMu.Lock()
    if idx, ok := p.byTag[tag]; ok {
        // Some other goroutine already published this tag.
        // Hand off to that slot, release ours.
        existing := p.slots[idx]
        existing.pinCount++
        if existing.usageCount < maxUsageCount {
            existing.usageCount++
        }
        s.tag = BufferTag{}
        s.valid = false
        s.dirty = false
        s.pinCount = 0     // <-- release our reservation
        s.usageCount = 0
        p.poolMu.Unlock()
        return existing, blk, nil
    }
    s.tag = tag
    s.valid = true
    s.dirty = true
    // pinCount is already 1 from reservation — DO NOT overwrite.
    p.byTag[tag] = slotIdx
    p.poolMu.Unlock()
    return s, blk, nil
}
```

The diff vs the current code is one line added (the early `s.pinCount = 1`
reservation), one line removed (the late `s.pinCount = 1` overwrite),
plus the two error-path rollbacks.

## 3. Why this is safe

The reservation makes the slot indistinguishable to `evictLocked`
from any other in-use slot. The clock-sweep predicate
`if s.pinCount > 0 { continue }` skips the slot.

The `s.tag = BufferTag{}` zero value during the I/O window is fine —
no other goroutine can reach this slot via `byTag` (it was just
deleted) and the slot itself is excluded from eviction.

The error path rollback releases the reservation atomically under
poolMu so the slot rejoins the free pool.

## 4. Alternatives considered

### A. Per-slot mutex on PinNew's I/O window

Heavyweight; introduces a per-slot lock that `Pin` would also have to
respect. The pinCount field already has the necessary semantics —
reusing it is cleaner.

### B. Two-step PinNew with caller-managed reservation

Caller calls `Pool.ReserveSlot()` then `Pool.PinNewOnReserved(slot, rel)`.
This pushes the reservation into the API surface. Considered overkill
for a single internal use case (B-tree's split path).

### C. Hold poolMu across the I/O

Defeats the purpose of releasing it for I/O parallelism. Would
serialise all PinNews tree-wide.

The chosen fix (reservation pinCount=1) is the minimal change that
restores correctness without altering the API surface.

## 5. Tests

- `internal/storage/bufpool_test.go::TestPinNewConcurrentEviction`
  (new): N goroutines call PinNew concurrently; each writes a
  distinct value to its returned slot; at the end every slot's
  contents match the writer's expected value (verifies no slot
  was stolen mid-I/O).
- `internal/access/btree/multi_writer_stress_test.go` re-enabled
  under `-race` (drop the `raceEnabled` skip).

## 6. Acceptance

- `go test ./internal/storage/... -count=1 -race -timeout 60s` PASS.
- `go test ./internal/access/btree/... -count=10 -race -timeout 180s`
  PASS — no unpin underflow, no deadlock.
- `go test ./... -count=1 -timeout 300s` PASS.

## 7. References

- `internal/storage/bufpool.go::(*Pool).Pin` line 717 (the correct
  reference pattern: pinCount=1 before releasing poolMu).
- `internal/storage/bufpool.go::evictLocked` lines 1140-1146 (the
  eviction predicate skipped by pinCount > 0).
- `analysis/btree-staged-enhancement-results-2026-05-06.md` §10
  (the M0055-bufpool-pin-race flag).
