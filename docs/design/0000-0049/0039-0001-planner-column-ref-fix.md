# 0039-0001 — Planner Column-Index Alignment Fix

**Status:** draft
**Parent milestone:** M0039
**Date:** 2026-05-03

## 1. Objective

Fix three ColumnRef-index alignment bugs in the planner pipeline so that:
- All TPC‑H queries return correct row counts (0/22 return 0 rows, currently 9).
- The `MultiHashJoin` operator resolves all N join keys (4/4 for Q2, currently 3/4).
- The memory‑reduction targets of M0036‑M0038 can be measured and verified.

## 2. Background

The planner constructs physical plan trees (`Join`, `Sort`, `Aggregate`,
`Project` nodes) where each operator carries `ColumnRef` nodes with an
`Index` field. The executor resolves `ColumnRef.Index` by direct array access:

```go
// executor/expr.go
case *planner.ColumnRef:
    return row[col.Index], nil
```

Therefore `Index` must be within the row's schema width **and** point to
the logical column that the planner intended. When it does not, the executor
either accesses a wrong-typed Datum (triggering `compareDatum` errors, now
soft‑fallen‑back in M0038‑0003) or compares the correct type but against the
wrong column value, silently producing wrong results (0 matching rows).

Three root causes have been identified. Sections 3‑5 describe each.

## 3. Fix A: `pushPredicatesIntoCrossJoins` — global → local remap

### Current behaviour

`pushPredicatesIntoCrossJoins` (`planner.go`, called at line 348) walks a
left‑deep CROSS‑join tree and promotes CROSS joins to hash joins. For each
CROSS join, `pushOneConjunct` (`pushdown.go:54`) tries to push a WHERE‑clause
conjunct that spans both children. When successful, it calls:

```go
if lk, rk, okSplit := splitEqualityForHash(c, leftWidth); okSplit {
    j.LeftKey = lk
    j.RightKey = rk
    j.Algo = JoinAlgoHash
}
```

`splitEqualityForHash` (`planner.go:947`) returns the predicate's left/right
ColumnRef nodes **unchanged**. These ColumnRefs carry indices relative to the
**global FROM‑clause schema** (e.g. `ps_suppkey idx=6` in a 5‑table scope).
But the Join's schema is the **local concatenation** of its two children's
outputs (e.g. 4 cols for a 2‑table join). Index 6 is out of range for a
4‑column row.

### Fix

After obtaining `lk` and `rk` from `splitEqualityForHash`, remap each
`ColumnRef.Index` from global scope to the Join's local output schema.

Given the Join has local schema `leftSchema + rightSchema` with left width W:

- **LeftKey remap**: The global index falls in the left child's columns.
  Compute `localIdx = globalIdx - leftChild.globalOffset`. This is the
  column index within the left child's output. Add 0 (already in the
  left part of the Join output).

- **RightKey remap**: The global index falls in the right child's columns.
  Compute `localIdx = globalIdx - rightChild.globalOffset`. Then add W
  (left‑width shift) so the key indexes into the full Join output row.
  This matches the executor's `concatRows(nullRow(W), rightRow)` usage.

`globalOffset` for each child is computed from the cumulative scan‑width
table of the FROM clause (the same `cumOffsets` used by
`tableForCol` / `scanForCol`).

### Affected files

- `internal/planner/pushdown.go` — `pushOneConjunct`
- `internal/planner/planner.go` — `splitEqualityForHash` or a new remap
  helper

## 4. Fix B: Unnest‑pass ColumnRef alignment

### Current behaviour

`unnestSubqueriesInPlan` (`unnest.go`) walks the plan tree, identifies
correlated scalar subqueries, and rewrites them as `Join(Aggregate(...),
...)` nodes. The new nodes contain ColumnRefs that reference the
**pre‑unnest** plan shape.

After unnest completes, the bushy DP (`tryBushyDP`) and chain detection
(`rewriteMultiWayChain`) may **replace** parts of the plan tree (the join
subtree) with a different structure (bushy‑optimised or MultiHashJoin).
The ColumnRefs in the unnest‑generated `Aggregate` / parent `Join` still
reference the old tree's schema order. When the MultiHashJoin replaces a
binary‑join chain, its output column order is **scanner DFS pre‑order**,
which differs from the original left‑deep order.

