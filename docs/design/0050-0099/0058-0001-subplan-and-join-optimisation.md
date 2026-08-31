# Design Doc 0058-0001 — SubPlan Cache, JOIN Unnesting, NUMERIC Fast Path, TCP Cancel

**Status:** draft
**Milestone:** 0058 — TPC-H SubPlan & Join-Unnesting Performance Fixes
**Author:** Ryo Kanbayashi
**Date:** 2026-05-06

## 1. Purpose

This document specifies the implementation approach for the five
performance and correctness gaps identified during the M0054-0007
emulate run (2026-05-06). The gaps are:

1. Non-correlated SubPlan evaluated once per outer row (constant result cached incorrectly)
2. EXISTS/NOT EXISTS not unnested to semi-join/anti-join
3. NUMERIC decode allocates `*big.Int` even for integer-valued columns
4. Q19 OR-of-ANDs join predicate falls back to CROSS JOIN
5. TCP disconnect does not cancel the in-flight query

---

## 2. Gap 1 — Non-correlated SubPlan constant-key cache

### 2.1 Root cause

`SubqueryCache` (`internal/executor/context.go`) maps an outer-row
key to a cached result. The key is derived from all columns of the
current outer row. For a non-correlated subquery (one with zero
`OuterColumnRef` nodes), the subquery result is identical for every
outer row, but the cache key changes with every new row — so every
evaluation is a cache miss.

### 2.2 Fix

**Planning time:** Add a field `IsNonCorrelated bool` to `SubqueryPlan`
(or the relevant plan node). Set it to `true` when the subquery's
expression tree contains zero `OuterColumnRef` nodes. This can be
determined during `planSubquery()` in `internal/planner/planner.go`.

**Execution time:** In `evalSubquery()` / `collectInValues()` /
`evalExistsExpr()` in `internal/executor/expr.go`, when
`IsNonCorrelated` is true, use a fixed cache key (e.g., the empty
string `""`) instead of serialising the outer row. This guarantees a
cache hit on every call after the first.

**Cache eviction:** The fixed key must not persist across query
boundaries. The `SubqueryCache` is already per-query (allocated in
`Context`), so no change needed.

### 2.3 Affected queries

| Query | Subquery type | Outer rows | Speedup expected |
|-------|-------------|-----------|-----------------|
| Q11 | HAVING scalar | ~8 K groups | ~8 K× (54 min → < 1 s) |
| Q18 | IN (GROUP BY lineitem) | ~6 M rows | ~6 M× + stops RSS growth |
| Q22 | scalar avg in Filter | ~150 K rows | ~150 K× |

### 2.4 Files

- `internal/executor/context.go` — `SubqueryCache`, cache-key logic
- `internal/executor/expr.go` — `evalSubquery`, `collectInValues`, `evalExistsExpr`
- `internal/planner/planner.go` — set `IsNonCorrelated` during plan build
- Relevant plan node struct (add `IsNonCorrelated bool` field)

---

## 3. Gap 2 — EXISTS/NOT EXISTS → semi-join/anti-join

### 3.1 Root cause

The planner's subquery-unnesting pass (`internal/planner/planner.go`,
M0040 extension) handles scalar and IN subqueries but does not convert
`EXISTS(subq)` to a semi-join or `NOT EXISTS(subq)` to an anti-join.
The executor evaluates them as SubPlans, calling Open/Next/Close on the
inner plan once per outer row.

### 3.2 Fix

Extend the unnesting pass to recognise the pattern:

```
Filter: EXISTS(SELECT 1 FROM inner WHERE outer.key = inner.key)
```

and rewrite it to:

```
SemiJoin(outer, inner, ON outer.key = inner.key)
```

Similarly for NOT EXISTS → AntiJoin.

**Conditions for safe unnesting:**
- The EXISTS subquery must be a `SELECT … FROM single_table WHERE corr_pred`.
- The correlation predicate must be an equality on a key column of the
  inner table (for index utilisation).
- No LIMIT, DISTINCT, aggregation, or GROUP BY in the subquery body.
- The subquery may reference outer columns only in the WHERE predicate.

**Operator addition:** If no `SemiJoinOp` / `AntiJoinOp` exists,
implement them as thin wrappers over `HashJoinOp` (build the inner
side into a hash table keyed on the join key; for each outer row, probe
the table; for semi-join emit the outer row on first hit; for anti-join
emit the outer row only if the probe misses).

### 3.3 Affected queries

| Query | Type | Inner table | Outer rows |
|-------|------|-------------|-----------|
| Q4 | EXISTS | lineitem (idx_lineitem_orderkey) | 1.5 M orders |
| Q21 | EXISTS + NOT EXISTS | lineitem | 6 M lineitem |

### 3.4 Files

- `internal/planner/planner.go` — unnesting pass
- `internal/executor/operators_join_agg.go` — SemiJoinOp / AntiJoinOp (new or extended)
- `internal/executor/expr.go` — remove SubPlan eval path for unnested EXISTS

---

## 4. Gap 3 — NUMERIC int64 fast path

### 4.1 Root cause

`parseNumeric()` in the backend decode path (`internal/pgproto/` or
`internal/executor/`) unconditionally allocates a `*big.Int` even for
small integer values. At 400 ns/column and 16 columns per lineitem row,
this costs ~6.4 µs/row → 38.4 s for a 6 M-row SeqScan.

### 4.2 Fix

In `parseNumeric()`:

1. Check the NUMERIC wire-format header: if `ndigits ≤ 4` and
   `dscale == 0` (integer value, ≤ 9 999 decimal digits of magnitude),
   decode directly into `int64` using integer arithmetic.
