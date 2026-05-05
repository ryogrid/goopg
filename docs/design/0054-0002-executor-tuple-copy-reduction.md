# Design Doc 0054-0002 — Executor Tuple Copy Reduction

**Status:** proposed
**Milestone:** 0054 — TPC-H Performance & Optimisation Follow-Through
**Sub-task:** input to M0054-0005 (perf fixes)
**Author:** Ralph (autonomous agent)
**Date:** 2026-05-05
**Cross-refs:** `docs/design/0054-0001-tpch-perf-investigation-methodology.md`

## 1. Observed problem (Q9 pprof, 60 s CPU window)

The M0054-0004 pprof survey of TPC-H Q9 on the goopg cluster
(SF=1, HammerDB-loaded) shows the executor is allocation-bound,
not compute-bound:

| Symbol | cum% | flat% | Notes |
|---|---|---|---|
| `runtime.systemstack` | 82.35 | — | umbrella for GC paths |
| `runtime.gcBgMarkWorker` / `runtime.gcDrain` | ~78 | — | GC marker workers |
| `runtime.scanobject` | 76.75 | 16.93 | heap-scan inner loop |
| `runtime.findObject` | 30.57 | **29.30** | single flat-top symbol |
| `runtime.(*gcWork).putObjFast` | — | 9.03 | GC work queue push |
| `executor.(*aggregateOp).Open` (real query work) | 18.37 | — | Q9's GROUP BY |

GC consumes roughly **78 %** of CPU during steady-state Q9. Real
query processing (`aggregateOp.Open`, expression evaluation,
hash-table lookup) is the remaining minority.

In-use heap snapshot at the same time: **5.14 GB** live, of which:

- `executor.(*spillReader).ReadRow` — 1.65 GB (spill-to-disk
  hash-join read-back rows; M0037).
- `executor.drainRowsBounded` — 0.49 GB (the in-memory
  tail of the bounded drain).
- `executor.concatRows` — 0.16 GB (per-row build/probe slice
  concatenation).
- `executor.(*joinOp).openLazyHashJoin` (cum) — 2.80 GB (build-side
  hash-table rows held by the lazy hash join).

Cumulative allocations since process start (the GC mark cost is
proportional to the *live* set, but the allocation rate drives the
GC frequency):

- `executor.DecodeRow` — **70 GB** total allocated.
- `executor.concatRows` — **56 GB** total allocated (per-row, build × probe).
- `executor.nullRow` — **33 GB** total allocated (per-row null padding).

The ratio is the actionable signal: every probe row in Q9 walks
through `concatRows` two or three times (once per join level), each
call `make([]Datum, …)`s a fresh slice, and every allocated
`Datum` slice is a pointer-bearing heap object the GC marker has to
walk on every cycle. `findObject` 29.30 % flat is the GC mark
phase chasing those pointers.

## 2. Root cause analysis

### 2.1 The shape of `Row` in goopg

`Row` is a slice of `Datum` (`internal/executor/datum.go:205`):

```go
type Row []Datum
```

`Datum` is a 96-byte union-style struct
(`internal/executor/datum.go:55`-`86`) carrying inline scalar fields
plus three pointer-bearing fields: `String string`, `Bytes []byte`,
and `NumericBig *big.Int`. Every `Datum` therefore costs the GC
marker three pointer slots to scan, and every `Row` is a heap object
plus N × 96 bytes of `Datum` payload.

### 2.2 Each operator allocates a fresh Row in `Next()`

The Volcano-style `Next()` contract in goopg returns a freshly
allocated `Row` value most of the time. The hot-path allocators are:

#### 2.2.1 `concatRows` — every join output is a fresh slice

`internal/executor/operators_join_agg.go:726`:

```go
func concatRows(a, b Row) Row {
    out := make(Row, 0, len(a)+len(b))
    out = append(out, a...)
    out = append(out, b...)
    return out
}
```

`concatRows` is called from every join algorithm. In the lazy
hash-join `nextLazy` body
(`internal/executor/operators_join_agg.go:380-393`):

```go
for o.lazyActive && o.lazyMatchIdx < len(o.lazyMatches) {
    m := o.lazyMatches[o.lazyMatchIdx]
    o.lazyMatchIdx++
    var joined Row
    if o.plan.BuildLeft {
        joined = concatRows(m, o.lazyRow)
    } else {
        joined = concatRows(o.lazyRow, m)
    }
    …
}
```

— that is one fresh `[]Datum` per emitted row. For Q9, ~22 M
`nextLazy` invocations land here.

