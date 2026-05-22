# 0107-0007d — `Pool.SlotPinCount(tag)` lock-free pin-count probe

Milestone: M0107-0007 (Phase D4: WAL insert striping + FSM page
distribution) — slice C foundation 3 of 3.

Parent design: `docs/design/perf-optimize/07-wal-fsm-insert.md` §3
(FSM-driven pin-count-aware page ranking); helper spec from
`docs/design/perf-optimize/06-bufpool-lockfree.md` §4 ("Helper:
`Pool.SlotPinCount`").

## Why

The parent design's `selectInsertPage` ranks FSM-returned candidate
pages by current pin count and prefers the coldest one, so concurrent
inserters land on different pages instead of all piling onto the tail.
The ranking loop reads pin counts at hot-path rates (once per candidate
per insert), so the probe must be lock-free and must not touch the
slow-path `pinMu`.

Inlining the bit-mask arithmetic at every caller would scatter
knowledge of `slotState`'s layout (`slotPinMask` / `slotGenShift` /
`slotValidBit`) across the codebase. A helper method on `*Pool`
isolates the layout so future re-packings of `slotState` ripple no
further than `bufpool.go`.

## What landed

`Pool.SlotPinCount(tag BufferTag) int32` in
`internal/storage/bufpool.go`:

```go
func (p *Pool) SlotPinCount(tag BufferTag) int32 {
    slotIdx, gen := p.bm.Lookup(tag)
    if slotIdx < 0 {
        return 0
    }
    s := &p.slots[slotIdx]
    st := s.state.Load()
    if !stateValid(st) || stateGen(st) != gen {
        return 0
    }
    return int32(statePin(st))
}
```

Semantics:

- **Unmapped tag → 0.** `bufmap.Lookup` returns `slotIdx == -1` for
  tags absent from the table (never published, deleted, or
  tombstoned).
- **Mapped but stale gen → 0.** Between the `Lookup` and the
  `state.Load`, the slot may have been evicted and reused for a
  different tag. The seqlock-protected `bufmap` will not point us at
  the wrong slot, but a window exists where the slot we read carries
  a higher `gen` than the `(slotIdx, gen)` we observed. We treat that
  as "no longer mapped" — pin count of the new occupant has no bearing
  on the caller's decision about the original tag, so 0 is the right
  conservative answer.
- **Mapped but invalid → 0.** A slot under eviction (`!stateValid`)
  carries stale page bytes and is not a legal target for the FSM
  ranking; treat as 0 so callers don't prefer it.
- **Mapped + valid + gen matches → live pin count.** Single
  `state.Load()` read; no further coordination.

The helper does **not** acquire `pinMu` and does **not** mutate any
state. It is safe to invoke concurrently with `Pin` / `Unpin` /
`pinSlow` / `claimVictim` / `evictVictim`.

## Why the gen check matters

`bufmap.Lookup` performs a seqlock snapshot, so a torn `(slotIdx, gen)`
is detected and retried internally — when `Lookup` returns, the
`(slotIdx, gen)` it produced was at some point published together.

However, by the time the caller dereferences `&p.slots[slotIdx]` and
reads `s.state.Load()`, the slot's occupant may have changed: a victim
sweep evicted the old occupant (incrementing `gen`) and `pinSlow`
published a new tag. The new occupant's pin count is unrelated to
the caller's tag, so we must reject it via `stateGen(st) != gen`.

Without the gen check, the FSM ranking would treat a freshly-pinned
unrelated tag as a hot page for the candidate it was probing —
the worst-case effect is misranking (suboptimal page choice), but
the gen check costs one extra arithmetic op and removes the
correctness gap entirely.

## Why `!stateValid` returns 0

A slot transitioning through eviction goes through a window where it
is no longer the live occupant but the new occupant has not yet
published. During that window `stateValid` is false and `gen` may
have advanced. Both conditions catch the slot; we use `!stateValid`
as the cheaper primary check (no shift/mask vs the gen comparison),
and the gen comparison as a defence in depth for the (rare) case
where a slot has been re-validated for a different tag between our
`Lookup` and our `state.Load`.

## Cost

One `bufmap.Lookup` (one or two cache misses on the inner buckets
array; one atomic load on `inner`) plus one `state.Load` (one atomic
load on the slot). No mutex acquire, no condvar wait, no syscall.
Suitable for invocation from the heap-insert hot path at one call
per FSM candidate per insert (typically 4 candidates per insert per
the parent design's `candidatesPerInsert`).

## Tests (`internal/storage/storage_test.go`)

- `TestSlotPinCountUnmappedTag` — never-pinned tag returns 0.
- `TestSlotPinCountReflectsPinUnpin` — Pin → 1; second Pin → 2; Unpin
  → 1; final Unpin → 0. Mapping persists across the full unpin so the
  helper still hits the `bm.Lookup → state.Load` path on the final
  assertion (not the early `slotIdx < 0` return).
- `TestSlotPinCountAfterEviction` — after `InvalidateRel` clears the
  mapping, the probe returns 0 via the `slotIdx < 0` path.
- `TestSlotPinCountIsolatesByTag` — three tags pinned at counts
  3/1/0; the probe returns the correct count per tag (no cross-tag
  bleed; unpinned mapped tag returns 0).

Verified:

```
go test -race -count=1 -run 'TestSlotPinCount' ./internal/storage/   # 1.02 s PASS
go test -race -count=1 ./internal/storage/                           # 5.38 s PASS
```

## Slice C status

This is the third (and final) foundation for slice C of M0107-0007.
With all three foundations landed —

1. `FSM.GetCandidates(rel, minFreeBytes, n)` — top-K free-space
   ranking (`0107-0007b-fsm-get-candidates.md`).
2. `Pool.ExtendRelationBatch(rel, n)` — batched empty-page append
   (`0107-0007c-pool-extend-relation-batch.md`).
3. `Pool.SlotPinCount(tag)` — lock-free pin-count probe (this doc).

— the parent §3 executor consumer (`selectInsertPage` in
`internal/executor/operators_storage.go`) can now be written: it will
call `FSM.GetCandidates` for top-N candidates, rank them by
`Pool.SlotPinCount`, batch-extend via `Pool.ExtendRelationBatch` when
no candidate has enough free space, and register the extras in the
FSM. That executor work is the remaining slice-C task and is a
behaviour-changing change to the heap-insert hot path — it will land
in its own loop with the PG-compat WAL byte-diff gate from the parent
milestone.

Slice B (8-stripe `wal.Writer.appendLocks` per parent §2) remains
deferred — splitting `state.appendMu`'s four invariants (writePos,
walBuf state, memRing append, writeLSN advance) into per-stripe local
state vs. shared state is multi-loop scope.
