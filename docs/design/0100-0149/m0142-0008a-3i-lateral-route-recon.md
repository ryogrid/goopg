# M0142-0008a-3i-lateral-route — the route defect is one stage EARLIER than filed

Status: RECON COMPLETE (2026-09-20). No production file touched.
Kind: recon
Parent: M0142-0008a-3
Movement: none

## 1. What this recon was asked to decide

The task was filed as *"let Phase B search before Phase A lowers anything"*:
either Phase B sees the chain before `createNestLoopIndexJoinPlan` lowers a
parameterised probe, or Phase A's spliced result retains a path-level
`RequiredOuter`. It is the single named blocker for five tasks — M0142-0008a-3
increments (i) and (ii), `M0142-0008a-3i-lateral`, `M0144-0003a`, and (since
`M0144-0003b-1`) `M0144-0003b`.

**The filed framing is insufficient, and reordering Phase A and Phase B cannot
fix anything.** The lowering that produces the opaque leaves happens one stage
before EITHER phase runs.

## 2. goopg's actual source order

Three consecutive statements in `planSelect`'s WHERE arm
(`internal/optimizer/planner.go`):

| line | statement | what it does |
|---|---|---|
| 1499 | `pred, err = resolveExpr(whereQual, ctx)` | **plans every EXISTS/IN body to a finished `Node`** |
| 1539 | `node = unnestSubqueriesInPlan(node)` | rewrites to `Join{Semi/Anti}` whose `Right` IS that finished Node |
| 1540 | `node = runJoinSearchBelowPinned(node, origChain, ctx, cat)` | Phase A searches the chain, Phase B searches the spine |

Line 1499 is the lowering. `resolveExpr` reaches `planExistsExpr`
(`planner.go:15320-15333`) and `planSubqueryExpr`/the IN arm
(`:15200`, `:15217`, `:15300`), and every one of them calls
`planSelectWithParent` — a **full recursive planner run** — on the parser
subquery, then stores only the result:

```go
return &ExistsExpr{pos: x.Pos(), Negated: x.Negated, Plan: inner,
        IsNonCorrelated: !planHasOuterRef(inner)}, nil   // planner.go:15333
```

`ExistsExpr` and `InExpr` carry `Plan Node` and **do not retain
`x.Subquery`**. The parse tree is dropped at this point and is unreachable
from every later stage.

So by the time `unnestSubqueriesInPlan` runs, the semi/anti RHS is a finished
plan — with its own top-level output `*Project`, and, when the body's own
planner run elected one, a `*Gather` with a worker count already chosen.
Phase A has not run yet. Phase B has not run yet. Their order is irrelevant.

## 3. Why this is exactly the measured symptom

Four independent probes bottomed out on "the leaf is an already-planned
composite", and each attributed it to the phase it happened to be looking at:

| probe | what it found | attributed to |
|---|---|---|
| `M0142-0008a-3i-lateral` | dependency lowered to `Join{Lateral:true}` + `OuterColumnRef{Level:1}` | Phase A |
| `M0142-0008a-3i-leafcount` | `scan[0]=*Project`; Q35 `*NestedLoopIndexJoin`, `*Gather` | Phase A |
| `M0144-0003a` census | 53 `*Project`, 4 `*SeqScan`, 1 `*NestedLoopIndexJoin`, 1 `*Gather`; zero `*Filter` | Phase B's seam |
| `M0144-0003a` refutation | not one `*Project` is a positional identity; `would == scans` in every case | Phase B's seam |

All four are the same object seen from different angles: the finished plan
line 1499 built. `M0142-0008a-3i-verify` had already named the provenance
correctly — *"`unnestExistsExpr` clones the EXISTS body's already-planned
subquery tree, and every planned `SELECT` carries its own top-level
output-list `*Project`"* — but the route task was filed against the phases
rather than against the resolver.

## 4. PG's order, cited

`subquery_planner` (`postgres/src/backend/optimizer/plan/planner.c:651`):

| line | call | note |
|---|---|---|
| 737 | `pull_up_sublinks(root)` | `prepjointree.c:468` → `convert_EXISTS_sublink_to_join` (`subselect.c:1450`) turns the SubLink into a `JoinExpr` + subquery RTE, **while the body is still an unplanned `Query`** |
| 759 | `pull_up_subqueries(root)` | `prepjointree.c:1083`; flattens the new RTE into the parent's range table when `is_simple_subquery` (`prepjointree.c:1807`) accepts it |
| 1328 | `SS_process_sublinks(...)` | `subselect.c:2026` — plans the sublinks that were **not** pulled up |
| 1654 | `query_planner(root, ...)` | the join search, over the now-flattened range table |
| 441 | `create_plan(root, best_path)` | plan construction, after the search |

