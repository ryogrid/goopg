# 03 — Concrete-Type Volcano Executor

This chapter removes interface-based polymorphism from goopg's hot
execution path. The current `Operator` interface (4 methods, 36
implementations) and `TupleSlot` interface (5 methods) impose itab
dispatch on every `Next()` call and every slot access. The redesign
replaces both with concrete sum-type structs (tagged unions) and moves
plan / operator / slot / row payload allocation into `mctx`
([[01-memory-context]]). `Datum` becomes a pure value carried inside
contiguous slices ([[02-datum-pointer-free]]).

Cross-references: [[01-memory-context]] (mctx is the allocator for
every per-statement object), [[02-datum-pointer-free]] (Slot.Cells is
`[]Datum`).

## 1. Current state and why interfaces hurt here

Verbatim from `internal/executor/operator.go:24-29`:

```go
type Operator interface {
    Open(ctx *Context) error
    Next() (TupleSlot, error)
    Close() error
    Schema() planner.Schema
}
```

Verbatim from `internal/executor/slot.go:18-37`:

```go
type TupleSlot interface {
    SlotView
    Schema() planner.Schema
    Width() int
    Row() Row
    Materialize() *MaterializedSlot
    Release()
}
```

There are **36 concrete `Operator` implementations** (`seqScanOp`,
`indexScanOp`, `insertOp`, `updateOp`, `deleteOp`, `nestedLoopOp`,
`hashJoinOp`, `sortOp`, `aggregateOp`, ...) and **2 `TupleSlot`
implementations** (`MaterializedSlot`, `VirtualSlot`). The per-row
hot path calls `op.Next()` once per row; that is an itab lookup +
indirect call. Inside the loop, `slot.Row()` is another. The `Operator`
interface call alone shows up in `runtime.itabHashFunc` + `runtime.iface*`
samples in the cpu pprof at c=100 SO.

Practice doc §12 ("Function Call & Operator Dispatch") sizes this as
"the single biggest interpreter overhead in a Volcano-style executor"
and recommends devirtualization or vectorized execution. We adopt the
former, deferring the latter (OLAP-only benefit) to a future milestone.

Beyond dispatch, the existing slot pipeline imposes a deep-copy cost
at `executor.Run`:

```go
// internal/executor/executor.go:283 (current)
out = append(out, slot.Materialize().Row())
```

`Materialize()` calls `cloneRowOwned(s.row)` (`internal/executor/datum.go:310-340`),
which for each arena-backed Datum (`KindStringArena`/`KindBytesArena`)
allocates a fresh `[]byte` on the GC heap and memcpys the payload. At
50 columns × 6 400 rows/sec = 320 000 allocations/sec just for this
boundary. `analysis/perf-optimize/03-memory-and-allocs.md` §3.5 sizes
the per-UPDATE alloc at 105 KB; this Materialize copy is a meaningful
fraction.

The refactor eliminates both: dispatch via sum-type switch, and copies
by making `Slot` a `view` whose payload lives in the statement's
`mctx` and survives until statement end.

## 2. Design choice: sum-type Volcano

### Rejected alternatives

- **Vectorized batch execution** (one `Next()` per 1 024 rows instead
  of per row). The single biggest lift available in modern executors,
  but it requires column-batch buffers and per-kind kernel codegen.
  OLAP-focused. Out of scope for the OLTP-driven measurements in
  `analysis/perf-optimize/`; we revisit when an OLAP-heavy milestone
  arrives.
- **Function-pointer struct** (`type Op struct { Next func(*Op) (...) }`).
  Devirtualizes one level but the function pointer is itself a GC root
  and the compiler cannot inline through it. Strictly worse than the
  sum-type form below.
- **Per-plan codegen** (generate a `executePlan_<hash>(...)` function
  per plan at plan time). Practice doc §12.4. Highly effective but the
  toolchain cost (template generation, build-time compile, dispatch by
  plan-hash) is substantial. Defer to a future plan-cache milestone.

### Adopted: tagged-union `OpNode`

