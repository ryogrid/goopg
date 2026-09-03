# Executor bundle (take3): README_EXECUTOR

**Goal: executor-side OLAP parity after plan parity.** Take3 01–09 gets
goopg to *select* PostgreSQL 18.3's plan; this bundle gets goopg to
*execute* it at approaching cost — same shape, approaching time.

**Motivating number.** TPC-H Q6 runs the node-for-node PG-identical plan
and still takes **23.40 s serial against PG's 0.99 s (23.6×)** — take5–take7
halved it to 3.79 s serial / 0.838 s parallel (4.1× PG's 0.203 s), and the
open gaps in `12-executor-gap-analysis.md` own the rest (11 §17).

## Relation to take3 01–09

01–09 target **plan parity** (bars A1–A5, 09 §4.1). 07 §6 catalogues the
executor residual with pointers and declares it out of scope; this bundle
is the analysis it defers to. Rule: **executor items move no plan** — every
item pins plan-shape via the 09 §2.2 instrument (or goopg-vs-goopg capture
until it lands) and hands moved plans to the plan bundle (13 §1 EX-P5).

## Document map

| file | contents |
|---|---|
| [10-executor-pg-design.md](10-executor-pg-design.md) | PG 18.3 executor oracle: dispatch, slots, scans, joins, hash, sort, parallel, expr, memory contexts. |
| [11-executor-goopg-design.md](11-executor-goopg-design.md) | goopg executor at HEAD `adf2d1e13`, section-for-section vs 10, with the §17 hot-spot table (LANDED/OPEN/STALE) and §18 verification log. |
| [12-executor-gap-analysis.md](12-executor-gap-analysis.md) | Ranked executor gaps G-EX1…G-EX8 with PG/goopg mechanisms, measured costs, negatives, STALE ledger. |
| [13-executor-target-design.md](13-executor-target-design.md) | Phased plan EX0 (instruments) → EX1 (narrowing) → EX2 (clones) → EX3 (spill/sort/hash) → EX4 (expr) → EX5 (parallel); principles, sequencing, risks. |
| [TODO_EXECUTOR.md](TODO_EXECUTOR.md) | Execution checklist, one checkbox ≈ one commit, with gates and acceptance bars E1–E6. |
| REVIEW_EXECUTOR.md | *Planned, not yet written* — agent-review synthesis for 10–13, mirroring take3 REVIEW.md. |

## Reading order

- **To understand the problem**: 12, then 11 §17 (hot-spot table) and 07 §6.
- **To do the work**: 13 and TODO_EXECUTOR.md, with 10–11 as reference.
- **To review a change**: take3 09 (shared gates), then the phase section
  of 13 the item belongs to.

## Ground rules

1. **PG semantics are the spec**, including spill/batch discipline (13 §1 EX-P1).
2. **No query-specific forcing** (EX-P2). **One variable per commit** (EX-P3).
3. **Allocator + time together** — a CPU win that doubles allocs is not a win (EX-P4).
4. **Both suites, fresh server per arm**, serial control for parallel work (EX-P6).
5. **Values, never counts**, for projection/join-adjacent changes (09 §1 R8).

## Scope

In: per-row width/deform/detoast, retention-boundary clones, spill/sort/hash
batching, per-operator expression compilation, parallel slab parity.
Out: plan selection (take3 01–09), `Datum` re-layout below 48 B, JIT/LLVM,
TOAST statistics and other planner inputs this bundle consumes (13 §10).

## Status

Design complete; EX0–EX5 not started (take5–take7 landings recorded `[x]` in
TODO_EXECUTOR.md). Progress is tracked in [TODO_EXECUTOR.md](TODO_EXECUTOR.md).
Take6-landed rows (ToLower, atomics, CLOG) are closed with numbers in 12 §11
and are not re-gated — their per-query timings are recorded in the EX0-06
per-query baselines (both suites) that E1 diffs against.

(End of file)
