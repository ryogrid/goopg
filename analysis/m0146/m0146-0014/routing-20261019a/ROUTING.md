# M0146-0014 — S9 pass at 0d94a7133 (2026-10-19): files M0146-0225

Trigger: no item 0-9 task is selectable and actionable.
- M0146-0224 closed at 0d94a7133 (Q78 MATCH at both scales).
- M0146-0214 is blocked-on M0146-0216.
- M0146-0215 and M0146-0218 wait on the owner's stats import.
- M0146-0216 waits on the owner's index refresh.
- M0146-0042 has no stock: census4 shows rendering=0 at both scales.

Captures:
- Shipped corpora: M0146-0224's `analysis/m0146/m0146-0224/census4/` (m24d). It was captured on code tree e6d93c0b,
  which is HEAD's code; 8ea3190a7 and 0d94a7133 add no other planner change.
- `imp-*`: this pass's PG-statistics clones. Each is an offline `cp -a` of the shipped corpus running the HEAD binary
  on a private 559x port (`tmp/s9-imp/run.sh`). The restores ran the `pg_restore_*_stats` calls from the reference's
  `pg_dump --statistics`: 474/474 true at SF0.25, and 478 true at SF1. The 3 SF1 errors are a stray `public.bt`
  table in the reference that is not part of the corpus.
- First-divergence files come from `scripts/pg-plan-first-divergence.py`. The differ's `--verbose` output is in
  `verbose/`.

| corpus | match |
|---|---|
| TPC-H | 22 / 22 |
| TPC-DS SF0.25 | 91 / 99 |
| TPC-DS SF1 | 78 / 99 |
| TPC-DS SF0.25, PG statistics | 95 / 99 |
| TPC-DS SF1, PG statistics | **92 / 99** |

## Exclusions

- Live tasks: Q64 (M0146-0214), Q76 (M0146-0215), SF1 Q87 (M0146-0218).
- Import-routed: a shipped record that disappears, or changes category, under PG statistics.
  - SF0.25: Q38, Q87, Q99, and Q77 (aggregation-strategy changes to sort-strategy).
  - SF1: Q4, Q7, Q11, Q18, Q26, Q31, Q38, Q48, Q56, Q62, Q69, Q99, and Q13 (qual-placement changes to
    join-method).

## Ranking (records that persist under PG statistics with the same category)

| category | records |
|---|---|
| scan-type | 2 (SF1 Q53, Q63) |
| sort-strategy | 1 (SF0.25 Q4) |
| join-method | 1 (SF0.25 Q69) |
| aggregation-strategy | 1 (SF1 Q23) |
| qual-placement | 1 (SF1 Q72) |
| join-order | 1 (SF1 Q77) |

scan-type leads alone. Q53 and Q63 diverge at the same node: depth 9, under a Memoize, on the probe into `store`.

## Filed

**M0146-0225**: TPC-DS SF1 Q53 (and Q63), scan-type, SHAPE-DIFF at depth 9 under Memoize.
- PG: `Index Only Scan using store_pkey on store (cost=0.14..0.15 rows=1 width=4)`.
- goopg: `Index Scan using store_pkey on store (cost=0.14..0.15 rows=1 width=1812)`.

The costs are identical and goopg's width is the whole row. Either goopg never builds the index-only path for this
inner relation, because its attribute set is not narrowed to `s_store_sk`, or it builds it and loses a cost tie that PG
resolves toward the index-only path. Both records persist under PG statistics. SF0.25 Q53 and Q63 already MATCH, so
the SF1 record is the one to drive.
