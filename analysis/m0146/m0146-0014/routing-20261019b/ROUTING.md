# M0146-0014 — S9 pass at fe64139d5 (2026-10-19): files M0146-0226

Trigger: no item 0-9 task is selectable and actionable.
- M0146-0225 closed at fe64139d5 (SF1 Q53/Q63 MATCH).
- M0146-0214 is blocked-on M0146-0216.
- M0146-0215 and M0146-0218 wait on the owner's stats import.
- M0146-0216 waits on the owner's index refresh.
- M0146-0042 has no stock: rendering=0 at both scales.

Captures:
- Shipped TPC-DS: M0146-0225's `analysis/m0146/m0146-0225/census/` (m25). Both scales were captured on
  fe64139d5's code tree.
- TPC-H: census4 (`analysis/m0146/m0146-0224/census4/`). fe64139d5 only changes statements with a window function,
  and TPC-H has none.
- `imp-*`: this pass's PG-statistics clones on the fe64139d5 binary (`tmp/s9-imp/run.sh`). 474/474 restores
  returned true at SF0.25 and 478 at SF1; the 3 SF1 errors are the reference's stray `public.bt` table.
- PG arms: census4's.

| corpus | match |
|---|---|
| TPC-H | 22 / 22 |
| TPC-DS SF0.25 | 91 / 99 |
| TPC-DS SF1 | 80 / 99 |
| TPC-DS SF0.25, PG statistics | 95 / 99 |
| TPC-DS SF1, PG statistics | **94 / 99** |

## Exclusions

- Live tasks: Q64 (M0146-0214), Q76 (M0146-0215), SF1 Q87 (M0146-0218).
- Import-routed: a shipped record that disappears, or changes category, under PG statistics.
  - SF0.25: Q38, Q87, Q99, and Q77 (aggregation-strategy changes to sort-strategy).
  - SF1: Q4, Q7, Q11, Q18, Q26, Q31, Q38, Q48, Q56, Q62, Q69, Q99, and Q13 (qual-placement changes to
    join-method).

## Ranking (records that persist under PG statistics with the same category)

| category | records |
|---|---|
| sort-strategy | 1 (SF0.25 Q4) |
| join-method | 1 (SF0.25 Q69) |
| aggregation-strategy | 1 (SF1 Q23) |
| qual-placement | 1 (SF1 Q72) |
| join-order | 1 (SF1 Q77) |

Five-way tie. S9(3) prefers the SF0.25 record, which leaves Q4 and Q69. Q4 goes first: it has the lower query id
and the shallower first divergence (depth 1, directly under the Limit).

## Filed

**M0146-0226**: TPC-DS SF0.25 Q4, sort-strategy, SHAPE-DIFF at depth 1 (under Limit: PG Incremental Sort | goopg
Sort). The same record persists under PG statistics.

- PG (Presorted Key t_s_secyear.customer_id) runs a chain of Nested Loops over a Merge Join of two sorted CTE scans
  (`Sort → CTE Scan on year_total t_s_secyear` and `t_c_secyear`, 30 rows each), and finishes with an Incremental
  Sort.
- goopg chains Nested Loops over unsorted CTE scans and finishes with a full Sort.
- The inputs already differ under identical statistics:
  - CTE scan estimates: goopg 9 and 29 rows against PG's 10 and 30.
  - The year_total HashAggregate: goopg 46822.64..55046.66 against PG 53401.85..61625.87, over the same input
    (Hash Join 8189.60..32019.42).
