# Planner refactor, take 3 — PostgreSQL-parity query planning for goopg

Take 3 of the planner-parity bundle. The goal is unchanged: goopg selects
PostgreSQL 18.3's plan for the same query on the same data (**plan parity**)
on the two OLAP evaluation workloads (TPC-H SF=1, TPC-DS SF=0.5), and takes
the time movement that follows from it.

**Baselines** (take2 measurements, 2026-08-31, S-cold, ±17% noise band):

| suite | goopg | PG 18.3 | ratio |
|---|---|---:|---:|
| TPC-H SF=1, 21 comparable queries | 227.0 s | 22.9 s | **9.9×** |
| TPC-DS SF=0.5, 95 comparable queries | 1173 s | 536 s | **2.2×** |

Honest-ratio caveat: the 9.9× was measured with goopg holding an 8×
`work_mem` advantage (512 MB boot default vs the PG reference cluster's
explicit 64 MB). Aligned at 64 MB / 2 GB (`effective_cache_size`), goopg
moved 248.71 s → 403.27 s (+62%, row counts identical), so the honest ratio
against PG's 22.9 s is nearer **17.6×** (07 §2.1; per-measurement binaries
differ — every pre-Sep figure was measured blind on restarted servers,
07 §2.1).

**Why take3.** Take2 landed large parts of Phases 0–2 (EXPLAIN costs,
`PlannerSettings`, `DisabledNodes` for joins, `cost_rescan`, merge costing,
MCV pairing, the pg_statistic decode fix), reverted two attempts with
measurements (P2-02b BootVal, P4-01b leaf narrowing), and ruled several
avenues out. The open remainder is re-sequenced here with **PathTarget
first**: width, not cardinality, is the leading cost-side hypothesis at
equal cardinality (07 §3.2), and P4-01 now precedes the search-coverage and
BootVal work it blocks.

## Document map

| file | contents |
|---|---|
| [01-pg-planning-pipeline.md](01-pg-planning-pipeline.md) | PG 18.3's pipeline: `standard_planner` → `subquery_planner` → `query_planner` → `grouping_planner` → `create_plan`; Path/RelOptInfo/`add_path`, pathkeys, parallel paths, planner GUCs; reimplementation checklist. |
| [02-pg-cost-model.md](02-pg-cost-model.md) | `costsize.c` function by function with formulas and constants: scans, sorts, aggregation, the three join methods, rescan, gather, subplans; `disabled_nodes`; worked examples; checklist. |
| [03-pg-statistics-infrastructure.md](03-pg-statistics-infrastructure.md) | What ANALYZE collects, `pg_statistic` layout, `examine_variable`, every restriction and join estimator, extended statistics, FK selectivity, invalidation; checklist. |
| [04-goopg-planning-pipeline.md](04-goopg-planning-pipeline.md) | goopg's counterpart to 01 at HEAD `d5f8a6ff9`, section for section, with the structural divergence list; absorbs the take2 landings. |
| [05-goopg-cost-model.md](05-goopg-cost-model.md) | goopg's counterpart to 02, with a per-function fidelity table and the same worked examples from goopg's formulas. |
| [06-goopg-statistics-infrastructure.md](06-goopg-statistics-infrastructure.md) | goopg's counterpart to 03, with a fidelity table; records the decode and ndistinct fixes plus Phase-1 measurement guidance. |
| [07-gap-analysis.md](07-gap-analysis.md) | The difference and its consequences at this HEAD: what still holds vs what is stale, gaps ranked by plan-parity leverage, per-query evidence, reliability notes. |
| [08-target-design.md](08-target-design.md) | The design: one planner, PG statistics, PG cost inputs, delete the rest; Phases 0–6 in PathTarget-first order, with sequencing constraints and a risk register. |
| [09-verification-and-acceptance.md](09-verification-and-acceptance.md) | What is measured and what counts as done: gate failures R1–R8, instruments, bars A/B/C, per-phase gates, methodology, the acceptance run. |
| [TODO.md](TODO.md) | The execution checklist for the remaining work. One checkbox ≈ one commit. Progress log at the end. |
| [.review-design.md](.review-design.md) | Agent-review record: findings and how each was resolved. |

## Reading order

- **To understand the problem**: 07, then 09 §1.
- **To do the work**: 08 and TODO.md, with 01–06 as reference.
- **To review a change**: 09, then the phase section of 08 it belongs to.

## Ground rules

1. **PG is the spec.** Where PG approximates, goopg reproduces the
   approximation; a better estimate that produces a different plan is a
   failure (08 §1 P1).
2. **No query-specific forcing, no penalty multipliers, no shape
   preferences** (08 §1 P3).
3. **A plan-shape change is timed on both suites**, fresh server per arm;
   per-query and TOTAL arms are complementary (08 §1 P7; 09 §2.3).
4. **One variable per commit**; cancelling pairs move in one commit (08 §1 P5).
5. **Every deferral gets a `.ralph/deferral_ledger.md` row** with an upstream
   citation and a resume point (08 §1 P6).
6. **Negative results are kept verbatim** (07 §5).

## Scope

This bundle targets **plan parity** — bars A1–A5 in 09 §4.1 — and the time
movement that follows from it. It does not promise time parity on plan work
alone: Q6-type executor residuals (identical plan, 23.40 s vs 0.99 s) are
out of scope here and catalogued with pointers in 07 §6.

## Status

Design complete; take3 phases not started (take2 landings absorbed per
TODO.md). Progress is tracked in [TODO.md](TODO.md).

(End of file)