2. Return the result as `pgtype.Numeric{Int: &big.Int{}, Exp: 0}` with
   the `big.Int` set via `SetInt64()` — or, if the planner already
   knows the column type is integer-valued (INT4/INT8 declared in
   pg_attribute), return a native `int64` and bypass `Numeric` entirely.
3. Fall back to the existing `*big.Int` path for large values or
   non-zero `dscale`.

**Alternative — column-type-aware decode:** The planner knows the
declared type of each column (`pg_attribute.atttypid`). Pass the
declared type to the tuple decoder; when `atttypid` is `INT4` or
`INT8`, use a direct integer decode instead of `parseNumeric`. This
eliminates the `big.Int` allocation entirely for the common case.

### 4.3 Expected gain

Fermi estimate: 400 ns → ~50 ns per integer NUMERIC column after the
fast path. Q17 baseline 70.4 s → ~10 s (assuming 80 % of cost is
decode). Actual gain depends on the column-type-aware path.

### 4.4 Files

- `internal/pgproto/types.go` or equivalent NUMERIC decode location
- `internal/executor/scan.go` (tuple decode path)

---

## 5. Gap 4 — OR-of-ANDs join condition extraction (Q19)

### 5.1 Root cause

Q19's WHERE clause has the form:

```sql
WHERE (p_partkey = l_partkey AND <cond1>)
   OR (p_partkey = l_partkey AND <cond2>)
   OR (p_partkey = l_partkey AND <cond3>)
```

The planner's join-condition extractor looks for top-level AND-ed
equalities; it does not descend into OR branches to find the common
factor `p_partkey = l_partkey`. This causes the planner to emit a
`Nested Loop (CROSS)` with an upper Filter.

### 5.2 Fix

In the join-condition extraction step of the planner, detect the
OR-of-ANDs pattern where every OR branch contains the same equijoin
equality. Extract the common factor as the join key; leave the full
OR predicate as a post-join filter.

Specifically:
1. When the WHERE clause is a disjunction (`OR` node), collect all
   conjuncts from each OR branch.
2. Find equalities of the form `t1.col = t2.col` that appear in
   **every** OR branch.
3. Promote those equalities to join predicates; keep the full OR as a
   residual filter applied after the join.

This converts Q19's plan from `CROSS JOIN + Filter` to
`Hash Join (l_partkey = p_partkey) + Filter`.

### 5.3 Files

- `internal/planner/planner.go` — join-condition extraction

---

## 6. Gap 5 — TCP disconnect → immediate queryCtx.Cancel()

### 6.1 Root cause

The server's client goroutine (`internal/server/server.go`) reads
the next frontend message in a blocking loop. When the client
disconnects (EOF on the TCP socket) or sends a CancelRequest, the
goroutine discovers this only at the next `conn.Read()` call —
**after** the current query finishes or the next network write
attempts to flush. This means a hanging query holds 100+ % CPU until
it completes or produces output.

### 6.2 Fix

Implement a cancel-on-disconnect mechanism:

1. At query start, store `queryCtx, queryCancel` in the per-connection
   state alongside the existing cancel registry entry.
2. Start a goroutine that does a non-blocking poll on the connection
   socket (via `conn.SetReadDeadline(now) + conn.Read()` in a loop, or
   `net.Conn` with a separate "watch" goroutine reading a 1-byte peek).
   When `io.EOF` or a CancelRequest message is detected, call
   `queryCancel()`.
3. The query loop (`SubPlan` eval, hash-join build, SeqScan iterator)
   already checks `ctx.Err()` at key points (M0054 commits `a216093`,
   `f0b1c2c`). The cancel propagates through those checks within ≤ 1 ms.

**Alternative (simpler):** use `net.Conn.SetReadDeadline()` on the
query goroutine's read path. When a `DeadlineExceeded` error fires,
check whether the context has been cancelled; if not, reset the
deadline and retry. This avoids a separate goroutine at the cost of
slightly more complex error handling.

### 6.3 Files

- `internal/server/server.go` — client loop, cancel wiring

---

## 7. Gap 6 — WaitEventEnd hooks (observability)

### 7.1 Root cause

`open.go` has 14 `WaitEventStart` call sites but only 8 have
matching `WaitEventEnd`. The 6 unclosed I/O paths (DataFileRead/Write/
Extend/Sync, WALWrite, WALSync, BufferPin) always show empty
`wait_event` in `pg_stat_activity`, making it impossible to
distinguish CPU-bound from I/O-bound queries from the outside.

### 7.2 Fix

Audit `internal/storage/open.go` for every `WaitEventStart` call;
add a deferred `WaitEventEnd` immediately after each start. Use Go
`defer` to ensure the end is always called even on error return paths.

### 7.3 Files

- `internal/storage/open.go` — add `defer WaitEventEnd()` after each
  `WaitEventStart` call site that currently lacks one.

---

## 8. Acceptance criteria summary

| Sub-task | Key acceptance criterion |
|----------|--------------------------|
| M0058-0001 | Q11 completes in < 120 s; Q18 completes without OOM |
| M0058-0002 | Q4 and Q21 each complete in < 60 s |
| M0058-0003 | Q17 elapsed ≤ 40 s (≥ 43 % reduction from 70.4 s baseline) |
| M0058-0004 | Q19 EXPLAIN shows `Hash Join` on l_partkey=p_partkey |
| M0058-0005 | After CancelRequest, goopg CPU drops to < 5 % within 5 s |
| M0058-0006 | `pg_stat_activity.wait_event` non-null during active I/O |
| M0058-0007 | Re-run report documents new elapsed times for Q4/Q11/Q18/Q19 |
