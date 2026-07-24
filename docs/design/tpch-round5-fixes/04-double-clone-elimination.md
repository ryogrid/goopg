# 04 — Double-Clone Elimination

| field | value |
| --- | --- |
| priority | HIGH — eliminates redundant O(width) copy per hash-build row |
| risk | Low |
| files | `internal/executor/operators_storage.go`, `internal/executor/operators_join_agg.go` |
| discovered during | Round 5 codebase exploration (not directly in the original report) |

## 1. Motivation

During codebase exploration for the Round 5 design bundle, a **double-clone** was
discovered in the seqScan → hash-join-build pipeline. Every row that goes from a
sequential scan into a hash table is copied **twice**:

1. **In `seqScanOp.Next()`** (`operators_storage.go:1677`): `cloneRowOwned(o.scanRow)`
   to release the page RLock. This deep-copies arena-backed string/bytes into owned
   `[]byte` allocations.
2. **In `drainRowsCtx`** (`operators_join_agg.go:3184-3189`): checks `rowHasArena(row)`
   (which is now `false` since step 1 already materialized arena bytes), then does
   `acquireRow(len(row)) + copy(dup, row)` — a second O(width) copy.

For TPC-H Q9, the MHJ build drains 6M `lineitem` rows + 1.5M `orders` rows +
other build tables — roughly 8M rows, each 16 columns wide. Two copies per row
means **16M redundant `acquireRow` + `copy` operations**.

This is not visible as a distinct function in the CPU profile because both steps
are inlined into larger call chains, but the cumulative allocation from the row
pool (`init.3.func1` at 9.94 GB for Q9, 25.41 GB for Q4) reflects this double
allocation.

## 2. Current state

### 2.1 seqScanOp.Next() — the first clone (`operators_storage.go:1622-1677`)

```go
// Inside seqScanOp.Next(), after tuple decode:
o.scanRow = acquireRow(len(o.cols))                          // line 1622: pool get + zero
err = DecodeRowIntoMctxPGTuple(o.scanRow, o.cols, ...)       // line 1629: arena-backed decode
if o.detoastNeeded { DetoastRow(o.scanRow) }
// ... enum/ACL post-processing ...

row = cloneRowOwned(o.scanRow)                                // line 1677: ← FIRST CLONE
// Release page RLock
// Return slot with owned row
```

The `cloneRowOwned` at line 1677 is **necessary** because the decode uses the
page arena (`o.sctx`). When the page RLock is released, the arena is reset on
the next page read, invalidating all arena-backed string/bytes Datums. The
clone promotes them to heap-allocated `[]byte`.

But there's a subtlety: not all columns are arena-backed. Fixed-width types
(int4, int8, float8, date, bool) produce Datums with `ArenaID == 0` — no
arena aliasing. Only varchar/text/name/bytea columns use arena backing.
For a row like `lineitem` (4 int4 + 4 date + 3 numeric + 5 varchar), only
5 of 16 columns need materialization. The other 11 are already heap-safe
Datum values.

### 2.2 drainRowsCtx — the second copy (`operators_join_agg.go:3166-3193`)

```go
func drainRowsCtx(op Operator, ctx *Context) ([]Row, error) {
    rows := make([]Row, 0, 1024)
    for {
        slot, err := op.Next()
        if err == EOF { break }
        row := slotRow(slot)
        var dup Row
        if rowHasArena(row) {
            dup = cloneRowOwned(row)     // arena: deep-copy bytes (alloc per column)
        } else {
            dup = acquireRow(len(row))   // ← SECOND COPY: pool get + copy(dup, row)
            copy(dup, row)
        }
        rows = append(rows, dup)
    }
    return rows, nil
}
```

After `seqScanOp.Next()` returns, `rowHasArena(row)` is **always false** — the
row was already cloned through `cloneRowOwned`. So `drainRowsCtx` always takes
the `acquireRow + copy` path, duplicating the row for the hash table.

### 2.3 Why both copies exist

The first copy (seqScanOp) is required for page-RLock release. The second copy
(drainRowsCtx) is required because `drainRowsCtx` is a general-purpose drain
function that doesn't know whether the input operator's rows are already owned.
It must assume arena aliasing is possible.

## 3. Design

### 3.1 Approach: ownership transfer hint

The cleanest fix works at the `drainRowsCtx` level: tell it that the operator
produces already-owned rows, so the second copy can be skipped.

**Option A: Add a variant function `drainRowsOwned`**

