# R26/2 — the `outer-link-no-sjinfo` decline traced to an ordering bug, and why the obvious fix is unsafe

*2026-09-09. Follows the R26 audit's top reason (7 of 13 declines).
No code shipped; the diagnosis is the deliverable, and so is the
falsified fix.*

## 1. The divergence is real and visible in the plan

TPC-DS Q49 join nodes:

| | nodes |
|---|---|
| **PG 18.3** | 6 x `Nested Loop` — **no outer join at all** |
| **goopg** | 3 x `Hash Join` + 3 x **`Hash Left Join`** |

PG's `reduce_outer_joins` demotes those LEFT JOINs to inner, because
Q49's `WHERE wr.wr_return_amt > 10000` is strict on the nullable side.
goopg keeps them. That is a direct plan divergence, independent of
costing.

## 2. Minimal repro, isolated

| shape | verdict |
|---|---|
| `A LEFT JOIN B ON …` (top level, no WHERE) | admitted |
| same inside a derived table, no strict WHERE | admitted |
| same **+ `WHERE b.col > 10000`** (strict on nullable side) | **declined**, `joinInfoList=0` |

Instrumented at the guard: `outerLinks=1 joinInfoList=0`.

## 3. The cause is an ORDERING bug in `planFromClause`

1. the loop calls `planFromItem` and builds the **node tree**, reading
   each `FromExpr` while it is still `LEFT`;
2. **then** `reduceOuterJoins(s.FromExprs, …)` mutates those same
   `FromExpr`s in place, demoting to INNER;
3. **then** `deconstructJointreeScopedSJI` reads the mutated items and
   correctly builds **no** `SpecialJoinInfo`.

So the plan says `JoinTypeLeft` and `root->join_info_list` says there is
no outer join. `extractSearchLeaves` then reports an outer link with no
matching SJI, the fail-closed `outerLinksHaveSJInfos` guard declines the
statement, and it falls to the legacy path — where, per K27, it can
never converge on PG's plan by any costing work.

PG has no such split: `reduce_outer_joins` runs in `subquery_planner`,
ahead of path generation, so the demotion reaches everything.

## 4. Moving the call is NOT the fix — measured

Moving `reduceOuterJoins` above the node-building loop (PG's position)
broke **six** tests, including two on VALUES:

```
leftjoin_search_admission_test.go:128: got 0 rows, want 1
right_join_spine_rows_test.go:71: rows = [], want [102,,] —
  a WHERE qual on a RIGHT JOIN's nullable arm was evaluated below
  the join that produces the NULLs
```

Wrong rows, not moved plans. The reason is the important part:

> **goopg's outer-join demotion has never driven a plan.** Because it
> ran after the node tree was built, its only consumer was the SJI
> deconstruction. Its correctness *as a plan rewrite* was therefore
> never exercised — and when exercised, it produces wrong answers.

That is the "dead code is not a reference implementation" trap: an
unreached path had no evidence behind it, and reading it as correct
because it exists would have shipped dropped rows.

## 5. What the round for this must do

Not "move the call". The demotion logic itself needs auditing as a plan
rewrite before it can drive one:

1. Establish which demotions are sound when the qual placement is also
   affected — the RIGHT JOIN case above shows a demotion that makes a
   WHERE qual land below the join producing the NULLs.
2. PG's own rule (`reduce_outer_joins`, prepjointree.c) demotes only
   when the WHERE is strict on the nullable side **and** the qual stays
   above the join. Compare goopg's `collectNonNullableTableNames` /
   `applyDemotion` against it clause by clause.
3. Only then move the call, with the values tests above as the gate
   they already proved themselves to be.

## 6. Value delivered without shipping code

- The Q49 divergence is now attributable to a specific, located
  ordering bug rather than to costing.
- The obvious one-line fix is **falsified with evidence**, so the next
  attempt does not spend itself rediscovering that.
- `reduceOuterJoins` is now known to be plan-unsafe in its current
  form — a fact nothing in the tree recorded, because nothing had ever
  run it that way.
