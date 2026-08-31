# M0097-0094 — Correlated IN Subquery: Semi-Join Fix

## Problem

Correlated IN subqueries of the form
```sql
SELECT f1, f2 FROM t outer WHERE f1 IN (SELECT f2 FROM t WHERE f1 = outer.f1)
```
returned 0 rows instead of the correct result.

## Root Cause (Two-Layer Bug)

### Layer 1: Wrong key name in innerKey → `reresolveJoinByName` corruption

`unnestInExpr` constructed the join's `innerKey` with:
```go
innerKey := &ColumnRef{
    Index: outerWidth,
    Name:  params[0].SubCol.Name,  // equijoin column name, e.g. "f1"
    ...
}
```

The `SubCol` is the equijoin column inside the inner plan (e.g. `inner.f1` from `WHERE inner.f1 = outer.f1`).  But the inner plan projects a **different** column (e.g. `f2`), so `innerKey.Name = "f1"` while the inner plan output contains `"f2"`.

After unnesting, `reresolveJoinByName.predRebind` re-binds ColumnRef indices by name.  For `innerKey = ColumnRef{Index:3, Name:"f1"}`:
- Tries right schema (`[f2]`) for "f1" → not found
- Falls back to left schema (`[f1,f2,f3]`) for "f1" → found at index 0!
- Sets `innerKey.Index = 0`

Because `innerKey` is the **same pointer** used as `join.RightKey`, the hash-join build then hashes `keyRow[0]` = null (the left-side padding region), making every hash-table entry key `"n"` (null).  Probe keys are `datumKey(outer.f1) = "m:1:0"` etc., which never match `"n"` → 0 rows.

### Layer 2: JoinTypeInner instead of JoinTypeSemi → duplicate rows

Even after fixing Layer 1, using `JoinTypeInner` produced duplicate outer rows (one per matching inner row) instead of the correct semi-join "at most one match per outer row" semantics.

## Fix

**Layer 1** (`internal/planner/unnest.go`, `unnestInExpr`): set `innerKey.Name` from the inner plan's actual output column (first output column of `innerPlan.Output()`), not from `SubCol.Name`.  This ensures `predRebind` can find "f2" on the right side and leaves `innerKey.Index` at `outerWidth` (correct).

```go
innerOutName := params[0].SubCol.Name
if out := innerPlan.Output(); len(out) > 0 {
    innerOutName = out[0].Name
}
innerKey := &ColumnRef{Index: outerWidth, Name: innerOutName, ...}
```

**Layer 2**: change join type from `JoinTypeInner` to `JoinTypeSemi` (or `JoinTypeAnti` for NOT IN), and use outer-only schema — exactly matching the existing `unnestNonCorrelatedInExpr` pattern.  Also drop the IN conjunct from the filter predicate (the semi-join encodes the equality via `LeftKey`/`RightKey`) and mark the inner Project as `IsolatedScope` to prevent NLI rewriting.

## Impact

- `subselect` regress diff: 721 → 711 (-10 lines, improved)
- No regressions in previously-passing tests

## Tests

Covered by the `TestPort_RegressSuite/subselect` regress test (correlated IN queries now return correct row counts) and the broader planner/executor unit-test suites.