```go
// drainRowsOwned drains all rows from an operator whose output is
// guaranteed to be already owned (no arena aliasing). The rows are
// appended directly without a second copy.
func drainRowsOwned(op Operator, ctx *Context) ([]Row, error) {
    rows := make([]Row, 0, 1024)
    for {
        slot, err := op.Next()
        if err == EOF { break }
        row := slotRow(slot)
        // Trust that the row is already owned.
        rows = append(rows, row)
    }
    return rows, nil
}
```

Callers that know their child produces owned rows (seqScanOp with
`cloneRowOwned` already applied) call `drainRowsOwned` instead of
`drainRowsCtx`.

**Option B (preferred): Add a transfer-mode flag to seqScanOp**

Add a field to `seqScanOp` that, when set, skips the per-page `cloneRowOwned`
and instead transfers the arena-backed decode buffer directly to the caller:

```go
type seqScanOp struct {
    // ... existing fields ...
    transferOwnership bool // when true, Next() returns rows without cloneRowOwned;
                           // the caller (drainRowsCtx) is responsible for
                           // materializing arena bytes before the next Next() call
}
```

When `transferOwnership == true`:
1. `seqScanOp.Next()` skips the `cloneRowOwned` at line 1677 and returns the
   arena-backed row directly (along with the page RLock held until the caller
   processes it).
2. The caller (`drainRowsCtx`) calls `cloneRowOwned` (since `rowHasArena` is
   now true), doing exactly one deep copy per row — the correct number.

This eliminates the redundant second copy while keeping `drainRowsCtx` general.
The seqScanOp + drainRowsCtx pair becomes: 1 clone per row, not 2.

The `transferOwnership` flag is set by the planner or executor when the scan
feeds directly into a hash build or sort (i.e., any operator that drains all
rows at Open() time). The drain function's `cloneRowOwned` call handles the
arena→heap promotion, so the seqScanOp can skip its own clone.

### 3.2 Refined approach: skip seqScanOp's clone, let drainRowsCtx do it

Since `drainRowsCtx` already has the `rowHasArena` / `cloneRowOwned` path
(lines 3184-3185), the simplest change is:

1. **In `seqScanOp.Next()`**: Add a boolean field `yieldArenaRows bool`.
   When true, skip the `cloneRowOwned` at line 1677 and yield arena-backed rows.
   The page arena is NOT reset until the next `Next()` call — the caller must
   consume (materialize) the row before calling `Next()` again.
2. **In `drainRowsCtx`**: No changes needed. It already calls `cloneRowOwned`
   when `rowHasArena` is true (line 3185). With arena-backed rows flowing in,
   `rowHasArena` returns true, and `cloneRowOwned` does the one-and-only
   materialization.

This is 1 row clone instead of 2 — a 50% reduction in hash-build row allocation.

### 3.3 When to set yieldArenaRows

The `yieldArenaRows` flag is set to `true` when:
- The scan feeds directly into a drain operator (hash build, sort, materialize
  node in the plan), AND
- The drain operator drains all rows at `Open()` time before producing any
  output.

This is the common case for TPC-H hash joins: the build-side scan is fully
drained into the hash table before the probe begins. The planner/executor
knows this from the plan shape (hash join build child).

### 3.4 Alternative: explicit transfer via a new interface

Rather than a boolean flag, add an optional interface:

```go
// OwnedRowProducer is implemented by operators that can transfer
// ownership of their output rows to the caller. When DrainOwned is
// true, the operator guarantees each row is independent (no arena
// or buffer aliasing) and can be retained by the caller.
type OwnedRowProducer interface {
    ProducesOwnedRows() bool
}
```

`seqScanOp` implements this interface, returning `true` when the planner has
determined the output feeds into a drain-and-retain operator:

```go
func (o *seqScanOp) ProducesOwnedRows() bool {
    return o.yieldArenaRows
}
```

`drainRowsCtx` checks this interface and, when true, directly `append`s rows
without the copy:

```go
func drainRowsCtx(op Operator, ctx *Context) ([]Row, error) {
    rows := make([]Row, 0, 1024)
    owned, _ := op.(OwnedRowProducer)
    for {
        slot, err := op.Next()
        if err == EOF { break }
        row := slotRow(slot)
        if owned != nil && owned.ProducesOwnedRows() {
            rows = append(rows, row)     // already owned — no copy
        } else if rowHasArena(row) {
            rows = append(rows, cloneRowOwned(row))
        } else {
            dup := acquireRow(len(row))
            copy(dup, row)
            rows = append(rows, dup)
        }
    }
    return rows, nil
}
```

But this doesn't solve the core problem — `seqScanOp` produces arena-backed rows
(`yieldArenaRows == false` by default), and `drainRowsCtx` still needs to
clone them. The key insight is:

**The seqScanOp already materializes arena bytes before returning.** The
`cloneRowOwned` at line 1677 is the materialization. If we move that
materialization into `drainRowsCtx` (by setting `yieldArenaRows = true`),
we eliminate the redundant copy. The total number of `cloneRowOwned` calls
per row stays at 1 — it just moves from seqScanOp to drainRowsCtx.

### 3.5 Recommended minimal implementation

1. Add `yieldArenaRows bool` to `seqScanOp`.
2. In `seqScanOp.Next()`, when `yieldArenaRows` is true, skip the
   `cloneRowOwned` at line 1677. The returned row contains arena-backed
   Datums. The page RLock is held until the next `Next()` call, but the
   caller (drainRowsCtx) processes the row immediately via `cloneRowOwned`
   before calling `Next()` again, so the arena stays valid.
3. Set `yieldArenaRows = true` when constructing the seqScanOp for a
   hash-build or sort-build child (planner or executor Open-time).
4. Verify `drainRowsCtx` and `drainRowsBounded` handle arena-backed rows
   correctly (they already do — the `rowHasArena` check at line 3184).

## 4. Implementation steps

1. **Add `yieldArenaRows bool` field** to `seqScanOp` struct.
2. **Guard the `cloneRowOwned`** at `operators_storage.go:1677`:
   ```go
   if !o.yieldArenaRows {
       row = cloneRowOwned(o.scanRow)
   } else {
       row = o.scanRow
   }
   ```
3. **Set `yieldArenaRows = true`** when constructing seqScanOps that feed into
   hash-build or sort-build drains. Find the construction sites with:
   ```bash
   grep -rn "seqScanOp{" internal/executor/ | grep -v "_test.go"
   grep -rn "SeqScan" internal/executor/operators_storage.go | head -20
   ```
   The executor creates seqScanOps during plan-to-operator translation
   (look for the function that maps `planner.SeqScan` nodes to operators).
   When the parent plan node is a hash join build child or a sort/materialize
   node, set `yieldArenaRows = true` on the constructed seqScanOp. The
   plan-node-type check is:
   - `planner.MultiHashJoin` — the build children are all children except `ProbeTable`
   - `planner.HashJoin` — the build child is `children[buildSide]`
   - `planner.Sort` — the input child
4. **Update `drainRowsBounded`** (`spill.go:314`) to pass arena-backed rows
   to `spillWriter.WriteRow` — note that `WriteRow` reads Datums by value
   and never retains them, so arena-backed input is safe (it just reads
   `d.Kind`, `d.Int`, `d.StringValue()`, etc., and encodes).
5. **Run tests:**
   ```bash
   go test ./internal/executor/ -count=1
   ```
6. **Profile Q9 alloc_space** before/after: expected reduction in row-pool
   cum alloc (`init.3.func1`) proportional to the hash-build row count.

## 5. Risk assessment

| Risk | Impact | Mitigation |
| --- | --- | --- |
| Arena reset before drainRowsCtx processes the row | Use-after-free — silent data corruption | The drain loop processes the row synchronously before calling `Next()` again. The arena is only reset in the next `Next()` call. This is already the contract for arena-backed decode. |
| `yieldArenaRows` not set for a legitimate drain caller | Double-clone continues (current behaviour) — no corruption | Backward compatible: `yieldArenaRows` defaults to `false` |
| `yieldArenaRows` set for a non-drain caller (e.g., filter→project→hashagg) | Arena-backed rows reach operators that retain references across `Next()` calls | Only set `yieldArenaRows` when the immediate parent is a drain operator. The planner knows the plan tree shape. |
| `drainRowsBounded` spills arena-backed rows | Spill file contains data that references invalidated arena memory | `WriteRow` reads Datums by value (encodes kind + payload) and does not retain references. The spilled data is self-contained. |

## 6. Verification

1. **Decode correctness:** Run Q9 with and without `yieldArenaRows` — output
   row counts and values must match exactly.
2. **Allocation profile:**
   ```bash
   go tool pprof -sample_index=alloc_space -top bench/tpch/pprof/q9_allocs_after.pb.gz
   ```
   Expected: `init.3.func1` (row pool) cum alloc drops by ~30–50% for hash-join
   queries (fewer `acquireRow` calls per build row).
3. **Race detector:**
   ```bash
   go test -race ./internal/executor/ -run TestHashJoin -count=1
   ```

## 7. Related improvements

- [03-row-decode-fast-path.md](./03-row-decode-fast-path.md) — the decode strategy that produces arena-backed Datums; Fix 04 eliminates the redundant copy of those Datums.
- [01-spill-writer-stack-elimination.md](./01-spill-writer-stack-elimination.md) — the spill writer that `drainRowsBounded` delegates to; the spill writer's `WriteRow` already handles arena-backed input correctly.