Every operator becomes a concrete struct embedded in a tagged-union
`OpNode`. The hot path is one `func opNext(n *OpNode, dst *Slot) error`
function with a `switch n.Kind` dispatching to per-kind tight-loop
functions. Go's compiler can inline the switch arms for small operators
(filter, project, limit). Large operators (sort, hash-join build, agg
accumulate) are out-of-line.

## 3. `OpNode` layout

```go
type OpKind uint8

const (
    OpInvalid OpKind = iota
    OpSeqScan
    OpIndexScan
    OpIndexOnlyScan
    OpFilter
    OpProject
    OpLimit
    OpSort
    OpAggregate
    OpDistinct
    OpUnion
    OpNestedLoop
    OpHashJoin
    OpInsert
    OpUpdate
    OpDelete
    OpMerge
    OpUpsert
    OpLockRows
    OpValues
    OpGenerateSeries
    OpScalarFuncScan
    OpCTEScan
    OpWorkTableScan
    OpRecursiveUnion
    OpWindowAgg
    OpProjectSet
    OpExplain
    OpSetTransaction
    OpTransaction
    OpCheckpoint
    OpDDL
    OpVacuum
    OpAnalyze
    OpCluster
    OpCall
    OpCopy
    // ... up to 36 total; see internal/executor/operators_*.go
)

type OpNode struct {
    Kind     OpKind
    _pad     [3]byte
    children [4]int32  // indices into op-tree slab; -1 == none
    ext      int32     // index into opExtSlab for ops with > 4 children or oversized state
    state    [opStateSize]byte   // raw bytes; cast to per-Kind state via unsafe.Pointer
}

const opStateSize = 192   // = max(sizeof(seqScanState), sizeof(hashJoinState), ...)
                           // determined at implementation time by fieldalignment + size profiling
```

The `state` field is **raw bytes**, large enough for the largest
per-kind state struct. For each `OpKind`, a `func castXxxState(n *OpNode) *xxxState`
helper performs the unsafe cast:

```go
func seqScanStateOf(n *OpNode) *seqScanState {
    return (*seqScanState)(unsafe.Pointer(&n.state[0]))
}
```

The state-size constant is the **single source of truth**; if a new
operator exceeds it, the implementer either splits state into ext-slab
allocations or grows the constant (with a build-time test).

### Per-kind state structs

Examples (sized against the current concrete types in
`internal/executor/operators_storage.go`, `operators_join_agg.go`, etc.):

```go
type seqScanState struct {
    plan        *planner.SeqScan  // mctx-allocated, NOT GC heap
    cols        []catalog.Column
    nBlocks     storage.BlockNumber
    curBlock    storage.BlockNumber
    curSlot     uint16
    slotMax     int
    pinned      storage.Pin       // pointer-free per [[06-bufpool-lockfree]]
    activePage  storage.Page
    ring        *storage.ScanRing
    prefetchedThru storage.BlockNumber
    scanCells   []Datum           // reused decode buffer
    arena       mctx.ContextID    // sub-region for payload bytes
}

type filterState struct {
    pred      *planner.Expr
    childIdx  int32   // resolves to OpNode in op-tree slab
}

type hashJoinState struct {
    plan      *planner.MultiHashJoin
    table     hashTable             // pointer-free open-addressing hash table
    buildDone bool
    probeRow  []Datum
    buildCtx  mctx.ContextID        // sub-region for build-side rows
}
```

The per-kind structs are themselves allocated **as part of the parent
`OpNode.state` bytes**, not on the GC heap. The only pointer-typed
fields in any state struct are the `*planner.XxxNode` (which also lives
in mctx) and `*storage.ScanRing` (storage-engine-owned, low pointer
count). Wherever a state field could be replaced by an mctx offset
or dense-array index without loss of clarity, it is.

### Children encoding

`OpNode.children [4]int32` indexes into a per-statement **op-tree slab**:
`stmtCtx` holds a `[]OpNode` allocated from mctx; an operator
references its inputs by their index. -1 means "no child here." Most
operators have 0–2 children (scans = 0, filter/project = 1, joins/setops
= 2); the inline `[4]int32` covers all except the rare wide cases,
which use `ext` to address an overflow record in `opExtSlab`.

This is the practice-doc §8 "use indices instead of pointers" pattern,
applied to the op tree.

## 4. Hot-path `Next`

