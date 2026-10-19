# M0146-0014 — S9 pass at 18d2c73b0 (2026-10-19): files M0146-0227

Trigger: no item 0-9 task is selectable and actionable.
- M0146-0226 closed at c65fb3855 (SF0.25 Q4 and SF1 Q64 MATCH).
- M0146-0214 is blocked-on M0146-0216.
- M0146-0215 and M0146-0218 wait on the owner's stats import.
- M0146-0216 waits on the owner's index refresh.
- M0146-0042 has no stock: rendering=0 at both scales.

Captures:
- Shipped corpora (all three): M0146-0226's `analysis/m0146/m0146-0226/census/` (m26), captured on c65fb3855's code
  tree. 18d2c73b0 is a fix_plan note only.
- `imp-*`: this pass's PG-statistics clones on the HEAD binary (`tmp/s9-imp/run.sh`). 474/474 restores returned
  true at SF0.25 and 478 at SF1; the 3 SF1 errors are the reference's stray `public.bt` table.
- TPC-DS PG arms: census4's.

| corpus | match |
|---|---|
| TPC-H | 22 / 22 |
| TPC-DS SF0.25 | 92 / 99 |
| TPC-DS SF1 | 81 / 99 |
| TPC-DS SF0.25, PG statistics | 96 / 99 |
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
| join-method | 1 (SF0.25 Q69) |
| aggregation-strategy | 1 (SF1 Q23) |
| qual-placement | 1 (SF1 Q72) |
| join-order | 1 (SF1 Q77) |

Four-way tie; S9(3) prefers the SF0.25 record, Q69.

## Filed

**M0146-0227**: TPC-DS SF0.25 Q69, join-method, SHAPE-DIFF at depth 3 (under Sort: PG Hash Right Anti Join | goopg
Nested Loop Anti Join).

- PG (43130.36): Hash Right Anti Join on `catalog_sales.cs_ship_customer_sk = c.c_customer_sk` at the top. Its
  inner is `customer ⋈ EXISTS(store_sales)`, and the web_sales NOT EXISTS is a nested Hash Right Anti Join below.
- goopg (43183.05): Nested Loop Anti Join with Join Filter `c.c_customer_sk = web_sales.ws_bill_customer_sk` at the
  top. The catalog_sales anti join sits below it as a Hash Right Anti Join.
- goopg's plan is 52.69 dearer than PG's.
- The EXISTS leg already differs: the Nested Loop over the store_sales HashAggregate costs 19132.67..19430.53 in
  goopg and 19132.67..19681.46 in PG.
