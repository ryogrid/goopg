# R75 SCOPE: SEMI search-admission slice, route (i)

Lineage: R74 FORK (fork at `unnestExistsExpr`, menu rec (i): admit
the unnested SEMI joinrel to the search). Architecture anchor: the
SEMI spine is PINNED ABOVE the search by design
(`runJoinSearchBelowPinned`, `predp.go:73`) — the search optimises
only below it. Route (i) extends the search to the pinned joinrel
itself; it does not unpin anything else.

## P0 — spike (unit harness, no server)

Precedent: `joinsearchunnest_test.go` (Q17 decorrelated shape planned
at both flag settings with real-width catalog fixtures).

1. Build in-harness: outer rel (orders-shaped) + inner rel with a
   parameterised index path (lineitem-shaped, `l_orderkey = outer`)
   + `JoinSemi` sjinfo over the relset.
2. Call `addPathsToJoinrel` (or `addNLIPaths` directly if the joinrel
   will not assemble in-harness — record which) and assert: a SEMI
   NLI path is filed, priced via the shared costing
   (`joinpathsnli.go:341`), with
   Total ≥ outer Total (the PG inequality, `costsize.c:3267/:3307`).
3. Stop conditions: PASS with the filed path's numbers, or BLOCKED
   with the named missing harness piece (parameterised-inner
   construction outside a full plan is the prime suspect). Either is
   a committable P0 outcome; no integration code in P0.

## P1 — integration splice (design only in this slice unless P0 is clean)

- Where the search-built SEMI subtree replaces the legacy
  `Join{SEMI,Hash}` (at the unnest site vs a post-search pass), how
  the pinned spine in `predp.go` interacts (search prices the joinrel
  the spine pins — ordering constraint), and where `createPlanNode`'s
  `stampPlanCost` funnel (`createplan.go:50`) picks up the winner so
  EXPLAIN prints planner numbers (R73 seam closed by construction).
- `nestloopOnly` (`joinpaths.go:265`) already restricts Semi to NL —
  no new admission rules, only relset/sjinfo construction.
- Implementation follows as R76 iff P0 passes; P0-BLOCKED re-scopes
  instead (no heroic harness work inside this slice).

## Gates (slice commit bar, not Step-0)

- values 24/24 + TPC-DS SF0.25 sweep PASS=96 all-zero.
- pp: Q4 verdict delta required (toward PG); Q21/Q22 (EXISTS/NOT
  EXISTS family) deltas listed read-only; Q13 untouched.
- EXPLAIN byte-identity discipline for non-target queries
  (admission must not move anything it does not price).

## Explicitly out

Selectivity/rows, widths, election rules, fuzz tiebreak, Q13-inner,
parallelism. No cost-term changes: the existing `:341` costing is
used as-is; this slice is admission only.
