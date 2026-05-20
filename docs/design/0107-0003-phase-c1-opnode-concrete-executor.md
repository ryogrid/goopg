# 0107-0003 Phase C.1 — Concrete-Type Volcano Executor (OpNode Framework)

**Status**: accepted  
**Milestone**: M0107-0003  
**Parent design**: `docs/design/perf-optimize/03-executor-concrete.md`  
**Commit**: Phase C.1 initial landing

---

## Problem

`internal/executor` has a two-level interface abstraction:

1. `Operator` interface (4 methods: Open/Next/Close/Schema) — 41 concrete types
2. `TupleSlot` interface (6 methods) — returned by `Next()`

Every row in the hot execution path incurs:
- One itab lookup + indirect call for `Operator.Next()`
- One itab lookup + indirect call for `TupleSlot.Row()` / `TupleSlot.Get()`
- `Materialize()` deep-copy at every retention boundary (`executor.Run`,
  sort, hash-join, aggregate)

Additionally, the GC scans `MaterializedSlot.row []Datum` and the embedded
`arena *mctx.Context` pointer (now `ArenaID`, post-Phase B) in every Datum
per GC cycle, proportional to row throughput.

## Solution — Phase C.1

Phase C.1 of the design doc lands the framework and migrates the four most
common hot-path operators to concrete dispatch.

### New types (`internal/executor/opnode.go`)

**`Slot`** — a concrete, stack-allocatable row vessel:

```go
type Slot struct {
    schema planner.Schema
    Cells  []Datum
    HasRow bool  // false == DML nil-row; true == real row (may have 0 cols)
}
```

`Slot` implements both `TupleSlot` and `SlotView` so it is a drop-in
replacement at all call sites that accept those interfaces. `HasRow`
distinguishes DML nil-rows (INSERT/UPDATE/DELETE returns `(nil, nil)` to
signal "no result row") from valid rows whose column list happens to be
empty (e.g. the input to `SELECT 1`, which has a zero-column Values node).

**`OpNode`** — the tagged-union operator node:

```go
type OpNode struct {
    Kind   OpKind        // discriminant
    childA *OpNode       // primary child; nil if none
    childB *OpNode       // secondary child; nil if none
    state  any           // per-Kind state struct (GC-traced)
}
```

The `any` state field (vs the `[192]byte` raw-bytes from §3 of the design
doc) is used in Phase C.1 to remain GC-safe before Phase C.3 moves plan
trees into mctx. The `any` → raw-bytes refactor is a mechanical change
once plan allocation is mctx-backed.

**`opNext(n *OpNode, dst *Slot) error`** — the single hot-path entry point.
Dispatches via `switch n.Kind`; Go's compiler can inline small arms.

### Migrated operators (Phase C.1)

| Kind | State type | Per-row dispatch |
|------|-----------|-----------------|
| `OpSeqScan` | `*seqScanOp` | Direct concrete method call (no itab) |
| `OpFilter`  | `*filterState` | `evalExprSlot` with concrete `*Slot` |
| `OpProject` | `*projectState` | `evalExpr` per target |
| `OpLimit`   | `*limitState` | Skip/count with concrete child call |

All other operators (41 total) remain behind `OpAdapter` (one type-assert
per `opNext` call + the legacy `op.Next()` interface cost — identical to
pre-Phase-C performance for those paths).

### `BuildFast` and `RunFast`

```go
func BuildFast(plan planner.Node) (*OpNode, error)
func RunFast(n *OpNode, ctx *Context) ([]Row, error)
```

Drop-in replacements for `Build` + `Run`. Non-migrated plan nodes fall
back to `OpAdapter(Build(plan))`.

## Key design decisions

### `any` state vs `[192]byte` raw bytes

The design doc (§3) calls for `state [opStateSize]byte` with unsafe casts.
This is unsafe if the opNode slab is mctx-allocated (GC won't trace
pointers within the bytes). Phase C.1 uses `state any` to remain GC-safe.

Migration path: once Phase C.3 lands (plan tree in mctx), the `state any`
can be replaced with `state [N]byte` + explicit GC roots, or the slab can
be moved to the GC heap with pre-declared pointer offsets.

### `HasRow` flag

`len(Cells) == 0` is ambiguous between:
- DML nil-row (`INSERT ... RETURNING` with no matches, or bare DML)
- Empty-column real row (SELECT without FROM, `SELECT 1` — the Values
  node has zero input columns)

`HasRow = false` signals the first; `HasRow = true` signals the second.
`Reset()` sets `HasRow = false`; `fillFromTupleSlot` sets it based on
whether the TupleSlot is nil.

### Pointer-based children vs slab indices

The design doc uses `children [4]int32` into a `[]OpNode` slab for
cache-friendly iteration. Phase C.1 uses `*OpNode` pointers for
simplicity. The pointer → slab-index refactor is part of Phase C.2.

## Verification

Phase C.1 verification gates:

- `go test -race ./internal/executor/ ./internal/server/` — PASS
- All 13 Phase C.1 tests pass (`TestSlot*`, `TestRunFast*`, `TestBuildFast*`)
- Legacy `Run` path unchanged; `RunFast` produces bit-identical rows
- `BuildFast` wraps non-migrated operators in `OpAdapter` transparently

## Phase C.1 follow-up (loop 11) — dispatch wiring + OpUpdate/OpDelete/OpSort

### What changed

**`OpUpdate`, `OpDelete`** (new concrete kinds, no Operator child):

