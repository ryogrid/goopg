# M0146-0009p — a parameterised probe's rows ignore its movable join clauses (held)

Status: landed 2026-10-13 (`b27c58316`) after the hold lifted. Parent:
M0146-0009. Filed by M0146-0005 slice 115. Held 2026-10-04 to 2026-10-13 on
the index-probe cost divergence (B8, M0145-0008ag). The SF1 Q72 regression
it causes is kept per AGENT.md R3 and filed as M0146-0172.

## The divergence

TPC-DS Q72's `inventory_pkey` probe reports rows=527, three times PG's 176.
The cause is `get_parameterized_baserel_size` (`./postgres/src/backend/optimizer/path/costsize.c`):

- PG takes `clauselist_selectivity` over every clause movable into the
  parameterised path (`ppi_clauses`).
- That includes `inv_quantity_on_hand < cs_quantity`, which binds no index
  key; PG gives it the default 1/3.
- goopg's parameterised producers (index, skip and bitmap) multiply only the
  selectivity of the key equalities (`parameterizedIndexSelectivity`).

On a repro (`pr_inv` probed by `item`, filter `qty < c.qty`), PG reports
rows=10 for the probe, and goopg reported 30.

## The fix that was tried

`movableNonEquiJoinSelectivity` multiplies the rows of every parameterised
index, skip and bitmap path by each movable join clause that is not an
equijoin (`join_clause_is_movable_into`). The repro probe then reads rows=10.
The patch, with its test `TestParameterizedProbeRowsApplyMovableJoinClauses`
(which fails on HEAD), is
`analysis/m0146/m0146-0009p/ppi-rows-movable-clauses.wip.patch`.

| gate | result |
|---|---|
| units, tpch-spotcheck | pass |
| sf025 sweep | 96/96 |
| TPC-H arm | 24/24 |
| ea-ratchet | pass |
| fire set | **failed** |

The fire set fired 17 queries with matches and categories flat at SF0.25.
At SF1, Q72 dropped two categories (scan-type, qual-placement), but its new
plan **timed out** (`introduced=Q72`). The plans are in
`analysis/m0146/m0146-0009p/`.

## Why it timed out

With PG's 1/3 applied, the item-only `inventory_pkey` probe (rows 236)
followed by a Memoized `date_dim d2` probe became cheaper in goopg's model
than the route PG elects. PG first hash-joins `d2`, then probes inventory on
(date, item), rows=1. goopg prices that route higher than PG at both steps:

| step | PG | goopg |
|---|---|---|
| the (date, item) probe | 0.43..5.98, rows=1 | 0.38..10.42, rows=3 |
| the `{cs, hd, d1, cd}` subtree under the hash | 44279 | 47926 |

With rows estimated at 58 for that subtree, the item-only route expands
to tens of millions of probe rows at execution.

The first gap is the index-probe cost divergence tracked as B8
(M0145-0008ag, an owner-parked multiplier). Landing PG's row count before
the probe costs agree turns a correct estimate into a worse election.

## Resume

Re-apply the patch once M0145-0008ag (or another fix of the probe-cost gap)
lands, and re-run the fire set. Q72 at SF1 is the witness.

## Landed (2026-10-13)

The hold lifted once M0146-0159 retired the x2 multiplier (`de45f8a6f`) and
M0145-0008ag closed on its re-measure. The patch re-applied unchanged.

- **The first gap is closed.** goopg prices Q72's (date, item)
  `inventory_pkey` probe at 0.38..5.42, against PG's 0.43..5.98.
- **Fire set** (HEAD `db31b0600` vs the patch,
  `analysis/m0146/m0146-0009p/landed-fireset-summary.txt`):
  - It fired 17 queries at SF0.25 and 16 at SF1.
  - MATCH is flat at 68 and 45.
  - SF1 parameterisation 26 → 25; every other category is flat.
  - No timeout was introduced.
- **Values.** sf025 PASS=99, and the TPC-H acceptance arm is identical.
  tpch-spotcheck, units, postmaster and ea-ratchet pass.
- **SF1 Q72: 3.3s → 216s (×65)**, confirmed by two alternating repeats on
  fresh clones (`landed-q72-sf1-*.plan.txt`).
  - The second gap is still open: goopg prices the {cs, hd, d1, cd}
    subtree under the `d2` hash at 47,899, PG at 44,279.
  - With PG's rows on the probe, goopg's item-only route (the
    `inventory_pkey` probe on `inv_item_sk` alone, rows=236, then the
    date-dimension joins over 13,819 rows) costs 54,874. That undercuts
    goopg's own PG-shaped route (55,265), and it executes tens of millions
    of probe rows.
  - The estimate is PG-faithful, so the change stays (R3). The election is
    M0146-0172's.
  - SF0.25 Q72 is unchanged (25–29s in the sweep).
