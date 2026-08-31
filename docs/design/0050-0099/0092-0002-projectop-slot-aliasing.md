# Design 0092-0002 — projectOp slot aliasing

**Status:** authoritative for M0092-0002 implementation.
**Milestone:** [M0092](../../milestones/0092-lazy-row-emission-in-scan-and-project.md).

## Problem

`internal/executor/operators.go:94` currently does:

```go
return asSlot(o.schema, cloneRow(o.out)), nil
```

`cloneRow(o.out)` allocates a fresh Row via `acquireRow →
rowPool.New → make(Row, width)`. Because consumers retain
slot references past Close, the cloned Rows never return to
the pool — the pool is cold, every cloneRow allocates fresh.
This is the load-bearing residual after M0091 (34 % of allocs
per query).

## Approach

Drop the cloneRow:

```go
return asSlot(o.schema, o.out), nil
```

The returned slot aliases `o.out`. On the next Next() call,
`o.out` is overwritten with the projection of the next input
row. **Contract: caller must consume / materialize before
the next Next() call.**

## Consumer audit

Per the Explore agent investigation:

| consumer | retain? | safety |
|---|---|---|
| `filterOp` (operators.go) | no — pass-through | ✓ |
| `limitOp` (operators.go) | no — pass-through | ✓ |
| `sortOp` (operators.go:296) | yes; calls `Materialize()` | ✓ |
| `windowOp` (operators_window.go:42) | yes; calls `Materialize()` | ✓ |
| `lockRowsOp` (operators_lockrows.go:240) | yes; calls `Materialize()` | ✓ |
| `joinOp` (operators_join_agg.go:94/149) | yes; `drainRowsCtx` copies | ✓ |
| `aggregateOp` (operators_join_agg.go:790) | extracts Datums via `evalExpr` + `MaterializeArena` | ✓ |
| `recursiveUnionOp` (operators_recursive_cte.go:66/99) | yes; explicit `copy()` | ✓ |
| Simple-query result loop (dispatch.go:355-379) | no — formats cells fresh per iter | ✓ |
| Extended-query result loop (dispatch_extended.go:117-139) | no — formats cells fresh per iter | ✓ |
| **`nestedLoopIndexJoinOp.currentOuter` (operators_nljoin.go:211)** | **yes — alias-only** | **✗ violator** |

## Prerequisite — fix `nestedLoopIndexJoinOp`

`operators_nljoin.go:211` currently stores `o.currentOuter =
outerRow` without cloning. After this commit removes the
cloneRow in projectOp, `outerRow` aliases `o.out` and the
next outer Next() overwrites `o.out` — corrupting
`o.currentOuter` mid-inner-loop.

The fix (lands in Commit B before this design's
implementation):

```go
outerRow := slotRow(outerSlot)
if cap(o.currentOuter) < len(outerRow) {
    o.currentOuter = make(Row, len(outerRow))
} else {
    o.currentOuter = o.currentOuter[:len(outerRow)]
}
copy(o.currentOuter, outerRow)
```

This preserves the per-NLI buffer reuse pattern (no fresh
allocation per outer row when capacity is sufficient) while
making `o.currentOuter` independent of upstream buffer reuse.

## Test coverage

- Existing executor test suite must pass unchanged.
- Existing TPC-H + pgbench correctness tests must pass.
- If any test breaks, that's an unaudited consumer — either
  (a) add Materialize at that consumer or (b) revert this
  commit.

## Expected impact

- Per-query cloneRow allocation eliminated from projectOp
  (~88 KB / query for pgbench select-only's 1-row result).
- GC pressure drops correspondingly.
