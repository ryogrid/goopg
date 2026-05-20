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
- **Phase C.3 PARTIAL** (loop 14): ExprNode sum-type for expression evaluation
  landed. New `internal/executor/exprnode.go` adds `ExprKind` enum, `ExprNode`
  tagged-union struct, `exprTreeSlab` type with `buildExpr(planner.Expr) int32`,
  and `evalFastExpr(slab, idx, slot, ctx)` dispatcher. `opTreeSlab` gains an
  `exprs exprTreeSlab` field; `buildRec` compiles Filter predicates and Project
  target expressions into the slab at plan-build time. `opOpen` gains an
  `exprs exprTreeSlab` parameter so Filter/Project states receive the slab at
  open time. `filterOpNext` and `projectOpNext` now call `evalFastExpr` instead
  of `evalExprSlot`/`evalExpr`, eliminating interface type assertions for
  `ColumnRef`, `IntegerConst`, `BooleanConst`, `NullConst`, `BinaryOp`, and
  `UnaryOp`. All other expression kinds fall back to `ExprAdapter`
  (delegates to `evalExprSlot`), preserving correctness. 10 new regression
  tests in `phase_c_test.go`. All executor/server tests pass `-race`.
  **Remaining C.3 work**: PlanNode sum-type, parser mctx (delete `tokenSlicePool`
  / `parserPool`), and `OpNode.state any` → raw bytes (requires mctx for plan tree).
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

### Phase C.3 (loop 14) — ExprNode sum-type

- `internal/executor/exprnode.go` (new) — ExprKind enum, ExprNode struct,
  exprTreeSlab type, buildExpr(), evalFastExpr()
- `internal/executor/opnode.go` (modified) — opTreeSlab.exprs field;
  filterState.{predIdx, exprs}; projectState.{targExprs, exprs};
  opOpen signature adds exprs; Filter/Project Open wires exprs;
  filterOpNext/projectOpNext use evalFastExpr
- `internal/executor/executor.go` (modified) — BuildFast initialises exprs slab;
  buildRec Filter/Project cases compile expressions; RunFast passes tree.exprs
- `internal/executor/phase_c_test.go` (modified) — TestBuildExprSlabCommonKinds,
  TestEvalFastExprCommonKinds, TestRunFastFilterExprNodePopulated

### Phase C.3 (loop 16) — PlanNode sum-type: eliminate planner.Node references from execution state

**Problem**: After ExprNode (loop 14) and parser mctx (loop 15), three execution-state structs
still held GC-traced references to planner.Node/Expr objects:

- `filterState.pred planner.Expr` — kept as a "ExprAdapter fallback origin". Redundant: the
  exprTreeSlab's `ExprAdapter.orig` field already roots the original `planner.Expr` for the
  lifetime of the statement.
- `projectState.plan *planner.Project` — used in `opOpen` only for `len(p.Targets)`. Redundant:
  `len(s.targExprs)` gives the identical count; `targExprs` is already set at `buildRec` time.
- `limitState.plan *planner.Limit` — used in `opOpen` to evaluate `p.Limit` and `p.Offset`
  expressions via the old `evalExpr(s.plan.Limit, nil, ctx)` path. The expressions were not
  pre-compiled into the exprTreeSlab, forcing the plan reference to be retained.

**Changes** (Phase C.3 concrete-state cleanup):

1. **`filterState.pred planner.Expr` removed**. `buildRec` no longer stores
   `pred: p.Predicate` in the literal; the exprTreeSlab's `ExprAdapter.orig` provides the GC
   root for the original predicate tree. `filterState` is now `{predIdx, exprs, ctx}` — no
   interface-valued fields.

2. **`projectState.plan *planner.Project` removed**. `opOpen` Project branch changed from
   `len(s.plan.Targets)` to `len(s.targExprs)`. `projectState` no longer holds a pointer to
   the planner node.

