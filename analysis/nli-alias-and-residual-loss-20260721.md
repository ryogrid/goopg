# NLI rewrite loses the inner alias and the join residual (TPC-H Q7 wrong results)

**Date:** 2026-07-21
**Branch:** `planner-kaizen`
**Found during:** SF1 baseline capture for the correlated-subquery-planning bundle
(phase S0-3) — Q7 returned **486,357 rows** where PostgreSQL 18.3 returns **4**.
**Status:** both defects fixed and verified; Q7 now returns 4 rows with the
nested-loop-index-join rewrite still applied.

---

## 1. Symptom

The 22-query SF1 sweep recorded `Q7: OK elapsed=173.23s rows=486357`. TPC-H Q7
groups by `supp_nation, cust_nation, l_year`, so it can return at most a handful
of rows; PostgreSQL returns 4 on the same scale factor. A row count that large
means the query was returning roughly one group per input row.

This was **not** a regression from the correlated-subquery work: the binary that
produced it contained only stages S0-1 (EXPLAIN rendering) and S0-2 (counters),
neither of which touches execution, and Q7 contains no subquery at all. An older
record (`analysis/tpch-runner-measurement-report-2026-05-06.md`) shows Q7
returning 4 rows, so the defect entered the tree between May and July 2026.

## 2. Diagnosis

Reducing the query showed the grouping keys themselves were wrong:

```
 supp_nation                              | cust_nation  | l_year | n
------------------------------------------+--------------+--------+---
 28reYGutbQ0LwxilSDX 0Uaxu 4fYRkol6hEn    | PERU         |   1995 | 1
 ,SAJH,21xwZzneLNFAs                      | KENYA        |   1995 | 1
```

`supp_nation` (`n1.n_name`) was returning **supplier address strings**, and
`cust_nation` showed nations that the `FRANCE`/`GERMANY` predicate should have
excluded. Two independent defects, both in `tryBuildNLI`
(`internal/planner/nl_index_join.go`):

### Defect A — the inner `IndexScan` dropped the FROM-clause alias

The rewrite built the inner scan as

```go
inner := &IndexScan{pos: …, Table: innerScan.Table, Index: idx, schema: innerScan.Output()}
```

copying everything **except `Alias`**. For a self-join (`FROM nation n1, nation n2`)
later passes disambiguate the two relations by alias, so `n1.*` bound against a
neighbouring relation's slots — `n1.n_nationkey` yielded `Supplier#000007547`
(supplier's `s_name`) and `n1.n_name` yielded a supplier address, a constant
one-column shift into `supplier`.

Minimal contrast that isolates it: with a `WHERE n1.n_name = 'FRANCE'` filter the
plan keeps `n1` as a `Seq Scan on public.nation n1` and everything resolves; drop
the filter and `n1` becomes the NLI's inner `Index Scan … on public.nation`
(no alias) and the columns shift.

### Defect B — the join's residual predicate was discarded

`residualPred` was assigned **only** on the OR-factoring path
(M0054-0006-followup-Q19). On the ordinary path `j.Predicate` was dropped, per this
assumption recorded in the code:

> Otherwise no residual: the equi-conjunct is fully consumed by the IndexScan probe
> and non-key conjuncts are separated upstream via `pushdown.go`.

That assumption does not hold for a predicate spanning **two** relations: pushdown
cannot place it on either scan, so it stays on the join. Q7's nation pair —
`(n1.n_name='FRANCE' AND n2.n_name='GERMANY') OR (n1.n_name='GERMANY' AND n2.n_name='FRANCE')`
— is exactly that shape, and it vanished from the plan entirely.

Confirmed by toggling the rewrite off (2-day window, correct answer is 20):

| `enable_nestloop_index` | rows |
|---|---:|
| `on` (default) | 4 848 |
| `off` | **20** |

## 3. Fix

Both defects are the same class — *the rewrite loses information carried by the
nodes it replaces* — and both are fixed while **keeping** the NLI rewrite:

1. **Alias**: `Alias: innerScan.Alias` is propagated to the inner `IndexScan`.
2. **Residual**: conjuncts of `j.Predicate` that the index probe does not enforce
   are collected (`nliConsumedByProbe` matches an index column name *and* pointer
   identity of the probe key, so a second equality binding the same inner column
   to a different outer expression correctly survives) and placed on
   `NestedLoopIndexJoin.Predicate`.

The residual's `ColumnRef` indices cannot be reused verbatim: they were resolved
against this join's layout at bind time, and a rewrite lower in the tree may since
have reordered a child's output. They are re-resolved against `outer ++ inner`
using the same `(Name, SourceTableIdx)` rule `reresolveJoinByName`'s `predRebind`
already uses, which is what makes self-joined aliases (Q7's `n1`/`n2`, Q21's three
`lineitem` aliases) resolvable at all. Two safety properties:

- Semi/Anti joins project the outer schema only, so a residual reaching into the
  inner side has nowhere to live — those decline the rewrite instead.
- Every reference is resolved **before** any index is written back, so a rewrite
  that ends up declining leaves the shared predicate untouched.

An intermediate attempt that retained the residual *without* re-resolution is
worth recording as a trap: it produced **0 rows** — the predicate was present but
evaluated against stale indices, silently filtering everything. A retained-but-
misresolved residual is more dangerous than a dropped one, because the plan looks
correct.

## 4. Verification

| stage | Q7 rows | note |
|---|---:|---|
| before | 486 357 | both defects live |
| after defect A fix | 1 250 | = 25 × 25 × 2, i.e. grouping keys now correct but the nation pair still unfiltered |
| after defect B fix | **4** | matches PostgreSQL 18.3 |

The rewrite is still applied: the fixed Q7 plan retains its `Nested Loop` nodes,
and the 2-day reduction returns the correct 20 rows with `enable_nestloop_index`
left at its default.

Gates run on the fix: units suite green; `scripts/tpch-spotcheck.sh` PASS
(Q12 = 2 rows, Q13 = 33 rows — the canonical silent-regression tripwires);
`internal/planner` and `internal/executor` package tests green.

## 5. Follow-ups

- **`EXPLAIN` does not print `NestedLoopIndexJoin.Predicate`.** During diagnosis
  the retained residual was invisible in the plan text, which is why the
  mis-resolved intermediate state was hard to see. `emitNodeDetailLines` has no
  case for the NLI node; adding one would make this class of defect visible.
- **The OR-factoring path assigns `residualPred = j.Predicate` without
  re-resolution**, so it carries the same stale-index hazard this fix addresses on
  the ordinary path. It has not been observed failing (Q19 is its user), but it
  should be routed through the same re-resolution.
- Other row counts in the SF1 sweep differ from the PostgreSQL reference numbers
  in `analysis/tpch/goopg-pg-tpch-plan-compare-260718/` (Q3, Q10, Q11, Q16). The
  two data sets are independently generated by HammerDB, so some divergence is
  expected, but the magnitudes (Q11: 785 vs 1 119) deserve their own check.