```go
// internal/executor/exec_pump.go
func opNext(ops []OpNode, idx int32, dst *Slot) error {
    n := &ops[idx]
    switch n.Kind {
    case OpSeqScan:
        return seqScanNext(seqScanStateOf(n), dst)
    case OpFilter:
        return filterNext(ops, n, dst)
    case OpProject:
        return projectNext(ops, n, dst)
    case OpLimit:
        return limitNext(ops, n, dst)
    // ... 36 arms total
    case OpHashJoin:
        return hashJoinNext(ops, n, dst)
    default:
        return fmt.Errorf("executor: unknown op kind %d", n.Kind)
    }
}
```

`opNext` is the *only* per-row entry point. The compiler inlines small
arms (`OpFilter`, `OpProject`, `OpLimit`) directly; the result is one
indirect-free switch per row. Practice doc §13 (BCE) guidance applies
inside each per-kind kernel.

Each per-kind kernel takes `ops []OpNode` (so it can recurse to its
children via index) and the destination `*Slot`. The kernel is a pure
function on operator state; no method receiver, no virtual dispatch.

Example — `filterNext` (one of the simplest, fully inlined):

```go
func filterNext(ops []OpNode, n *OpNode, dst *Slot) error {
    s := filterStateOf(n)
    child := n.children[0]
    for {
        if err := opNext(ops, child, dst); err != nil {
            return err   // includes EOF
        }
        if expr.Eval(s.pred, dst).BoolValue() {
            return nil
        }
        // else loop; reuse dst.Cells, no alloc
    }
}
```

Note: `expr.Eval` is itself a tagged-union dispatch on `planner.Expr`
(60-ish concrete Expr kinds); the same sum-type pattern applies. The
existing `internal/executor/expr.go` already does some of this; the
refactor completes it.

## 5. `Slot` redesign (interface deleted)

```go
type Slot struct {
    Schema  planner.Schema    // pointer; refers to mctx-allocated Schema; tolerable
    Cells   []Datum           // 24-byte Datum (pointer-free per [[02-datum-pointer-free]])
    Owner   mctx.ContextID    // arena holding payload bytes
    Pin     storage.Pin       // pointer-free; for slots aliasing a buffer-pool page
    pinHeld bool              // true if Pin must be released on Slot.Reset
}
```

`Slot` is **always a concrete struct**, never an interface. Consumers
that need multiple slot kinds (scan slot vs join virtual slot)
distinguish via field semantics:
- **Owner = scanOpArenaCtxID** — payload lives in the scan operator's
  sub-region; valid until the scan calls Reset.
- **Owner = stmtCtx.ID()** — payload lives in the statement context;
  valid until end of statement.
- **Pin.SlotIdx != -1** — payload aliases a buffer-pool page; pin
  must be Unpin'd via `slot.Reset()` before next iteration.

### Operations

```go
// Reset clears Cells (truncate to 0) and releases any held pin.
func (s *Slot) Reset()

// CopyTo deep-copies s into dst, allocating payload bytes in dstCtx.
// Replaces the current cloneRowOwned + Materialize pattern. Used by
// sort, hash-join build, aggregate, and the public Run -> caller
// boundary.
func (s *Slot) CopyTo(dst *Slot, dstCtx *mctx.Context)

// Get returns d.Cells[i] (bounds-checked).
func (s *Slot) Get(i int) Datum { return s.Cells[i] }

// Width returns len(s.Cells).
func (s *Slot) Width() int { return len(s.Cells) }
```

There is no `Materialize()` and no `Row()` method. The slot **is** the
row. Consumers that need to retain a slot past the producer's next
`Next` call use `CopyTo`. The discipline is documented per-operator;
sort/hash-join/aggregate are the primary CopyTo callers.

## 6. Plan and AST allocation from mctx

`planner.Plan(stmt parser.Stmt, cat catalog.Catalog, ctx *mctx.Context)
(planner.Node, error)`. All 65 concrete plan-node types collapse into a
parallel sum-type:

```go
type PlanKind uint8
const (
    PlanInvalid PlanKind = iota
    PlanSeqScan
    PlanIndexScan
    PlanNestLoop
    PlanHashJoin
    PlanSort
    PlanLimit
    PlanProject
    PlanFilter
    PlanInsert
    PlanUpdate
    PlanDelete
    PlanAggregate
    // ... 65 total
)

type PlanNode struct {
    Kind     PlanKind
    children [4]int32   // -1 == none; indices into plan-tree slab
    ext      int32
    state    [planStateSize]byte
}
```

The plan tree is a `[]PlanNode` slab allocated from `stmtCtx`. Tree
construction in `planSelect` calls `mctx.AllocSlice[PlanNode](stmtCtx,
1)[0]` (or similar) to append; the slice grows via the standard
`append` semantics, but the backing array is mctx-resident. **No GC
heap allocations** during planning.

Expression nodes (`planner.Expr` and subtypes — `BinaryOp`, `FuncCall`,
`ColumnRef`, `CaseExpr`, `SubqueryExpr`, etc.) get the same treatment.
There are roughly 20–30 Expr kinds; same `ExprKind` enum + `ExprNode`
sum-type.

The same applies to AST nodes (`parser.Stmt`, `parser.Expr`). The
existing `tokenSlicePool` and `parserPool` in `internal/parser/parser.go`
are deleted; the parser allocates its tokens and internal state from
an ephemeral `ExprContext` child of `stmtCtx`.

Consequence: by the end of statement, every parse / plan / operator
allocation is bulk-reclaimed by `stmtCtx.Release()`. The GC sees only
the slice headers of the mctx chunks.

## 7. Per-row pipeline at steady state

```go
// executor.go (post-refactor)
func (r *Runner) Run(rootIdx int32, sink ResultSink) error {
    slot := r.slotPool.Get()
    defer r.slotPool.Put(slot)
    for {
        slot.Reset()
        if err := opNext(r.ops, rootIdx, slot); err != nil {
            if errors.Is(err, EOF) { return nil }
            return err
        }
        if err := sink.Write(slot); err != nil {
            return err
        }
    }
}
```

- `slot` is reused across rows; `slot.Reset()` truncates `Cells` to 0
  and unpins any held buffer.
- `opNext` is the one entry point; no interface dispatch.
- `sink.Write` is the wire encoder (see §9).

If the caller is one that needs the rows retained (e.g., the test
harness collecting all output, or a subquery executor that runs the
inner plan to completion), it calls `slot.CopyTo(&saved, stmtCtx)`
to take ownership. The `cloneRowOwned` deep-copy site at
`executor.go:283` is replaced by an explicit `CopyTo`, and the deep
copy lands in the caller's mctx — **not** on the GC heap.

## 8. Operators that need pointers across boundaries

A few operators must retain rows across `Next` calls:

- **`sortOp`** — at `Open`, drains the child completely. Each child
  row is `CopyTo`-ed into a per-sort sub-region of `stmtCtx`. The
  sort kernel sorts indices into that slab, not pointers.
- **`hashJoinOp` (build side)** — at `Open`, drains the build child
  and inserts each row into the hash table, `CopyTo`-ing the row into
  a per-hash-table sub-region.
- **`aggregateOp`** — accumulates rows by group key; per-group state
  lives in a sub-region.
- **`lockRowsOp`** — retains slots until commit/abort; uses the txn
  context's sub-region.

In every case, the sub-region is a child mctx of `stmtCtx` (or
`txnCtx` for `lockRowsOp`), acquired in `Open` and `Release`d in
`Close`. The per-operator memory is bounded by the operator's lifetime.

## 9. Wire-encode path

`internal/protocol.FrameWriter` already has per-connection scratch
buffers (M0092-0004; `frame.go:152-171`). The wire encoder accepts a
`*Slot`, iterates `Slot.Cells`, and for each `Datum` writes the wire
encoding via the new accessors ([[02-datum-pointer-free]] §5). The
payload bytes accessed via `d.BytesValue()` alias the mctx chunk; the
encoder copies them into the per-connection output buffer (already a
`[]byte` owned by the FrameWriter).

No new allocations per row. The encoder's existing zero-alloc behaviour
(M0092-0004) is preserved; we just rewire the source of the bytes.

## 10. Cold-path operators