PG plans a sublink body only if pull-up already refused it. goopg plans every
body unconditionally and pulls up afterwards. That single inversion is the
whole route defect.

## 5. The corpus witnesses are all flattenable

Read the five witnesses' bodies directly
(`bench/tpcds/runtime_goopg/tpcds-data/queries/`):

- **Q16 / Q94** — `exists (select * from catalog_sales cs2 where
  cs1.cs_order_number = cs2.cs_order_number and cs1.cs_warehouse_sk <>
  cs2.cs_warehouse_sk)`, plus a `not exists` of the same shape. One base
  relation each.
- **Q35 / Q10 / Q69** — `exists (select * from store_sales,date_dim where
  c.c_customer_sk = ss_customer_sk and ss_sold_date_sk = d_date_sk and
  d_year = 1999 and d_qoy < 4)` and two siblings over `web_sales`/
  `catalog_sales`. Two base relations each.

Every one is a plain `SELECT` over 1–2 base relations with ordinary quals: no
aggregate, no `HAVING`, no window, no set operation, no `DISTINCT`, no
`LIMIT`/`OFFSET`, no `ORDER BY`. **All five satisfy PG's `is_simple_subquery`
in full**, which is why PG flattens them and searches one problem where goopg
searches a spine over finished plans.

This also says the reachable fix is not exotic: the flattenable subset covers
100% of the named witnesses, so the hard residue PG itself keeps as a SubPlan
does not have to be solved to move them.

## 6. The route, and why it is a different task shape

The minimal PG-faithful route has one structural prerequisite and one
transform:

1. **Retain the parse tree.** Add the parser subquery to `ExistsExpr`/`InExpr`
   alongside `Plan`. This is purely additive: all 52 `.Plan` readers in
   `unnest.go` keep working unchanged, and the 7 `planSelectWithParent` call
   sites (`planner.go:5232, 5251, 13192, 15200, 15217, 15300, 15329`) each
   still hold the parser node they pass in, so the field costs one assignment
   per site.
2. **Flatten before planning.** `unnestSubqueriesInPlan` (or a new
   pre-resolve pass) tests the retained query against a goopg
   `is_simple_subquery` analogue and, when it passes, splices the body's FROM
   items into the outer join list as REAL relations with its quals added to
   the outer predicate — discarding `.Plan` for that sublink entirely — so the
   single search that follows sees base relations, not a finished plan.

The honest sizing consequence: step 2 changes **when a sublink body is
planned**, which means the eager plan must become conditional, and that is a
planner-pipeline change rather than a seam or admission change. It is the same
work item PG calls `pull_up_subqueries`, and it is also exactly what
`M0144-0003b`'s residual needs (a UNION ALL branch that never reached the
search has the same cause: its subtree was planned before the search existed).

Deliberately NOT proposed: widening `chainCarriesLateral`, descending the
opaque `*Project`, or adding the synthetic RHS participant. All three were
measured and refuted (`M0142-0008a-3i-lateral`, `M0144-0003a`,
`M0142-0008a-3i-leafcount`); each attacks the seam, and the seam is downstream
of the defect.

## 7. What the implementing task must still establish

- The leaf arithmetic must be **re-derived, not carried over**. The current
  gate is `len(scans) != nprefix+len(semiAnti)` (`joinsearchseam.go:326`);
  flattening changes both sides of that equation, so quoting today's
  `Q16 nrels=4 nprefix=4 scans=3` shortfall as the post-fix target would be
  wrong.
- The P0-H11 `cumulativeFromSpans` span round-trip must close in the SAME
  change — it is latent only because nothing gets through today
  (`M0142-0008a-3i-reach-verify`).
- Q78's `outer-over-derived` firewall must not weaken (hard owner constraint,
  banner FROZEN-PREFIXES note).
- Correlation references in a flattened body (`cs1.cs_order_number` above)
  must be re-based into the outer chain's column space at splice time; PG gets
  this for free because the reference is still a `Var` over a range-table
  index, while goopg's is an `OuterColumnRef{Level:1}` that only means
  something relative to the nesting it is being removed from.

## 8. Status of the five blocked tasks

Unchanged — all five stay blocked, and they stay blocked on ONE thing. What
this recon changes is that the thing is now named correctly and has a
concrete, PG-cited route with a 5/5 flattenable witness set, instead of a
phase-ordering description that no implementation could have satisfied.
