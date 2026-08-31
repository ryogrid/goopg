# Design 0092-0007 — SlotFromRow stack-aliasing

**Status:** authoritative for M0092-0007 implementation.
**Milestone:** [M0092](../../milestones/0092-lazy-row-emission-in-scan-and-project.md).

## Problem

`internal/executor/slot.go::SlotFromRow`:

```go
func SlotFromRow(s planner.Schema, r Row) *MaterializedSlot {
    return &MaterializedSlot{schema: s, row: r}
}
```

Heap-allocates a fresh `MaterializedSlot` (32 B + GC
overhead) per call. Called from every operator's `Next()`
via `asSlot`. For pgbench's select-only path: 2 calls per
query (indexScanOp.Next + projectOp.Next) = ~64 B / query
at 437 TPS = ~28 KB/s.

The slot pointer is treated as ephemeral by callers (M0092
contract: "slot valid until next Next()"). So the same slot
struct could be reused per Next() call by embedding it on
the operator.

## Approach

Each leaf operator carries an embedded `slot
MaterializedSlot` field. `Next()` updates the slot's fields
in-place and returns `&o.slot`. No per-call allocation.

```go
type indexScanOp struct {
    ...
    slot MaterializedSlot  // embedded; reused per Next()
}

func (o *indexScanOp) Next() (TupleSlot, error) {
    ...
    o.slot.schema = o.Schema()  // can hoist to Open
    o.slot.row = row
    return &o.slot, nil
}
```

Contract: `&o.slot` is valid until the NEXT Next() call (or
Close()). The caller MUST NOT retain the pointer across
Next() boundaries. If retention is needed, the caller calls
`slot.Materialize()` which already produces an independent
slot per M0092-0002.

## Migration scope

Apply to:
- `indexScanOp` (operators_index.go)
- `indexOnlyScanOp` (operators_indexonly.go)
- `seqScanOp` (operators_storage.go)
- `projectOp` (operators.go)

These are the leaf / projection ops on the hot path.

Defer to later (out of M0092-0007 scope):
- `filterOp`, `limitOp` (pass-through; just return child's slot)
- `sortOp`, `aggregateOp`, `joinOp` (materialize internally;
  emission slot is already typically &o.outSlot)
- `nestedLoopIndexJoinOp` (has its own outerMS /
  joinBuf state)

The hash/sort/join ops already pattern-match this design
(they have outSlot fields); only the leaf scan / project
ops are still using SlotFromRow per Next.

## Safety

The M0092-0002 audit already confirmed all consumers
either:
- consume slot immediately (filterOp, limitOp, server
  dispatch loop) — safe with aliasing.
- call Materialize before retention — safe with aliasing
  (Materialize always deep-copies post-M0092-0002).

The new "&o.slot" pointer is stable across Next() calls
(same address). A consumer that accidentally retains the
pointer would see the LATEST row, not the one it expected.
This matches the existing "row aliases internal buffer"
contract from M0092-0002; we're just narrowing the slot
struct alongside.

## Test coverage

- Existing tests must pass.
- New unit test: confirm `&o.slot` returned by successive
  Next() calls is the same address (pins the contract).
- Existing `BenchmarkIndexScanPointLookup` should show 1
  fewer alloc/op.

## Expected impact

- 32 B / Next saved per slot-emitting operator.
- pgbench select-only: 2 ops × 32 B = 64 B / query.
- 437 TPS × 64 B = ~28 KB/s allocation reduction.
- Smaller than M0092-0004 / M0092-0006 but trivial to
  implement and tests well.
