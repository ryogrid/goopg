# M0146-0014 — S9 parity replenishment census, 2026-10-14

Capture sha: `82c287bc7` (HEAD at capture; no planner change after it in this pass). Captures: the TPC-DS fire-set
gate at both scales (all 99 queries, current tree vs the PG reference) and `scripts/jointree-parity-capture.sh
tpch`. First-divergence records come from `scripts/pg-plan-first-divergence.py`.

| corpus | match | first-divergence rollup |
|---|---|---|
| TPC-DS SF0.25 | 77 / 99 | sort-strategy 7, join-order 4, aggregation-strategy 4, parallelism 2, join-method 2, scan-type 1, qual-placement 1, parameterisation 1 |
| TPC-DS SF1 | 64 / 99 | join-order 8, sort-strategy 8, aggregation-strategy 6, parameterisation 4, parallelism 3, qual-placement 3, scan-type 2, join-method 1 |
| TPC-H | 13 / 22 | sort-strategy 2, scan-type 2, qual-placement 2, parameterisation 1, join-order 1, join-method 1 |

## Ranking (S9 step 3)

- sort-strategy: 17 first divergences across the corpora; minus the waived COSTTIE records (SF0.25 Q4,
  SF1 Q52) leaves 15 actionable — the top category. join-order follows with 13.
- Item 3 was exhausted first: M0146-0042's rendering records (Q78 both scales, Q46 SF1) all sit under a
  join-order first divergence, and 0007 / 0012b / 0067 / 0175 rank below item 10 (S7 carve-out).

## Filed

- M0146-0190 — sort-strategy, "under GroupAggregate: PG Sort over Gather | goopg Gather Merge over a worker
  Sort", TPC-DS SF0.25 Q26 (also Q33, Q45 at SF0.25; Q18, Q26 at SF1).
