# M0146-0014 — S9 pass at 8465f5e72 (2026-10-19): files M0146-0228

Trigger: no item 0-9 task is selectable and actionable.
- M0146-0227 closed at 8465f5e72 (Q69).
- M0146-0214 is blocked-on M0146-0216.
- M0146-0215 and M0146-0218 wait on the owner's stats import.
- M0146-0216 waits on the owner's index refresh.
- M0146-0042 has no stock.

Captures:
- Shipped corpora (all three): M0146-0227's `analysis/m0146/m0146-0227/census2/` (m27b), captured on 8465f5e72's
  code tree.
- `imp-*`: this pass's PG-statistics clones on the HEAD binary (`tmp/s9-imp/run.sh`). 474/474 restores returned
  true at SF0.25 and 478 at SF1; the 3 SF1 errors are the reference's stray `public.bt` table.
- TPC-DS PG arms: census4's.

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
| aggregation-strategy | 1 (SF1 Q23) |
| qual-placement | 1 (SF1 Q72) |
| join-order | 1 (SF1 Q77) |

Three-way tie, all at SF1. Q23 goes first: it has the lowest query id and the shallowest first divergence
(depth 2).

## Filed

**M0146-0228**: TPC-DS SF1 Q23, aggregation-strategy, SHAPE-DIFF at depth 2 (under CTE: PG Finalize HashAggregate |
goopg HashAggregate). It persists under PG statistics.

- PG (189437.85..191187.85): `best_ss_customer` is a Finalize HashAggregate over a Gather of four workers' Partial
  HashAggregates over a Parallel Hash Join (store_sales ⋈ customer).
- goopg (179918.25..181501.58): a plain HashAggregate over a serial Hash Join (Seq Scan on store_sales ⋈ Index Only
  Scan on customer_pkey).
- The CTE's HAVING compares the sum against `0.95 * (InitPlan 2).col1`. That is a candidate reason for goopg not
  offering the partial arm. The other possibility is that goopg prices its serial arm lower than PG would.
