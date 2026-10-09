# M0146-0108 — a NestLoop param over an inlined CTE deparses into its body

Status: done 2026-10-08 (8b1e04c2b). Parent: M0146-0042.

## Problem

TPC-DS Q64's `cross_sales` CTE probes store\_returns with a key from
`cs_ui`, a single-reference CTE:

```
goopg:  Index Cond: (sr_item_sk = cs_ui.cs_item_sk)
PG:     Index Cond: (sr_item_sk = catalog_sales.cs_item_sk)
```

M0146-0107 ledgered this as a stripped grouped subquery. The real
mechanism is a different one:

- `cs_ui` is an *inlined* CTE.
- M0146-0103 stamped CTE scan schemas with the reference's binding id,
  so the probe key's id names the `cs_ui` reference.
- `nestLoopParamThroughOuter` (M0146-0106) asks the label lookups first.
  `resolveLabelInAncestorSrc` answered with the inlined scan's name, so
  the key chase never ran.

## PG behaviour

`inline_cte` turns a single-reference, non-recursive CTE into a
subquery. setrefs strips its trivial Subquery Scan
(`trivial_subqueryscan`). There is then no `cs_ui` scan level to name,
and `get_parameter` deparses the NestLoop param through the loop's outer
plan into the grouped body. The same holds for a UNION's first arm,
through `set_deparse_plan`.

## Change

`nestLoopParamThroughOuter` runs the key chase first.

- **The chase crossed a query level.** If it went into an inlined CTE's
  body or a UNION's first arm (`reg.chaseCrossedLevel`), its answer wins
  over the label lookups.
- **Otherwise** the order is unchanged: the outer plan's relation labels,
  then the chase.

## Verification

- **Probe** (PG 18.3 vs goopg, `enable_hashjoin = enable_mergejoin =
  off`). A CTE `x`, referenced twice, whose body joins m108b to an inlined
  grouped CTE `u`. The whole plan is identical to PG:
  `Index Cond: (k = m108a.k)`. HEAD printed `(k = u.k)`.
- **Test.** `TestNestLoopParamOverInlinedCTE`. Disabling the
  crossed-level preference fails it.
- **TPC-DS fire set** (results identical). Only Q64 changes.
  - At SF0.25 the probe line now matches PG; qualifier-only differences
    30 → 29.
  - At SF1 PG's plan probes a different relation at that point. goopg's
    line uses PG's naming.
  - The classifier's categories are unchanged.
- **TPC-H.** Plans are byte-identical.
- **Regress A/B** (16 files). Identical apart from join's known row-order
  flap and rowsecurity's pointer text.
- **Gates.** units, TPC-H spotcheck, acceptance arm, fire set, SF0.25 sweep
  (96 PASS) and ea-ratchet all PASS.
