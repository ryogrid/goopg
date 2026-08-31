# 0031-0002 — Executor GC Leak Code Review

**Status:** draft  
**Parent milestone:** M0031  
**Date:** 2026-05-02

## 1. Objective

Audit every operator in `internal/executor/` for patterns where heap memory remains
**reachable** (and thus uncollectable by Go's GC) after the operator's `Close()` returns
or across re-`Open()` cycles. Identify concrete fix proposals, prioritized by estimated
heap impact for TPC-H Q2 at SF=1.

## 2. Operator Audit Summary

| Operator | File | Close() nil's buffers? | Severity | Q2 Impact |
|----------|------|----------------------|----------|-----------|
| **joinOp** | `operators_join_agg.go:399` | **No** | HIGH | Outer join tree holds ~15 MB of `o.rows` |
| **sortOp** | `operators.go:242` | **No** | HIGH | Outer sort holds ~160 KB; subquery sort holds more |
| **aggregateOp** | `operators_join_agg.go:649` | **No** | MEDIUM | Subquery aggregate retains output |
| **windowOp** | `operators_window.go:247` | **No** | MEDIUM | Not used in Q2, but same pattern |
| **lockRowsOp** | `operators_lockrows.go:338` | **No** | LOW | Not used in Q2 |
| **recursiveUnionOp** | `operators_recursive_cte.go:38` | **No** | MEDIUM | Not used in Q2, but monotonic growth |
| **indexScanOp** | `operators_index.go:99` | **YES** | — | **Good reference pattern** |
| **seqScanOp** | `operators_storage.go:100` | **YES** (pins) | — | Properly unpins buffer pages |
| **filterOp** | `operators.go:90` | N/A (streaming) | — | No accumulation |
| **projectOp** | `operators.go:58` | N/A (streaming) | — | No accumulation |
| **valuesOp** | `operators.go:25` | N/A (ref only) | — | Plan references only |
| **limitOp** | `operators.go:152` | N/A (streaming) | — | No accumulation |

## 3. Detailed Audit Per Operator

### 3.1 joinOp — HIGH

**File:** `internal/executor/operators_join_agg.go:20-408`

**Problem:** `Close()` calls `o.left.Close()` and `o.right.Close()` but does NOT nil
`o.rows`. The `o.rows` slice holds the entire join result (all joined rows produced in
`Open()`). For Q2's outer query hash joins, this is ~5 MB per join × 3 joins = ~15 MB
retained after the outer query completes.

**Additionally**, when a `joinOp` is re-`Open()`ed (e.g., for correlated subquery
re-execution), `o.rows` from the previous invocation is NOT explicitly freed — only
overwritten by the new `run*Join` calls. While append-based growth resets implicitly,
the old backing array may linger if capacity was large.

**Fix proposal:**
```go
func (o *joinOp) Close() error {
    o.rows = nil     // allow GC
    o.ctx = nil
    o.idx = 0
    errL := o.left.Close()
    errR := o.right.Close()
    if errL != nil {
        return errL
    }
    return errR
}
```

**Q2 impact:** Outer join retained rows (~15 MB) would be freed.

### 3.2 sortOp — HIGH

**File:** `internal/executor/operators.go:172-251`

**Problem:** `Close()` only calls `o.child.Close()`. `o.rows` holds the fully-buffered
child output (all rows, NOT copied — just the original Row references). For Q2's outer
sort, this is ~2,000 rows × 8 Datum ≈ 160 KB retained. For the subquery's sort, if
present, the rows would also be retained but are collectable via the operator's scope.

**More critically**: if `sortOp` is re-`Open()`ed (subquery re-execution), the old
`o.rows` is overwritten but the old backing array persists until GC. Additionally,
`o.idx` is not reset to 0 — but `Open()` always sets it to 0 implicitly because
`o.rows` is reassigned to `nil`-equivalent via `o.rows = o.rows[:0]` (append to nil
initial capacity) — actually, `Open()` does `o.rows = append(o.rows, row)` which on
a non-nil slice would append to existing capacity. But a new `sortOp` is constructed
per `Build()`, so the slice starts nil. Still, no explicit nil on Close.

**Fix proposal:**
```go
func (o *sortOp) Close() error {
    o.rows = nil
    o.idx = 0
    o.ctx = nil
    return o.child.Close()
}
```

### 3.3 aggregateOp — MEDIUM

**File:** `internal/executor/operators_join_agg.go:410-651`

**Problem:** `Close()` only calls `o.child.Close()`. The `o.rows` field holds aggregated
output rows. For the subquery's MIN aggregate (1 row × 1 Datum), this is negligible
(~100 bytes). However, `o.ctx` is also not nilled, keeping a reference to the Context
and thus to `ctx.OuterRows`.

**Fix proposal:**
```go
func (o *aggregateOp) Close() error {
    o.rows = nil
    o.ctx = nil
    o.idx = 0
    return o.child.Close()
}
```

### 3.4 windowOp — MEDIUM (not Q2-relevant)

**File:** `internal/executor/operators_window.go:1-248`

**Problem:** `Close()` only calls `o.child.Close()`. `o.rows` holds deep-copied child
rows. For window functions on large inputs, this can be significant. Additionally,
the sort comparator re-evaluates PARTITION BY and ORDER BY expressions on every
comparison (O(N log N) evaluations), causing allocation churn.

**Fix proposal:**
```go
func (o *windowOp) Close() error {
    o.rows = nil
    o.ctx = nil
    o.idx = 0
    return o.child.Close()
}
```

### 3.5 lockRowsOp — LOW

**File:** `internal/executor/operators_lockrows.go:338`

**Problem:** `Close()` only calls `o.child.Close()`. The `o.pending` slice holds entries
accumulated during `Open()`. For SELECT FOR UPDATE over a large table, this could be
significant. Not relevant to Q2.

**Fix proposal:** Add `o.pending = nil` in `Close()`.

### 3.6 recursiveUnionOp — MEDIUM

**File:** `internal/executor/operators_recursive_cte.go:11-43`

**Problem:** `Close()` only calls `o.anchor.Close()`. The `output` and `working` slices
accumulate ALL fixpoint rows across iterations. Neither is nilled. Additionally, the
recursive member operator is never closed (`o.recursive` is opened per-iteration in
`Next()` but its Close is not called). Not relevant to Q2.

**Fix proposal:**
```go
func (o *recursiveUnionOp) Close() error {
    o.output = nil
    o.working = nil
    o.ctx = nil
    if o.recursive != nil {
        _ = o.recursive.Close()
    }
    if o.anchor != nil {
        return o.anchor.Close()
    }
    return nil
}
```

### 3.7 Subquery execution path — CRITICAL for Q2

**File:** `internal/executor/expr.go:617-662`

**Pattern:** `subqueryImpl` creates a fresh operator tree via `Build()`, Opens it, and
defer-Closes it. The operator and all its data go out of scope when the function returns.

```go
func subqueryImpl(x *planner.SubqueryExpr, ctx *Context) (Datum, error) {
    op, err := Build(x.Plan)       // fresh operator tree
    op.Open(ctx)                     // drains children into operator buffers
    defer func() { _ = op.Close() }()  // closes but doesn't nil buffers
    row, err := op.Next()
    // ...
    return val, nil
    // op is local → unreachable after return → GC can collect
}
```

**Assessment:** With the current `Close()` implementations that **do not nil buffers**:
- `op` goes out of scope — the operator struct becomes unreachable.
- The operator struct holds a reference to `o.rows` (a slice).
- The slice backing array becomes unreachable via `op`.

**This actually works correctly with ideal GC** — when `op` is no longer reachable,
all its fields (including `o.rows`) are also unreachable. GC can collect everything.

**BUT** — there is a subtlety. The `ctx.OuterRows` slice is shared across all
subquery invocations:

```go
func evalSubquery(x *planner.SubqueryExpr, row Row, ctx *Context) (Datum, error) {
    ctx.OuterRows = append(ctx.OuterRows, row)
    defer func() { ctx.OuterRows = ctx.OuterRows[:len(ctx.OuterRows)-1] }()
    return subqueryImpl(x, ctx)
}
```

The `ctx.OuterRows` grows by 1 per invocation (push), then shrinks by 1 (pop via defer).
But the backing array NEVER shrinks. After 2,000 invocations, `ctx.OuterRows` has a
backing array of capacity ~2048 (doubling pattern). This is ~2,048 × pointer-size ≈ 16 KB.
Negligible. However, for deeply nested subqueries, multiple levels of `OuterRows` could
accumulate.

**Additionally**, the `ctx.Params`, `ctx.Vars`, and `ctx.WorkTableRows` fields are also
shared across subquery invocations on the same `ctx`. None of these are Q2-relevant.

### 3.8 Per-row/per-comparison allocation churn

These patterns create temporary allocations that are GC'd normally but increase GC
pressure and allocation rate:

| Location | Pattern | Frequency in Q2 | Est. per-invocation |
|----------|---------|----------------|-------------------|
| `operators.go:199` | `sortOp.Open()` — `append(o.rows, row)` (no copy) | 1× | 2,000 rows |
| `operators.go:202-237` | Sort comparator re-evaluates key expressions per comparison | O(N log N) | ~2,000 × log(2000) ≈ 22,000 comparisons × 4 keys = 88K evals |
| `operators_join_agg.go:138-150` | `runHashJoin` — `concatRows(leftPad, r)` per build row | 1× per build table | ~10K concats for supplier build |
| `operators_join_agg.go:663-664` | `drainRows` — `make(Row, len(row)); copy` per child row | 1× per child | All rows duplicated |
| `operators_join_agg.go:504` | `evalGroupKey` — `make(Row, ...)` per input row | Per subquery row | ~4 rows × few Datum |
| `operators.go:65` | `projectOp.Next()` — `make(Row, len(o.targets))` per emitted row | Per outer row | 2,000 × 8 Datum |
| `expr.go:47-48` | `evalSubquery` called per outer row | 2,000× | Each triggers Build+Open+Close cycle |

## 4. Cumulative Retained Memory After Query

### 4.1 Outer query operators (instantiated once)

| Operator | Retained field | Size estimate |
|----------|---------------|---------------|
| joinOp (part×partsupp) | `o.rows` | ~4 MB (2,000 rows × 14 Datum) |
| joinOp (+nation) | `o.rows` | ~5 MB (2,000 rows × 18 Datum) |
| joinOp (+region) | `o.rows` | ~6 MB (2,000 rows × 21 Datum) |
| sortOp | `o.rows` | ~160 KB (2,000 rows × 8 Datum) |
| filterOp, projectOp | N/A (streaming) | 0 |
| **Total retained** | | **~15 MB** |

This 15 MB is NOT freed until the outer execution completes and the `Operator` variable
from `Run()` / the portal goes out of scope. For a persistent backend, this memory lives
for the duration of the portal (simple query: until results are sent; extended query:
until portal is closed). In practice, the backend's `Run()` returns and then the operator
is not referenced, so GC collects it. **This is not a long-term leak for simple queries.**

### 4.2 Subquery operators (2,000 instantiations)

Each subquery invocation creates a fresh operator tree that become unreachable when
`subqueryImpl` returns. With ideal GC: **zero retained memory from subqueries after
the outer query completes.**

### 4.3 ctx.OuterRows

After all 2,000 subquery invocations complete, `ctx.OuterRows` has capacity ~2048:
~16 KB retained. Negligible.

## 5. Prioritized Fix Proposals

### Priority 1: Add ANALYZE after data load (eliminates CROSS joins)

**Impact:** Q2 becomes executable. Without this, the CROSS joins OOM immediately.

**Location:** `bench/tpch/build_schema_goopg.sh` — add `ANALYZE` statements after
data load. Or better: run ANALYZE on all TPC-H tables as part of the HammerDB build.

### Priority 2: Nil operator buffers in Close() (15+ MB reclaimed)

**Impact:** Ensures no operator retains data beyond its logical lifetime. For Q2, ~15 MB
of outer join/sort state is freed when the outer query's Run() completes.

**Files to change:**
- `internal/executor/operators_join_agg.go:399` — joinOp.Close()
- `internal/executor/operators.go:242` — sortOp.Close()
- `internal/executor/operators_join_agg.go:649` — aggregateOp.Close()
- `internal/executor/operators_window.go:247` — windowOp.Close()
- `internal/executor/operators_lockrows.go:338` — lockRowsOp.Close()
- `internal/executor/operators_recursive_cte.go:38` — recursiveUnionOp.Close()

### Priority 3: Cache subquery result / unnest subquery (20 GB allocation avoided)

**Impact:** Eliminates 2,000 re-executions of the subquery's join tree. Instead of
allocating ~10 MB per outer row, the subquery is executed once (~10 MB allocated,
GC'd after unnest). Q2 becomes executeable within 512 MiB.

**Approach A — unnesting (preferred):** In `planSubqueryExpr`, detect that the correlated
`p_partkey` can be lifted as a GROUP BY key in the outer query: turn the scalar subquery
into an extra join + GROUP BY. This matches PostgreSQL's subquery unnesting logic.

**Approach B — caching (simpler):** In `evalSubquery`, memoize the scalar result per
distinct correlated parameter value. Use a `map[string]Datum` keyed by the outer row's
correlated column values. The map is cleared when the outer query's Run() completes.

### Priority 4: Streaming hash join (build-side only buffered)

**Impact:** Eliminates the need to `drainRows` the probe side. For Q2's outer hash joins,
this reduces peak memory from `leftRows + rightRows + o.rows` to `buildRows + o.rows`
(~50% reduction). The probe side streams through without being fully buffered.

**File:** `internal/executor/operators_join_agg.go:40-75` — modify `joinOp.Open()` to
drain only the build side, and stream the probe side through the hash table.

## 6. Good Patterns

### indexScanOp — reference implementation

`internal/executor/operators_index.go:99-102`:
```go
func (o *indexScanOp) Close() error {
    o.rows = nil
    o.tids = nil
    return nil
}
```
This nils both buffers explicitly. All operators should follow this pattern.

### seqScanOp — proper buffer pool unpin

`internal/executor/operators_storage.go:100-106`:
```go
func (o *seqScanOp) Close() error {
    if o.pinned != nil {
        o.ctx.Pool.Unpin(o.pinned)
        o.pinned = nil
    }
    return nil
}
```
Buffer pool pages are properly unpinned.

## 7. Reference

- `internal/executor/operators_join_agg.go` — joinOp, aggregateOp, drainRows, concatRows, nullRow
- `internal/executor/operators.go` — sortOp, projectOp, filterOp, valuesOp
- `internal/executor/operators_window.go` — windowOp
- `internal/executor/operators_lockrows.go` — lockRowsOp
- `internal/executor/operators_recursive_cte.go` — recursiveUnionOp
- `internal/executor/operators_storage.go:100` — seqScanOp.Close() (good pattern)
- `internal/executor/operators_index.go:99` — indexScanOp.Close() (good pattern)
- `internal/executor/expr.go:617-662` — evalSubquery / subqueryImpl
- `internal/executor/context.go:53-59` — OuterRows field