In the merge join (`runMergeJoin`,
`operators_join_agg.go:243-247`), `concatRows` is invoked inside the
inner `for a := li; a < i` × `for b := rj; b < j` loop and the
result is appended to `o.rows` — the row slice plus its `Datum`
payload survives the entire query.

In the nested-loop fallback (`runNestedLoop`,
`operators_join_agg.go:93-115`), `concatRows` runs O(N×M) times,
and the outer-join padding cases call `concatRows(l, nullRight)`
or `concatRows(nullLeft, r)`, each of which materialises a fresh
slice plus a fresh null-padded slice on every call.

#### 2.2.2 `nullRow` — fresh null-padded slice every call

`internal/executor/operators_join_agg.go:733`:

```go
func nullRow(n int) Row {
    out := make(Row, n)
    for i := range out {
        out[i] = NullDatum
    }
    return out
}
```

Outer-join paths re-derive the null padding each row.
`openLazyHashJoin`
(`operators_join_agg.go:146,169`) also calls
`concatRows(l, nullRow(rightWidth))` and
`concatRows(nullRow(leftWidth), r)` on the build path *for hash-key
evaluation only* — and then immediately discards the concatenated
row. This is a near-pure allocation-cost call site.

`nextLazy` reaches for nullRows on every outer-join LEFT-fallback
emit
(`operators_join_agg.go:367-368, 422-426`).

#### 2.2.3 `DecodeRow` — fresh row per scanned tuple

`internal/executor/codec.go:57`:

```go
func DecodeRow(cols []catalog.Column, data []byte) (Row, error) {
    row := make(Row, len(cols))
    if err := DecodeRowInto(row, cols, data); err != nil {
        return nil, err
    }
    return row, nil
}
```

A `DecodeRowInto` reusable variant already exists
(`codec.go:68`) — and is used by UPDATE
(`operators_storage.go:953-970`), which keeps a `scanRow` buffer
across slots — but `seqScanOp.Next`
(`operators_storage.go:194`) and `indexScanOp.Open`'s scan callback
(`operators_index.go:159`) both call `DecodeRow`, allocating a
fresh `Row` per visible tuple. The lineitem scan in Q9 is ~6 M
rows, so this alone costs ~6 M Row allocations per Q9.

Inside `DecodeRow`, the value side also allocates: TOAST-pointer
decoding (`codec.go:87`) does `append([]byte(nil), data[…])` per
flagged column, and the varlen string lane (`codec.go:241`)
materialises a fresh `string(data[…])` per column — these are the
small allocations that drive the 70 GB cumulative `DecodeRow`
total.

#### 2.2.4 `spillReader.ReadRow` — fresh row per spill row

`internal/executor/spill.go:75`:

```go
func (r *spillReader) ReadRow() (Row, error) {
    …
    data := make([]byte, dataLen)         // line 89
    …
    row := make(Row, nCols)               // line 104
    pos := n
    for i := uint64(0); i < nCols; i++ {
        d, n2, err := decodeDatum(data[pos:])
        …
        row[i] = d
        pos += n2
    }
    return row, nil
}
```

Two allocations per row: the read buffer and the `Row`. With Q9's
build side spilling ~10 M rows, both allocations land in the live
set during the scan of the spill file because each `Row` is then
held by the consumer (`spillOp.Next` →
`drainRowsBounded.rowsOp` → hash-table chain). Hence the **1.65 GB
live** carried by `spillReader.ReadRow`.

`drainRowsBounded` has a similar pattern at
`internal/executor/spill.go:314`:

```go
dup := make(Row, len(row))
copy(dup, row)
rows = append(rows, dup)
```

— and in `drainRows` itself
(`operators_join_agg.go:710-723`).

#### 2.2.5 `multiHashJoinOp.copyOut` — fresh row per emitted

The MHJ already has a reusable `lazyOut` buffer
(`internal/executor/multi_hash_join.go:51,252`), but the public
`Next()` contract forces a copy in `copyOut`
(`multi_hash_join.go:559-563`):

```go
func (o *multiHashJoinOp) copyOut() Row {
    row := make(Row, len(o.lazyOut))
    copy(row, o.lazyOut)
    return row
}
```

This exists because *the parent operator might keep a reference*
across `Next()` calls. Whether the parent really needs that is
operator-specific (filter/project/limit do not; sort/hash-build do).

#### 2.2.6 `projectOp.Next` — fresh row per output

`internal/executor/operators.go:60-74`:

```go
func (o *projectOp) Next() (Row, error) {
    in, err := o.child.Next()
    …
    out := make(Row, len(o.targets))
    for i, t := range o.targets {
        v, err := evalExpr(t, in, o.ctx)
        …
        out[i] = v
    }
    return out, nil
}
```

