# Design 0072-0001 — indexScanOp slot-aware BindOuter (Q5 GC residual fix)

**Milestone:** M0072-0001
**Status:** draft
**Owner:** TBD
**Branch:** `gc-oriented-refactor` (continuation)
**Depends on:** `docs/design/0068-0002-tuple-slot-pipeline.md`
(slot pipeline contract — landed M0071-0011..0015).

## Context

The Q5 CPU+heap pprof captured during the M0071-0014
post-commit verification (`pprof-data/m0071-0014/`) shows the
slot pipeline successfully eliminated MHJ's `lazyOut` 99.23%
allocation source, dropping GC fraction from ~60% (M0067) to
~16%. The new dominant allocators are:

| Function                    | Q5 heap cum%  | Bytes (480 s) |
|-----------------------------|--------------:|--------------:|
| `nestedLoopIndexJoinOp.Next`| 70.12 %       | 1219 GB       |
| `indexScanOp.Rescan`        | 62.42 %       | 1085 GB       |
| `btree.RangeScan`           | **27.02 %**   | 470 GB        |
| `acquireRow` (row pool)     | **23.79 %**   | 414 GB        |

Both `btree.RangeScan` and `acquireRow` flow through
`indexScanOp.Rescan` — called per-outer-row by
`nestedLoopIndexJoinOp` from `internal/executor/operators_nljoin.go`.
The slot pipeline reaches the NLI's outer/inner emit path,
but the IndexScan's internal row materialisation is still on
the legacy Row contract:

1. `nestedLoopIndexJoinOp.boundRow` (allocated once at Open
   via `acquireRow(outerW + innerW)`) is filled per-outer
   with `outer ++ nullInner` and passed to
   `indexScanOp.BindOuter(row)` and `Rescan(row)`.
2. `indexScanOp.lookupKey / lookupRangeBounds / lookupKeys`
   call `evalExpr(ke, o.outerRow, o.ctx)` — Row-based
   eval; reads the outer prefix.
3. The BTree iteration callback (`scanFn`) calls
   `DecodeRow(o.plan.Table.Columns, tuple.Data)` per matched
   tuple — fresh `Row` allocation every tuple — then
   `append(o.rows, row)` accumulates. **No buffer reuse.**

The slot pipeline's `evalExprSlot` (`internal/executor/expr.go:30`,
M0071-0011) reads outer columns via `slot.Get(col)` without
materialising. We can pass the NLI's persistent `outerMS`
slot directly to BindOuter, eliminating `boundRow` entirely.
The decoded-row reuse is independent: mirror seqScanOp's
`o.scanRow` pattern (`operators_storage.go:72`) — decode
into a reusable buffer, `cloneRow` once on append.

## Goals

- Change `indexScanOp.BindOuter` to accept a `SlotView` and
  an explicit `outerWidth int`. Eliminate `o.outerRow Row`
  field.
- Rewire the three lookup sites (`lookupKey`,
  `lookupRangeBounds × 2`, `lookupKeys`) to call
  `evalExprSlot(ke, o.outerSlot, o.ctx)`.
- Delete `boundRow` from `nestedLoopIndexJoinOp` — pass
  `o.outerMS` directly; no per-outer concatenation.
- Reuse `o.scanRow` as the per-tuple decode buffer; `cloneRow`
  on `append(o.rows, ...)` so retention is explicit.

## Non-goals

- **Q9 chained-NLI rebind.** The slot read still uses the
  planner-bound `ColumnRef.Index`; this design alone does
  not move column resolution onto virtual coordinates.
  M0072-0002 handles Q9.
- **IndexOnlyScan slot-aware BindOuter.** IndexOnlyScan is
  never driven from NLI today (`operators_indexonly.go:208`
  uses `nil` for outer eval). Stub added for interface
  consistency; behaviour deferred to M0073.
- **Inner row arena.** `o.scanRow` reuse plus `cloneRow` on
  append still allocates per retained row. Per-batch
  String/Bytes arena (M0072-0004) is the deeper fix; this
  design drops the per-tuple Row alloc, not the per-string
  byte alloc.

## Proposed interface

### Signature change

```go
// indexScanOp.BindOuter — old:
func (o *indexScanOp) BindOuter(row Row)

// indexScanOp.BindOuter — new:
func (o *indexScanOp) BindOuter(slot SlotView, outerWidth int) {
    o.outerSlot = slot
    o.outerWidth = outerWidth
}

// indexScanOp.Rescan — old:
func (o *indexScanOp) Rescan(outerRow Row) error

// indexScanOp.Rescan — new:
func (o *indexScanOp) Rescan(outerSlot SlotView, outerWidth int) error {
    o.outerSlot = outerSlot
    o.outerWidth = outerWidth
    // ... existing rest of Rescan unchanged
}
```

`outerWidth` is captured explicitly so the lookup sites'
bounds checks remain equivalent to the legacy `len(o.outerRow)`
without requiring a Width() method on SlotView. The
single-table standalone `Open → Rescan(nil, 0)` path keeps
working: `nil` slot means "no outer correlation," matching
`evalExprSlot`'s nil-slot contract from M0071-0011.

### NLI driver simplification

```go
// nestedLoopIndexJoinOp.Next — old:
copy(o.boundRow[:o.outerWidth], outerRow)
copy(o.boundRow[o.outerWidth:], o.nullInner)
o.inner.BindOuter(o.boundRow)
if err := o.inner.Rescan(o.boundRow); err != nil { ... }

// nestedLoopIndexJoinOp.Next — new:
o.outerMS.row = outerRow
o.inner.BindOuter(o.outerMS, o.outerWidth)
if err := o.inner.Rescan(o.outerMS, o.outerWidth); err != nil { ... }
```

