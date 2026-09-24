# M0145-0008aa: a non-simple ANY body is pulled up as one derived semi-side leaf

Status: **LANDED 2026-09-25** (`9e99ea944`), with one part held back.
- **Landed:** the derived-body arm.
- **Held back:** the CTE-leaf promotion. It failed the SF1 fire-set gate
  (see "Held back" below) and is filed as M0145-0008ac.

Task: `.ralph/fix_plan.md` M0145-0008aa (Kind: impl, Parent: M0145-0008z).
Evidence: `analysis/m0145/m0145-0008aa/`.

## What PG does

`convert_ANY_sublink_to_join`
(`postgres/src/backend/optimizer/plan/subselect.c:1333`) gates only on:
- which parent rels the sub-select references;
- whether the testexpr references the parent;
- whether the testexpr is volatile.

It never asks whether the body is simple. The sub-select becomes a subquery
RTE aliased `ANY_subquery` (subselect.c:1390-1401) on the semi side of a new
`JOIN_SEMI`, and the testexpr becomes the join qual. `pull_up_subqueries`
flattens that RTE later only when `is_simple_subquery` admits it
(`postgres/src/backend/optimizer/prep/prepjointree.c:1143`). Otherwise the
subquery stays one rel in the join search.

A body that references parent Vars becomes a LATERAL RTE (`use_lateral`,
subselect.c:1357).

## What goopg did

`pullUpAnyBody` (`internal/optimizer/jointreepullup.go`) flattens a body into
its own base-relation leaves. It therefore declined every body that
`sublinkBodyIsSimple` refuses (`any-body-not-simple`), and the statement took
the post-hoc unnest. TPC-H Q18's `l_orderkey IN (SELECT … GROUP BY … HAVING …)`
is the corpus witness (M0145-0008z).

## What changed

- `pullUpAnyBody` runs the testexpr gates first (volatility, a sublink in the
  operand). It then hands a non-simple body to `pullUpAnyDerivedBody`.
- `pullUpAnyDerivedBody` plans the body the way goopg plans a FROM-clause
  subquery — goopg's subquery RTE — through `planFromClause` over the
  one-item FROM list `(<body>) AS ANY_subquery`. The result is ONE leaf:
  - `jtPulledBody.derived` is set on it;
  - its one link qual is `operand = ANY_subquery.col0`, synthesised exactly
    as the flat arm synthesises its link.

  It declines:
  - a body whose plan still references the parent
    (`any-derived-body-correlated`). This is PG's LATERAL case; goopg's
    search builds no parameterised path for a derived leaf.
  - a body with more than one output column.
- `seamLeafBinding` (`internal/optimizer/joinsearchseam.go`) admits a pulled
  leaf whose body is `derived`. It drops the binding's synthetic
  `*catalog.Table` so that `seamLeafRelInfo` prices the leaf from its plan
  (`EstimateRows`), as it does a CTE leaf.
- `assignPulledSourceOffsets` shifts a derived leaf's `SourceTableIdx` when
  `remapSourceTableIdx` can walk the leaf's plan. It cannot walk `Distinct`
  or set-operation nodes; such a leaf keeps offset 0. The shift only keeps
  EXPLAIN names apart: the leaf is its own scope and the link reads it by
  position. That is also what the post-hoc route does with the same plan
  (ledgered).

## Measured

- **TPC-H fire set (parallel, private clone):** Q18 only.
  - PLAN-PARITY match 3 → 3; `aggregation-strategy` 4 → 3.
  - Q18's join order is now closer to PG's. The semi join sits under the
    `orders ⋈ customer` join, joining `orders` to the aggregated body first,
    as PG's `orders ⋈ HashAggregate(lineitem)` does.
    - Before: `analysis/m0145/m0145-0008aa/q18-baseline.plan.txt`.
    - After: `q18-candidate.plan.txt`.
    - PG: `q18-candidate-pg.plan.txt`.