3. **`limitState.plan *planner.Limit` removed; LIMIT/OFFSET expressions compiled into slab**.
   `buildRec` Limit case now calls `tree.exprs.buildExpr(p.Limit)` and
   `tree.exprs.buildExpr(p.Offset)` and stores the resulting indices in
   `limitState.{limitExprIdx, offsetExprIdx}` (both `int32`; `noExpr = -1` when the clause is
   absent). `opOpen` Limit branch replaced `evalExpr(s.plan.Limit/Offset, nil, ctx)` with
   `evalFastExpr(exprs, s.limitExprIdx/offsetExprIdx, nil, ctx)` — routing through the
   integer-dispatch expression evaluator, eliminating the last `planner.Limit` reference from
   hot-path execution.

4. **`plannode.go` (new)** — `PlanKind` enum, `PlanNode` struct with
   `payload [planPayloadSize]byte` + `orig planner.Node` (for unmigrated nodes), `planTreeSlab`
   type, and builder/accessor helpers:
   - `buildPlanFilter(predIdx, childA) int32` + `PlanFilterPredIdx(*PlanNode) int32`
   - `buildPlanLimit(limitExprIdx, offsetExprIdx, childA) int32` + `PlanLimitExprs(*PlanNode) (int32, int32)`
   - `buildPlanAdapter(orig, childA) int32`
   Foundation for future full migration of SeqScan and Project (where table/column metadata
   will be stored as raw bytes, eliminating the last GC-traced plan pointers).

5. **`opTreeSlab.plans planTreeSlab` added** — plan slab allocated alongside ops/exprs slabs
   in `BuildFast`, immutable after `BuildFast` returns.

**Net GC impact**: For a typical `SELECT id FROM t WHERE id > N LIMIT M` query, the hot-path
operator tree (SeqScan → Filter → Project → Limit) now holds:
- filterState: 0 planner.Expr references (was 1)
- projectState: 0 planner.Node references (was 1)
- limitState: 0 planner.Node references (was 1); 2 new int32 fields
Total GC-traced plan references eliminated from the 4 concrete state structs: 3.

**Files changed** (loop 16):
- `internal/executor/plannode.go` (new) — PlanKind, PlanNode, planTreeSlab, builder/accessors
- `internal/executor/opnode.go` (modified) — filterState drops pred; projectState drops plan;
  limitState drops plan/adds limitExprIdx+offsetExprIdx; opTreeSlab gains plans field;
  opOpen Limit/Project branches updated
- `internal/executor/executor.go` (modified) — BuildFast initialises plans slab; buildRec
  Filter/Limit/Project cases updated for new state layout
- `internal/executor/phase_c_test.go` (modified) — TestPlanNodePlanFilterPayload,
  TestPlanNodePlanLimitPayload, TestPlanNodeRoundtripNegativeOne, TestLimitStateExprIdx,
  TestLimitOffsetStateExprIdx, TestFilterStateNoPredField, TestLimitOffsetExecution

### Phase C.3 (loop 15) — Parser mctx migration

**Problem**: `internal/parser/parser.go` held two global `sync.Pool` objects:
- `tokenSlicePool` — recycles `[]Token` backing arrays (~64 tokens × 40 B/token per call)
- `parserPool` — recycles the 32-byte `parser` struct

Each call to `Parse()` or `ParseExpr()` incurred two `sync.Pool.Get()` + `Put()` round-trips
(~40–80 ns on contended paths). The `parser` struct is 32 bytes and perfectly allocatable on
the stack; pooling it adds overhead without meaningful benefit.

**Change**:
1. `parserPool` deleted. `Parse()` and `ParseExpr()` now use `var p parser` (stack allocation).
2. `Parse()` and `ParseExpr()` accept an optional `*mctx.Context` parameter (variadic, backward-
   compatible). When non-nil, token backing is allocated from the arena via
   `mctx.AllocSlice[Token](mc, 64)[:0]` — a single bump-pointer operation, no GC-heap object
   created, freed in bulk when `mc.Release()` fires.
