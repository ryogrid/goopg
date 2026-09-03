# Planner refactor, take 2 — PostgreSQL-parity query planning for goopg

This bundle documents PostgreSQL 18.3's query planner, cost model and
statistics infrastructure; documents goopg's; analyses the difference; and sets
out a design and an execution plan for making goopg select PostgreSQL's plan for
the same query on the same data.

**Why now.** On the two evaluation workloads, measured 2026-08-31:

| suite | goopg | PG 18.3 | ratio |
|---|---:|---:|---:|
| TPC-H SF=1, 21 comparable queries | 227.0 s | 22.9 s | **9.9×** |
| TPC-DS SF=0.5, 95 comparable queries | 1173 s | 536 s | **2.2×** |

Four previous rounds of planner work landed a great deal — a PG-shaped join
search, multi-column hash keys, hybrid hash spill, bitmap and index-only paths,
persisted index correlation — and closed several avenues with measurements.
What none of them produced is an instrument that answers the question the
project is actually asking: *does goopg emit PostgreSQL's plan?* There is no
committed goopg-vs-PostgreSQL plan diff over either corpus, and EXPLAIN prints
`cost=0.00..0.00` on every node. Building that instrument is the first phase of
this plan.

## Document map

| file | contents |
|---|---|
| [01-pg-planning-pipeline.md](01-pg-planning-pipeline.md) | PostgreSQL's pipeline: `standard_planner` → `subquery_planner` → `query_planner` → `grouping_planner` → `create_plan`. Path/RelOptInfo/PathTarget/ParamPathInfo, `add_path` dominance, pathkeys, parallel paths, planner GUCs. Ends with an 85-item reimplementation checklist. |
| [02-pg-cost-model.md](02-pg-cost-model.md) | `costsize.c` function by function with formulas and constants: scans, sorts, aggregation, the three join methods, materialisation, memoize, rescan, gather, subplans. Cost GUCs, `disabled_nodes`, worked arithmetic examples. Ends with a 92-item checklist. |
| [03-pg-statistics-infrastructure.md](03-pg-statistics-infrastructure.md) | What ANALYZE collects and how, `pg_statistic` layout, `examine_variable`, every restriction and join selectivity estimator, `clauselist_selectivity`, extended statistics, FK selectivity, group estimation, invalidation. Ends with its own checklist. |
| [04-goopg-planning-pipeline.md](04-goopg-planning-pipeline.md) | goopg's counterpart to 01, section for section, with a 31-item structural divergence list. |
| [05-goopg-cost-model.md](05-goopg-cost-model.md) | goopg's counterpart to 02, with a per-function fidelity table and the same worked examples computed from goopg's formulas. |
| [06-goopg-statistics-infrastructure.md](06-goopg-statistics-infrastructure.md) | goopg's counterpart to 03, with a 113-row fidelity table. Resolves two long-standing contested claims about the tree. |
| [07-gap-analysis.md](07-gap-analysis.md) | The difference and its consequences: structural gaps, per-query evidence, what previous rounds already established, what is out of scope, and where the measurements themselves are unreliable. |
| [08-target-design.md](08-target-design.md) | The design: make the Path search the only planner, feed it PostgreSQL's statistics and cost inputs, delete everything that plans around it. Seven phases, with a risk register. |
| [09-verification-and-acceptance.md](09-verification-and-acceptance.md) | What is measured, with which instrument, and what counts as done. Five gate failures that already happened, and the rules that follow from them. |
| [TODO.md](TODO.md) | The execution checklist. One checkbox ≈ one commit. Progress log at the end. |
| [REVIEW.md](REVIEW.md) | Agent-review record: findings and how each was resolved. |

## Reading order

- **To understand the problem**: 07, then 09 §1.
- **To do the work**: 08 and TODO.md, with 01–06 as reference.
- **To review a change**: 09, then the phase section of 08 it belongs to.

## Ground rules

1. **`./postgres/` is the specification and is never modified.** Every claim
   about PostgreSQL in 01–03 cites a file and a function. Where PostgreSQL uses
   an approximation goopg could improve on, goopg reproduces the approximation —
   plan parity is the objective, and a better estimate that produces a different
   plan is a failure of this project.
2. **No query-specific forcing.** No rule, penalty, threshold or shape
   preference that identifies a benchmark query, a table name, or a recognisable
   query shape. No new penalty multiplier on cost totals. Both prohibitions were
   established by measurement, not by taste (08 §1, 09 §8).
3. **A plan-shape change is timed on both suites.** A row-count gate cannot
   catch a plan-shape regression, by construction, and "no plan changed" is
   scoped to the suite that was run. Both statements are things this project
   learned the expensive way (09 §1).
4. **One variable per commit, enforced by sequencing.**
5. **Every deferral gets a `.ralph/deferral_ledger.md` row** with an upstream
   citation and a concrete resume point.
6. **Negative results are kept verbatim.** Several sections of 07 exist only
   because earlier rounds recorded what did not work and why, without rewriting
   it afterwards.

## Scope

This bundle targets **plan parity** — bars A1–A5 in 09 §7.1 — and the time
movement that follows from it. It does not claim to close the TPC-H time gap on
its own: TPC-H Q6 runs the node-for-node PostgreSQL-identical plan and still
takes 23.40 s against PostgreSQL's 0.99 s. The executor-side residuals behind
that number are catalogued in 07 §6 with pointers, and are explicitly out of
scope here.

## Status

Design and analysis complete; implementation not started. Progress is tracked
in [TODO.md](TODO.md), whose progress log carries the baseline row above.