- **Q18 estimate got worse.** The candidate estimates 3.0M rows at cost
  1.52M; the baseline estimated 539K at 411K, and PG estimates 470K at 404K.
  - goopg keeps a Hash Semi Join over the derived leaf, and its selectivity
    keeps 750K of the 1.5M `orders` rows.
  - PG unique-ifies the grouped side (`create_unique_path`, whose
    `query_is_distinct_for` proves the grouped body distinct, so the unique
    path costs nothing extra). It then estimates an **inner** Hash Join
    against 117,504 groups.
  - goopg's `createUniquePath` is reachable only for the legacy
    `PathPrebuilt` atomic RHS and has no distinct-subquery fast path. Filed
    as **M0145-0008ab**.
- **Q18 runtime** (acceptance arm): 7.06 s → 5.89 s.
- **TPC-DS fire set:** no changed query at SF0.25 or SF1. The derived arm
  fires on no TPC-DS query; Q14/Q23/Q95 are simple bodies over a CTE and
  stay declined by the CTE-leaf gate.

## Held back: promoting the CTE-leaf admission

The task also covered TPC-DS Q14/Q23, whose simple bodies read a CTE. That
admission already exists behind `GOOPG_PULLUP_CTE_LEAF` (M0145-0011 scope (c);
the seam half is M0145-0013). The first candidate promoted it
unconditionally.
- **SF0.25:** values held (`MISMATCH=0`), but Q95 went 3 s → 16 s.
- **SF1 fire set:** Q95 **TIMEOUT, introduced**, so the gate FAILs.

PG's own Q95 plan has the same shape:
- nested-loop semi joins with the `ws_wh` CTE scan on the inner side;
- but its inner `web_returns` access is an Index Only Scan **parameterised
  by the outer `ws1.ws_order_number`**, pushed through a hash join.

goopg builds no parameterised path through a join, so the same shape
rescans a 3M-row hash join per outer row.

The change is PG-faithful and is not discarded (R3). The combined diff is
preserved at
`analysis/m0145/m0145-0008aa/combined-derived-arm-plus-cte-leaf-promotion.patch`,
and the promotion is filed `[!]` as **M0145-0008ac**, blocked on
parameterised inner paths (the M0146-0012 family), with an escalation block.

## Tests

`internal/optimizer/pullup_any_derived_test.go`:
- `TestPullUpAnyDerivedBody`: GROUP BY+HAVING, DISTINCT, ORDER BY+LIMIT and
  UNION bodies take the jointree route into a searched semi join.
- `TestPullUpAnyDerivedBodyDeclines`: a correlated grouped body keeps the
  post-hoc route.
- `TestPullUpAnyDerivedBodyRecord`: the record is one leaf, flagged
  `derived`, aliased `ANY_subquery`, with the link `OuterColumnRef = col0`.

A probe against a private PG 18.3 instance gave identical values on grouped,
CTE, DISTINCT+LIMIT, UNION and correlated bodies.

## Gates (staged code `9e99ea944`)

- units: PASS.
- `tpch-spotcheck`: PASS.
- acceptance arm: 24 MATCH against the M0145-0008u arm.
- `tpcds-sf025`: 96 PASS, `MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0`.
- fire-set gate (SF0.25 + SF1): PASS, fires none.
- TPC-H fire set: fires Q18, no timeouts.
- lineage guard: OK.
- pgbench smoke: PASS.

Movement: none. TPC-H match 3 → 3, and `aggregation-strategy` 4 → 3 is
inside the ±3 noise band.

## Not ported (ledgered)

- Correlated (LATERAL) derived bodies.
- The unique-ify of a derived semi-side leaf, and `query_is_distinct_for`
  (M0145-0008ab).
- `remapSourceTableIdx` has no `Distinct` or set-operation arms, so a derived
  leaf over them can print colliding EXPLAIN names (`o.ok = o.ok`).
- The CTE-leaf promotion (M0145-0008ac).
