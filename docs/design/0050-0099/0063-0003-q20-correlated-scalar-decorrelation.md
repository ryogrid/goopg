# Design 0063-0003 — Q20 correlated scalar subquery decorrelation

| field      | value |
| ---------- | ----- |
| status     | draft |
| date       | 2026-05-07 |
| milestone  | 0063 — TPC-H residual long-tail v2 |
| supersedes | — |

## 1. Problem

Q20 cancels at 600 s on TPC-H SF=1. M0062-0004 relaxed
`canUnnestInExpr` to admit nested IN, so Q20's outer two-level
IN now plans correctly. The remaining bottleneck is the
**innermost correlated scalar subquery** that gets re-evaluated
per `partsupp` row.

Q20 SQL (`internal/testutil/tpch/tpch.go:20`):

```sql
SELECT s_name, s_address FROM supplier, nation
 WHERE s_suppkey IN (
         SELECT ps_suppkey FROM partsupp
          WHERE ps_partkey IN (
                SELECT p_partkey FROM part WHERE p_name LIKE 'forest%'
          )
            AND ps_availqty > (
                SELECT 0.5 * SUM(l_quantity)
                  FROM lineitem
                 WHERE l_partkey  = ps_partkey
                   AND l_suppkey  = ps_suppkey
                   AND l_shipdate >= date '1994-01-01'
                   AND l_shipdate <  date '1994-01-01' + interval '1 year'
         ))
   AND s_nationkey = n_nationkey
   AND n_name = 'CANADA'
 ORDER BY s_name;
```

The innermost subquery,
`SELECT 0.5 * SUM(l_quantity) FROM lineitem WHERE l_partkey =
ps_partkey AND l_suppkey = ps_suppkey AND ...`, is correlated
on `ps_partkey` and `ps_suppkey`. With non-correlated SubPlan
caching from M0058-0001, the result is cached on (key, key) —
but the cache is keyed on the outer-row composite, so each
distinct `(ps_partkey, ps_suppkey)` pair gets its own SubPlan
execution. At ~80 K matched partsupp rows × ~ms per inner
SubPlan, the total is hundreds of seconds.

## 2. Hypothesis

The fix is **scalar-subquery decorrelation**: rewrite the inner
SubPlan into a hash-keyed aggregate over `lineitem` joined to
the partsupp probe stream. Specifically:

```
ps_availqty > (SELECT 0.5 * SUM(l_quantity)
                 FROM lineitem
                WHERE l_partkey = ps_partkey AND l_suppkey = ps_suppkey AND ...)
```

becomes:

```
ps_availqty > 0.5 * agg.sum_quantity
```

where `agg` is a pre-computed aggregate join:

```
LEFT JOIN (
  SELECT l_partkey, l_suppkey, SUM(l_quantity) AS sum_quantity
    FROM lineitem
   WHERE l_shipdate >= '1994-01-01' AND l_shipdate < '1995-01-01'
   GROUP BY l_partkey, l_suppkey
) agg
  ON agg.l_partkey = ps_partkey AND agg.l_suppkey = ps_suppkey
```

`SUM(...)` over an empty set returns NULL, and `ps_availqty > NULL`
is NULL → falsy. So semantically equivalent for the > check.

## 3. Critical code paths

| Path | File:line |
| ---- | --------- |
| Subquery unnesting entry | `internal/planner/unnest.go::unnestSubqueriesInPlan` |
| Scalar SubqueryExpr unnesting (existing for non-correlated) | `internal/planner/unnest.go::unnestSubquery` (line ~250-380) |
| `canUnnestSubquery` gate | `internal/planner/unnest.go::canUnnestSubquery` (line 152-195) |
| `collectUnnestParams` (equi-pair extraction) | `internal/planner/unnest.go::collectUnnestParams` (line 198-227) |
| SubPlan cache | `internal/executor/expr.go::evalSubquery` |

The existing `unnestSubquery` handles the simple case where the
inner is `Aggregate(Filter(SeqScan))` and the correlation is a
single equijoin pair. Q20's inner has TWO equi-pairs
(`l_partkey = ps_partkey` AND `l_suppkey = ps_suppkey`), and the
existing code is supposed to handle that via M0054-0008's
multi-param unnesting (`unnest_multi_param_test.go`).

The remaining gap is whether `unnestSubquery` fires for Q20's
specific shape — most likely **no**, because the inner subquery
itself sits inside an `IN (SELECT ...)` that's already at depth 2
of the IN-unnesting recursion. The recursive
`unnestSubqueriesInPlan` call at the bottom of `unnestInExpr`
should descend into the cloned partsupp inner-plan and unnest
the scalar — but that path may not currently expose the
ps_partkey / ps_suppkey OuterColumnRefs in a form that
`unnestSubquery` recognises.

## 4. Proposed change

1. **Verify** that `unnestSubqueriesInPlan` recursively
   descends into `*InExpr.Plan`'s subtree after the IN itself
   has been unnested.
2. **Trace Q20's plan tree** post-M0062-0004: enable a debug
   log under `unnestSubqueriesInPlan` to print whether the
   inner SubqueryExpr is reached and whether `canUnnestSubquery`
   returns true / false / what params it collects.
3. **If `canUnnestSubquery` rejects**, extend the gate to
   accept the multi-param-correlation case present here
   (it should already, per M0054-0008; verify).
4. **If unnesting fires but produces a wrong shape** (e.g.
   the GROUP BY isn't being added, or the scalar comparison
   isn't being routed to a Filter on the joined output),
   fix at the rewriting step.

## 5. Acceptance

- Q20 OK in < 600 s on TPC-H SF=1.
- New end-to-end test with Q20-shape minimal SQL pins the
  decorrelation: the EXPLAIN shows a Hash Join on
  `(l_partkey, l_suppkey)` with an Aggregate child on
  lineitem (instead of a SubPlan).
- A new test in `internal/planner/unnest_multi_param_test.go`
  covers the IN-IN-scalar shape.
- `go test ./...` PASS.

## 6. Risks & rollback

- The decorrelation requires LEFT JOIN semantics
  (NULL-on-no-match for the scalar). M0061-0001 added Semi/
  Anti to the executor; LEFT JOIN with hash already exists.
  No new operator needed.
- The aggregate with multi-column GROUP BY may be slow
  itself if `(l_partkey, l_suppkey)` cardinality is huge.
  At SF=1 partsupp has ~800 K rows, so the aggregate is
  bounded.

## 7. Out of scope

- General correlated-subquery decorrelation framework. This
  design covers only the IN-IN-scalar shape Q20 needs.
- Q22's correlated NOT EXISTS (already handled by
  M0061-0001).