Compare to upstream's `ExecBuildProjectionInfo` /
`EEOP_ASSIGN_*_VAR` (executor `README` lines 218-228), which writes
into the *result slot's pre-allocated `tts_values[]` array*.

#### 2.2.7 `sortOp` and `aggregateOp` retain rows by design

`internal/executor/operators.go:191-200` (sort) and
`internal/executor/operators_join_agg.go:536-548` (aggregate
group output) accumulate `[]Row` for the lifetime of the query.
This is upstream-aligned (Sort and Material legitimately copy —
see `nodeMaterial.c:147` `tuplestore_puttupleslot`) but every
held `Row` is also a heap object the GC scans every cycle, which
is why scanobject is 76.75 % cum even when allocation rate is low.

### 2.3 The mechanical consequence

For Q9, the allocation chain per emitted aggregate group is roughly:

```
seqScan/indexScan      → 1 Row alloc per visible heap tuple
↓
filterOp               → 0 (pass-through)
↓
multiHashJoin/joinOp   → 1 Row alloc per emitted joined row (concatRows + copyOut)
                       + 1 Row alloc per build-side hash insert (held)
                       + 1+ nullRow alloc per outer-fallback or hash-key probe
↓
projectOp              → 1 Row alloc per output row
↓
aggregateOp.Open       → 1 Row alloc per output group (held)
↓
sortOp                 → 1 []Row alloc + retains all child rows (held)
```

Live + retained pointer-bearing slices stay around long enough that
GC marker workers (`gcBgMarkWorker`) chase them on every concurrent
cycle, and the allocation rate keeps the GC pacing tight. Hence the
78 % CPU spent in GC even though Q9 itself is not allocating
exotic large objects — it is the *cardinality* of small `Row`
allocations that dominates.

## 3. Design principles from PostgreSQL upstream

### 3.1 Slot ownership model (own vs. ref-only)

`execTuples.c:4-8`:

> Routines dealing with TupleTableSlots. These are used for resource
> management associated with tuples (eg, releasing buffer pins for
> tuples in disk buffers, or freeing the memory occupied by transient
> tuples). Slots also provide access abstraction that lets us
> implement "virtual" tuples to reduce data-copying overhead.

Concretely: a `TupleTableSlot` carries a `tts_flags` discriminator
indicating whether the slot **owns** the tuple bytes (so it must
free them on `clear`) or merely **references** them (e.g. into a
pinned buffer page). A SeqScan over a heap relation stores the
returned tuple via `ExecStoreBufferHeapTuple` — the slot points
straight into the buffer page and the tuple is *not* copied into
heap memory; the buffer pin keeps it alive
(`execTuples.c:32-33`).

### 3.2 Pipeline slot reuse

`execTuples.c:41-46`:

> The important thing to watch in the executor code is how pointers
> to the slots containing tuples are passed instead of the tuples
> themselves … It also allows us to avoid physically constructing
> projection tuples in many cases.

Each plan-state node has its own `ScanTupleSlot` and
`ResultTupleSlot`. The slot is **reused across `ExecProcNode`
calls**: every call clears and refills the slot. A parent that
wants the row past the next `Next()` must `ExecCopySlot` it into
its own slot.

### 3.3 Format conversion happens lazily

`execTuples.c` materialises a HeapTuple → MinimalTuple only when the
caller asks (`ExecFetchSlotMinimalTuple`,
`tts_minimal_materialize` at line 587), and converts to a virtual
form (the Datum/isnull arrays) only when somebody *reads* a column
out of the slot (`slot_getsomeattrs` /
`heap_deform_tuple`, `execTuples.c:421-423`,
`609-611`). MinimalTuple is the storage form Sort and HashJoin
write to disk; HeapTuple is the in-memory shape returned by scans;
virtual is the deformed-into-Datum-array shape that expressions
read.

### 3.4 Virtual tuples for projection (zero physical copy)

`execTuples.c:1731-1754` documents the protocol:

> Mark a slot as containing a virtual tuple.
>
> The protocol for loading a slot with virtual tuple data is:
>   * Call `ExecClearTuple` to mark the slot empty.
>   * Store data into the Datum/isnull arrays.
>   * Call `ExecStoreVirtualTuple` to mark the slot valid.
>
> This is a bit unclean but it avoids one round of data copying.

Projection (`ExecProject`, README §`Targetlist Evaluation`, lines
218-228) emits an `EEOP_ASSIGN_TMP` that writes directly into the
result slot's `tts_values[]` array. No new Datum array is allocated
per row — the slot owns the array, the result is rewritten in place.

### 3.5 Sort/Material/Hash-build copy on insert (upstream-aligned)

`nodeMaterial.c:147`:

```c
if (tuplestorestate)
    tuplestore_puttupleslot(tuplestorestate, outerslot);

ExecCopySlot(slot, outerslot);
```

Material/Sort hand a copy to the tuplestore (which owns the
copied bytes) **and** copies into its own slot for the immediate
return. Hash join inserts via `ExecHashTableInsert`
(`nodeHashjoin.c:1241`) using a `MinimalTuple` extracted from the
inner slot — that copy is mandatory because the inner slot is
about to be overwritten by the next `ExecProcNode`. Spill
write/read paths (`ExecHashJoinSaveTuple`,
`nodeHashjoin.c:1414` and `ExecHashJoinGetSavedTuple`) round-trip
through MinimalTuple bytes — the read-back also lands in a
**reusable slot**, not a fresh heap allocation per row.

### 3.6 Synthesis

The PG upstream design reduces to four rules:

1. **Slot is the unit of ownership**, not the row.
2. **Pipelines pass slot pointers, not row data**, and clear-then-
   refill the slot on each pull.
3. **Operators that must retain rows copy at retention time**, into
   their own private storage (tuplestore / hash table / sort
   buffer). The copy is one-way and matched 1-to-1 with the live
   set the operator must keep.
4. **Virtual tuples (Datum arrays) are the cheap on-stack form**;
   physical (Heap/Minimal) tuples are the durable serialisable
   form, used only for I/O (spill, network, disk).

## 4. Application to goopg

### 4.1 Operator-by-operator adaptation table

The "must keep" column states which copies remain mandatory under
the upstream model. The "can drop" column states which copies are
unnecessary given strict read-once consumer semantics.

