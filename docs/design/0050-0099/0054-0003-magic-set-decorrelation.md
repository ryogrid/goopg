# 0054-0003 — Magic-Set / SIPS Decorrelation for Correlated Aggregates

**Status:** Draft. Sub-task of M0054-0008.
**Author:** goopg perf-analysis branch (run-013 finding).
**Date:** 2026-05-06.

## 1. Problem

TPC-H Q20's main predicate contains a correlated aggregate:

```sql
ps_availqty > (SELECT 0.5 * sum(l_quantity)
               FROM lineitem
               WHERE l_partkey = ps_partkey
                 AND l_suppkey = ps_suppkey
                 AND l_shipdate >= date '1994-01-01'
                 AND l_shipdate <  date '1994-01-01' + interval '1 year')
```

The subquery references the OUTER `partsupp` row's `ps_partkey` and
`ps_suppkey`. Today's executor evaluates this subquery
**per outer probe** — for each `(ps_partkey, ps_suppkey)` candidate
the inner SELECT re-aggregates lineitem matching the correlation
predicate. SF=1 has 800K partsupp × 6M lineitem = ~4.8 trillion
inner-row visits worst case.

Empirical measurement (run-013, 2026-05-06): Q20 was still running
at >90 minutes wall-clock under NLI ON — exceeding the 7200 s
HammerDB budget. NLI helps the **outer** side's joins (supplier ×
nation, supplier × partsupp) but does not penetrate the correlated
subquery's inner re-evaluation; that loop dominates.

## 2. Goal

Convert the correlated re-evaluation into a **single batched
aggregation** keyed on the correlation columns, materialised once
before the outer loop, and probed per outer row.

In Postgres terms: replace the SubPlan with an InitPlan that
computes `(l_partkey, l_suppkey, agg)` once, then a hash-probe
inside the outer scan.

In datalog terms: this is **magic-set transformation** — the outer
filter's correlation columns become the magic set, restricting the
inner aggregation to only the keys actually probed.

In Postgres-planner terms: this is a flavour of **sideways
information passing (SIPS)** — the outer side passes the relevant
correlation values to a pre-built hash side.

## 3. Plan-tree shape

### Today's plan (sketch)

```
Filter(ps_availqty > SubPlan(corr-aggregate))
  └─ HashJoin(supplier × partsupp)
      ├─ SeqScan(supplier)
      └─ SeqScan(partsupp)

SubPlan: Aggregate(SUM(l_quantity))
           └─ Filter(l_partkey = $1 AND l_suppkey = $2 AND l_shipdate range)
               └─ SeqScan(lineitem)
```

The SubPlan is invoked per outer row, with `$1 = ps_partkey` and
`$2 = ps_suppkey` bound from the outer scope.

### Proposed plan (after M0054-0008)

```
HashJoin(probe ps_availqty against pre-aggregated)
  ├─ HashJoin(supplier × partsupp)
  │   ├─ SeqScan(supplier)
  │   └─ SeqScan(partsupp)
  └─ HashAggregate keyed on (l_partkey, l_suppkey)
       └─ Filter(l_shipdate range; correlation columns NOT included)
           └─ IndexScan or SeqScan(lineitem)
```

The aggregate is computed ONCE over the lineitem range; the result
is a hash table keyed by `(l_partkey, l_suppkey)` mapping to
`sum(l_quantity)`. The outer Filter becomes a join on the hash
plus the `>` comparison.

## 4. Implementation outline

### 4.1 Detection — when is the rewrite legal?

The transformation is sound when:

- The subquery is **scalar** (single-row, single-column return).
- The subquery's WHERE includes equality conjuncts of the form
  `inner.col = outer.col` for one or more columns. These are the
  correlation columns; they become the GROUP BY of the hoisted
  aggregate.
- The subquery body is a single `Aggregate(GroupBy=∅, Aggs={f(...)})`
  over a Filter (WHERE) over a SeqScan / IndexScan.
- The aggregate function is one that distributes over disjoint
  partitions (sum, count, min, max, avg) — anything that can be
  pre-computed by `GROUP BY correlation-columns`.

### 4.2 Planner work

New planner pass `decorrelateScalarAggregate` runs after the
existing subquery planning:

1. Walk the plan tree for `*SubqueryExpr` whose Body is the shape
   above.
2. Extract the correlation columns from the WHERE: any
   `inner.colA = outer.colB` where `outer.colB` resolves to a
   `*OuterColumnRef`.
3. Rewrite the SubPlan body:
   - Drop the correlation conjuncts from the WHERE (they become
     GROUP BY).
   - Add `correlation_columns` to the Aggregate's GroupExprs.
   - Hoist the new Aggregate above the outer scope so it
     materialises BEFORE the outer loop runs.
4. Rewrite the outer `Filter(ps_availqty > SubPlan(...))` into a
   `HashJoin(... ON correlation = correlation, residual:
   ps_availqty > agg)`.

### 4.3 Executor work

- The hoisted Aggregate is a regular HashAggregate. Already
  supported.
- The new join over the materialised aggregate is a regular
  HashJoin. Already supported.
- The SubPlan operator is no longer invoked for this query — it
  remains for non-decorrelatable correlated subqueries.

### 4.4 Cost gate

Decorrelation always speeds up — converts O(outerRows × innerScan)
to O(innerScan + outerRows × log-or-O(1) probe). No cost-model
threshold needed for correctness, but a feature gate is added for
rollback (`enable_correlated_subquery_decorrelation`, default on).

## 5. Acceptance

- Q20 SF=1 wall-clock ≤ 600 s (10 min) — currently >90 min. (The
  Q20 outer side has additional optimisation opportunities; 600 s
  is the sub-task target, not the absolute lower bound.)
- A synthetic test with one outer table (1000 rows) and one inner
  table (10000 rows) joined by a correlated `sum(...)` shows
  decorrelation produces results identical to the naive subplan
  evaluation.
- A unit test confirms decorrelation does NOT fire when the
  subquery's body has a non-distributing aggregate, a non-equality
  correlation, or a non-scalar subquery (multi-row, multi-column).
- EXPLAIN renders the rewritten plan tree.

## 6. Out of scope

- **Non-aggregate correlated subqueries** (correlated EXISTS, IN,
  ANY/ALL with subquery on the right). These need separate
  decorrelation strategies (semi-join / anti-join transform) — a
  follow-up task if Q20's nested-IN pattern becomes the bottleneck
  after the aggregate fix lands.
- **Multi-level correlation**. The current proposal handles
  one-level correlation (subquery references its immediate outer).
  Two-level correlation needs recursive lifting.
- **Window functions** in subqueries.

## 7. Open questions

- Q1: Does the Postgres-style InitPlan vs. SubPlan distinction map
  cleanly to goopg's existing SubqueryExpr? Or do we need a new
  plan node `*InitPlan` for the hoisted aggregate?
- Q2: When the correlation columns have NULLs in the inner scope,
  the hash join semantics differ from the per-row equality. Need
  to confirm Q20's `l_partkey/l_suppkey` are NOT NULL in the
  HammerDB schema (they are, per the schema; documenting here).

## 8. References

- Postgres `subselect.c` SS_process_sublinks / convert_ANY_sublink_to_join.
- Mumick et al., *Magic-Sets and Other Strange Ways to Implement
  Logic Programs* (1990).
- TPC-H Q20 reference plan in DuckDB / cockroachdb / Citus.
