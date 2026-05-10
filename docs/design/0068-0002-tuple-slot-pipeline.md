# Design 0068-0002 — TupleSlot Pipeline (replaces BorrowSemantics)

**Milestone:** M0068-0002
**Status:** draft
**Owner:** TBD
**Branch:** `perf-analysis`
**Supersedes:** the BorrowSemantics row-level contract from
M0054-0005a / M0059.

## Context

The goopg executor currently passes data row-by-row as
`Row = []Datum`. Per `review/postgres_vs_goopg_performance_divergence.md`
§1, this contrasts with PostgreSQL's `TupleTableSlot`
polymorphism (`postgres/src/backend/executor/execTuples.c`,
`TTSOpsVirtual` / `TTSOpsHeapTuple` / `TTSOpsMinimalTuple` /
`TTSOpsBufferHeapTuple`).

The current pipeline has these problems:

1. **No virtual representation.** Filter/Project/Limit/NLI
   either return a borrowed `Row` (alias of child's buffer)
   or `cloneRow` it. There's no concept of "I reference column
   3 of slot X and column 7 of slot Y without owning either."
2. **Hash tables store materialized `[]Row` slices.** Each
   slot is a separate `[]Datum` allocation.
3. **`BorrowSemantics`** (`internal/executor/operator.go:101`)
   approximates the slot lifetime contract row-by-row, but
   the explicit `OwnedRow` / `BorrowedRow` enum is ambiguous
   for cases like aggregate-build (which extracts column
   values into accumulator state without retaining the row)
   versus sort-build (which retains every row until Open
   completes). Operators that get the contract wrong corrupt
   data silently — the safety net is a runtime test in
   `borrow_test.go`, not a static guarantee.

The user explicitly approved removing `BorrowSemantics` if the
slot model offers a better contract. It does: each slot kind
encodes its own lifetime semantics, and the operator pipeline
can reason about lifetimes structurally.

## Goals

- Define `TupleSlot` interface with three concrete kinds
  (Materialized, Virtual, BatchRef).
- Replace `Row = []Datum` as the canonical pipeline carrier.
- Remove `Borrowable`, `OwnedRow`, `BorrowedRow`,
  `setChildBorrow` from `internal/executor/operator.go`.
- Provide explicit slot-level lifetime methods
  (`Materialize()`, `Pin()`, `Release()`).

## Non-goals

- Columnar batching (M0069+). This milestone keeps row-at-a-
  time slot semantics; the slot interface is **batch-aware**
  internally (BatchRef references a row inside a batch) but
  the pipeline carries one slot per `Next()` call.
- Datum struct redesign (M0068-0001, separate).

## Proposed interface

```go
// TupleSlot is the goopg replacement for `Row = []Datum`.
// Implementations represent rows in different storage forms:
// fully materialized, virtual (column references across
// multiple sources), or batch-relative (offset into a column
// arena).
type TupleSlot interface {
    Schema() planner.Schema  // unchanged
    Width() int              // number of columns
    Get(col int) Datum       // O(1) read; may resolve indirection
    IsNull(col int) bool

    // Materialize returns a slot whose values are independent
    // of any source state. Calling Materialize() on a
    // MaterializedSlot is a no-op (returns self). Virtual /
    // BatchRef slots copy referenced values into a fresh
    // backing.
    Materialize() *MaterializedSlot

    // Release returns the slot to the pool; the caller must
    // not access any column afterwards.
    Release()
}
```

### Concrete kinds

**`MaterializedSlot`** — owns a `[]Datum` (current `Row`
behavior). Used by hash-table storage, sort buffer, aggregate
group key, copy-to-output.

**`VirtualSlot`** — references column positions across one or
more source slots. Replaces the `BorrowedRow` flow:

```go
type VirtualSlot struct {
    schema  planner.Schema
    sources []TupleSlot
    cols    []virtualCol  // (sourceIdx, sourceCol)
}
type virtualCol struct {
    sourceIdx, sourceCol int16
}
```

Use cases:
- Filter/Project pass-through (single source).
- NLI joinBuf (two sources: outer slot + inner slot).
- MHJ probe output (probe slot + N build slots).

VirtualSlot is **valid only until the next `Next()` call** on
any of its sources. Operators that need to retain it call
`Materialize()`.

**`BatchRefSlot`** — references row N inside a column-batch
arena. Used by hash-table storage when build values come from
a batch:

```go
type BatchRefSlot struct {
    batch *ColumnBatch  // future M0069+ optimization
    row   int32
}
```

For M0068, BatchRefSlot is a stub that wraps a
`MaterializedSlot`; the full columnar batch implementation
lands in a follow-up milestone. Defining the interface
upfront avoids re-plumbing every operator twice.

### Operator surface

```go
type Operator interface {
    Open(ctx *Context) error
    Next() (TupleSlot, error)  // was: (Row, error)
    Close() error
    Schema() planner.Schema
}
```

Slot ownership rules (replaces BorrowSemantics):

- `Next()` returns a slot the caller may read until the next
  call to `Next()` on the SAME operator.
- To retain across `Next()` calls (e.g. hash-table store,
  sort buffer): call `Materialize()`. The returned
  `*MaterializedSlot` is independent.
- The slot returned by `Next()` MUST be released (call
  `Release()`) before the next call. (Materialize counts as
  release if the returned MaterializedSlot is a NEW
  allocation; pass-through Materialize on an already-
  materialized slot is a no-op release.)

The pipeline operator helpers (filterOp, projectOp, limitOp,
NLI joinBuf, MHJ probe) construct `VirtualSlot` instances by
wrapping their child's slot. No row-level copy.

## What gets removed

| Removed | Replacement |
| ------- | ----------- |
| `type Borrowable interface { SetBorrow(BorrowSemantics) }` | implicit via slot kind |
| `OwnedRow` / `BorrowedRow` enum | slot's `Release()` /  `Materialize()` |
| `setChildBorrow(op, BorrowedRow)` | always-virtual pass-through |
| `cloneRow(row)` | `slot.Materialize()` |
| `*nestedLoopIndexJoinOp.SetBorrow` | NLI builds `VirtualSlot` containing outer + inner |
| `*multiHashJoinOp.SetBorrow` (M0066-0002) | MHJ build stores `MaterializedSlot`; probe returns `VirtualSlot` |
| `*projectOp.SetBorrow` | always pass-through |
| `*filterOp.SetBorrow` | always pass-through |
| `*limitOp.SetBorrow` | always pass-through |

The M0066-0002 win (eliminating `copyOut` 99.23 % of Q5
allocations) is preserved structurally: MHJ's probe path
returns a `VirtualSlot` that combines the probe row with the
hash-table build slot — no `make+copy`.

## Migration plan

Stage 1: introduce `TupleSlot` interface + `MaterializedSlot`
backed by `[]Datum`. Adapter functions:
`SlotFromRow(row Row) *MaterializedSlot` and
`Row(slot TupleSlot) Row`. All operators continue to receive
`Row` internally; operator interface changes signatures to
return `TupleSlot`, but each operator wraps `Row` in
`MaterializedSlot` at the boundary.

Stage 2: migrate operators bottom-up to consume `TupleSlot`
natively (start with leaves: SeqScan, IndexScan).

Stage 3: introduce `VirtualSlot` + `Pin`/`Release` lifetime;
flip filterOp / projectOp / limitOp to virtual passthrough.

Stage 4: NLI's `joinBuf` becomes a `VirtualSlot{outer, inner}`.

Stage 5: MHJ's lazyOut becomes a `VirtualSlot{probe, build0,
build1, ...}`. The current `SetBorrow` path is removed.

Stage 6: aggregateOp / sortOp / hash-build flows call
`Materialize()` explicitly at retention boundaries.

Stage 7: remove `Borrowable`, `OwnedRow`, `BorrowedRow`,
`setChildBorrow` from `operator.go`. Delete `borrow_test.go`
in favor of slot-lifetime tests.

## Verification

- `go test ./...` PASS at every stage.
- 22-query SF=1 sweep at `cancel-after=1200s`:
  - OK count ≥ 20 (parity with M0067).
  - Q5 GC CPU share < 15 % (was ~30 % post-M0066, ~65 %
    pre-PIVOT).
- pprof CPU shows `runtime.duffcopy` / `memmove` for slot
  copies ≤ 10 % (was 60 % at M0067).

## Risks

- **Operator surface change.** Every operator's `Next()`
  signature changes. Mitigation: Stage 1's adapter pattern
  lets us flip the interface in one mechanical commit, then
  migrate operator-by-operator.
- **Slot lifetime bugs.** Forgetting to call Materialize
  before retention silently corrupts. Mitigation: race
  detector + targeted unit tests + a debug build that
  invalidates VirtualSlot column reads after the source's
  next `Next()` (catch-fast assertion).
- **Adapter overhead.** `SlotFromRow` / `Row(slot)` round-
  trip during migration adds one allocation per row in the
  transition window. Mitigation: keep migration window
  short; final state has zero adapters.
- **NLI / MHJ correctness.** The `VirtualSlot` for joinBuf /
  lazyOut must address column N in the right source. Bug-
  potential: off-by-one in `(sourceIdx, sourceCol)`.
  Mitigation: golden-row tests for each join type.

## References

- `internal/executor/operator.go:101-132` — current Borrow
  contract.
- `internal/executor/multi_hash_join.go:34-72` — M0066
  SetBorrow path.
- `internal/executor/operators_nljoin.go` — joinBuf.
- `internal/executor/operators.go` — filter/project/sort.
- `postgres/src/backend/executor/execTuples.c` —
  TupleTableSlotOps reference.
- `practice/go_gc_optimized_programming.md` §3 (`[]*Item` vs
  `[]Item`), §9 (use indexes instead of pointers).
