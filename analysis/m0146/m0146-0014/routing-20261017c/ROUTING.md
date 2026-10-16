# M0146-0014 — S9 pass at 472e41064 (2026-10-17): files M0146-0223

Trigger: every item 0-9 task is gated:
- M0146-0214 is blocked-on M0146-0216;
- M0146-0215 and M0146-0218 wait on the owner's stats import;
- M0146-0216 waits on the owner's index refresh.

Captures: M0146-0222 slice 3's `analysis/m0146/m0146-0222/census3/` (472e41064's code; that commit adds only tests on
top), plus this pass's PG-statistics clones (`imp-*`, HEAD binary, all restore calls true).

| corpus | match |
|---|---|
| TPC-H | 22 / 22 |
| TPC-DS SF0.25 | 89 / 99 |
| TPC-DS SF1 | 76 / 99 |
| TPC-DS SF0.25, PG statistics | **93 / 99** |
| TPC-DS SF1, PG statistics | 90 / 99 |

## Exclusions

- Live tasks: Q64 (M0146-0214), Q76 (M0146-0215), SF1 Q87 (M0146-0218).
- Import-routed: a shipped record that disappears, or changes category, under PG statistics.
  - SF0.25: Q38, Q87, Q99, Q77.
  - SF1: Q4, Q7, Q11, Q13, Q18, Q26, Q31, Q38, Q48, Q56, Q62, Q69, Q99.

## Ranking (records that persist under PG statistics with the same category)

| category | records |
|---|---|
| parallelism | 2 (Q39 at both scales) |
| qual-placement | 2 (SF0.25 Q78; SF1 Q72) |
| scan-type | 2 (SF1 Q53, Q63) |
| join-order | 2 (SF1 Q77, Q78) |
| sort-strategy | 1 (SF0.25 Q4) |
| join-method | 1 (SF0.25 Q69) |
| aggregation-strategy | 1 (SF1 Q23) |

The four-way tie at 2 goes to parallelism: Q39 is one mechanism at both scales, and its SF0.25 record is preferred.

## Filed

**M0146-0223**: TPC-DS SF0.25 Q39, parallelism, SHAPE-DIFF at depth 4 (under HashAggregate: PG Hash Join Inner |
goopg Gather). In the `inv` CTE:
- PG runs `Hash Join (item) → Gather → Hash Join (warehouse) → Parallel Hash Join (inventory, date_dim)`, joining
  `item` above the Gather.
- goopg puts the `item` hash join inside the workers: `Gather → Hash Join (warehouse) → Hash Join (item) → …`.

goopg's `item_pkey` Index Only Scan is 478.29 against PG's 531.08.
