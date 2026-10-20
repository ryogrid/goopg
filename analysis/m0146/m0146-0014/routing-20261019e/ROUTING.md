# M0146-0014 — S9 pass at 4906691d7 (2026-10-19): files M0146-0229

Trigger: no item 0-9 task is selectable and actionable.
- M0146-0228 closed at 4906691d7 (Q23 statement 1).
- M0146-0214 is blocked-on M0146-0216.
- M0146-0215 and M0146-0218 wait on the owner's stats import.
- M0146-0216 waits on the owner's index refresh.
- M0146-0042 has no stock.

Captures (all on 4906691d7's code tree):
- Shipped TPC-DS corpora: M0146-0228's fire-set candidate captures, `analysis/m0146/m0146-0228/fireset3/`, with
  their PG arms.
- TPC-H: `analysis/m0146/m0146-0228/census3/m28c-tpch*`.
- `imp-*`: PG-statistics clones on the HEAD binary (`tmp/s9-imp/run.sh`). 474/474 restores returned true at SF0.25 and
  478 at SF1; the SF1 errors are the reference's stray `public.bt` table. The SF1 clone is M0146-0228's census3 capture.

| corpus | match |
|---|---|
| TPC-H | 22 / 22 |
| TPC-DS SF0.25 | 92 / 99 |
| TPC-DS SF1 | 82 / 99 |
| TPC-DS SF0.25, PG statistics | 97 / 99 |
| TPC-DS SF1, PG statistics | **94 / 99** |

## Exclusions

- Live tasks: Q64 (M0146-0214), Q76 (M0146-0215), SF1 Q87 (M0146-0218).
- Import-routed: a shipped record that disappears, or changes category, under PG statistics.
  - SF0.25: Q38, Q69, Q87, Q99, and Q77 (aggregation-strategy changes to sort-strategy).
  - SF1: Q4, Q7, Q11, Q18, Q26, Q31, Q38, Q48, Q56, Q62, Q99, and Q13 (qual-placement changes to join-method).

## Ranking (records that persist under PG statistics with the same category)

| category | records |
|---|---|
| sort-strategy | 1 (SF1 Q23) |
| qual-placement | 1 (SF1 Q72) |
| join-order | 1 (SF1 Q77) |

Three-way tie, all at SF1. Q23 goes first: it has the lowest query id and the shallowest first divergence
(depth 1).

## Filed

**M0146-0229**: TPC-DS SF1 Q23, sort-strategy, SHAPE-DIFF at depth 1 (under Limit: PG Incremental Sort | goopg Sort).
It persists under PG statistics. The divergence is in query23's second statement.

- PG (Limit 336628.68..336635.75): an Incremental Sort (presorted key c\_last\_name, c\_first\_name) over a Merge
  Append of the UNION ALL's two GroupAggregates, each already ordered by its group key.
- goopg (Limit 336691.00..336691.25 under PG statistics): a full Sort over a plain Append of the same two
  GroupAggregates.
- The branch estimates differ as well: PG 173 + 126 = 299 rows, goopg 150 + 76 = 226.