3. When `mc` is nil (all existing test callers and plpgsql), the `tokenSlicePool` pool path
   is used unchanged (no regression for non-hot callers).
4. `internal/server/dispatch.go::dispatchSimpleQueryViaExecutor` creates an ephemeral
   `mctx.KindExpr` child of `connTx.SessCtx` before calling `parser.Parse()`, passes it,
   and releases it immediately after parsing completes (tokens are only needed during parsing).

**Files changed**:
- `internal/parser/parser.go` — `parserPool` deleted; `Parse`/`ParseExpr` signatures widened
  with variadic `mc ...*mctx.Context`; mctx allocation path added.
- `internal/server/dispatch.go` — parseCtx created from `connTx.SessCtx` before Parse call;
  released immediately after.
- `internal/parser/mctx_parse_test.go` (new) — `TestParseMctxPath` and
  `TestParseExprMctxPath` verify mctx and pool paths produce equivalent ASTs.

### Phase C.3 (loop 17) — SeqScan migration: remove `*planner.SeqScan` from seqScanOp

**Problem**: `seqScanOp` held a `plan *planner.SeqScan` field — a GC-traced pointer to the
planner struct. Every GC cycle traced `seqScanOp → planner.SeqScan → catalog.Table` and
`seqScanOp → planner.SeqScan → planner.Schema ([]SchemaCol slice)`. Additionally, `Next()`
called `ctx.Catalog.RelFileNode(o.plan.Table)` on every invocation, acquiring and releasing
the catalog `RWMutex.RLock` for each row — a hot-path lock bottleneck on wide relations.

**Changes**:

1. **`seqScanOp` struct**: `plan *planner.SeqScan` removed; replaced with:
   - `schema planner.Schema` — output schema (copied from `p.Output()` at construction)
   - `tbl *catalog.Table` — table reference (copied from `p.Table`; keeps catalog.Table alive)
   - `pos int` — parser position for error messages (copied from `p.Pos()`)
   - `rel storage.RelFileNode` — relation file node, cached once in `Open()` (zero before Open)

2. **`newSeqScanOp(p)`**: now sets `schema`, `tbl`, `pos`, `cols` from the plan; does NOT hold
   a reference to the `*planner.SeqScan` struct after construction.

3. **`seqScanOp.Open()`**: computes `o.rel = ctx.Catalog.RelFileNode(o.tbl)` once and caches it.
   All subsequent `Next()` and `currentTID()` calls use `o.rel` directly — zero catalog
   RLock acquisitions per row.

4. **`seqScanOp.Schema()`**: returns `o.schema` directly (was `o.plan.Output()`).

5. **`seqScanOp.Next()`**: `rel := o.rel` (was `rel := o.ctx.Catalog.RelFileNode(o.plan.Table)`).
   Slot schema set via `o.slot.schema = o.schema` (was `o.Schema()`).

6. **`seqScanOp.currentTID()`**: returns `o.rel` directly (was re-computed from catalog).

7. **`plannode.go`**: `PlanSeqScan` comment updated to reflect concrete migration status.
   Migration status section updated (PlanSeqScan moved from "adapter" to "fully concrete").

**Net GC impact**: One `*planner.SeqScan` heap object eliminated from the GC-traced set per
statement, plus one indirection removed from the hot-path operator tree. For a full table scan,
the catalog `RLock` + `RUnlock` cycle that previously fired on every `Next()` call is replaced
by a single acquisition in `Open()`.

**Files changed** (loop 17):
- `internal/executor/operators_storage.go` — seqScanOp struct rewritten; newSeqScanOp/Schema/
  Open/Next/currentTID updated
- `internal/executor/plannode.go` — PlanSeqScan comment + migration status updated
- `internal/executor/phase_c_test.go` — imports storage; new
  `TestSeqScanOpNoPlanPointer` (verifies schema/tbl/pos populated, rel zero pre-Open) and
  `TestSeqScanOpRelCachedAfterOpen` (verifies rel populated post-Open)
