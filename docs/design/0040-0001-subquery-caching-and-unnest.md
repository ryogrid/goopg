# 0040-0001 — Subquery Result Caching and IN‑Subquery Unnest

**Status:** draft
**Parent milestone:** M0040
**Date:** 2026-05-03

## 1. Objective

Eliminate the per‑outer‑row re‑execution of correlated subqueries in goopg v0
via two complementary mechanisms:

1. **Executor‑level caching** — Materialise correlated subquery results keyed
   on outer‑column values so each distinct outer value triggers at most one
   inner‑plan execution.

2. **Planner‑level unnest** — Extend the M0033 subquery‑unnest pass to
   recognise `IN (subquery)` expressions and rewrite them as hash semi‑joins,
   removing the nested‑loop evaluation entirely.

## 2. Background

### 2.1 Current behaviour (v0, pre‑M0040)

Every correlated subquery is executed **per outer row**:

```go
// executor/expr.go:641‑677 — collectInValues
func collectInValues(x *planner.InExpr, row Row, ctx *Context) ([]Datum, error) {
    ctx.OuterRows = append(ctx.OuterRows, row)
    defer func() { ctx.OuterRows = ctx.OuterRows[:len(ctx.OuterRows)-1] }()
    op, err := Build(x.Plan)          // RE-BUILD per call
    op.Open(ctx)                       // RE-SCAN from block 0
    for { r, _ := op.Next(); drain }
    op.Close()
}
```

The same pattern holds for `evalSubquery` (lines 720‑755). For TPC‑H Q20
this means the `lineitem` SeqScan (4.4M rows) is re‑opened per partsupp row
(800K rows), yielding ~3.5 × 10¹² tuple probes.

### 2.2 Why the unnest pass (M0033) does not help

`findSubqueryInExpr` (unnest.go:48‑87) only visits `*planner.SubqueryExpr`:

```go
if s, ok := e.(*SubqueryExpr); ok { return s }
```

`InExpr` (`column IN (subquery)`) is simply never found.  The existing
`canUnnestSubquery` / `unnestSubquery` pipeline has all the infrastructure
needed (semi‑join creation, `clonePlanReplacingOuter` for correlated‑column
replacement), but the entry point does not reach `InExpr`.

## 3. Design: Subquery Result Caching (M0040‑0001)

### 3.1 Cache key

The cache is keyed by a hash of the **outer‑row column value(s)** that the
subquery depends on.  For a correlated `IN` with one outer reference, the key
is `datumKey(outerRefValue)`.  For a multi‑column correlation (e.g. Q20's
`l_partkey = ps_partkey AND l_suppkey = ps_suppkey`), the key is a
concatenation of both.

### 3.2 Cache storage

Add a cache to the subquery evaluation path.  The cleanest place is a
per‑query map attached to the plan's `SubqueryExpr` / `InExpr` node, or to
the `Context`.  Choosing the `Context` avoids modifying the planner types.

```go
// In Context (executor/operator.go or a new file)
type subqueryCacheEntry struct {
    values []Datum     // for IN subquery
    scalar Datum       // for scalar subquery
    err    error
}

type Context struct {
    ...
    // subqueryCache maps subquery-id + outer-key → result.
    // Cleared when OuterRows stack depth changes.
    subqueryCache map[subcacheKey]subqueryCacheEntry
}

type subcacheKey struct {
    planID uintptr  // identity of the SubqueryExpr / InExpr node
    key    string   // datumKey(outerColumnValue) or composite
}
```

### 3.3 Cache in `collectInValues`

```go
func collectInValues(x *planner.InExpr, row Row, ctx *Context) ([]Datum, error) {
    // 1. Build cache key from correlated outer refs
    //    (collect OuterColumnRefs from the inner WHERE)
    cacheKey := buildSubqueryCacheKey(x, row)
    if entry, ok := ctx.subqueryCache[cacheKey]; ok {
        return entry.values, entry.err
    }

    // 2. Execute inner plan (unchanged from current code)
    ctx.OuterRows = append(ctx.OuterRows, row)
    defer func() { ctx.OuterRows = ctx.OuterRows[:len(ctx.OuterRows)-1] }()
    op, err := Build(x.Plan)
    if err != nil { ... }
    op.Open(ctx)
    for { r, _ := op.Next(); collect }

    // 3. Store result
    ctx.subqueryCache[cacheKey] = subqueryCacheEntry{values: out, err: nil}
    return out, nil
}
```

### 3.4 Cache in `evalSubquery`

Same pattern — the scalar result is cached per outer‑key.

### 3.5 Cache invalidation

When the `OuterRows` stack changes depth (entering / leaving a subquery
scope), all cache entries from the level‑1 scope are invalidated.  This
can be tracked by the `OuterRows` length:

```go
type subqueryCacheEntry struct {
    values    []Datum
    scalar    Datum
    err       error
    scopeLen  int   // OuterRows length when cached
}
```

