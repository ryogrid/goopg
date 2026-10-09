# M0146-0048 — windows differing only in NULLS FIRST/LAST stay apart

Status: done (2026-10-03). Parent: M0146 (banner item 2a, third S2 batch).

## The defect

`buildWindowStage` groups window calls by `windowSpecKey`, and one group
becomes one WindowAgg. The key recorded each ORDER BY item's expression
and ASC/DESC but not its NULLS ordering. So `rank() OVER (ORDER BY x NULLS
FIRST)` and `rank() OVER (ORDER BY x)` collapsed into one group, and both
were computed under the NULLS FIRST order. With x in (1, NULL, 2), PG 18.3
gives the second rank 1, 2, 3 for x = 1, 2, NULL; goopg gave 2, 3, 1.

## The fix

`windowSpecKey` appends the effective NULLS ordering (`sortByNullsFirst`:
the explicit clause, else NULLS FIRST for DESC and NULLS LAST for ASC) to
each ORDER BY item. An explicit default (`ASC NULLS LAST`) therefore keys
the same as the implicit one and still shares the window, as PG's equal
SortGroupClauses do.

## Verification

- Scratch probes against PG 18.3: three rank pairs (NULLS FIRST vs
  default, DESC NULLS LAST vs DESC, ASC NULLS LAST vs default) give
  identical values. EXPLAIN shows two WindowAggs where PG has two, and one
  where PG has one.
- `TestWindowsDifferingInNullsOrderStayApart` pins the values and the
  shared-window case.
- Regress `window` is byte-identical.
- Gates: units, spotcheck, sweep 96/96, fire set (no plan changes), TPC-H
  arm.
