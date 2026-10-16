# M0146-0014 — S9 pass at e3a7ae742 (2026-10-17): files M0146-0222

Trigger: every item 0-9 task is gated:
- M0146-0214 is blocked-on M0146-0216;
- M0146-0215 and M0146-0218 wait on the owner's stats import;
- M0146-0216 waits on the owner's index refresh.

Captures: M0146-0221 slice 2's `analysis/m0146/m0146-0221/census2/`, built from e3a7ae742's code. That commit adds
only tests on top. The `imp-*` captures are private clones with the reference's statistics imported.

| corpus | match |
|---|---|
| TPC-H | 22 / 22 |
| TPC-DS SF0.25 | 88 / 99 |
| TPC-DS SF1 | 76 / 99 |
| TPC-DS SF0.25, PG statistics | 92 / 99 |
| TPC-DS SF1, PG statistics | 90 / 99 |

## Exclusions

- Live tasks: Q64 (M0146-0214), Q76 (M0146-0215), SF1 Q87 (M0146-0218).
- Import-routed: a shipped record whose first divergence disappears or moves under PG's statistics.
  - SF0.25: Q38, Q87 (M0146-0217); Q99; Q77's aggregation record, which moves to sort-strategy at depth 8.
  - SF1: Q4, Q7, Q11, Q18, Q26, Q31, Q38, Q48, Q56, Q62, Q69, Q99; Q13's qual-placement record, which moves to
    join-method.

## Ranking (records that persist under PG statistics)

| category | records |
|---|---|
| sort-strategy | 3 (SF0.25 Q4, Q72; SF1 Q72) |
| parallelism | 2 (Q39 at both scales) |
| join-order | 2 (SF1 Q77, Q78) |
| scan-type | 2 (SF1 Q53, Q63) |
| join-method | 1 (SF0.25 Q69) |
| qual-placement | 1 (SF0.25 Q78) |
| aggregation-strategy | 1 (SF1 Q23) |

Within sort-strategy, Q72 is one mechanism at both scales, so its SF0.25 record is filed.

## Filed

**M0146-0222**: TPC-DS SF0.25 Q72, sort-strategy, SHAPE-DIFF at depth 3.

- PG: `GroupAggregate → Nested Loop Left → Nested Loop → Gather Merge → Sort`. The ordering is established below the
  joins, inside the parallel part, and preserved through them.
- goopg: `GroupAggregate → Sort → Nested Loop Left → …`, sorting once at the top.
- goopg's plan is cheaper by its own costing: 15267.53 against PG's 15490.83. So either PG never builds goopg's shape,
  or PG would price it higher than goopg does.
