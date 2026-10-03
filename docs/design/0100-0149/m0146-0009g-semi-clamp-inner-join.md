# M0146-0009g — a semijoin's selectivity is clamped to the inner join's

Status: done (2026-10-03, `d8c1d2f8a`). Parent: M0146-0009 (banner item 3,
interleaveable statistics/cardinality work).

## The question

TPC-DS Q23's `cs_item_sk IN (SELECT item_sk FROM frequent_ss_items)` gets a
semi fraction of 4582/15993 = 0.2865 in PG 18.3 when the grouped CTE has a
HAVING, and 0.5 without one. goopg gave 0.5 in both cases.

## How it was resolved

A private build of PG 18.3 (`tmp/pg18-optdebug`, the M0144-0004 tree,
never `./postgres`) got an `elog(LOG)` before and after
`eqjoinsel_semi`'s nd2 clamps. On its TPC-DS SF0.25 clone, variant A
(HAVING) and variant B (no HAVING) logged the same thing:

    nd1=16224 nd2=200 isdef1=0 isdef2=1 var2rel=4527 inner=4527   -> nd2 stays 200, isdefault
    nd1=16224 nd2=200 isdef1=0 isdef2=1 var2rel=13580 inner=13580 -> same

So `eqjoinsel_semi` (`./postgres/src/backend/utils/adt/selfuncs.c:2642`)
punts to 0.5 for both. The multi-key GROUP BY stops
`examine_simple_variable`, so nd2 is the default 200. The patch was
reverted and the tree rebuilt.

The split comes from `eqjoinsel`'s SEMI/ANTI arm
(`selfuncs.c:2280`; the clamp is at `:2417`):

    selec = Min(selec, inner_rel->rows * selec_inner);

PG's comment explains the clamp: a semijoin cannot yield more rows than the
inner join over the same inputs (N1 * Ssemi <= N1 * N2 * Sinner).
`selec_inner` is `eqjoinsel_inner` over the unclamped nds, computed "in
all cases" before the jointype switch.

- With HAVING the CTE is 4582 rows, and 4582/15993 is below the 0.5 punt.
- Without HAVING it is 13746 rows, and 13746/15993 is above it.

## What landed

Both siblings clamp at PG's level: after the `eqjoinsel_semi` port, whose
own tests still pin it alone.

- **Search:** `joinClauseSelectivityForJoin`'s `=` arm (joinselectivity.go),
  with `Sinner = eqJoinSelectivityExt`, goopg's inner equality arm.
- **Plan node:** `semiJoinMatchFraction` (cardinality.go), per pair, with
  `Sinner = semiPairInnerSelectivity`. That is the MCV inner arm when both
  sides have lists, else `(1-nf1)(1-nf2)/max(nd1, nd2)`.

## Verification

- `TestSemiJoinSizeClampedByInnerJoin`: a grouped-join CTE whose grouping
  key is an expression, so neither engine has statistics for it.
  - HEAD gives 50000 (the punt), goopg now 15000, PG 18.3 15009.
- Fire set: Q10, Q23, Q35, Q56, Q60 and Q69 fire, with no timeouts.
  - SF1 match 28 → 29: Q69 now matches PG.
  - SF0.25 match 39 → 38: Q56 lost its match on a Sort-over-Gather
    election. Kept per R3 and filed as M0146-0009l.
- ea-ratchet 10/10; TPC-H arm PASS; SF0.25 sweep 96/96; spotcheck; units.

## Residuals (ledgered)

- `eqJoinSelectivityExt` is the no-MCV inner arm, so the search's
  `Sinner` lacks `eqjoinsel_inner`'s MCV matching. The plan-side twin
  has it.
- goopg's CTE-output synthesis (`cteSynthNDistinct`, M0145-0009) gives a
  grouped CTE column an nd where PG's `examine_simple_variable` stops at a
  multi-key GROUP BY. A plain-column grouping key therefore still diverges
  from PG's punt, independently of this clamp.
