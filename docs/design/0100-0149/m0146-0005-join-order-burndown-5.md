# M0146-0005 (part 5): slices 71+ — index-only coverage and the needed set

Continuation of [m0146-0005-join-order-burndown-4.md](m0146-0005-join-order-burndown-4.md)
(slices 56-70), split per the design-doc size rule (D3). Same task and census
family.

## Recon 71: M0146-0005bs — an EXISTS body's star voids the needed set (not landed)

After slice 70, TPC-DS Q94's only divergence is its anti-join probe: PG
plans `Index Only Scan using web_returns_pkey on web_returns wr1`, and
goopg an Index Scan. Q94 writes the probe as
`NOT EXISTS (SELECT * FROM web_returns wr1 WHERE ...)`. PG's
`simplify_EXISTS_query` (subselect.c) discards an EXISTS target list
before planning, so the star reads nothing. goopg's needed-column
collector (pathindexonlyneed.go) walks the body's targets, meets the
`StarExpr` and declines, which makes the whole statement's needed set
unknown. No index-only path, and no narrowing, is then offered anywhere
in the statement.

I tried dropping a targets list made only of stars and constants from an
EXISTS body, in both collectors. A PG 18.3 oracle unit test (anti join
over `SELECT *`, count 19) went green. On TPC-DS it was a net loss, so the
change was reverted:

- Q94 kept its plain `wr1` probe. The fire set shows the plan changed,
  but the probe did not become index-only. So the anti-join probe over
  the pulled EXISTS leaf comes from a route this producer does not reach,
  or the leaf is not bare there. Not yet traced.
- Q10 and Q35 (SF0.25), and Q35 at SF1, fell back to the unsearched
  syntactic plan with `seam-decline reason=residual-hits-pad`.
  - Their needed sets became known, so narrowing padded columns. Slice 70
    pads per alias.
  - `searchedResidualHitsPad` (narrowoutput.go) is keyed by NAME. For a
    residual holding a correlated sublink (Q35's
    `exists(...) or exists(...)`), it declines when any padded column's
    name is needed anywhere. A column padded on one date\_dim alias is
    needed on another, so the check fires.
  - `boundaryPaddedNames` has no qualifier to attribute by, because a
    `SchemaColumn` carries none.

Prerequisites before the EXISTS change can land (filed as M0146-0005bs):

1. Make the residual pad check alias-aware. Carry each padded slot's
   qualifier from the boundary filler, which already computes it, and
   test it with `neededColumnNamedFor`.
2. Trace which producer builds Q94's anti-join probe over the pulled
   EXISTS leaf, and give it the index-only arm.

Evidence: `analysis/m0146/m0146-0005/recon71/`.

## Slice 72: M0146-0005bs — the EXISTS star lands, with a residual pad check that reads outer references

Recon 71's first prerequisite is done here, and the EXISTS change lands.

- `residualColumnRefsByName` (narrowoutput.go) no longer marks the walk
  partial when the residual holds a correlated sublink. A correlated plan
  reads the row it runs under only through its `OuterColumnRef`s, and each
  of them carries the column's name.
  - Every outer reference whose level reaches this scope or beyond is
    reported by name, including those in nested sublinks
    (`walkPlanExprsDeep`).
  - Over-reporting (a reference that a lateral binder inside the plan
    satisfies, or one aimed further out) only makes the fallback fire more.
  - A nameless outer reference still makes the walk partial.
- Q35's `exists(...) or exists(...)` residual reads only
  `c.c_customer_sk`. The old partial walk fell back on any statement-needed
  column padded anywhere, and slice 70's per-alias pads made that fire.
- `existsBodyForColumns` (pathindexonlyneed.go) drops an EXISTS body's
  target list when it holds only stars and constants, as simplify\_EXISTS\_query
  does. A `SELECT *` inside EXISTS therefore no longer voids the
  statement's needed set.

Tests:

- `TestExistsStarProbeIsIndexOnly` uses a PG 18.3 oracle: an anti join over
  `NOT EXISTS (SELECT * ...)` probes `p_pkey` index-only, count 19.
- `TestResidualColumnRefsByNameSublinkScopes` now pins the new contract:
  total, reporting the outer reference, and partial only for a nameless one.

Movement:

- None on the instruments. Five fires (Q10 Q16 Q35 Q69 Q94) change plans
  with no category movement at either scale, and all execute.
- The `residual-hits-pad` seam declines that recon 71 hit are gone: the
  sweep shows only its baseline `leaf-count` declines.
- ea-ratchet stays at 10. TPC-H plans are identical. Units, spotcheck, the
  sweep (96/96) and the arm (24/24) pass. Regress is unchanged apart from
  join.sql's flaps.

Q94 is still not index-only, and the cause is now traced. The `wr1` probe
is a parameterised SKIP path (`index.parameterised.skip`). The join binds
`wr_order_number`, the second key of `web_returns_pkey`, and PG 18's
matching node is an index-only skip scan. goopg's `IndexOnlyScan` has no
skip prefix, which is why `createIndexScanPlan` refuses one. The index-only
arm of `addOneParameterizedSkipPath` needs executor skip support in
`indexOnlyScanOp` first. Filed as M0146-0005bt.

Evidence: `analysis/m0146/m0146-0005/slice72/`.

## Slice 73: M0146-0005bt — index-only skip probes

Slice 72 traced TPC-DS Q94's remaining divergence to its `wr1` anti-join
probe. The probe binds `wr_order_number`, the SECOND key of
`web_returns_pkey`, so it is a PG 18 skip scan, and PG makes it index-only
under the same `check_index_only` rule as any index path. goopg's
`IndexOnlyScan` had no skip prefix, so the parameterised skip producer
offered only the heap-fetching probe.

- The skip enumeration moved out of `indexScanOp` into a shared
  `btreeSkipEnum` (new btree\_skip.go):
  - `init` evaluates the bound columns once per Rescan, and reports a NULL
    bound as an empty scan;
  - `next` returns the next prefix group's (lo, hi) probe, for both the
    tuple and the blob key formats.
  
  `indexScanOp` delegates to it (its lazy cursor-per-group drive is
  unchanged). `indexOnlyScanOp.Rescan` gains a skip branch that runs one
  bounded range scan per group, materialised eagerly like every other
  index-only shape.
- `IndexOnlyScan.SkipPrefix` is added on the plan node. The index-only
  branch of `createIndexScanPlan` accepts a parameterised skip path, and
  `setNLIProbeKeys` keeps `Keys` under a skip prefix. EXPLAIN renders only
  the bound quals under their own columns, in `formatIndexOnlyCond` and in
  `probeKeyEqualities` (the Memoize/NLI qual reader).
- `addOneParameterizedSkipPath` gains the index-only arm of slice 69
  (bare leaf, covering index, per-alias needed set, allvisfrac costing and
  covered widths).

Test: `TestIndexOnlySkipProbe` covers two PG 18.3 oracle shapes over
`q_pkey(k1, k2)` probed on k2. The anti join gives count 19; the inner join
gives count 18 and `sum(k1)` 36, reading the skipped key column back out.
It fails with the producer arm removed.

Movement (fire set):

