# Planner Overview (Milestone 0003)

| Field      | Value                                                  |
| ---------- | ------------------------------------------------------ |
| Status     | draft                                                  |
| Date       | 2026-04-29                                             |
| Milestone  | 0003 - HammerDB TPC-H Workload                         |
| Refines    | [root-0011-planner.md](root-0011-planner.md), [0003-0002-join-executors.md](0003-0002-join-executors.md), [0003-0003-statistics-and-cardinality.md](0003-0003-statistics-and-cardinality.md) |
| Supersedes | -                                                      |

## Problem

`root-0011-planner.md` documents the v0 planner architecture and the
rule-based mapping from parser AST nodes to plan nodes. Milestone 0003 added
multiple planner features (hash joins, merge joins for RIGHT/FULL equality
joins, EXPLAIN wrappers, view substitution, subquery support, and cardinality
estimation plumbing).

Without a milestone-level overview, those changes are scattered across
feature-specific docs and tests, making it hard to reason about current planner
behavior and the remaining gaps for TPC-H Q1-Q22.

## Scope

This doc summarizes the current planner behavior for M0003 and serves as the
entry point to feature-specific design docs.

In scope:

- SELECT planning pipeline for scans, joins, grouping, projection, sort, and
  limits.
- DML/DDL/utility wrappers in the planner surface.
- Join algorithm selection rules (nested-loop/hash/merge).
- Cardinality estimation seams (`EstimateRows`) and where estimates are exposed.
- Explicitly deferred cost-model and join-order optimization work.

Out of scope:

- Executor internals (covered by `root-0012` and related M0003 docs).
- Storage/WAL details.

## Current Planner Pipeline

For `SELECT`, planner flow is:

1. Build FROM tree (`SeqScan`, virtual `Values`, view expansion via
   `planScanRangeVar`).
2. Resolve JOIN predicates and produce `planner.Join` nodes.
3. Choose join algorithm for eligible equality predicates.
4. Attach `Filter` for WHERE.
5. Attach `Aggregate` + HAVING filter when required.
6. Attach `Project` for target list.
7. Attach `Sort` and `Limit`/`Offset`.
8. Wrap in `Explain` when `EXPLAIN <stmt>` is requested.

This keeps planner output as a simple, serializable node tree consumed by
executor `Build`.

## Join Algorithm Selection

When predicate decomposition (`splitEqualityForHash`) finds a single disjoint
left-key/right-key equality:

- `INNER` / `LEFT` joins: planner sets `Join.Algo = JoinAlgoHash`.
- `RIGHT` / `FULL` joins: planner sets `Join.Algo = JoinAlgoMerge`.
- Other shapes (inequality, conjunctive predicates, CROSS, mixed-side
  expressions): planner keeps `JoinAlgoNestedLoop` fallback.

Planner stores `LeftKey`/`RightKey` on `planner.Join` for hash/merge paths.

## Cardinality Estimation Surface

`planner.EstimateRows(node)` computes rough row-count estimates bottom-up and is
currently surfaced through EXPLAIN text labels.

Implemented:

- SeqScan from table stats (`RowCount`).
- Join estimate for hash/merge equality joins using
  `|L| * |R| / max(NDistinct(L.k), NDistinct(R.k))` when stats exist.
- Conservative defaults when stats are missing.

Not yet implemented:

- Cost-based join reordering.
- Cost-based hash vs merge choice for INNER/LEFT joins.
- Predicate-shape-aware filter selectivity for complex expressions.

## Relationship to Feature Docs

- Join algorithms and executor behavior: [0003-0002-join-executors.md](0003-0002-join-executors.md)
- Statistics and estimates: [0003-0003-statistics-and-cardinality.md](0003-0003-statistics-and-cardinality.md)
- HammerDB/TPC-H integration constraints: [0003-0004-hammerdb-tpch-integration.md](0003-0004-hammerdb-tpch-integration.md)
- Subqueries, CASE, date/interval, EXPLAIN, views: `0003-0005` through
  `0003-0010`

## Deferred Work

- Cost-based planner that can change join order and algorithm by estimated cost.
- Multi-column equality decomposition for hash/merge keys.
- Expanded selectivity model (MCV/histogram use, correlated predicates).
- Search-space management for larger join graphs.