| Operator | Current allocation pattern | Proposed change | Mandatory copies that remain (and why) |
|---|---|---|---|
| `seqScanOp.Next` | `DecodeRow` allocates a fresh `Row` per visible tuple (`operators_storage.go:194`). | Keep one `scanRow []Datum` on `seqScanOp` (mirroring the UPDATE pattern at `operators_storage.go:953-970`). Refill via `DecodeRowInto`. The buffer-page pin already provides the byte-safety upstream relies on. | TOAST-resolved varlen bytes (lines 199-204) when needed — the row owns the resolved bytes, not the page. |
| `indexScanOp` | `o.rows = append(o.rows, row)` accumulates every match in `Open` (`operators_index.go:170`). Each `row` is a fresh `DecodeRow`. | Convert to streaming `Next()`: hold the btree iterator, decode-into-buffer per emitted row. The current upfront accumulation is itself a pre-existing cost. | Until streaming is wired (M0054-0005b), reuse a `scanRow` so the per-tuple `DecodeRow` becomes `DecodeRowInto`. |
| `filterOp.Next` | Pass-through (already ref-only); no allocation. (`operators.go:92-105`) | No change. | None — already upstream-aligned. |
| `projectOp.Next` | `make(Row, len(o.targets))` per row. (`operators.go:60-73`) | Allocate one `out` row buffer in `Open`. Overwrite in place per `Next`. Document that callers may only hold the returned `Row` until the next `Next()` call (matches upstream's slot-reuse contract). | None — projection writes column-by-column, no need for a fresh slice. |
| `joinOp` (lazy hash, `nextLazy`) | `concatRows(m, o.lazyRow)` per emitted row (`operators_join_agg.go:382-385`). `nullRow(...)` per outer fallback (lines 367-368, 422-426). | Allocate a single `lazyOut Row` of width `lazyLW + lazyRW` once in `Open`. `nextLazy` writes left-half + right-half into `lazyOut` and returns it. Pre-allocate `nullLeft`, `nullRight` once in `Open`. | Build-side rows held in `lazyHash[key]` are mandatory copies (probe will overwrite the build child's `Row`). |
| `joinOp` (merge / nested-loop, `runMergeJoin`, `runNestedLoop`) | `concatRows` per emit, accumulated in `o.rows`. | Pool the `concatRows` output Row allocator: `out` Rows are written into a shared `[]Datum` arena per-batch, with a per-row slice header. (Effectively a `joinRowPool` keeping a free list of `Row` objects.) | Retention into `o.rows` itself is mandatory because parent (sort, group) reads them later. |
| `multiHashJoinOp` | `lazyOut` already reused; `copyOut()` allocates per `Next` (`multi_hash_join.go:559-563`). | Add `passThrough bool` flag: when the parent is filter/project (decided in `Open` via parent-info from planner, or by an explicit `Operator.WantsBorrowed()` capability bit), skip the copy and return `lazyOut` directly. Filter/project don't retain the row past the call. Sort/aggregate parents retain → keep the copy. | Sort/aggregate retention copies remain. Hash-key probe path (`evalHashKey` calls `concatRows(l, nullRow)` at `operators_join_agg.go:146,169`) is replaced with a stack-shaped `make(Row, leftWidth+rightWidth)` allocated once per Open. |
| `lazyHashJoin` build path | `concatRows(l, nullRow(rightWidth))` per build row purely to evaluate the hash key (`operators_join_agg.go:146,169`). | Evaluate the hash key against the build row alone — `evalExpr` on a synthesised row that is `o.buildEvalBuf` (length leftWidth+rightWidth, allocated once, the irrelevant half stays at NULL forever). | Build-side retained rows still copied from the source (since the source Row is reused by the upstream scan). |
| `spillHashJoin` (`drainRowsBounded` → `spillOp`) | `spillReader.ReadRow` allocates a `[]byte data` and a fresh `Row` per row (`spill.go:89, 104`). | Reuse `data` buffer and `row` slice on the reader. Returned rows are caller-owned for the duration of one `Next()` only; the consumer (hash-build) **must** copy if it retains. Hash-build is the single consumer here so this becomes one `make(Row, n) + copy` per retained row, no different from the current cost — but the *transient* allocations on the reader vanish. | Hash-build retention: still copy. (Net: -1 alloc per spill row.) |
| `sortOp` | Buffers all child rows; no per-row alloc beyond what child emits. (`operators.go:191-200`) | When the child returns *borrowed* rows (because we let it, per the project/filter changes above), `sortOp` must copy on insertion. Use `dup := make(Row, len(row)); copy(dup, row)` mirroring the existing `drainRows` pattern at `operators_join_agg.go:720-722`. | Mandatory: retention requires copy. Upstream Sort uses MinimalTuple in tuplestore (`nodeMaterial.c:224`) — same trade. |
| `aggregateOp.Open` | One Row per output group, plus per-input-row alloc only inside `evalGroupKey`. (`operators_join_agg.go:485-548`) | `evalGroupKey` builds `vals := make(Row, 0, …)` (line 556) per input row even when the only consumer is a `strings.Join`. Eliminate the `vals` Row when GroupExprs is empty (no groupValues stored). For non-trivial GroupExprs, allocate on first occurrence per group key only. | Output rows of distinct groups are mandatory retention. |

### 4.2 The `Operator` capability extension

A small interface bit lets the parent advertise whether it retains:

```go
// In internal/executor/operator.go.
//
// BorrowSemantics describes whether an Operator's Next() returns
// a Row the caller may keep ("OwnedRow") or that the caller must
// finish reading before the next call ("BorrowedRow").
type BorrowSemantics int

const (
    OwnedRow BorrowSemantics = iota
    BorrowedRow
)

// Borrowable is the optional capability advertised by Operators
// that can return BorrowedRows. Plain Operators stay at OwnedRow.
type Borrowable interface {
    SetBorrow(BorrowSemantics)
}
```

The executor wiring at `internal/executor/executor.go` walks the
plan tree once after construction and calls
`SetBorrow(BorrowedRow)` on a child whose parent is filter, project,
limit, or `outputOp` (the topmost protocol-writer that consumes
each row before pulling the next). For sort/material/hash-build
parents, the child stays at the default `OwnedRow` and continues
to allocate fresh rows.

This is the goopg analogue of upstream's "the caller must
`ExecCopySlot` if it retains the slot" rule — except enforced by
the operator's own knowledge of who its parent is, since goopg has
no slot abstraction yet.

### 4.3 The single-row reuse buffer pattern

For each operator that adopts borrow-on-output:

```go
type joinOp struct {
    …
    out Row // reusable output buffer; len == lazyLW + lazyRW
    borrow BorrowSemantics // set by SetBorrow
}

func (o *joinOp) Open(ctx *Context) error {
    …
    o.out = make(Row, o.lazyLW + o.lazyRW)
}

func (o *joinOp) nextLazy() (Row, error) {
    …
    // copy build half + probe half into o.out
    if o.borrow == OwnedRow {
        // Caller retains: must hand out a copy.
        return cloneRow(o.out), nil
    }
    return o.out, nil
}
```

The `cloneRow` helper is a single `make + copy` keeping the existing
contract for retention paths, so the change is bisectable per
operator.

## 5. Phased implementation plan (input to M0054-0005)

The work splits into three independently shippable stages.

### 5.1 Stage M0054-0005a — Per-row buffer reuse on leaf scans (low risk, mid effect)

**Files touched:**

- `internal/executor/operators_storage.go:71-214` — add
  `seqScanOp.scanRow []Datum`; replace `DecodeRow` at line 194 with
  `DecodeRowInto`; clone before return (the parent of seqScan is
  almost always filter/project, both already pass-through, so
  pre-stage-b this is a defensive clone — line `return row, nil`
  becomes `return cloneRow(o.scanRow), nil`).
- `internal/executor/operators_index.go:144-181` — same pattern
  inside the btree scan callback. The accumulation into
  `o.rows` already does its own clone, so this stage just stops
  the per-callback `DecodeRow` from allocating: `o.scanRow` reused,
  appended-clone retained.
- `internal/executor/spill.go:75-115` — `spillReader.ReadRow`
  reuses `r.data []byte` and `r.row Row` buffers across calls;
  document that `Row` is invalidated by next `ReadRow`.
- `internal/executor/spill.go:269-330` — `drainRowsBounded`'s
  `dup := make(Row, len(row))` becomes `dup := cloneRow(row)`
  (functional no-op, just unifies the helper).

**Expected pprof shift (Q9):**

- `runtime.findObject` flat 29.30 % → ≤ 18 % (allocations per row
  decrease by ~1 in the leaf path; cumulative `DecodeRow` 70 GB
  → ≤ 25 GB).
- `runtime.scanobject` cum 76.75 % → ≤ 55 % (same effect, mark
  cost is proportional to live pointer-bearing objects).

**Regression risk:**

- `internal/executor/storage_dml_test.go`,
  `index_scan_tpch_test.go`, `range_index_scan_test.go`: these
  hold returned rows and re-fetch — the defensive clone protects
  this. No behaviour change expected.
- `internal/executor/spill_test.go`: must verify that the
  reused-buffer reader still round-trips correctly when the
  consumer retains rows (it must clone — assert behaviour
  preserved).
- `internal/executor/multi_hash_join_test.go`,
  `tpch_test.go`: end-to-end TPC-H result-parity tests catch
  any aliasing bug.

**Acceptance: ** stage a passes its own targeted bench
(`internal/testutil/tpch/`), `findObject` is no longer the flat
top, and Q9 walltime improves by ≥ 20 %.

### 5.2 Stage M0054-0005b — Borrow-semantics abstraction (medium risk, large effect)

**Files touched:**

- `internal/executor/operator.go` — add the
  `BorrowSemantics` enum and `Borrowable` interface declared in
  §4.2.
- `internal/executor/executor.go` — walk the plan tree after
  build; call `SetBorrow(BorrowedRow)` on children of
  `filterOp`, `projectOp`, `limitOp`, and the outermost protocol
  writer (`cmd/goopg/main.go`'s row sink — a single
  `outputOp.SetBorrow(BorrowedRow)` if it implements Borrowable).
- `internal/executor/operators.go:60-74` (project) — rewrite
  `projectOp.Next`: allocate `o.out` once in `Open`, fill in place,
  honour `o.borrow` on return.
- `internal/executor/operators.go:92-106` (filter) — already
  pass-through; just propagate `SetBorrow` to child.
- `internal/executor/operators_join_agg.go:120-180,366-435` (lazy
  hash join) — replace `concatRows` and `nullRow(...)` with the
  reusable `o.out` / `o.nullLeft` / `o.nullRight` buffers
  declared in §4.3. Honour `o.borrow` in `nextLazy`.
- `internal/executor/multi_hash_join.go:559-563` — `copyOut` honours
  `o.borrow`. When `BorrowedRow`, return `o.lazyOut` directly.
- `internal/executor/operators.go:186-247` (sort) — when the child
  is `BorrowedRow`, sort *must* clone on insertion (line 199:
  `o.rows = append(o.rows, cloneRow(row))`). Cleanest: sort always
  clones; if child happens to return owned rows, the clone is
  redundant but small.
- `internal/executor/operators_join_agg.go:485-548` (aggregate) —
  same: aggregate clones the row only as far as it retains
  `groupValues`, which it already does.

**Expected pprof shift (Q9):**

- `executor.concatRows` cumulative 56 GB → ≤ 5 GB (only the
  retention paths still allocate).
- `executor.nullRow` cumulative 33 GB → < 1 GB (one
  per-Open allocation, not per-row).
- `runtime.gcBgMarkWorker` cum from ~78 % → ≤ 30 %.

**Regression risk:**

- The aliasing rule "BorrowedRow invalidates on next Next()" is
  novel in goopg; any operator that violates it produces silent
  data corruption.
  - Mitigation: in `-tags executor_borrow_assert` builds, the
    Borrowed row carries a "consumed" marker (an extra
    out-of-band field on a debug wrapper). Any read after the
    next `Next()` panics. Run all `internal/executor/*_test.go`
    under this build tag in CI.
- Operator authors outside the touched set (recursive CTE,
  window, setop, lock-rows, upsert) keep `OwnedRow` semantics by
  default — the change is opt-in per operator.

**Acceptance:** Q9 in-use heap drops below 3 GB; full
`go test ./...` passes including `-tags executor_borrow_assert`.

### 5.3 Stage M0054-0005c — `concatRows` / `spillReader` / `nullRow` pooling (low risk, mid effect)

After stages a+b, the surviving allocators are the merge / nested-
loop join (which retains rows in `o.rows`) and the aggregate /
sort retention copies. These are mandatory under upstream's design
but can still be served from a pool to keep the GC marker working
on a *bounded* set of long-lived `Row` objects rather than an
ever-growing churn.

**Files touched:**

- `internal/executor/row_pool.go` (new) — define
  `rowPool` with `sync.Pool` semantics keyed on capacity (small
  power-of-two buckets: 1, 2, 4, 8, 16 columns).
- `internal/executor/operators_join_agg.go:85-118` (nested loop) —
  borrow from the pool for each `concatRows` output stored into
  `o.rows`.
- `internal/executor/operators_join_agg.go:190-273` (merge join) —
  same.
- `internal/executor/spill.go:75-115` (`spillReader.ReadRow`) —
  the read buffer goes through the pool too.
- `internal/executor/operators_join_agg.go:710-723` (`drainRows`)
  — pool the dup'd rows.
- `internal/executor/operators_join_agg.go:733` (`nullRow`) —
  document that the function still allocates and is now called
  exactly once per join `Open`. (Pooling NULL rows is over-
  engineering — only the call-rate is the issue.)

**Expected pprof shift (Q9):**

- In-use heap from the post-stage-b 3 GB target → ≤ 1.5 GB
  (mark cost is proportional to *number of live pointer-bearing
  objects*; pooling collapses this by orders of magnitude).
- `runtime.scanobject` cum target ≤ 30 %.

**Regression risk:**

- `sync.Pool` rows from the wrong size bucket can mask
  out-of-bounds writes. Mitigation: each bucket panics in -race
  builds if the borrowed slice is shorter than the requested
  width.

**Acceptance:** Stage-c-only delta on Q9 walltime ≥ 10 %; all
existing pool-sensitive tests
(`internal/executor/multi_hash_join_test.go`,
`internal/executor/spill_test.go`,
`internal/executor/explain_analyze_test.go`,
`internal/executor/tpch_test.go`) pass on -race.

### 5.4 Sequencing rationale

Stage a is shippable independently and provides immediate ≥ 20 %
walltime improvement for any query that reads a lot of rows. It is
also the prerequisite for stage b: once leaf scans hand out
borrowed rows, the rest of the pipeline can borrow without violating
the source-of-truth contract.

Stage b is the *design* change — it touches the operator interface.
It depends on stage a (so the leaf is borrow-aware) and unlocks
stage c (pooling makes more sense once allocations are concentrated
in known retention sites).

Stage c is the cleanup that converts what used to be one-shot
allocations of held rows into a cycling allocation pattern, which
is what the GC marker actually likes (steady-state working set, no
churn).

## 6. Acceptance criteria (M0054-0005 closure bar)

A reviewer can close M0054-0005 when, and only when, the following
all hold:

1. **pprof regression check**: rerun `pprof-all.sh` against a Q9
   power-test with the patch applied and produce
   `cpu_q9_after.prof`.
   `go tool pprof -top -cum cpu_q9_after.prof | head -20` must show
   `runtime.scanobject` cum ≤ 30 % (vs. 76.75 % baseline).
2. **Heap regression check**: heap profile during Q9 steady state
   must show in-use ≤ 3 GB (vs. 5.14 GB baseline). Captured via
   `pprof-all.sh`'s heap window.
3. **Result parity**: `internal/testutil/tpch/` test suite plus
   `internal/executor/tpch_test.go` plus the HammerDB power-test
   pass with no row-count regression versus baseline.
4. **Test suite**: `go test ./...` PASS, including the optional
   `-tags executor_borrow_assert` run for stage b.
5. **Analysis report regenerated**:
   `analysis/tpch-pprof-bottleneck-survey.md` is regenerated with
   the after-fix top-10 / top-3 sections so the comparison is
   visible in the same artefact reviewers used for the baseline.
6. **Anti-pattern register honoured** (M0054-0001 §7): no item in
   stages a/b/c is closed by claiming "out of scope" or "needs
   more investigation". Each stage names every file it touches
   in this document.

## 7. Upstream cross-reference table

PostgreSQL upstream paths are relative to
`postgres/src/backend/executor/`.

| Concern | Upstream | goopg today | goopg after |
|---|---|---|---|
| Slot ownership rule | `execTuples.c:1-8`, `nodeMaterial.c:147-149` (`tuplestore_puttupleslot` + `ExecCopySlot`) | `Row = []Datum` everywhere; no ownership rule | `BorrowSemantics` advertised by Operator (§4.2) |
| Pipeline reuse | `execTuples.c:41-46` (slot pointers, not tuples) | Each `Next()` returns a fresh `Row` | Reusable `o.out` per operator + borrow propagation |
| Virtual tuple for projection | `execTuples.c:1731-1754` (`ExecStoreVirtualTuple`); README §"Targetlist Evaluation" lines 218-228 | `projectOp.Next` `make(Row, len(targets))` per call (`operators.go:60-74`) | `projectOp.out` allocated in Open, written in place |
| Sort/Material retention copy | `nodeMaterial.c:147` (`tuplestore_puttupleslot`); `execTuples.c:587-611` (`tts_minimal_materialize`) | `sortOp.rows` retains child rows; `aggregateOp.rows` retains group rows | Retention path **explicit**: `cloneRow(row)` on insertion |
| Hash-join build copy on insert | `nodeHashjoin.c:1241` (`ExecHashTableInsert`); `nodeHashjoin.c:1414-1488` (`ExecHashJoinSaveTuple` / `ExecHashJoinGetSavedTuple`) | `joinOp.lazyHash[key] = append(..., l)` (`operators_join_agg.go:150,173`); `spillReader.ReadRow` allocates per row | Build copy retained; spill read-back reuses one buffer per `spillReader` |
| Buffer-pin zero-copy scan | `execTuples.c:32-33` (`ExecStoreBufferHeapTuple`) | `seqScanOp` decodes into a fresh `Row` per tuple (`operators_storage.go:194`) | `seqScanOp.scanRow` reused via `DecodeRowInto`; pin still held by storage layer |
| Per-tuple memory context | README §"Memory Management" lines 270-287 | None (Go GC) | `rowPool` (stage c) is the goopg analogue: bounded cycling buffers per width bucket |
| Slot-clear before refill | `execTuples.c:1734` (`ExecClearTuple` first) | n/a (slice overwrite is implicit) | n/a (still implicit; documented as the borrowed-row invalidation rule) |

The largest divergence point is that goopg has no `tts_isnull[]`
parallel array — NULL is encoded inside `Datum.Kind`. This is
intentional (one less allocation per Row) and remains under this
proposal.

## 8. Out-of-scope notes (not deferrals)

Per M0054-0001 §7 the following are *explicit decisions made now*,
not items punted to a later milestone:

- **Vectorised executor / batch tuples**: goopg stays scalar
  Volcano. Vectorisation is a separate redesign that subsumes this
  doc; until then the Volcano-shaped wins above are the correct
  next step.
- **MinimalTuple-style on-disk encoding for spill**: the existing
  `spill.go` per-Datum binary encoding is fine; replacing it with
  upstream's MinimalTuple wire format is a type-system milestone
  concern, not GC pressure.
- **Datum interface vs. struct**: keeping `Datum` as a struct
  (`datum.go:55`) is documented as deliberate (the struct is "much
  cheaper than per-row heap allocation"). This proposal does not
  reopen that decision; it only attacks the *Row*-level
  allocation rate.

## 9. References

- pprof baseline: `analysis/tpch-pprof-bottleneck-survey.md`
  (M0054-0004 deliverable)
- Methodology: `docs/design/0054-0001-tpch-perf-investigation-methodology.md`
- Upstream README: `postgres/src/backend/executor/README` lines
  41-46, 218-228, 270-287
- Upstream slot infrastructure:
  `postgres/src/backend/executor/execTuples.c:1-8, 1731-1754,
  587-611`
- Upstream Material:
  `postgres/src/backend/executor/nodeMaterial.c:90-150`
- Upstream HashJoin:
  `postgres/src/backend/executor/nodeHashjoin.c:1241, 1414-1488`
- goopg `Row`/`Datum`: `internal/executor/datum.go:55-205`
- goopg codec: `internal/executor/codec.go:57-99`
- goopg join hot path: `internal/executor/operators_join_agg.go:60-435,
  710-740`
- goopg MHJ: `internal/executor/multi_hash_join.go:51, 252, 559-563`
- goopg spill: `internal/executor/spill.go:75-330`
- goopg leaf scans: `internal/executor/operators_storage.go:71-214,
  945-980` (UPDATE pattern); `internal/executor/operators_index.go:144-196`
- goopg project/filter/sort: `internal/executor/operators.go:44-256`