The following operators are off the hot pgbench path and may retain
the legacy `Operator` interface during early migration (Phase C of
[[09-migration-and-rollout]]):

- `vacuumOp`, `clusterOp`, `analyzeOp`, `ddlOp`, `explainOp`,
  `setTransactionOp`, `transactionOp`, `checkpointOp`, `callOp`,
  `pgGetPublicationTablesOp`, `pgInputErrorInfoOp`.

These keep an adapter shim:

```go
// internal/executor/adapter.go (transitional)
type opAdapter struct {
    op  LegacyOperator
}

func (a *opAdapter) Run(ctx *Context, sink ResultSink) error {
    // Drive a legacy Operator through the public ResultSink boundary.
    // Used only for cold-path operators during Phase C of the migration.
}
```

By the end of Phase C, every operator is migrated and the legacy
`Operator` interface and `opAdapter` are deleted.

## 11. PG counterparts

| goopg concept                  | PG counterpart                                              |
|--------------------------------|-------------------------------------------------------------|
| `OpNode` sum-type              | `Plan` / `PlanState` tags in `postgres/src/include/nodes/`  |
| Plan-tree slab                 | `palloc(sizeof(SeqScan))` etc. into `PlannerInfo` context   |
| `Slot` (concrete)              | `TupleTableSlot` in `postgres/src/include/executor/tuptable.h:120` |
| `Slot.CopyTo`                  | `ExecCopySlot` in `postgres/src/backend/executor/execTuples.c` |
| Per-row scratch context        | `ExprContext.ecxt_per_tuple_memory` in `execTuples.c`       |
| Cold-path operator adapter     | N/A — PG uses uniform `ExecProcNode` dispatch               |

PG uses `Plan` / `PlanState` enums (`NodeTag`) for similar tagged-union
dispatch in places where C limits inheritance; our sum-type discipline
matches that pattern.

## 12. Cost reasoning

Per `analysis/perf-optimize/03-memory-and-allocs.md` §3.3, `planner.Plan`
chain accounts for 26.4 % of c=100 SO allocations. With plan nodes
moved into mctx, that drops to **0 %**. Combined with the `cloneRowOwned`
elimination (boundary copies become CopyTo into a sized region), and
the seqScan / project / filter operators no longer allocating result
rows on the GC heap, the per-statement GC churn drops to near zero
for OLTP statements.

CPU side: the `runtime.scanobject` cum% drops further as the live
heap excludes the plan-tree and operator-tree pointer graph. Combined
with [[01-memory-context]] and [[02-datum-pointer-free]], we expect
`gcBgMarkWorker` to fall below **15 %** at c=10 SO.

## 13. Verification

After Phase C of [[09-migration-and-rollout]] ships:

- **Compile-time** — `grep -RIn 'type.*Operator interface\|type.*TupleSlot interface'
  internal/executor/` returns zero (only the deleted operator.go reference
  remains, and only as comment history).
- **Symbol count** — `mcp__serena__find_symbol Operator` returns the
  cold-path adapter only; all 36 hot-path implementations gone.
- **Sizeof** — `unsafe.Sizeof(OpNode{}) <= 220` asserted (sized at
  implementation, including the `state [N]byte` budget).
- **Plan-tree allocation** — runtime allocations from `planner.Plan`
  in an `allocs.pb.gz` capture drop to **0 KB** (all in mctx). c=10
  SO `planner.Plan` cum% in `allocs.pb.gz` drops from 26.4 % to < 0.5 %
  (just slice-header growth of the plan-tree slab).
- **CPU dispatch** — `runtime.itabHashFunc` / `runtime.iface*` symbols
  drop out of cpu profile top-40 at c=10 SO.
- **TPS** — c=10 SO TPS reaches the **≥ 8 000** band (Phase A+B+C
  combined). c=50 SO reaches **≥ 18 000**.
- **Run-time row materialization** — replace `executor.go:283`'s
  `slot.Materialize().Row()` site with `slot.CopyTo(&out[i], stmtCtx)`;
  the new path's CPU profile shows no GC-heap allocation in the
  per-row callsite.
- **TPC-H regression** — all queries still pass; q5 and q9 (the
  M0077-unlocked ones) still complete within 10 % of pre-refactor
  wall-clock.