When `len(ctx.OuterRows) != entry.scopeLen`, the entry is stale.

### 3.6 Collecting outer refs for the cache key

An `InExpr`'s inner plan may reference `OuterColumnRef` nodes in its
Filter predicates.  These are the correlated columns.  The cache key is
built from the actual values of those columns in the **current** outer
row:

```go
func buildSubqueryCacheKey(x *planner.InExpr, row Row) subcacheKey {
    // Collect OuterColumnRefs from the inner plan's WHERE clause
    // Each OuterColumnRef has an Index in the outer row
    // Build datumKey(row[ref.Index]) for each ref
    // Combine into a single string key
}
```

## 4. Design: IN‑Subquery Unnest (M0040‑0002)

### 4.1 Detection

Extend `findSubqueryInExpr` (unnest.go:48) to also recognise `InExpr`:

```go
func findSubqueryInExpr(e Expr, target *SubqueryExpr) *SubqueryExpr {
    // … existing BinaryOp/UnaryOp/… walk …
    case *InExpr:
        if x.Plan != nil {
            // Return a marker that we can process
            return nil // or extend the return type to handle InExpr
        }
}
```

Alternatively, add a new function `findInExprInExpr` that mirrors
`findSubqueryInExpr` but looks for `*planner.InExpr`.

### 4.2 Unnestability check

An `IN (subquery)` can be unnested when its inner plan is a simple
`SELECT col FROM table WHERE col = outer_ref`:

- The inner plan must be a `Project(SeqScan)` with a `Filter` that
  contains one or more equijoin pairs (`OuterColumnRef = ColumnRef`).
- All other filters in the inner plan must reference only the inner
  table (single‑side).
- No aggregates, no GROUP BY.

### 4.3 Rewrite

The rewrite creates a **semi‑join** (`JoinTypeSemi`):

```
Before: Filter(s_suppkey IN (subquery), Join(supplier, nation))
After:  JoinTypeSemi(
            Join(supplier, nation),
            Project(SeqScan(partsupp)) (with dedup on ps_suppkey),
            Predicate: s_suppkey = ps_suppkey
        )
```

The `JoinTypeSemi` operator already exists in `planner.JoinTypeSemi`.  The
executor's hash join handles it by building a deduplicated set of the inner
side and probing.

### 4.4 Implementation steps

1. **Extend `findSubqueryInExpr`** to return an interface (or use a
   separate function) that can hold either `*SubqueryExpr` or
   `*InExpr`.

2. **Add `canUnnestInExpr`** with a relaxed precondition (no aggregate
   required — plain `SELECT col FROM table` is fine).

3. **Add `unnestInExpr`** that:
   - Collects `unnestParam` pairs from the inner WHERE clause
   - Creates the semi‑join plan tree using
     `clonePlanReplacingOuter` (same as M0033)
   - Returns the rewritten node

4. **Wire into `unnestSubqueriesInPlan`**: after processing
   `SubqueryExpr`, also process `InExpr` in the same filter.

### 4.5 Complexity

| Component | Files touched | Est. lines |
|-----------|--------------|------------|
| `findSubqueryInExpr` extension | `unnest.go` | ~15 |
| `canUnnestInExpr` | `unnest.go` | ~30 |
| `unnestInExpr` (semi‑join construction) | `unnest.go` | ~60 |
| Wiring in `unnestSubqueriesInPlan` | `unnest.go` | ~10 |
| **Total** | | **~115** |

## 5. Verification

### 5.1 Unit tests

| Test | Verifies |
|------|----------|
| `TestCacheInSubquery` | Correlated `IN` subquery evaluated at most once per outer value |
| `TestCacheScalarSubquery` | Correlated scalar subquery cached |
| `TestCacheInvalidation` | Cache cleared when outer scope changes |
| `TestUnnestInExpr` | Simple `IN (SELECT … WHERE col = outer.col)` rewritten to semi‑join |
| `TestUnnestInExprRejectNoEquijoin` | Non‑equijoin `IN` subquery left uncached/unnested |

### 5.2 Integration tests

| Test | Expected |
|------|----------|
| `TestRunTPCHQueriesAgainstSyntheticData` | 22/22 PASS (no regression) |
| `TestTPCHResultParity` | identical ≥ 13, errored = 0 |
| HammerDB SF=1 power test | Q20 ≤ 120 s |

## 6. Reference

- `internal/executor/expr.go:601‑677` — `evalInExpr`, `collectInValues`
- `internal/executor/expr.go:720‑755` — `evalSubquery`
- `internal/planner/unnest.go:45‑87` — `findSubqueryInExpr`
- `internal/planner/unnest.go:91‑114` — `canUnnestSubquery`
- `internal/planner/unnest.go:307‑443` — `clonePlanReplacingOuter`
- `internal/executor/operators_join_agg.go` — hash join, semi‑join
- `internal/planner/plan.go:106‑115` — `InExpr`
- `analysis/tpch-q20-bottleneck-analysis.md` — Q20 complexity analysis
