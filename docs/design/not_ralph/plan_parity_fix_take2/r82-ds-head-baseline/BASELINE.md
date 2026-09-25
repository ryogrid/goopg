# R82 — TPC-DS HEAD baseline (SF0.25) + nearest-miss ranking

## Method

Private clone `/tmp/pp2/clone-ds025-r82` (cp-a of
`bench/tpcds/runtime_goopg/data-sf025`), HEAD binary
(`tmp/goopg-r79p1`, code-identical to HEAD — post-R81 commits
are docs-only), `:5557`, pinned env
(`work_mem='64MB'`, `max_parallel_workers_per_gather=4`),
99 EXPLAIN capture → `pg-plan-parity-diff.py` vs
`bench/tpcds/plans-pg`. Peer `:5533` untouched; PG refs
untouched (zero PG contact).

## Verdicts

`match=1 shapediff=68 missingnode=27 error=3`
(Q36/70/86 ERROR = querygen skips, known).

| category | count |
|---|---|
| join-order | 95 |
| parallelism | 88 |
| sort-strategy | 80 |
| aggregation-strategy | 74 |
| join-method | 62 |
| scan-type | 58 |
| parameterisation | 49 |
| rendering | 22 |
| qual-placement | 11 |

Q9 MATCH holds on SF0.25 (R46's win survives the scale move).

## Nearest misses (3 categories)

- Q25/Q28/Q29 `[join-order,sort-strategy,parallelism]` —
  Gather Merge + join-order divergences (parallel machinery
  family; bigger).
- Q26 `[join-order,join-method,parallelism]`, Q7
  `[join-order,sort-strategy,parallelism]` — same family.
- **Q41 `[join-order,parameterisation,sort-strategy]`** —
  NO parallelism, NO agg-strategy. Cleanest next target:
  goopg `Sort→Unique→Limit→Sort→SeqScan(SubPlan $0)` vs PG
  `Limit→Unique→Sort→SeqScan(SubPlan i1.i_manufact)` —
  Limit-under-Unique + top Sort (limit-pushdown artifact?)
  and correlated-param display ($0 vs outer var).
- Also 3-cat but not selected: Q91 MISSING-NODE (PG-only
  Materialize — bigger machinery) and Q96
  `[join-order,join-method,scan-type]` (Merge Join vs Nested
  Loop + leaf-set divergence under a *shared* Gather —
  join-order/method work; Gather on both sides so not a
  parallelism gap, but harder than Q41's local
  Limit/Unique/Sort placement).

## Selected next round

Q41 Limit/Unique/Sort placement + correlated-param rendering
(R83 candidate). Q25/26/28/29/Q7 need parallel machinery
(K43 family) — explicitly deferred.

## Evidence

- `/tmp/pp2/r82/ds-head.txt` (99 captures),
  `/tmp/pp2/r82/ds-head.conv.txt`,
  `/tmp/pp2/r82/ds-diff.txt`, `/tmp/pp2/r82/ds-diff-v.txt`
- `:5557` stopped after capture; clone retained.