`updateOp` and `deleteOp` use `extractScan` to drive storage directly, so
they have no `Operator` child. Adding `OpUpdate`/`OpDelete` kinds eliminates
one itab dispatch per DML row:
- Before: `adapterOpNext` → 1 itab call (`opAdapterState.op.Next()`)
- After: `updateOpKernelNext`/`deleteOpKernelNext` → 0 itab calls (direct
  concrete method call on the concrete type produced by the type-assert)

**`OpSort`** (new kind, child bridged via `opNodeOperator`):

`sortOp.Open()` drains the child in a tight loop; each child row previously
cost one itab dispatch. The `opNodeOperator` bridge wraps an `*OpNode` as
an `Operator`, adding one function call per child row at the bridge but
enabling the child subtree to run on concrete switch dispatch. For
`Sort(Filter(SeqScan))`, the bridge adds one call but removes two itab
dispatches (Filter→SeqScan chain). Net win for plans with ≥2-level children.

**`OpIterator` + `BuildFastIterator`** (drop-in replacement for `executor.Build`):

`OpIterator` wraps `*OpNode` as an `Operator`, enabling backward-compatible
wiring into the server dispatch loop without structural changes. Key methods:
- `Schema()`: returns `plan.Output()` first; falls back to adapter's `Schema()`
  for dynamic-schema operators (CALL, INSERT with RETURNING).
- `RowsAffected()`: delegates to `updateOpState.op`, `deleteOpState.op`, or
  the adapter's underlying operator for INSERT.
- `Next()`: calls `opNext(it.node, &it.dst)` and returns `nil` for DML nil-rows
  (preserving the legacy nil-slot convention checked by the dispatch loop).

**`dispatch.go`** wired:

Both `executor.Build` calls in `executeOneSimpleStmt` (line 777) and
`executeFetchAll` (line 1188) replaced with `executor.BuildFastIterator`.
The dispatch loop is unchanged; `*OpIterator` implements `Operator` and
`RowCounter` transparently.

### Operator migration status after loop 11

| OpKind     | Status  | Dispatch              |
|------------|---------|-----------------------|
| OpSeqScan  | migrated | concrete *seqScanOp  |
| OpFilter   | migrated | concrete switch arm   |
| OpProject  | migrated | concrete switch arm   |
| OpLimit    | migrated | concrete switch arm   |
| OpUpdate   | migrated | concrete *updateOp   |
| OpDelete   | migrated | concrete *deleteOp   |
| OpSort     | migrated | concrete *sortOp (child via opNodeOperator bridge) |
| OpInsert   | migrated | concrete *insertOp (VALUES child via opNodeOperator bridge; ON CONFLICT falls back to OpAdapter) |
| OpJoin     | migrated | concrete *joinOp (left/right via opNodeOperator bridges) |
| OpAdapter  | shim     | legacy Operator interface |

### What's NOT yet concrete

`OpAggregate`, `OpDistinct`, `OpValues`, `OpNestedLoopIndexJoin`, `OpSetOp`,
`OpRecursiveUnion`, `OpWindow`, and cold-path ops. These remain in `OpAdapter`.

## Remaining scope (Phase C.1 follow-up loops)

- **Phase C.1 complete** (loop 12): `OpInsert` and `OpJoin` landed with
  `opNodeOperator` bridges; `RowsAffected()` updated for `OpInsert`;
  5 new / updated regression tests in `phase_c_test.go`.
- **Phase C.2 COMPLETE** (loop 13): slab indices + `Slot.CopyTo` landed.
  `OpNode.childA/childB` changed from `*OpNode` to `int32` slab indices
  (`noChild = -1` sentinel). New `opTreeSlab` type holds the `[]OpNode`
  backing store; `opNodeOperator` and `OpIterator` hold `*opTreeSlab` +
  `int32` root index. `opOpen/opNext/opClose` now take `(ops []OpNode, idx
  int32, ...)` instead of `(n *OpNode, ...)`. `CopyInto` renamed to `CopyTo`.
  `BuildFast` returns `(*opTreeSlab, int32, error)`; `RunFast` takes
  `(*opTreeSlab, int32, *Context)`. All executor/server/planner/parser tests
  pass with `-race`.
- **Phase C.3**: move plan tree into mctx; delete parser/planner GC-heap alloc;
  add `Slot.CopyTo(dst *Slot, dstCtx *mctx.Context)` with mctx-backed allocation
- **Performance gate**: verify `runtime.itabHashFunc` drops from top-40 once
  all hot-path operators are migrated

## Files changed

- `internal/executor/opnode.go` (modified) — added OpUpdate, OpDelete, OpSort
  kinds; updateOpState, deleteOpState, sortOpState, opNodeOperator, OpIterator,
  BuildFastIterator; updated opOpen/opClose/opNext switches; new kernels
- `internal/executor/executor.go` (modified) — BuildFast cases for Sort/Update/Delete;
  BuildFastIterator alias (delegates to opnode.go)
- `internal/executor/phase_c_test.go` (modified) — TestBuildFastNodeKinds updated;
  new TestRunFastSort, TestBuildFastIteratorSchema, TestBuildFastIteratorRowsAffected,
  TestOpIteratorNilSlotForDMLNoRow; loop-12 additions: TestRunFastInsert (renamed
  from TestRunFastOpAdapterFallback), TestRunFastInsertRowsAffected,
  TestRunFastJoinConcrete, OpInsert+OpJoin cases in TestBuildFastNodeKinds
- `internal/server/dispatch.go` (modified) — executor.Build → BuildFastIterator
  in executeOneSimpleStmt and executeFetchAll (2 sites)
