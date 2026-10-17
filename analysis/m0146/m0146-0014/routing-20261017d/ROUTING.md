# M0146-0014 — S9 pass at 1a8760b93 (2026-10-17): files M0146-0224

Trigger: every item 0-9 task is gated:
- M0146-0214 is blocked-on M0146-0216;
- M0146-0215 and M0146-0218 wait on the owner's stats import;
- M0146-0216 waits on the owner's index refresh.

Captures: M0146-0223's `analysis/m0146/m0146-0223/census/` (1a8760b93's code). The `imp-*` captures are this pass's
PG-statistics clones (HEAD binary; 474 SF0.25 and 478 SF1 restore calls returned true).

| corpus | match |
|---|---|
| TPC-H | 22 / 22 |
| TPC-DS SF0.25 | 90 / 99 |
| TPC-DS SF1 | 77 / 99 |
| TPC-DS SF0.25, PG statistics | 94 / 99 |
| TPC-DS SF1, PG statistics | **91 / 99** |

## Exclusions

- Live tasks: Q64 (M0146-0214), Q76 (M0146-0215), SF1 Q87 (M0146-0218).
- Import-routed: a shipped record that disappears, or changes category, under PG statistics.
  - SF0.25: Q38, Q87, Q99, Q77 (aggregation-strategy → sort-strategy).
  - SF1: Q4, Q7, Q11, Q13 (qual-placement → join-method), Q18, Q26, Q31, Q38, Q48, Q56, Q62, Q69, Q99.

## Ranking (records that persist under PG statistics with the same category)

| category | records |
|---|---|
| qual-placement | 2 (SF0.25 Q78; SF1 Q72) |
| scan-type | 2 (SF1 Q53, Q63) |
| join-order | 2 (SF1 Q77, Q78) |
| sort-strategy | 1 (SF0.25 Q4) |
| join-method | 1 (SF0.25 Q69) |
| aggregation-strategy | 1 (SF1 Q23) |

The three-way tie at 2 goes to qual-placement through Q78. Q78 diverges at depth 2 at both scales: qual-placement at
SF0.25 and join-order at SF1. Both records are the same swap of the two outer joins, so one mechanism serves two of
the tied categories, and its SF0.25 record is preferred.

## Filed

**M0146-0224**: TPC-DS SF0.25 Q78, qual-placement, SHAPE-DIFF at depth 2 (under Sort: PG Merge Left Join | goopg Merge
Left Join).

The query is `ss LEFT JOIN ws … LEFT JOIN cs …` over three grouped CTEs.
- PG commutes the outer joins: `(ss ⟕ cs) ⟕ ws`, top join 42027.86..42481.11, with the COALESCE filter on the ws join.
- goopg keeps `(ss ⟕ ws) ⟕ cs`, top join 42027.89..42542.55, which is 61 dearer than PG's plan.

At SF1 the same swap appears under an Incremental Sort (PG 137297.41..137328.58 with ws last; goopg
137281.75..137313.75 with cs last, rows 310 against PG's 624).