- PLAN-PARITY match SF0.25 35 → 36, SF1 27 → 28 (Q94).
- CATEGORIES-EXCL-MATCH scan-type SF0.25 30 → 28, SF1 36 → 34 (Q94, and
  Q16's probe).
- ea-ratchet stays at 10. TPC-H plans are identical. Units, spotcheck, the
  sweep (96/96) and the arm (24/24) pass. Regress btree\_index,
  create\_index, index\_including, join, subselect and select are
  unchanged.

Evidence: `analysis/m0146/m0146-0005/slice73/`.

## Slice 74: M0146-0005bu — a correlated scalar sublink on one relation is that relation's restriction

TPC-DS Q1, Q30 and Q81 filter
`ctr1.ctr_total_return > (SELECT avg(...) * 1.2 FROM ctr ctr2 WHERE
ctr1.k = ctr2.k)`. PG 18.3 places this on the `ctr1` CTE Scan:
`distribute_qual_to_rels` takes a clause's relids from `pull_varnos`, and a
correlated SubPlan contributes its testexpr Vars plus the outer Vars its
parameters carry. All of those name `ctr1`, so the clause is a base
restriction there (Q30 SF1: `rows=109` on the scan). goopg kept every
correlated sublink conjunct of a multi-relation scope as a join residual
(M0146-0015a), so the filter sat on the top Nested Loop.

- `correlatedScalarSublinkLeaf` (local\_filters.go) admits a conjunct that
  `conjunctLocalEligibility` declines when all of the following hold:
  - its only sublinks are scalar;
  - its same-scope columns all lie in binding 0;
  - every outer reference in its inner plans names binding 0 of this scope,
    and none reaches past it.

  EXISTS and IN stay excluded because the post-planning EXISTS→ANY and
  unnest passes read them off the top qual holder.
- The admission is built on `walkExprRefs`, which is complete over Expr
  types and fails closed. The inner plans are judged by `planEscapesBy`,
  which is `planHasEscapingOuterRef` with the escape rule passed in (the
  historical rule is `outerRefReachesPast`). Its Visit dispatch is pinned in
  the walker inventory as a classifier, next to its sibling.
- **Offset 0 only.** Binding 0's leaf coordinates are the FROM-cumulative
  ones, so neither `localizeExprToLeaf` nor the unlowered inner plan moves a
  coordinate. The rebase needed for any other binding is ledgered.
- **Join-width fix in the unnest driver.** goopg's scalar-aggregate unnest
  (not a PG transform) still decorrelates such a leaf qual. It appends the
  aggregate's columns to the host, which is harmless under the top Project
  but shifts every coordinate past a join input. A plain-table repro counted
  0 where PG counts 993. `unnestKeepingWidth` now projects a widened join
  input back to its original columns.

Tests:

- `TestCorrelatedSublinkIsBaseRestriction`: the PG 18.3 shape, with the SubPlan
  on the r1 CTE Scan and count 801, read with the unnest post-pass off.
- `TestUnnestedLeafSublinkKeepsJoinWidth`: counts 993/993/801 with the pass
  on. It fails, reading 0, without `unnestKeepingWidth`.

Movement (fire set):

- The qual is placed as in PG for Q1 (SF0.25), Q30 and Q81 at both scales.
  TPC-H Q20's `ps_availqty > (SubPlan)` now filters the partsupp index scan,
  as in PG.
- PLAN-PARITY match is unchanged: SF0.25 36, SF1 28, TPC-H 11.
  CATEGORIES-EXCL-MATCH aggregation-strategy goes SF0.25 18 → 17 (Q1 no
  longer unnests at SF0.25). SF1 join-order goes 60 → 62 and scan-type
  34 → 35: with 109 driving rows instead of 327, the customer probe flips
  to a bitmap scan.
- The two differences left on Q30/Q81 are both outside this slice:
  - goopg prices the customer index probe at 12.25 where PG has 7.61. This
    is the parked M0142-0005c multiplier, and it is what makes the bitmap
    win.
  - EXPLAIN numbers `SubPlan 1` where PG numbers `SubPlan 2`. PG's plan ids
    count the CTE plan first; filed as M0146-0005bv.
- goopg still charges a sublink one operator in `qualEvalOps`; PG adds the
  SubPlan's per-call cost (`cost_subplan`). That is ledgered.
- Gates: units, spotcheck, sweep 96/96, arm 24/24 and ea-ratchet (10)
  pass. Regress subselect, with and join are unchanged (join shows only its
  known row-order flap).

Evidence: `analysis/m0146/m0146-0005/slice74/`.

## Slice 75: M0146-0005bv — SubPlan/InitPlan numbers follow PG's plan\_id

PG numbers a SubPlan or InitPlan by its position in `glob->subplans`, so
the number records planning order (`build_subplan`, `SS_process_ctes` and
`SS_make_initplan_from_plan` in subselect.c):

- CTE plans take ids first, in declaration order, and each is appended
  after its own body is planned. The ids are not printed.
- `make_subplan` plans a sublink's subquery before appending the sublink,
  so nested sublinks (planagg's MIN/MAX InitPlan included) come before the
  one that holds them.
- A simple EXISTS that converts to a hashable ANY is planned twice. The
  hashed ANY plan takes the second id, and it is the one EXPLAIN shows when
  hashing wins.

goopg numbered sublinks from 1 in render order. Every CTE statement was
therefore off by the CTE count: TPC-DS Q1/Q30/Q81 printed `SubPlan 1`
against PG's `SubPlan 2`, and Q14 printed InitPlans 1–4 against 3–6. Every
nested pair was inverted, and every hashed EXISTS was one low.

- `reservePGPlanIDs` (new explain\_plan\_ids.go) walks the plan once
  before rendering and reserves numbers in that order:
  1. the CTE sections, each body before its CTE;
  2. the plan spine pre-order, a node's own sublinks before its children,
     and each sublink's body before the sublink.

  An EXISTS→ANY conversion (`InExpr.UnknownEqFalse`) that renders `hashed`
  reserves a second id. `assignHashed` looks the reservation up by inner
  plan root; an unreserved sublink continues the sequence.
- `optimizer.NodeSublinks` pairs each subplan root with its sublink
  expression; `NodeSubplans` is now built on it.
- Four tests pinned goopg's old numbers and now pin PG 18.3's, checked on
  a live PG 18.3: `hashed SubPlan 2`/`4` for two EXISTS; `InitPlan 2` over
  planagg's `InitPlan 1`. `TestCorrelatedSublinkIsBaseRestriction`
  additionally pins `SubPlan 2` after one CTE.

Not modelled (ledgered):

- FROM subqueries are planned in range-table order after the level's
  sublinks (Q58's InitPlan 2/1/3).
- A CTE declared inside a sublink body is planned during that sublink.
- planagg's InitPlans at the query's own level come after its quals.
- A sublink goopg decorrelated still consumed an id in PG.

Movement (fire set):

- CATEGORIES-EXCL-MATCH parameterisation goes SF0.25 33 → 30 and SF1
  40 → 36. Match is unchanged (SF0.25 36, SF1 28).
- Q30/Q81 now differ from PG only by the customer probe (the parked
  index-probe multiplier).
- Regress: subselect goes 2811 → 2806 diff lines, and its hashed-EXISTS
  case now prints PG's `hashed SubPlan 2`. The join, partition\_prune and
  with hunks renumber sublinks in plan shapes PG does not build.
- Gates: units, spotcheck, sweep 96/96, arm 24/24 and ea-ratchet (10)
  pass.

Evidence: `analysis/m0146/m0146-0005/slice75/`.

## Slice 76: M0146-0005bw — a CTE scan carries its body's ordering to the grouping stage

TPC-DS Q24 groups and orders its `ssales` CTE by a prefix of the CTE's
own group key. PG 18.3 plans a GroupAggregate directly over the CTE Scan,
with no Sort below it or above it:

- `set_cte_pathlist` (allpaths.c, PG 17+) gives the scan the body's
  pathkeys through `convert_subquery_pathkeys`.
- `add_paths_to_grouping_rel`'s `is_sorted` test takes a presorted input
  path without a Sort.
- `create_agg_path` copies the subpath's pathkeys, so the ORDER BY is
  already satisfied.

goopg had the first rule only inside the join search
(`addCTEScanPathkeys`). A one-relation query never reaches that search,
and the grouping stage always stacked a Sort over a finished child.

- `inputNodePathkeys` gains a `*CTEScan` arm: a positional-identity step
  onto the body, like `*SubqueryScan`. A recursive self-reference wraps a
  WorkTableScan and is refused one step down.
- `addGroupingPaths`' SORTED arm has an `is_sorted` offer. When the finished
  child's derived ordering (`inputNodePathkeys`) contains the group
  pathkeys, it files AGG\_SORTED over the child itself, carrying those
  pathkeys, and offers no Sort over that same path.
- `aggregateEmissionPathkeys` reads that derived ordering for any other
  child, so the ORDERED step sees the aggregate's emission order.
  `groupingEmissionPathkeys`, its path-level sibling, already reads the
  child path's pathkeys.
- The same arm catches TPC-DS Q65's second-level `GROUP BY ss_store_sk` over
  the sorted `(ss_store_sk, ss_item_sk)` GroupAggregate, which is now a
  GroupAggregate as in PG instead of a HashAggregate.

Test: `TestGroupAggOverSortedCTEScanSkipsSort` pins the PG 18.3 shape: one
Sort (the body's), the GroupAggregate directly over `CTE Scan on s` with
the ORDER BY satisfied, count 50 and sum 12502500. It fails with the
grouping arm disabled.

Movement (fire set):

- PLAN-PARITY match goes SF0.25 36 → 37 (Q24). SF1 stays at 28; there Q24
  differs only by a parallel hash flag inside the CTE body.
- CATEGORIES-EXCL-MATCH:
  - join-order SF0.25 54 → 53, SF1 62 → 61;
  - sort-strategy SF0.25 33 → 32, SF1 36 → 35;
  - rendering +1 at both scales: Q65's two derived tables now line up node
    for node, which exposes the `store_sales` vs `store_sales_1` alias
    choice.
- TPC-H plans are identical. Units, spotcheck, sweep 96/96, arm 24/24 and
  ea-ratchet (10) pass.

Not done (ledgered): the PLAIN presorted-aggregate arm, the rollup arm and
the partial-aggregate split arm do not read `is_sorted` from a derived
ordering yet.

Evidence: `analysis/m0146/m0146-0005/slice76/`.

## Slice 77: M0146-0005bx — a partially presorted window input gets an Incremental Sort

TPC-DS Q89's window is `PARTITION BY i_category, i_brand, s_store_name,
s_company_name`. It sits over a GroupAggregate sorted on `(i_category,
i_class, i_brand, …)`, so the input shares a one-column prefix with the
window's ordering. PG 18.3's `create_one_window_path` then stacks an
Incremental Sort (`Presorted Key: item.i_category`) when
`enable_incremental_sort` is on. goopg priced that case as a full Sort and
built one.

- `addWindowPaths` reads `pathkeysCountContainedIn`. On a partial match it
  prices `costWindow`'s existing incremental arm, using the prefix's
  `estimateNumGroups` count, and records the prefix on the path as
  `PresortedCount`.
- `createWindowPlan` stacks `IncrementalSort{PresortedCount}`. It first
  re-checks the prefix against the built child (`inputNodePathkeys`); a
  child that lost the order falls back to the full Sort.
- The window's Incremental Sort is the first one built without a path
  stamp. EXPLAIN derives its estimate, and with no `*IncrementalSort` arm
  that estimate came out as rows=1, cost=0; the collapse reached Q89's
  Limit as `0.01..0.01`. The arms now exist next to `*Sort`'s:
  - `DeriveLegacyDisplayCost`: `cost_incremental_sort` over the child's
    costs;
  - its children list;
  - `EstimateRows`, `IsSmallDimensionSide`, `groupVarSourceNode`, the
    pass-through walkers;
  - `resolveBaseColumn` (joinkeyproof.go), whose arm list a guard test
    requires to agree with `relFilteredRowsWalk`'s.

Tests:

- `TestWindowInputIncrementalSort` pins PG's shape (count 1820, sum
  200010000). It fails with the arm disabled.
- `TestWindowInputIncrementalSortDisplayRows` pins the estimate: rows=1820
  and a 17xx startup, against PG's `1739.26..2061.71`.

Movement (fire set):

- CATEGORIES-EXCL-MATCH sort-strategy goes SF0.25 32 → 31 and SF1 35 → 34.
  Match is unchanged (37 / 28).
- Q89 now differs only by `d_year = ANY (2001)` vs PG's `d_year = 2001`:
  `transformAExprIn` builds a ScalarArrayOpExpr only for two or more
  non-Var elements. Filed as M0146-0005by.
- TPC-H plans are identical. Units, spotcheck, sweep 96/96, arm 24/24 and
  ea-ratchet (10) pass.

Evidence: `analysis/m0146/m0146-0005/slice77/`.

## Slice 78: M0146-0005by — a one-element IN list is a plain comparison

PG's `transformAExprIn` (parse\_expr.c) builds a ScalarArrayOpExpr only for
two or more non-Var list items. Every other item is compared on its own
with the IN's operator (`=`, or `<>` for NOT IN), so `x IN (c)` is exactly
`x = c`: TPC-DS Q89 prints `Filter: (d_year = 2001)`, and eqsel estimates it.
goopg kept `d_year = ANY (2001)`, Q89's last difference after slice 77.

- **Parser.** `x = ANY (ARRAY[c])` desugars to the same `parser.InExpr` as
  `x IN (c)`, yet it is PG's AEXPR\_OP\_ANY and stays an array comparison.
  The AST therefore gains `InExpr.Quantified` for the `op ANY|SOME|ALL
  (…)` spellings, list or subquery. Both parsers set it and agree:
  - yacc: `quantifiedAny`, and `NewInExpr` when an ALL operator is given;
  - legacy: all three returns of `parseAnyTail`.

  The parity goldens changed only by the added field (24 `true` rows, all
  ANY/SOME/ALL spellings), checked by stripping it and diffing against HEAD.
  No grammar rule changed.
- **Planner.** `oneElementInAsComparison` rewrites a non-quantified,
  one-item, non-row IN list to a parser `=`/`<>` BinaryOp before
  resolution. It runs in `planInExpr` (the WHERE and HAVING resolvers) and
  in `resolveExprAfterWindow`'s arm.

Test: `TestOneElementInIsEquality` pins PG 18.3's filters and estimates:

- `a IN (7)` → `(a = 7)`, rows=100;
- `a NOT IN (7)` → `(a <> 7)`, rows=9900;
- `b IN ('3')` → `(b = '3')`, rows=1000;
- `a = ANY (ARRAY[7])` keeps its ANY.

It fails with the rewrite disabled.

Movement (fire set):

- PLAN-PARITY match goes SF0.25 37 → 38 (Q89). CATEGORIES-EXCL-MATCH
  qual-placement goes SF0.25 12 → 10 and SF1 8 → 7 (Q33 and Q60 also carry
  one-element IN lists).
- Regress: inherit's `b in ('ab')` now prints PG's `(b = 'ab')`. The
  other ten numbered cases are unchanged apart from rowsecurity's
  pointer-text noise.
- TPC-H plans are identical. Units (parser parity included), spotcheck,
  sweep 96/96, arm 24/24 and ea-ratchet (10) pass.

Not done (ledgered):

- The multi-item list whose Var items PG splits into separate ORed
  comparisons.
- A row operand, which PG compares through `make_row_comparison_op`.
- View and rule deparse of the rewritten form.

Evidence: `analysis/m0146/m0146-0005/slice78/`.

## Slice 79: M0146-0005bz — a Sort Key chases its OUTER_VAR through joins

PG's `show_sort_keys` deparses a key through the target lists below it, down
to the join input, aggregate or relation that computes it. TPC-DS Q73
prints `Sort Key: (count(*)) DESC, customer.c_last_name`; goopg printed its
output labels, `cnt DESC, c_last_name`. The executor's key chase
(`resolveKeySource`) stopped at every join and scan. `sortKeyParts` only
started the chase when the Sort's child was an Aggregate or Project.

- **Join arms** (`*Join`, `*NestedLoopIndexJoin`) step into the input that
  holds the column, renumbered there. They first verify that the join's
  row really is left ++ right (left only for semi/anti), by name and
  relation (`joinOutputIsConcat`).
- **Scan arms** (Seq/Index/IndexOnly/BitmapHeap) end the chase at the
  relation. The name is pinned in that scan's own naming context
  (`reg.pinnedKeyName`), because relation ids restart per query level and
  resolving from the Sort would be ambiguous.
- **Project arm:** a base-table target is followed down to its scan when
  the scan names the same relation.
- **After a join**, returned expressions have their columns pinned to the
  node that evaluates them (`pinKeyExprNames`), as PG deparses each Var in
  its producing plan node. Regress join.sql's
  `(SELECT a c1, COALESCE(a) c2 FROM group_tbl t2)` first printed
  `coalesce(t1.a)`; it now prints `(coalesce(t2.a))`.
- **`sortKeyParts` entry (iii)** chases for any other child.
- **Fail-closed rules found by the regress A/B:**
  - A key never lands on a same-named column of another relation
    (`relMismatch`). partition\_join's `t2.b` had read as `t1.b` through a
    narrowing scaffold.
  - A Subquery Scan child keeps today's text, because PG's boundary
    rendering (`tmp1.sum_sales`) needs the sibling Group Key sites and the
    M0146-0005w step-through arm changed together.
  - The previously dead Gather/Gather Merge pass-through stays dead:
    enabling it mislabelled Q59's Finalize group key as
    `sum(CASE …)` (the partial layout).

Tests:

- `TestSortKeyChaseCrossesJoins`: `(count(*)) DESC, sk_c.ln`.
- `TestSortKeyChaseNamesTheEvaluatingLevel`: `t2.a, (COALESCE(t2.a))`.
- `TestSortKeyChaseKeepsTheKeysRelation`: `t1.a, t2.b, …`.

All are PG 18.3 oracle text. The first two fail with the join arm disabled.

Movement (fire set):

- CATEGORIES-EXCL-MATCH rendering goes SF0.25 20 → 17 (Q46, Q73, Q79) and
  SF1 21 → 20 (Q73), with no new rendering divergence. Match is unchanged
  (38 / 28).
- Regress (12 EXPLAIN-heavy cases): only join.sql moves, all toward PG
  (`t1.q1`, `i0.f1`, `coalesce(t2.a)`).
- Gates: units, spotcheck, sweep 96/96 (FORCE=1, because the nightly batch
  held the host; row verdicts are unaffected), the TPC-H arm 24/24 (run
  after the nightly finished) and ea-ratchet (10) pass.

Not done (ledgered):

- PG's Subquery Scan boundary (`alias.col`) for Sort and Group Key
  together.
- Set-operation keys deparsed through the first arm (Q5/Q71/Q77).
- The `_1` alias suffix (Q61).
- Casts inside function arguments (Q79's `(store.s_city)::text`).
- The Gather/partial-aggregate layout.

Evidence: `analysis/m0146/m0146-0005/slice79/`.

## Slice 80: M0146-0005ca — a kept Subquery Scan is a naming boundary for every key line

setrefs.c's `trivial_subqueryscan` removes a Subquery Scan with no quals and
a pass-through target list; one with quals survives. An upper OUTER\_VAR
then deparses to the scan's own column, never to the subquery's internals.
Examples:

- TPC-DS Q53/Q63: `Sort Key: tmp1.avg_quarterly_sales, tmp1.sum_sales, …`;
- regress union: `Sort Key: ss.x` over `Subquery Scan on ss / Filter`.

Slice 79 left this out, because the Sort and Group Key sites would have
disagreed. The regress evidence gives the discriminator: every kept scan in
PG's expected output carries a Filter, and the one goopg keeps without
quals (aggregates' `q1`) is removed by PG, which prints the internals.

- `resolveKeySource`'s Filter arm stops at a Filter directly over a Subquery
  Scan. It returns the scan's column, named `alias.col` in
  `reg.boundaryKeyName`, and the name is always qualified: a plan holding a
  subquery RTE has rtable \> 1, so PG's useprefix is on. Every key site
  shares this function, so Sort and Group Key agree by construction. A
  qual-less Subquery Scan stays transparent (the M0146-0005w arm).
- Slice 79's keep-today's-text branch for a Subquery Scan child is removed.

Test: `TestSortKeyStopsAtKeptSubqueryScan`, whose PG 18.3 oracle is
`Sort Key: tmp.av, tmp.s`.

Movement:

- Q53 and Q63 now print PG's Sort Key byte for byte. The parity diff
  already counted them as matches under its alias canonicalisation (N4),
  so CATEGORIES-EXCL-MATCH is unchanged (rendering 17 / 20).
- Regress union.sql goes 970 → 968 diff lines: two of its three `ss.x`
  keys now match.
- Q67 prints `dw2.*` where PG prints `dw1.*`, because PG turns
  `rk <= 100` into a WindowAgg run condition, which makes its `dw2`
  trivial. goopg has no run conditions, so it keeps `dw2`; the name is
  right for goopg's plan, and the old bare text mismatched too. Ledgered.
- Gates: units, spotcheck, sweep 96/96, arm 24/24, fire set, regress (12
  cases) and ea-ratchet (10) pass.

Evidence: `analysis/m0146/m0146-0005/slice80/`.

## Slice 81: M0146-0005cb — a key over a UNION deparses through the first arm

`set_deparse_plan` (ruleutils.c) makes an Append's first child its outer
plan. A key above a UNION ALL therefore prints the leftmost arm's
expression: TPC-DS Q5/Q77 show `('store channel'::text)`, and regress
union.sql shows `(1), (generate_series(1, 10))`. goopg printed the output
label (`channel`), because `resolveKeySource` declined at a `*SetOp`.

- **UNION arm:** a `*SetOp` with `Op == SetOpUnion` steps into `Left`,
  whose columns sit at the same positions; a left-deep chain reaches the
  leftmost arm one link at a time. INTERSECT and EXCEPT decline, because
  PG's SetOp node deparses through its own flagged input. The arm is
  another query level, so what the chase returns is pinned to its
  evaluating node, as past a join, and the key's relation id and name stop
  constraining it.
- **Partition and inheritance children:** goopg builds these fan-outs as
  UNION ALL SetOps too, but PG names such columns by the parent reference
  (`prt1.a`). A scan of a relation with `PartitionParentOID` or
  `InheritsParentOIDs` therefore declines (`scanNodeTable`), keeping
  today's text.

Test: `TestSortKeyDeparsesThroughFirstUnionArm`, with the PG 18.3 oracle

    Group Key: ('a chan'::text), ua.id
    Sort Key: ua.v, ua.id

It fails with the arm disabled. goopg omits the literal's `::text` cast,
which the test does not pin.

Movement:

- CATEGORIES-EXCL-MATCH rendering goes SF0.25 17 → 16. Per line, Q23, Q49
  and Q76 lose their key-text divergences at both scales (SF1's query
  count stays 20: Q49 and Q76 still differ elsewhere). No new rendering
  divergence appears.
- Regress: union.sql goes 968 → 953 diff lines. In inherit.sql three keys
  now print PG's `tenk1.thousand, tenk1.tenthous` / `a.thousand,
  a.tenthous`. One prints `(tenk1.thousand)`, because goopg casts the arm
  to int8. That comes from the ledgered integer-literal typing (a bare
  `42` is bigint in goopg and integer in PG, re-confirmed:
  `pg_typeof(42)`), so the UNION column unifies to int8.
- Gates: units, spotcheck, sweep 96/96, arm 24/24, fire set, regress (12
  cases) and ea-ratchet (10) pass.

Evidence: `analysis/m0146/m0146-0005/slice81/`.

## Slice 82: M0146-0005cc — a key deparses through an inlined CTE

A single-reference CTE is inlined as a subquery and flattened, so PG's plan
has no CTE Scan for it. A key over a UNION of such CTEs therefore deparses
into the first body: TPC-DS Q33/Q60 print `Group Key: item.i_manufact_id`,
where goopg stopped at the CTE reference (`ss.i_manufact_id`).

- `resolveKeySource` gains an inlined `*CTEScan` arm. It steps into the
  body at the same position and pins names as past a join. A materialised
  CTE keeps its scan (and name) in PG too, and a recursive self-reference
  has no body to enter, so both keep today's text.
- **Level-crossing flag.** Slice 79's Project-arm descent accepted a landing
  column only if its relation id equalled the target's. Across an inlined
  CTE body or a UNION arm the ids restart, so they never compare. The
  crossing arms now set `reg.chaseCrossedLevel`, and the descent accepts
  the landing on name alone when the flag is set.
- The names of a key found inside a body are pinned from the Aggregate or
  Project node itself, not its child: the body aggregate's own Group Key
  line resolves `item` from there.

Test: `TestGroupKeyDeparsesThroughInlinedCTE`, whose PG 18.3 oracle is the
outer `Group Key: it.m`. It fails with the arm disabled.

Movement:

- CATEGORIES-EXCL-MATCH rendering goes SF0.25 16 → 15 and SF1 20 → 19; the
  Group Key lines of Q33, Q56 and Q60 now match PG, with no new rendering
  divergence.
- Regress (12 cases) is unchanged apart from join.sql's known row-order
  flap.
- Gates: units, spotcheck, sweep 96/96, arm 24/24, fire set and ea-ratchet
  (10) pass.

Not done (ledgered): an aggregate's arguments are not chased. Q33/Q60's
Sort Key `(sum(ss.total_sales))` needs PG's
`(sum((sum(store_sales.ss_ext_sales_price))))`, including PG's forced
parentheses around a non-Var referent inside the call. Other leftovers are
grouping-set (MixedAggregate) keys (Q5/Q77) and Q61/Q66's chase failures.

Evidence: `analysis/m0146/m0146-0005/slice82/`.
