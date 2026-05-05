# 0039-0002 — Range Index Scan (B-tree range predicates)

**Status:** accepted
**Parent milestone:** M0039 follow-up / M0044 extension
**Date:** 2026-05-04

## 1. Objective

Enable the planner to emit `Filter(IndexScan)` for range predicates
(`<`, `<=`, `>`, `>=`, `BETWEEN`) on indexed columns, rather than
always falling back to `Filter(SeqScan)`.

Before this change:
- `WHERE col = key` → `IndexScan` (equality, exact probe)
- `WHERE col >= lo AND col < hi` → `Filter(SeqScan)` — **O(n)**

After this change:
- `WHERE col >= lo AND col < hi` → `Filter(IndexScan{LowKey=lo, HighKey=hi})` — **O(log n + k)**

Primary targets: TPC-H queries with date-range filters on `l_shipdate`
(Q6, Q14, Q15) and `o_orderdate` (Q3, Q4, Q5, Q10, Q12).

## 2. Architecture

```
Filter(predicate = full WHERE clause)
  └─ IndexScan(Index=idx, LowKey=<lo expr>, HighKey=<hi expr>)
```

The `IndexScan` narrows candidates to the key range `[lo, hi]` using the
B-tree's existing `RangeScan(lo, hi, fn)` API. The `Filter` re-evaluates
the full WHERE predicate for exact filtering (handles strict inequalities
`>` / `<` and any non-index conditions).

The equality case (`col = key`) is unchanged: returns a bare `IndexScan`
with no Filter wrapping.

## 3. B-tree `RangeScan` nil-bound support

`BTree.RangeScan(lo, hi, fn)` is updated to support nil bounds:

- **nil lo**: scan from the leftmost leaf (no lower limit). `descendToLeaf(nil)`
  already descends to the leftmost leaf; the `CompareKeys(it.key, nil) < 0`
  guard in the item-scan loop is guarded by `lo != nil` so nothing is skipped.
- **nil hi**: scan to the rightmost leaf (no upper limit). The
  `CompareKeys(it.key, hi) > 0` stop condition is guarded by `hi != nil`.

The page-level recovery skip (`keyExceedsHighKey`) is similarly guarded:
```go
if lo != nil && keyExceedsHighKey(op, lo) { ... }
```

## 4. Planner changes

### 4.1 `IndexScan` plan node

Two new fields added to `planner.IndexScan`:

```go
LowKey  Expr  // inclusive lower bound; nil = no lower bound
HighKey Expr  // inclusive upper bound; nil = no upper bound
```

`Key` is non-nil for equality scans (backward compat). For range scans,
`Key == nil` and at least one of `LowKey` / `HighKey` is non-nil.

### 4.2 New helpers

`collectAndConjuncts(e parser.Expr) []parser.Expr`
: Walks an AND chain and returns the leaf conjuncts.

`isConstantExpr(e planner.Expr) bool`
: Returns true iff the resolved expression tree has no `ColumnRef` or
  `OuterColumnRef`. Allows date arithmetic expressions like
  `date '1994-01-01' + interval '1 year'` as range keys (evaluated by
  `evalExpr` at Open-time with a nil row context).

`flipRangeOp(op string) string`
: Normalises `key op col` to `col flipOp key` (e.g. `key > col` → `col < key`).

`tryRangeIndexScan(...) (Node, bool, error)`
: Walks AND conjuncts, collects range predicates on the first B-tree-indexed
  column found, builds `IndexScan{LowKey, HighKey}`, and wraps it with
  `Filter(resolvedWhere)`.

### 4.3 `planIndexScanFromWhere` flow

```
tryEqualityIndexScan(where) → IndexScan (no Filter)    [existing]
   ↓ fail
tryRangeIndexScan(where) → Filter(IndexScan)            [new]
   ↓ fail
(nil, false, nil) → caller falls back to Filter(SeqScan)
```

### 4.4 `unnest.go` / plan tree walkers

`walkPlanExprs` and `clonePlanReplacingOuter` updated to handle `LowKey`
and `HighKey` on `IndexScan` nodes.

## 5. Executor changes

`indexScanOp.Open()` branches on `plan.Key != nil`:

- **Equality (Key != nil)**: existing `lookupKey()` + `RangeScan(key, key, fn)`.
- **Range (Key == nil)**: new `lookupRangeBounds()` evaluates `LowKey` / `HighKey`
  (nil → nil encoded key = unbounded), then calls `RangeScan(lo, hi, fn)`.

`indexScanPredicate()` in `operators_storage.go` returns nil for range scans
(`Key == nil`), causing `updateOp` / `deleteOp` to fall back to seq-scan
(safe; correctness unaffected).

## 6. Key-expression flexibility

The range key expression can be any constant expression — not just typed
literals. This includes:
- `timestamp '1994-01-01'` (`TypedStringLit`)
- `date '1994-01-01' + interval '1 year'` (`BinaryOp(TypedStringLit, IntervalLit)`)
- `$1` (`ParamRef` — parameterized queries)

`evalExpr(key, nil, ctx)` evaluates these without a row context at Open-time.

## 7. Correctness

The B-tree uses INCLUSIVE bounds (`lo ≤ key ≤ hi`). Strict predicates
(`>` and `<`) cause the index to include the boundary row, which the
`Filter` layer removes. This is correct for all cases:

| Predicate       | RangeScan bounds          | Filter removes   |
|-----------------|---------------------------|------------------|
| `col >= lo`     | `[lo, nil]`               | nothing extra    |
| `col > lo`      | `[lo, nil]` inclusive     | `col == lo` rows |
| `col <= hi`     | `[nil, hi]`               | nothing extra    |
| `col < hi`      | `[nil, hi]` inclusive     | `col == hi` rows |
| `col BETWEEN a AND b` | `[a, b]` (desugared by parser to `>= AND <=`) | nothing extra |

## 8. Verification

- 4 planner unit tests in `internal/planner/range_index_scan_test.go`:
  single lower bound, two-sided range, fallback-no-index, varchar range.
- 3 executor integration tests in `internal/executor/range_index_scan_test.go`:
  lower-bound only, two-sided range with exclusive hi, count-matches-seq-scan.
- `TestTPCHResultParity` identical=22 divergent=0 errored=0 — PASS.
- `TestRunTPCHQueriesAgainstSyntheticData` 22/22 PASS.

## 9. Out of scope

- Range index scans inside multi-table join plans (requires predicate
  pushdown into the inner side of nested-loop joins) — deferred.
- `LIKE 'foo%'` prefix range rewrites — deferred.
- UPDATE / DELETE with range index scan acceleration — deferred.
