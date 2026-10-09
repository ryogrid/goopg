# M0146-0079 — sublink result caches follow the enclosing outer rows

Status: done 2026-10-07 (`0859cbaf4`).

## Symptom

```sql
SELECT * FROM (VALUES (0,9998), (1,1000)) v(id,x),
  LATERAL (SELECT f1 FROM m79i
           WHERE f1 = ANY (SELECT unique1 FROM m79t WHERE unique2 = v.x OFFSET 0)) ss;
```

PG 18.3 returns `0|9998|0`. goopg returned that row plus `1|1000|0`. The
second left row (`x = 1000`, whose ANY list is `{5}`) reused the first left
row's list `{0}`. NOT IN and scalar subqueries inside a LATERAL item went
wrong the same way. Regress `join` shows the original witness.

## Cause

A correlated sublink's result is cached under a key in the "scoped" store:

- the sublink's own row, i.e. the row it is evaluated for;
- or a constant, for an `IsNonCorrelated` sublink.

The scoped store was cleared only when the depth of the `OuterRows` stack
changed. A sublink can also read a value from further up that stack. Here
the ANY sublink reads `v.x`, the left row of the LATERAL item around it.
Its key did not contain that value.

The lateral driver pops one left row and pushes the next, so the depth
stays the same and the store was never cleared. The second left row hit
the first one's cached result.

## PG

`nodeSubplan.c` re-evaluates a SubPlan when any parameter it depends on
changes (`chgParam`), whatever level the parameter comes from, and only
then.

## Change

- **`optimizer.PlanReadsPastParent(plan)`.** Wraps
  `planHasEscapingOuterRef(plan, 2)`. It reports whether the sublink's plan
  reads a value from above its immediate parent row; binders inside the
  plan, such as lateral joins, are honoured.
- **`Context.scopedSublinkKey`.** For such a sublink, appends the
  enclosing `OuterRows` values to the scoped key. The lookup runs before
  the sublink pushes its own row, so `OuterRows` is exactly the enclosing
  stack. The plan check runs once per sublink site and is cached in
  `SubPlanSiteStats`.
- **Call sites.** Every unlowered scoped key uses it:
  - IN/ANY, with both the row key and the `IsNonCorrelated` key;
  - non-correlated EXISTS;
  - scalar subqueries;
  - the two hashed-subplan stores derived from the IN slice.

  Param-lowered keys already encode their bound outer values, and are
  unchanged.

A first version instead cleared the whole scoped store whenever the
enclosing values changed. The SF0.25 sweep caught it:

- Q10, Q14 and Q35 timed out;
- Q54 went from 1 s to 38 s.

Independent sublinks nested inside a correlated one lost their cached
result on every outer row. Extending the key only for sublinks that depend
on the enclosing rows keeps the caching PG's semantics allow.

## Verification

- `TestLateralSublinkResultsFollowTheLeftRow` runs six shapes, every want
  PG 18.3's:
  - ANY;
  - EXISTS;
  - NOT IN;
  - scalar;
  - IN without OFFSET;
  - a two-deep LATERAL.

  Three of them fail at HEAD.
- A server probe of the same shapes is identical to PG.
- Regress A/B over 9 files, with no new divergent line anywhere:
  - `join`: 18370 → 18348 lines;
  - `subselect`: 2783 → 2751 lines;
  - the other seven files are identical.
- Gates:
  - units PASS;
  - tpch-spotcheck PASS;
  - acceptance arm 24/24;
  - sf025 96/96, plan shapes 99/99, total 380 s (374 s before the change);
  - fire set: no fires;
  - ea-ratchet PASS.

## Not covered (ledgered)

- **Plan shape.** PG pulls the witness's ANY sublink into a Hash Semi Join
  even with `OFFSET 0`. goopg keeps a SubPlan under the scan's filter.
- **Key width.** The extended key carries every enclosing row, not only
  the levels the sublink reads. A sublink that reads level 2 of a 3-deep
  stack is re-evaluated when level 3 changes too. The result is correct,
  but caching is coarser than PG's per-parameter invalidation.