### Fix

After bushy DP rewrites the tree and after chain detection replaces
subtrees, walk the **parent expressions** of the rewritten subtree
(e.g. the Join keys, Filter predicates, Sort keys, Project expressions)
and remap any `ColumnRef` that indexed into the old subtree to the new
subtree's `Output()` schema.

The mapping function:

1. Determine which **base table** the ColumnRef's pre‑rewrite position
   belonged to (using the old subtree's scan list and widths).
2. Find that table's position in the **new** subtree's output schema
   (the same scan, possibly at a different global offset).
3. Update `ColumnRef.Index` to the new position.

If the rewritten subtree is a `MultiHashJoin`, use `plan.Tables[k].Output()`
to compute per‑table widths.

### Affected files

- `internal/planner/unnest.go` — add a post‑rewrite remap pass
- `internal/planner/bushy.go` — `rewriteMultiWayChain` may invoke the remap

## 5. Fix C: `buildJoinFromDP` residual offset bug

### Current behaviour

`remapKeyToSubset` (`bushy.go:473`) has been fixed (M0038‑0001) so that
the global column offset `offset` increments for **all** tables, not just
subset members. The fix was verified to produce correct subset‑local
indices in unit tests. However, the Q2 E2E test showed the part‑partsupp
edge still receiving indices 9/9 instead of the expected 3/4. This
indicates a second bug.

**Hypothesis:** The edge in the join graph carries ColumnRefs whose
indices have already been altered by a prior call (e.g. from
`remapKeyToSubset` returning a **copy** pointer that is later re‑used as
an edge key). Investigation needed.

### Fix

1. Aud all code Paths that mutate `joinEdge.leftKey` / `joinEdge.rightKey`
   or pointers reachable from them.
2. If the edge ColumnRefs are being inadvertently aliased, clone them in
   `buildJoinGraph` so each DP invocation gets its own copy.
3. Re‑verify with the Q2 simplified plan: all 4 join keys must resolve
   to correct `MultiHashKey{LeftTable, LeftCol, RightTable, RightCol}`
   triples in `collectMultiHashTables`.

### Affected files

- `internal/planner/bushy.go` — `remapKeyToSubset`, `buildJoinGraph`,
  `buildJoinFromDP`, `collectMultiHashTables`

## 6. Verification

| Test | Expected Result |
|------|----------------|
| `TestTPCHResultParity` | 0 `goopg‑errored`, 0 `divergent row counts (0 vs >0)`, ≥ 20 identical |
| `TestMultiHashJoinInQ2Plan` | MultiHashJoin with **4** keys (not 3) |
| `TestBushyPlanWithUnnest` | PASS (already passing) |
| `TestBushyDPWithStats` | PASS (already passing) |
| `go test ./...` | No new failures |

### Integration test

Run the HammerDB SF=1 power test on a stable x86‑64 Linux machine (not
WSL2) after the fix lands. Verify:
- Q2 peak RSS ≤ 10 GB (M0038 target)
- All 22 queries return results with correct row counts
- Shared‑buffer pool size: 2048 MB (2 GiB), `GOMEMLIMIT=20GiB`

## 7. Implementation Order

1. **Fix A** (`pushPredicatesIntoCrossJoins` remap) — highest impact:
   the fallback path is taken for most multi‑table joins when the bushy DP
   has disconnected components or when stats are missing.

2. **Fix C** (`buildJoinFromDP` audit) — addresses the remaining
   single‑edge offset issue discovered in M0038 testing.

3. **Fix B** (unnest‑pass remap) — needed so unnest‑generated Join/Aggregate
   keys stay valid after bushy DP or chain detection rewrites their children.

## 8. Reference

- `internal/planner/bushy.go` — DP enumeration, `remapKeyToSubset`,
  `buildJoinFromDP`, chain detection
- `internal/planner/planner.go` — `pushPredicatesIntoCrossJoins`,
  `splitEqualityForHash`
- `internal/planner/pushdown.go` — `pushOneConjunct`, `classifyConjunctSide`
- `internal/planner/unnest.go` — subquery unnesting
- `analysis/0038-fix-compareDatum-cross-kind.md` — context on cross‑kind
  executor fallback
- `analysis/tpch-power-test-0038-report.md` — SF=1 power test results
