# 05 — Hash-Probe Clone Elimination

| field | value |
| --- | --- |
| priority | MEDIUM — 12–26% cum CPU where joins present (may be acceptable after Fixes 01–04) |
| risk | High (hash table internals, ref-counting, GC interaction) |
| files | `internal/executor/multi_hash_join.go`, `internal/executor/datum.go`, `internal/executor/slot.go` |
| precedent | M0071-0014 VirtualSlot composition (already eliminated per-match copies in the probe loop) |

## 1. Motivation

Profiling shows `cloneRowOwned` + `(*VirtualSlot).Row()` account for 12–26%
cumulative CPU in queries with joins:

| Query | `cloneRowOwned` cum CPU | `(*VirtualSlot).Row` cum CPU |
| --- | ---: | ---: |
| Q9 | 12.7% | 13.0% |
| Q4 (residual, post-Fix-01 est.) | ~5% | ~5% |
| Q7 (residual, post-Fix-01 est.) | ~5% | ~5% |

M0071-0014 (VirtualSlot composition) already eliminated per-match row
concatenation during the MHJ probe loop. The remaining cost is at retention
boundaries:

1. **Hash-build drain** (`drainRowsCtx` → `cloneRowOwned`): Addressed by Fix 04.
2. **Executor output boundary** (`executor.Run` → `cloneRowOwned` at `executor.go:604`):
   clones every output row before returning to the client.
3. **`VirtualSlot.Row()`** at `slot.go:160`: materializes a VirtualSlot into a
   `Row` for consumers that don't use the `SlotView` interface.

This document addresses items 2 and 3 — the remaining clone cost that persists
after Fixes 01–04.

## 2. Current state

### 2.1 VirtualSlot.Row() (`slot.go:159-165`)

```go
func (s *VirtualSlot) Row() Row {
    out := acquireRow(len(s.cols))   // pool get (already pooled, fast)
    for i := range s.cols {
        out[i] = s.Get(i)            // copy Datum by value (48 bytes) — no alloc
    }
    return out
}
```

The Datum value copy is allocation-free. The `acquireRow` is pooled. The cost
is O(width) per call, but since the VirtualSlot's source slots are
`MaterializedSlot`s whose rows are already owned (no arena bytes), there is no
`MaterializeArena` allocation.

This function's CPU profile contribution (13.0% cum for Q9) is primarily the
**cumulative cost of being called many times** from operators that call
`slotRow()` rather than using the `SlotView` interface directly.

### 2.2 cloneRowOwned at executor.Run (`executor.go:578-610`)

```go
// RunFast drains the operator and returns all rows.
func RunFast(op Operator, ctx *Context) ([]Row, error) {
    // ...
    for {
        slot, err := opNext(op, ctx)
        if err == EOF { break }
        dst := &Slot{Cells: make([]Datum, slot.Width())}
        slot.CopyTo(dst)
        out = append(out, cloneRowOwned(Row(dst.Cells)))  // ← RETENTION BOUNDARY CLONE
    }
    return out, nil
}
```

This clone is called once per output row of the entire query. For Q9 with 175
output rows, it's negligible. The cost in the profile is from intermediate
retention boundaries — not the final `RunFast` clone, but clones at sort/
aggregate/hash-build boundaries within the plan tree.

### 2.3 Where clones actually happen (TPC-H plan shapes)

For Q9's plan:

```
Sort (nation, o_year DESC)
  HashAggregate (2 keys)          ← drainRowsCtx: clone row into aggregate HT
    Nested Loop (INNER)
      Hash Join (INNER)
        Multi-Way Hash Join        ← drainRowsCtx: clone row into HT (Fix 04 target)
          Seq Scan lineitem        ← cloneRowOwned per page (Fix 04 target)
          ...
        Seq Scan part              ← cloneRowOwned per page
      Index Scan partsupp
```

Each retention boundary (hash table build, sort materialization, aggregate
hash table) clones the row. These are all necessary for correctness — the
row must outlive the producer's next `Next()` call.

The question is: **can any of these clones be avoided through shared ownership?**

## 3. Design

### 3.1 Analysis: where can clones be avoided?

| Clone site | Can avoid? | Why / why not |
| --- | --- | --- |
| seqScan → page RLock release | No | Arena bytes must be promoted before the next page read resets the arena |
| seqScan → hash table build | Addressed by Fix 04 | One clone per row (in drainRowsCtx) instead of two |
| Hash table entry → VirtualSlot probe output | **Already avoided by M0071-0014** | VirtualSlot uses `Get(col)` directly from source slots; no row materialization per match |
| VirtualSlot → HashAggregate build | **Hard** | The aggregate's hash table needs owned rows; currently VirtualSlot.Row() allocates a new Row. If the aggregate could work directly with SlotView, this clone goes away. |
| HashAggregate → Sort | Yes (copy is cheap) | Sort already materializes rows into its own storage |
| Sort → client output (RunFast) | Yes (negligible) | Only 175 rows for Q9 |

### 3.2 Recommended approach: extend SlotView adoption

The most impactful and lowest-risk change is to **extend `SlotView` usage**
in operators that currently call `slotRow()` or `.Row()` but only need column
access:

1. **`aggregateOp.applyAgg`** — currently extracts hash keys via `slotRow()`
   but only reads specific columns. Change to use `SlotView.Get(col)`.
2. **Filter/project operators** (`evalExprSlot`) — already accepts `SlotView`
   via the `expr.EvalSlot` interface. No change needed.
3. **Sort operator** — currently calls `slotRow()` to extract sort keys.
   Change to use `SlotView.Get(col)`.