The `nullInner` padding in `boundRow` was decorative — the
IndexScan only ever reads outer-side columns from the bound
row (verified via the `lookupKey` / `lookupRangeBounds`
audit). Deleting `boundRow` saves one `acquireRow(outerW +
innerW)` per Open and zero work per outer row.

### Decoded-row buffer reuse

```go
// scanFn (BTree iteration callback) — old:
row, err := DecodeRow(o.plan.Table.Columns, tuple.Data)
// ...
o.rows = append(o.rows, row)  // alloc per tuple, no clone

// scanFn — new:
if err := DecodeRowInto(o.scanRow, o.plan.Table.Columns, tuple.Data); err != nil {
    // ... handle error
}
// Detoast / projection / etc. on o.scanRow
o.rows = append(o.rows, cloneRow(o.scanRow))
o.tids = append(o.tids, tid)
```

`o.scanRow` is allocated once at Open (lazy on first decode).
`cloneRow` on append is the M0054-0005a pattern from
seqScanOp; retention is explicit. Net effect: one
allocation per retained row (the clone) instead of two
(`DecodeRow` makes a fresh `Row`, then append stores the
reference).

## Migration plan

Single-stage: all changes in one commit (Commit B per the
M0072 plan). The signature change is breaking but localised
— only `nestedLoopIndexJoinOp` calls `BindOuter` / `Rescan`
in production. Tests that drive `indexScanOp` directly
(`internal/executor/operators_index_test.go` etc.) are
updated to pass `nil, 0` for the standalone path.

## Verification

**Pre-commit gate** (per M0072 plan):
- Build server, fresh-restart.
- `./tpch-runner --queries=12,13,21
  --per-query-timeout=400s --cancel-after=380s` —
  Q12=2, Q13=35, Q21≥100. If any differs, do not commit.
- `go test ./internal/planner/... ./internal/executor/...
  ./internal/testutil/tpch/...` PASS.
- 21-query SF=1 sweep: all rows match the Phase-3 baseline.

**Q5 heap pprof rerun** (M0072-0001 specific):
```sh
mkdir -p pprof-data/m0072-0001
( go tool pprof -seconds=120 -output=pprof-data/m0072-0001/q5.cpu.prof \
    http://127.0.0.1:6060/debug/pprof/profile ) &
sleep 1
./tpch-runner --queries=5 --per-query-timeout=620s
wait
curl -s -o pprof-data/m0072-0001/q5.heap.prof \
    http://127.0.0.1:6060/debug/pprof/heap
go tool pprof -top -cum -sample_index=alloc_space \
    pprof-data/m0072-0001/q5.heap.prof | head -20
```

Acceptance: `acquireRow` cumulative drops sharply (target
≤ 5%; was 23.79%). `btree.RangeScan` cumulative drops to
≤ 15% (was 27.02%). `nestedLoopIndexJoinOp.Next` and
`indexScanOp.Rescan` cumulative reductions track with the
above.

**New unit test** —
`internal/executor/nlj_indexscan_slot_test.go`:
- Drive `nestedLoopIndexJoinOp` with a fake outer producing
  3 rows; assert `indexScanOp.outerSlot` is correctly bound
  and `evalExprSlot` resolves outer ColumnRef via slot.Get.
- Pin `cloneRow on append` invariant: instrument `acquireRow`
  call count; assert it does NOT scale with number of decoded
  inner tuples.

## Risks

| # | Risk | Mitigation |
|---|------|-----------|
| R1 | Lookup paths read outer column N where N >= outerWidth → panic on slot.Get | Capture outerWidth explicitly at BindOuter time; assert in lookup sites that `0 <= N < outerWidth`. Mirrors the legacy `len(o.outerRow)` bound. |
| R2 | Standalone IndexScan path (`Open → Rescan(nil, 0)`) breaks if any lookup expression has a ColumnRef | Single-table IndexScan key expressions reduce to constants by planner contract; verified in `internal/planner/index.go`. evalExprSlot with nil slot returns the structured error preserved from M0071-0011. |
| R3 | `cloneRow` on append still allocates per retained row | This is acknowledged scope; M0072-0004 (arena) is the deeper fix. The current target is `acquireRow` ≤ 5% (down from 23.79%), not zero. |
| R4 | `o.scanRow` reused buffer aliases past the next decode → silent corruption if a consumer retains | Caller MUST `cloneRow` on append; documented at the buffer's declaration site. New unit test pins this with a buffer-mutation probe. |
| R5 | IndexOnlyScan stub mismatch — adding BindOuter / Rescan to the interface would force IndexOnlyScan to implement them | Keep BindOuter / Rescan as concrete methods on `*indexScanOp` only (NOT on a shared interface). NLI calls `*indexScanOp` directly per the M0054-0006 contract; no interface change needed. |

## References

- `docs/design/0068-0002-tuple-slot-pipeline.md` — slot
  contract (TupleSlot / SlotView / Materialize).
- `internal/executor/expr.go:30` — `evalExpr` /
  `evalExprSlot` keystone (M0071-0011).
- `internal/executor/operators_index.go:65-87` — indexScanOp
  field layout (target of refactor).
- `internal/executor/operators_index.go:143-254` —
  BindOuter / Rescan (target of signature change).
- `internal/executor/operators_index.go:217-248` — scanFn
  callback (target of buffer reuse).
- `internal/executor/operators_nljoin.go:38-251` —
  nestedLoopIndexJoinOp (target of `boundRow` deletion).
- `internal/executor/operators_storage.go:72-249` —
  seqScanOp's `o.scanRow` pattern (precedent to mirror).
- `pprof-data/m0071-0014/q5.cpu.prof` /
  `q5.heap.prof` — the captures driving the targets.
