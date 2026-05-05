# Milestone 0056 — Buffer-Pool PinNew Race Fix + B-tree splitMu Removal

**Status:** in progress
**Depends on:** Milestone 0002 (concurrent B-tree), Milestone 0055 (Phase C INCOMPLETE_SPLIT lifecycle).
**Drives:** Production-grade B-tree write scalability — splitMu can finally be retired from the steady-state structural-update path now that the underlying buffer-pool concurrency bug it was masking is fixed.

## Context

The M0055 Stage 2 splitMu-removal experiment surfaced a buffer-pool bug:
under `-race` stress with 32 concurrent B-tree writers and frequent splits,
`storage.(*Pool).Unpin` panics with `unpin underflow on tag {…}`. The bug
does NOT appear without the race detector — the race detector's aggressive
scheduling exposes a window that's narrow but real.

Root cause: `Pool.PinNew` (used by `(*BTree)`'s split path to allocate the
fresh right-sibling page) has an I/O window between releasing `poolMu` and
re-acquiring it where the just-evicted slot has `pinCount = 0` and
`tag = BufferTag{}`. During this window, a concurrent `Pool.Pin` call
running `evictLocked` is free to choose the SAME slot as its victim
(eviction policy: pinCount==0 && usageCount==0). The concurrent Pin
publishes a different tag on the slot. When the original PinNew goroutine
re-acquires `poolMu` and overwrites `s.tag = tag` and `s.pinCount = 1`,
it tramples the concurrent Pin's reservation. Subsequent operations on
the same slot from BOTH goroutines decrement pinCount independently,
producing the underflow panic.

This bug pre-dates M0055; M0055-0004's multi-writer stress test is what
made it surface reliably. With M0055's splitMu still in the slow path,
the bug is masked because two writers can't both be in the slow-path
allocation window simultaneously.

## Scope

### Phase A — PinNew slot reservation (M0056-0001)

Hold the slot's pinCount across the I/O window so concurrent Pin callers'
`evictLocked` skips over it. Specifically, set `s.pinCount = 1` BEFORE
releasing `poolMu` in `PinNew`, mirroring the regular `Pin` path which
already does this at line 717 of `bufpool.go`.

The post-I/O re-acquisition path must be updated to NOT overwrite
pinCount (since the reservation already counted as 1) and to release
the reservation on error.

### Phase B — Re-enable -race for the multi-writer stress test (M0056-0002)

Remove the `raceEnabled` skip gate added in M0055-0004-followup-stage2-
splitmu-removal from `internal/access/btree/multi_writer_stress_test.go`.
With Phase A landed, the test must PASS under `-race` repeatably (≥10
iterations).

### Phase C — Remove splitMu from `Insert`'s slow path (M0056-0003)

With the bufpool race fixed, the M0055 INCOMPLETE_SPLIT lifecycle plus
per-page latches plus the race-safe `createNewRoot` re-read are
together sufficient to make `Insert`'s slow path lock-free above the
page-latch level. Specifically:

- `(*BTree).Insert`'s split path drops `bt.splitMu.Lock()` /
  `defer bt.splitMu.Unlock()`.
- `(*BTree).finishSplit`'s recursive descent path drops the same.
- `bt.splitMu` is renamed to a no-op or removed entirely.

Per-page latches (held by `pinW`/`unpinW`) serialise concurrent
mutations at each level. The race-safe `createNewRoot` handles the
new-root publication race. INCOMPLETE_SPLIT + `CompleteDeferredSplits`
handle crash-replay completion.

### Phase D — End-to-end validation (M0056-0004)

- All existing btree concurrent tests PASS under `-race` (≥10 iterations).
- `TestMultiWriterStress_M0055_Phase_C` passes under `-race` repeatably.
- New `TestMultiWriterStress_M0056_Heavy` extends the workload (16 ×
  5000 = 80K inserts in narrow per-writer ranges, forcing many splits
  per writer) and asserts no lost/duplicate keys.
- Full repo regression `go test ./... -count=1` PASS.

## Required Design Docs

- `docs/design/0056-0001-bufpool-pinnew-slot-reservation.md` —
  protocol for holding the slot reservation across the I/O window.

## Definition of Done

1. `Pool.PinNew` sets pinCount before releasing poolMu; the I/O window
   no longer exposes the slot to eviction.
2. `TestMultiWriterStress_M0055_Phase_C` and (new) `TestMultiWriterStress_M0056_Heavy`
   pass under `-race` repeatably (≥10 iterations).
3. `splitMu` is removed from `(*BTree).Insert`'s slow path.
4. The btree split protocol's correctness is validated by
   `internal/access/btree/multi_writer_stress_test.go` tests under
   `-race`.

## Out of Scope

- New buffer-pool eviction policies.
- B-tree internal-node split protocol changes beyond what M0055 already
  delivered.

## Reference

- `analysis/btree-staged-enhancement-results-2026-05-06.md` §10 DoD
  table footnote (the `M0055-bufpool-pin-race` flag).
- `internal/storage/bufpool.go::Pool.PinNew` (the I/O window).
- `internal/storage/bufpool.go::Pool.Pin` line 717 (the correct reference
  pattern: pinCount=1 before releasing poolMu).
- `internal/access/btree/btree.go::(*BTree).Insert` (the splitMu callsite).