4. **Nested loop join** — currently calls `slotRow()` for inner scans.
   Change to use `SlotView.Get(col)` when the inner is an index scan.

### 3.3 Advanced option: reference-counted hash-table entries

For the hash table → aggregate path (the biggest remaining clone volume),
store hash-table rows in a shared pool with reference counting:

```go
// SharedRow is a reference-counted wrapper around a Row.
// Multiple VirtualSlots can point at the same SharedRow;
// the underlying Row is released when the last reference is dropped.
type SharedRow struct {
    refs atomic.Int32
    row  Row
}

func (sr *SharedRow) Acquire() { sr.refs.Add(1) }
func (sr *SharedRow) Release() {
    if sr.refs.Add(-1) == 0 {
        releaseRow(sr.row)
    }
}
```

Hash tables store `[]*SharedRow` instead of `[]Row`. When `VirtualSlot.Row()`
is called, if all source slots are `*SharedRow`, the VirtualSlot creates a
composite `SharedRow` that holds references to each source row. The
`cloneRowOwned` at the retention boundary becomes an `Acquire()` on the
`SharedRow`.

**Problems with this approach:**
- Mutex/atomic overhead per reference-count operation (in the probe hot path).
- Garbage-collection complexity: `SharedRow`s are heap-allocated, creating
  pointer graphs that the GC must scan.
- Spill interaction: when a hash table spills, all `SharedRow` references
  must be dropped. If a consumer still holds a reference, the spill must wait
  or the consumer must materialize.
- The MHJ already avoids per-match copies via VirtualSlot composition. The
  remaining clone cost is at the aggregate/sort boundary — which is O(output_rows)
  not O(matches). For Q9: 175 output rows, not 6M probe iterations.

### 3.4 Verdict: defer the ref-counting approach

Given:
- Fix 01 alone reduces Q9 runtime from 30.64 s (already fast) by ~0 s (no spill),
  and Q4/Q7/Q13 by 3–7×
- Fix 04 eliminates the double-clone during hash build
- The remaining clone cost (12–26% cum CPU) is primarily at retention boundaries
  that operate on **output rows**, not per-match rows
- Ref-counting adds significant complexity to the hash table, spill, and GC paths

The recommendation is to **pursue the SlotView extension first** (section 3.2) —
eliminate `slotRow()` calls where only column access is needed — and **re-profile**
after Fixes 01–04 to determine if Fix 05's ref-counting approach is still justified.

## 4. Implementation steps (SlotView extension — recommended path)

1. **Audit all `slotRow()` call sites** in the executor pipeline:
   ```bash
   grep -rn "slotRow(" internal/executor/*.go | grep -v "_test.go"
   ```
2. **Identify callers that only read columns** (not retain rows). These can
   be changed to use `SlotView.Get(col)`.
3. **Update `aggregateOp.applyAgg`** to extract hash keys via `SlotView`
   instead of `slotRow()`.
4. **Update sort key extraction** to use `SlotView.Get(col)`.
5. **Re-profile Q9** and verify `(*VirtualSlot).Row()` CPU drops.
6. **Run all TPC-H queries** and verify output correctness.

## 5. Implementation steps (SharedRow ref-counting — advanced, deferred)

If pursued after re-profiling:

1. **Add `SharedRow` type** in `internal/executor/datum.go` with atomic
   reference counting.
2. **Change hash-table storage** in `multi_hash_join.go` from `map[K][]Row`
   to `map[K][]*SharedRow`.
3. **Update `VirtualSlot.Row()`** to return a `SharedRow`-backed Row when
   all source slots contain `SharedRow`s.
4. **Update all clone sites** (`cloneRowOwned`, `drainRowsCtx`) to call
   `Acquire()` on `SharedRow` instead of deep-copying.
5. **Add spill support**: when a hash table spills, call `Release()` on
   each `SharedRow` entry.
6. **Add race-detector tests** for concurrent acquire/release.

## 6. Risk assessment

| Risk | Impact | Mitigation |
| --- | --- | --- |
| SlotView extension breaks an operator that secretly retains rows | Silent data corruption (rows change under the operator) | Conservative audit: only change operators whose row lifetime is verifiably within one Next() call |
| SharedRow ref-count leaks | Memory leak — rows never freed | Add leak detection in tests; verify releaseRow is called for every SharedRow |
| SharedRow + GC interaction | Increased GC pressure from pointer-heavy data structure | Profile with GOGC=on to measure GC impact before committing to this approach |
| Spill + SharedRow interaction | Spill must materialize all referenced rows before freeing hash table | Add materialization pass before spill eviction |

## 7. Verification

1. **SlotView extension:**
   ```bash
   go test ./internal/executor/ -count=1
   tmp/tpch-runner --port=65433 --queries=all --parallel-workers=0
   ```
2. **SharedRow (if pursued):**
   ```bash
   go test -race ./internal/executor/ -run TestMultiHashJoin -count=1
   go test -race ./internal/executor/ -run TestSharedRow -count=1
   ```
3. **Profile comparison:** Q9 `cloneRowOwned` + `VirtualSlot.Row` cum CPU
   before and after.

## 8. Related improvements

- [01-spill-writer-stack-elimination.md](01-spill-writer-stack-elimination.md) — the primary fix that makes Fix 05 potentially unnecessary for the initial optimization pass.
- [04-double-clone-elimination.md](04-double-clone-elimination.md) — eliminates the hash-build clone redundancy, which is a larger allocation source than the probe-phase clone.
- M0071-0014 VirtualSlot composition — the precedent that already eliminated per-match copies in the MHJ probe loop; Fix 05 extends this to downstream operators.
