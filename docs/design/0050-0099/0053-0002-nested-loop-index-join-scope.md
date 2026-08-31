# Design Doc 0053-0002 — Nested-Loop Index Join: Scope Assessment

**Status:** draft (scope-only — no implementation in M0053)
**Milestone:** 0053 — HammerDB TPC-H Complete Run Verification & Report
**Author:** Ralph (autonomous agent)
**Date:** 2026-05-05

## 1. Background

Multi-table equality joins are currently planned as either hash joins
(`HashJoin`, `MultiHashJoin`, `LazyHashJoin`, `SpillHashJoin`) or as
nested-loop joins **with sequential scans on the inner side**. The
planner never turns a join condition like `o.o_orderkey = l.l_orderkey`
into a parameterised IndexScan on the inner table even when an index
on `l_orderkey` exists.

This is the right default for OLAP workloads (TPC-H) where the inner
table is large and scanned once per query — hash join is asymptotically
better than nested-loop-with-index-probes for such shapes. However,
when the outer side is small or highly selective, a nested-loop index
join (NLI) can outperform hash join because:

- It avoids building a hash table over a large inner relation.
- It only touches the inner pages relevant to outer keys (often <1% of
  the index).
- It preserves outer-side ordering (useful for ORDER BY pushdown).

PostgreSQL's planner picks NLI over Hash when the cost model says so;
this requires statistics-driven cost estimation, which goopg has only
in nascent form (M0006).

## 2. M0053 Decision

**Defer NLI implementation to a new milestone (M0054).**

Reasons:

1. **Architectural surface is large.** A correct NLI requires:
   - A new `IndexScan` mode that accepts a runtime-bound parameter
     (the outer row's join column value) rather than a planning-time
     constant.
   - A new `NestedLoopIndexJoin` planner rule that detects
     `outer.col_o = inner.col_i` join conditions, picks the side with
     a usable index as inner, and rewrites to NLI.
   - Optional: a `Materialize` operator on top of the outer when it
     is consumed multiple times.
   - Cost-model integration so the planner picks NLI when it really
     wins.
2. **TPC-H impact is bounded.** Most TPC-H queries are large-cardinality
   joins where hash join (already implemented) is the right algorithm.
   NLI would help small-outer queries but goopg has no row-count
   estimator hooked into join planning yet (M0006-0004 is planned).
3. **Posting-list overflow (M0053-0005) is the actual TPC-H blocker.**
   That fix alone unblocks run-011 even without NLI. The composite
   index support (M0053-0001) further improves filter selectivity for
   PARTSUPP / LINEITEM-PK probes.
4. **Constant-RHS index path already covers the date/range filters
   that drive most TPC-H query selectivity** (M0053-0002 verified).

## 3. Out-of-scope clarification

The earlier Pre-Run audit noted that `col1 = col2` predicates fall
through to SeqScan. That is **correct behaviour for the current
algorithm set**: a column-vs-column predicate inside a single relation
cannot use an IndexScan (the RHS depends on the row being scanned),
and across two relations it surfaces as a join condition handled by
hash/merge join. Neither case is a planner bug; both are addressed by
NLI when added.

## 4. Recommended M0054 Scope

When M0054 is opened, the following decomposition is recommended:

| Sub-task | Deliverable |
|----------|-------------|
| M0054-0001 | `Param`-bound IndexScan operator: bind value at runtime, probe via existing `tree.RangeScan`. |
| M0054-0002 | `NestedLoopIndexJoin` plan node + executor (drives outer, probes inner per row). |
| M0054-0003 | Planner rule: detect equi-join, pick index side, emit NLI when (a) outer is small, or (b) inner index is selective enough — uses M0006 stats. |
| M0054-0004 | Result-parity test matrix: confirm NLI returns identical rows to HashJoin for representative TPC-H join shapes. |
| M0054-0005 | EXPLAIN output for NLI; cost-model gate; rollback path. |

Until M0054 lands, multi-table joins continue to use hash join. This
is acceptable for HammerDB TPC-H power tests at SF=1.

## 5. References

- `internal/planner/planner.go` — `tryRangeIndexScan`, join planning
- `internal/executor/operators_index.go` — IndexScan operator (current,
  constant-key only)
- `internal/executor/operators_hashjoin*.go` — current join algorithms
- `docs/design/0006-*.md` — planner statistics (cost-model dependency)
- `docs/milestones/0053-hammerdb-tpch-complete-run-verification.md`
- PostgreSQL upstream: `postgres/src/backend/optimizer/path/joinpath.c`
  for the `Nestloop` path generation algorithm.
