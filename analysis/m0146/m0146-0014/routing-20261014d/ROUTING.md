# M0146-0014 — S9 parity replenishment census, 2026-10-14 (fourth pass)

Capture sha: `9129d26f0`, HEAD at capture, after M0146-0193 and the M0146-0042 Q58 slice. Captures: the TPC-DS
fire-set gate at both scales and `scripts/jointree-parity-capture.sh tpch`. First-divergence records come from
`scripts/pg-plan-first-divergence.py`.

| corpus | match | text-identical among MATCH |
|---|---|---|
| TPC-DS SF0.25 | 81 / 99 | 80 / 81 (Q59 differs) |
| TPC-DS SF1 | 66 / 99 | 65 / 66 (Q59 differs) |
| TPC-H | 14 / 22 | — |

Item 3 had nothing workable first:

- M0146-0042's only text record left is Q59's hash-clause order. That is EquivalenceClass creation order in
  `build_joinrel_restrictlist`, which the 2026-10-13 ledger row defers as plan state (executor hash-key order,
  merge pathkeys), outside the text-only scope.
- Q78's rendering rides a COSTTIE join-order swap.

## Ranking (S9 step 3)

Two classes are removed before counting:

- **Waived records:** RELPAGES, GEQO-RNG and COSTTIE (Q4, Q38, Q87, Q52, Q77, Q78, Q99), plus STALE-SF1 (SF1
  records whose query matches at SF0.25).
- **Records measured as ties or residuals since the last pass:**
  - Q16: its margin is the catalog_sales seq scan cost, 10521 vs 10578 (RELPAGES).
  - Q39: item_pkey IOS 430 vs 531 (M0146-0178).
  - Q45: 0.14 vs 0.13 (M0146-0190 ledger).
  - Q61: 36092 vs 36096.
  - Q69 and Q76: their PG plans differ between tmp/pgdbg and :65438 (40 and 15 diff lines, files here), so they
    are sample-sensitive ties.

Actionable counts after that:

| category | count | records |
|---|---|---|
| sort-strategy | 3 | Q72 at SF0.25 and SF1; TPC-H Q9 (a join row-estimate gap) |
| qual-placement | 3 | SF1 Q69 (sample-sensitive at SF0.25); TPC-H Q20 and Q21 |
| join-method | 1 | TPC-H Q18 |
| join-order | 2 | Q19; TPC-H Q10 |

The TPC-H records sit on loads with different statistics. The goopg :65433 HammerDB load has 6001988 lineitem
rows; PG's has 5998990.

## Filed

**M0146-0194 — TPC-DS SF0.25 Q72** (`depth=3 [sort-strategy] under GroupAggregate: PG Nested Loop Left | goopg
Sort`).

- The record is stable on tmp/pgdbg, so it is not a sample tie.
- The margin is 15196 vs 15490 (2%), outside the fuzz.
- PG keeps the item, warehouse and inventory joins inside a Gather Merge sorted on the group keys, and its
  GroupAggregate reads the presorted Nested Loop Left. goopg gathers lower, joins item and the rest serially above
  the Gather, and sorts for the aggregate.
