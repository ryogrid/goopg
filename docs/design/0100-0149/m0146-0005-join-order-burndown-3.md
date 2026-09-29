# M0146-0005 (part 3): slices 38+ — parameterized-probe qual placement

Continuation of [m0146-0005-join-order-burndown-2.md](m0146-0005-join-order-burndown-2.md)
(residual triage and slices 15-37) — split per the design-doc size rule (D3).
Same task and census family. Slices 36 and 37 (the probe-Filter rendering these
slices refine) are in part 2.

## Slice 38: M0146-0005al — the probe-Filter rule sees `= ANY (list)` operands

TPC-DS Q48 differed from PG in one category only, `qual-placement`. The
`customer_address` probe's OR clause

```
((ca_state = ANY ('ND','NY','SD')) AND ss_net_profit >= 0 AND ...) OR ...
```

stayed on the nested loop as a `Join Filter`, where PG prints it in the
inner index scan's `Filter` (its `ppi_clauses`, slice 36). The one level
higher `customer_demographics` join, whose clause has no IN list, already
rendered the PG way.

- **Cause.** `innerParamQual` classified each conjunct with the shallow
  `WalkExprTree`, which treats an `InExpr` as a leaf. Every inner read in
  this clause sits inside a `ca_state = ANY (...)` operand, so the clause
  looked outer-only (it reads only `ss_net_profit` otherwise) and the rule
  declined.
- **Fix.** New `optimizer.WalkExprHostScope` exposes the exhaustive
  `walkExprRefs` driver (`exprChildSlots`, `scopeIgnore`), which visits an
  IN node's operand and list. It returns false on an unenumerated
  expression kind, and `innerParamQual` then keeps the clause on the join
  (fail-closed). The display rewrite `renderedParamQual` already used the
  exhaustive cloner, so the moved clause renders correctly without change.
- **Test.** `TestInnerParamQualSeesInListOperand` (fails before the fix).

Movement: TPC-DS SF0.25 PLAN-PARITY match 16 → 17 (Q48 now matches PG),
`qual-placement` 17 → 15 (Q48, Q13). At SF1 the Q48 and Q13 plans change the
same way, but other divergences come first there, so the counts do not move.
TPC-H is unchanged (match 9). In the regress runner (10 EXPLAIN-heavy cases,
HEAD-built baseline) no output differs except `Build Time` timing. Evidence:
`analysis/m0146/m0146-0005/slice38/`.

## Slice 39: M0146-0005am — PG's ppi_clauses rules for equalities in a probe's residual

Slice 36 kept every residual containing an `inner = outer` column equality on
the join, on the theory that an equivalence-class clause is always a Join
Filter (TPC-H Q9/Q21). The TPC-DS SF0.25 census shows PG does two other
things with such equalities:

- **Q84**: goopg printed `Join Filter: (customer.c_current_cdemo_sk =
  customer_demographics.cd_demo_sk)` above a probe whose Index Cond is
  `cd_demo_sk = customer.c_current_cdemo_sk`. PG prints the clause nowhere:
  `generate_join_implied_equalities` yields one clause per class for the
  parameterized rel, and the index consumes it.
- **Q50**: `sr_customer_sk = ss_customer_sk` belongs to a class the
  `store_sales_pkey` probe does not use, and store_returns is in the probe's
  `required_outer`. PG puts it in `ppi_clauses`, so it prints as the probe's
  `Filter`.
- **Q9/Q21** differ from Q50 only because their equality names a relation
  outside `required_outer` (supplier, orders), and the existing
  `probeRelations` test already keeps those on the join.

The rule (`paramQualPlacement`, replacing `innerParamQual`) now classifies each
conjunct:

1. `restatesProbeKey`: an `inner.col = outer.x` equality where the probe binds
   index column `col` to the same outer column (`probeKeyEqualities` pairs
   `Key` / `Keys` (+ `SkipPrefix`) / `RangePrefix` with index columns the
   way `formatIndexCond` does) is dropped from the rendering.
2. Otherwise the conjunct must read the inner relation and stay within
   `required_outer` (the host-scope walk from slice 38), equality or not.
3. Any conjunct failing (2) keeps the whole residual on the join.

`renderedParamQual` returns `(qual, moved)`. A moved residual with no
remaining conjunct prints nothing on either line. The residual is still
evaluated in full by the executor (the dropped conjunct is implied by the
probe, so it never rejects a row), so ANALYZE's rejection count is
unaffected.

Movement: TPC-DS `qual-placement` 15 → 13 at SF0.25 (Q50, Q84) and 18 → 17
at SF1. Q17/Q25 and SF1 Q37 lose a key-restating Join Filter the same way.
TPC-H filter lines are byte-identical (Q9/Q21 keep theirs). In the regress
runner, seven `Filter: (t1.a = t2.a)` / `(t1.id = t2.id)` lines disappear from
join.sql's self-join-elimination EXPLAINs. They restated the probe's own
Index Cond, and PG prints none of them. Evidence:
`analysis/m0146/m0146-0005/slice39/`.

Ledgered: the moved equality keeps its written operand order. PG builds it
outer member first (`build_implied_join_equality(outer_em, inner_em)`), so
Q50 renders `(ss_customer_sk = store_returns.sr_customer_sk)` against PG's
`(store_returns.sr_customer_sk = ss_customer_sk)`.

## Slice 40: M0146-0005an — a probe residual splits per clause, and ANALYZE attributes its rejections

Slices 36 and 39 moved a parameterized nested loop's residual to the probe
only when every conjunct was movable. PG decides per clause
(`join_clause_is_movable_into`). In TPC-DS Q24, `customer.c_birth_country <>
upper(ca_country)` moves into the `customer_address` probe's `Filter`, while
`store.s_zip = ca_zip` stays the `Join Filter`, because store is outside the
probe's `required_outer`. goopg kept both on the join. Slice 36 deferred the
split because the executor keeps one rejection counter per join.

- **Placement** (`paramQualPlacement`). Each conjunct is dropped (it restates
  the key, slice 39), moved, or kept. The function returns `(probe, join,
  applies)`. The Join line (Lateral `Join`) or the NLI's `Filter` line prints
  the kept part, and the probe prints the moved part.
- **Relation identity.** Clauses are placed by `SourceTableIdx`, and a
  synthetic `(a JOIN b) JOIN c` showed that this id is only unique within a
  planning scope: the nested join's b carried the probe's id. `probeRelations`
  now returns the inner ids (the probe's output schema only; a bitmap
  probe's `Recheck Cond` repeats the key's outer column, which put part in
  the inner set for TPC-H Q19 during development) and the outer ids (the key's
  outer references). An outer column whose id is also an inner id is
  unidentifiable, so its conjunct stays on the join (fail-closed).
- **ANALYZE.** The join still evaluates the whole residual to decide. A new
  optional interface, `probeFilterAttributor`, is implemented by
  `nestedLoopIndexJoinOp` and `joinOp`. `maybeInstrument` wires it only when
  the residual splits across both lines. The operator then re-evaluates the
  moved part on each rejected row and counts the row there when that part
  fails (`stats.probeFilterRejected`), as PG's scan qual would have rejected
  it before the join qual ran. Only rejected rows pay for this, and only under
  ANALYZE. `splitParamQualRejections` gives the join line the remainder. On a
  synthetic join (expected probe 33, join 7) the join line reports 7. The
  probe line prints nothing because goopg does not instrument a probe driven
  inside the nested loop, which also hides slice 36's fully moved counts.
  That is ledgered.

Movement: TPC-DS SF1 PLAN-PARITY match 14 → 17 (Q17, Q25, Q29) and
`qual-placement` 17 → 14; SF0.25 `qual-placement` 13 → 12 (Q24). TPC-H filter
lines are byte-identical. The regress runner (10 cases) is unchanged versus
HEAD. Evidence: `analysis/m0146/m0146-0005/slice40/`.

## Slice 41: M0146-0005ao — the parameterized-probe partial nested loop admits LEFT

TPC-DS Q40's first divergence is its aggregate: PG runs `Finalize
GroupAggregate -> Gather Merge -> Partial GroupAggregate`, while goopg ran a
serial GroupAggregate. The cause is one level below. PG's `catalog_returns`
LEFT join (`Nested Loop Left Join` probing `catalog_returns_pkey`) runs
inside the workers. goopg could only run it above the Gather Merge, so no
partial aggregate could be split under it.

The parameterized-probe partial nested-loop family admitted {INNER, SEMI,
ANTI} (M0146-0002i/j). LEFT was held back by ledger rows `m0146-0002a` /
`m0146-0002j` until a left-probe consumer was measured. Q40 is that
consumer. A LEFT verdict is per outer row: a worker emits each qualifying
probe row, or the outer row null-padded when none qualifies. That makes it
as worker-local as ANTI. The four gates widen together (the move-together
rule):

- the producer: `addPartialNestLoopPaths`'s V1-nl-inner set
  (joinpathsnli.go), now PG's full dispatch set (joinpath.c:2022-2031);
- the classifier twin: `partialProbeNestLoopJointype` (gatherpaths.go);
- the plan-node twin: `partialProbeNestLoopJoinType` (parallel.go);
- the executor twin: `lateralProbeJoinPartial` (parallel_scan.go).

`TestParallelLateralLeftProbeIdentity` runs the LEFT probe under 1, 2 and 4
workers. It checks the total row count and, separately, the number of
null-padded rows (50 of 100). With the executor twin left at the old set it
fails with 100 padded rows, because each worker padded outer rows that other
workers' partitions matched. The refusal pins in the optimizer and executor
unit tests now use FULL/RIGHT, which need a cross-worker unmatched-inner
reduction.

Movement: TPC-DS SF0.25 PLAN-PARITY match 17 → 18 (Q40 = PG), `join-order` 66
→ 64, `join-method` 38 → 36, `parallelism` 47 → 46. At SF1, Q40 and Q80's LEFT
probes move inside the Gather Merge as well. Q40's category set there moves
from parallelism to sort-strategy, because a join order further down still
differs. SF1 match is unchanged at 17. At SF1 both plans also join the
pre-existing family (23 queries in the baseline) whose post-pass-parallelized
subtree shows a per-worker `Sort` cost below its unscaled child. That is the
Gather-stamping display, not new here. TPC-H plans are byte-identical apart
from costs. The regress runner (10 cases) is unchanged. Evidence:
`analysis/m0146/m0146-0005/slice41/`.

## Slice 42: M0146-0005ap — the partial-aggregate split stands on the cheapest partial path

TPC-DS Q99 (PG: `Finalize GroupAggregate -> Gather Merge -> Partial
GroupAggregate -> Sort -> Hash Joins over a Parallel Hash Join`) planned
instead as a serial Memoize nested-loop chain, parallelized after planning. The
DP trace shows the search did build PG's input: a partial Parallel Hash Join
path at the top relation (12890 per worker) and a Gather over it (14071.78,
the relation's cheapest). The grouping stage's split arm ignored it. It split
the aggregate over the COMMITTED serial child and priced the partial input
with `parallelSeedCost` (the whole serial run cost divided by the parallel
divisor), so the nested-loop chain cost 8404 per worker, below any real
partial path. The same function was the ledgered cause of TPC-H Q4's residual
(`M0146-0005af`: goopg's per-worker seed 25.7k against PG's partial path
68.9k).

### PG

`create_partial_grouping_paths` (planner.c:7435-7470) builds the partial
aggregate on `input_rel->cheapest_partial_path` and prices it at that path's
own per-worker cost. There is no divided-serial estimate anywhere.

### Change

1. **Partial seed** (`searchedCheapestPartialInput`, searchedtree.go). When
   the searched input rel has a partial path, `createGroupingPaths` rebuilds
   `PartialPathlist[0]` under a Gather through the same boundary as
   `searchedCheapestTotalInput`. `addPartialAggSplitPath` gets the partial
   path as a new argument: it unwraps the Gather as before, and takes the
   path's worker count, per-worker rows and cost instead of
   `parallelSeedCost`. With no partial path, the old seed stays (the
   committed-child route).
2. **Plain presorted aggregate pays for its Sort** (groupingpaths.go). The
   PLAIN arm built `Aggregate -> Sort` but charged `costAgg` on the unsorted
   seed. PG's `cost_agg` reads the input path's cost. Honest seeds exposed it:
   TPC-DS Q28's `Aggregate -> Sort -> Gather` priced below its own Sort beat
   PG's per-worker Sort under a Gather Merge.
3. **The Sort over `cheapest_total_path` is offered first** (groupingpaths.go).
   `add_paths_to_grouping_rel` walks `input_rel->pathlist`, which `add_path`
   keeps in ascending total cost, so the cheapest-total input (sorted) is
   offered before any presorted runner-up. `add_path`'s tie chain
   (parallel-safety, rows, then `compare_path_costs_fuzzily` at 1e-10) ends in
   "keep the old path" when startup and total trade off, so the order decides
   true near-ties. TPC-DS Q93: PG's `Sort -> Gather` beats a per-worker Sort
   under a Gather Merge by 0.03.
4. **Reconcile leaves a Finalize aggregate's keys on the partial input**
   (joinlayout.go). A Finalize aggregate is a copy of the original: its keys
   and arguments address the Partial's input row (`PartialSource`), not its
   child (the Gather over the partial-state row). `reconcileNLILayoutBody`
   re-resolved them against the child. That is a production pass, and it
   would have moved them. The splits now happen during planning, so TPC-H
   Q15's view aggregate sits inside the outer join search, and
   `assertSearchedTreeNeedsNoReconcile` panicked (`l_suppkey` 4 → 0). Test:
   `TestReconcileLeavesFinalizeKeysOnPartialInput`.

### Results

| instrument | before | after |
|---|---|---|
| TPC-DS SF0.25 PLAN-PARITY match | 18 | 19 (+Q55 +Q62, −Q40) |
| TPC-DS SF1 match | 17 | 18 (+Q74 +Q97, −Q93) |
| TPC-H match | 9 | 10 (+Q4) |
| TPC-DS aggregation-strategy SF0.25 / SF1 | 33 / 37 | 26 / 28 |
| TPC-DS sort-strategy SF0.25 / SF1 | 48 / 51 | 45 / 48 |
| TPC-DS parallelism SF0.25 / SF1 | 46 / 56 | 43 / 53 |

Costs rose (`join-method` +2 at both scales, `qual-placement` +2/+1,
`scan-type` +1 at SF1) where the partial input's join order differs from the
committed serial one. Q40 (SF0.25) and Q93 (SF1) now fall on the other side
of near-ties (ledgered). SF1 Q74's plan is now PG's, but goopg runs it about
5–9% slower. The query sits at the fire-set's 600 s limit in both arms: the
baseline took 591–603 s, the candidate 617–654 s. It timed out once and
passed on re-run (evidence `q74-sf1-timing.txt`; filed as M0146-0036).
Evidence: `analysis/m0146/m0146-0005/slice42/`.

## Slice 43: M0146-0005aq — a qual crosses a Gather only when it is parallel-safe

Investigating TPC-DS Q10's `parallelism` record (goopg evaluates an OR of two
hashed `ANY` SubPlans, each over its own `Gather -> Parallel Hash Join`, on a
nested loop beneath the statement's Gather, nesting parallel plans inside
workers) led to the qual-pushdown passes. PG never evaluates a
parallel-restricted or parallel-unsafe qual inside workers. A rel whose
restrictinfo is not `is_parallel_safe` gets `consider_parallel = false`
(`set_rel_consider_parallel`, allpaths.c), so no partial path, and no Gather,
ever sits beneath it. goopg's post-search pushdown crosses Gathers
unconditionally:

- `pushConjunctTraced`'s `*Gather` arm (O15, M0137-0016);
- `pushConjunctIntoCTEBodyTraced`'s `*GatherMerge` arm (R56).

A `nextval()` restriction, or a SubPlan over its own Gather, could therefore
be planted into worker plans.

`gatherPushableConjunct` (considerparallel.go) gates both arms. It is
`isParallelSafeExpr` with a nil catalog, since the passes carry none, plus a
fail-closed rule: a function that is not a builtin (`catalog.IsBuiltinProcName`)
keeps the conjunct above the Gather, because a user routine's `proparallel`
cannot be read without the catalog. Test:
`TestPushdownKeepsParallelUnsafeConjunctAboveGather` (the restricted and
user-routine cases cross without the gate).

Movement: none. No TPC-DS plan changes at either scale, TPC-H plans are
identical, and the regress runner (13 cases) shows only the known unordered
`join.sql` row flap. Q10's qual does not reach the workers through these
passes: it is attached to the nested loop by another placement, which is
filed as M0146-0037. Ledgered: a user routine that is actually `PARALLEL
SAFE` stays above a Gather here, where PG would push it (the catalog is not
threaded into the pushdown passes).

## Slice 44: M0146-0005ar — the partial-aggregate splices keep worker-unsafe wrappers above the Gather

M0146-0037 (TPC-DS Q10) was traced to its route with temporary
instrumentation. The search leaves Q10's residual
`ANY (c_customer_sk = (hashed SubPlan 1).col1) OR ANY (… SubPlan 2 …)` (each
SubPlan a `Gather -> Parallel Hash Join`) correctly above its Gather:
`Filter -> Project -> Gather -> joins`. The grouping stage's split producer
then unwraps the Gather: `gatherToUnwrapForPartialAgg` splices it out from
under the pass-through wrappers (`Filter(Project(Gather(X)))` becomes
`Filter(Project(X))`), and the winning "gathered" arm puts a new Gather on
top. The Filter, and the SubPlans' own Gathers with it, moved into the
workers. `spliceGatherOnPartialSpine` (the M0146-0025 route) does the same.

`wrapperRunsInWorkers` now gates both splices. A `*Filter` predicate or
`*Project` target is peeled below the new Gather only if it passes
`gatherPushableConjunct`, the rule slice 43 gave the qual-pushdown passes.
When a wrapper fails, the split declines for that input, and the aggregate
stays above the committed `Filter -> Gather`, whose Filter prints as the
Gather's own `Filter:` (evaluated in the leader). Test:
`TestGatherSplicesKeepWorkerUnsafeWrappersAbove`.

Movement: TPC-DS Q10 and Q35 (the OR-of-EXISTS queries) change at both
scales. Their parity counts do not move, because other divergences come
first. TPC-H Q17's correlated `l_quantity < (SubPlan 1)` filter now stays
above the Gather, where PG evaluates it (on its serial Hash Join). TPC-H
`parameterisation` 3 → 2, `aggregation-strategy` 2 → 1, `sort-strategy` 2 →
1, `parallelism` 5 → 4; the TPC-H match count is unchanged (10). Evidence:
`analysis/m0146/m0146-0005/slice44/`.

## Slice 45: M0146-0005as — a join residual's columns deparse through the child that produced them

TPC-DS Q46/Q68's `qual-placement` records were a rendering difference. goopg
printed `Join Filter: (ca_city <> bought_city)`, while PG prints
`(current_addr.ca_city)::text <> (customer_address.ca_city)::text`. PG's
ruleutils deparses an OUTER/INNER Var through the child plan's target list
(`resolve_special_varno`): a column that a grouped subquery republishes
under an alias prints as the grouped column it carries, and every column
prints with the relation that produced it. goopg rendered join-residual
columns by name and statement-level binding id only. That printed the alias
for a derived column, and it named the wrong relation whenever binding ids
collided across query levels: regress join.sql printed `(ax = t2.a)` for
`q1.ax = q2.a` (it is `t2.a = t3.a`), and `(i42.f1 > 1)` for `ON (i43.f1 >
1)`.

- `explainNames.resolvedColumn` generalizes `setOpResolvedColumn`'s walk
  (joins by `concatJoinSide`, Projects, Filters, Sorts, Gathers,
  SubqueryScans) with an Aggregate group-key arm and a Memoize arm. It has
  two modes. The set-operation mode is unchanged: it answers only when the
  walk crossed a SetOp, and keeps its old arms. The join-residual mode
  (`joinResidualColumn`) answers for any walk that ends at a named scan, and
  stops at a SetOp, because an appendrel Var prints with the parent's alias
  (`tuplesest_parted.b`, regress inherit), not the first branch's.
- `subPlanReg.joinRow` is set only while a join's residual is printed (the
  Join Filter, an index join's Filter, and the split residual of slices
  36–40). The ColumnRef arm resolves positionally first there and falls back
  to the name-based rendering.

Test: `TestJoinFilterResolvesSubqueryColumnToSource`.

Movement: TPC-DS `qual-placement` 14 → 12 at SF0.25 and 15 → 13 at SF1.
SF1 `rendering` 18 → 17, and SF1 PLAN-PARITY match 18 → 19 (Q46 = PG). TPC-H
plan text is identical. In the regress runner, join.sql's join filters now
name the right relations: ten lines change, each checked against its query.
Evidence: `analysis/m0146/m0146-0005/slice45/`.

Ledgered: Hash/Merge Cond keys and ordinary Filters keep the name-based
rendering; the residual walk stops at a set operation, where PG prints the
appendrel parent's alias; and the `::text`-style casts PG deparses are not
printed.

## Slice 46: M0146-0005at — set-op branches share the statement's CTEs, and an inlined CTE reference prices its body

In the census, TPC-DS Q5, Q33, Q56, Q60 and Q80 had an `Append` over
UNION ALL branches that cost almost nothing. Q5 printed `Append
(cost=0.00..3.90)` over three branches of about 20000 each, and a root of
15.73 where PG has 55167. Two defects combined to produce this.

- **The leftmost set-op branch preplanned the WITH list a second time.**
  `planSelectWithSettings` plans the leftmost branch by recursing on the
  set-op statement itself, with its SetOp chain and trailing
  sort/limit detached but `s.With` still attached. The recursion ran
  `preplanWithClause` again into a scope of its own. Each branch therefore
  counted its references against a different `plannedCTE`. A CTE read by
  two branches looked single-reference to each and was inlined twice,
  where PG keeps one `CTE x` with a CTE Scan per branch (`cterefcount` is
  statement-wide, `SS_process_ctes`). The recursion now runs with `s.With`
  detached as well. The chain's data-modifying CTEs, which the leftmost
  branch's duplicate preplan used to supply, are wrapped around the finished
  set operation (`wrapDMLCTEPrefix`) on both set-op returns.
- **An inlined CTE reference was priced as a bare CTE Scan.** `CTEScan` is
  not a cost carrier, and `legacyDisplayChildren` had no arm for it, so
  every reference cost `cpu_tuple_cost × rows`. That is `cost_ctescan`,
  which is right for a kept CTE, whose body PG charges as an initPlan. It
  is wrong for a reference PG inlines: after `inline_cte` that reference is
  an ordinary subquery, priced by `cost_subqueryscan` over its body
  (costsize.c:1491-1493). The arm now descends into the body when
  `CTEScan.Inlined()`. `costSubplanLeaf`, the join search's price for a
  sub-plan leaf, reads the same function, so inlined CTEs in join trees stop
  being free too. Note that the reference count is final only once the
  statement is planned: a set-op branch priced before a later branch adds
  the second reference still reads one.

Tests: `TestExplainUnionBranchesShareOneCTE` and
`TestExplainInlinedCTEPricesItsBody`. Both fail on the base tree.

Movement: TPC-DS Q33/Q56 now elect PG's `GroupAggregate` over the Append
(it was a HashAggregate) at both scales. Their first divergence moves one
level deeper to `PG Merge Append | goopg Sort`: aggregation-strategy
9 → 7 at SF0.25 and 13 → 11 at SF1, sort-strategy +2 at each. Q5/Q80's
Append and root costs are now in PG's range (Q5 root 59253 vs PG 55167).
The match counts are unchanged at 19 for both scales, and the TPC-H census
is identical.

In all-depth `CATEGORIES-EXCL-MATCH`, qual-placement falls 12 → 9 at SF0.25
and 13 → 11 at SF1. A few categories rise by 1–2: join-method,
scan-type, parameterisation, parallelism and rendering. Those rises are
alignment only. Q33/Q56's branch plans are byte-identical apart from text
width, and the tree diff now pairs them with PG's per-branch sorts under
its Merge Append. Evidence: `analysis/m0146/m0146-0005/slice46/`.

Census recon from the same loop (no change):

- Q34/Q68/Q73 (`Bitmap Heap Scan` vs `Index Scan` on `customer_pkey`) are
  the `indexProbeCostMultiplier` lineage (M0142-0005c, parked).
- Q37's join order follows from the cold visibility map: there is no
  catalog_sales Index Only Scan (probe 93 vs PG 0.83). That is the owner
  vacuum item.
- Q16 is a near-tie inside PG itself. Forcing goopg's shape on the
  reference costs 17033.6 against PG's chosen 17033.36.

Ledgered:

- PG charges a kept CTE's body to the plan root as an initPlan
  (`SS_charge_for_initplans`). goopg's root costs omit it.
- A second reference is aliased `a_1` in PG. goopg prints the bare name.
- `WITH x AS (INSERT …) (SELECT …)` is rejected at parse time
  (pre-existing).

## Slice 47: M0146-0005au — grouping a UNION ALL of sorted members merges them (generate_orderedappend_paths)

Slice 46 left TPC-DS Q33/Q56 electing PG's GroupAggregate over their UNION
ALL. The first divergence was then `PG Merge Append | goopg Sort`. Each
channel branch is a GroupAggregate on the item key, so it already emits rows
in the order the outer GROUP BY needs. PG's `generate_orderedappend_paths`
(allpaths.c) files a Merge Append on the appendrel for every ordering a
child delivers, and `add_paths_to_grouping_rel` walks the input rel's whole
pathlist and takes that path as presorted input. goopg sorted the whole
Append instead.

- `orderedAppendInput` (new `orderedappend.go`) handles the ordering the
  grouping stage asks for. The Aggregate's input must be a plain serial
  UNION ALL chain: no merge, distinct-input, parallel-aware or
  coerced-type link. For each member it reuses the member when it already
  delivers the group keys by output position, and otherwise adds an
  explicit Sort (`create_merge_append_path`). It builds the Merge Append as
  the existing left-deep `SetOp.MergeKeys` chain, which the executor
  already merges and EXPLAIN already renders. It declines when no member is
  presorted, an ordering `all_child_pathkeys` would never propose.
- `memberOrdering` is `convert_subquery_pathkeys` for a member. A member
  is typically a Project choosing its body's columns over an inlined CTE
  reference. It crosses Projects through bare-column targets, looks
  through an inlined CTEScan or a SubqueryScan (both keep body positions),
  stops at the first key the target list drops, and falls back to
  `inputNodePathkeys`.
- The grouping sorted arm files the candidate after the Sort-over-cheapest
  and searched-pathlist candidates, which is PG's pathlist order. It is
  priced by `mergeAppendCost` (cost_merge_append, now shared with
  `addUnionMergeAppendPath`).

Test: `TestExplainUnionAllOfSortedGroupsMergeAppends` fails on the base
tree. The witness over `item` (enable_hashagg off, serial) is
byte-identical to PG, and its result md5 matches:
`analysis/m0146/m0146-0005/slice47/`.

Movement: TPC-DS SF0.25 PLAN-PARITY match 19 → 20 (Q56 = PG). Q33's
first divergence moves from depth 3 to 5 at both scales. At SF0.25, every
all-depth category except qual-placement falls by 1–2; qual-placement rises
9 → 11, because Q33/Q60 now align deeper. TPC-H is identical.

Ledgered:

- An ordered appendrel is offered only to the grouping stage. PG's Merge
  Append also feeds merge joins and ORDER BY through the appendrel's
  pathlist.
- The Merge Append's displayed cost is the legacy derivation (children's
  totals), not the `cost_merge_append` figure the election used. The
  distinct-UNION arm shares this.
- The partial arm (a Gather Merge over a Parallel Append) and group-key
  reordering to match a member's ordering are not ported.

## Slice 48: M0146-0005av — an inlined CTE reference PG cannot pull up is a SubqueryScan

In TPC-DS Q5/Q80, the first divergence was `PG Subquery Scan on ssr |
goopg GroupAggregate`, under the Append of the three channel members.
`inline_cte` (subselect.c) makes a single-reference CTE an ordinary
RTE_SUBQUERY before `pull_up_subqueries` runs. A grouped body is one
pull-up refuses, so the reference stays a subquery with a SubqueryScan.
Each member's `'store' || s_store_id` target list is then applied to that
projection-capable scan, which setrefs keeps because its tlist differs
from the subplan's. goopg gave derived tables this wrapper (slice 24), but
not inlined CTE references. Those rendered as their body unless a filter
was attached.

- `plannedCTE.needsScan` records PG's pull-up verdict for the body. It uses
  the same `derivedSubqueryNeedsScan` test derived tables take, and treats
  a simple UNION ALL as pulled up (`subqueryChainIsSimpleUnionAll`).
- `wrapInlinedCTEScans` runs at Plan()'s tail, where the reference count
  is final. It wraps each inlined reference that needs a scan in a
  SubqueryScan labelled with the reference's alias. It copies only the
  spine above a wrapped reference, and never enters a kept CTE's shared
  body. A reference already under a SubqueryScan is not wrapped again: an
  inner Plan() call can finish the subtree first, which printed the label
  twice.
- `stripTrivialSubqueryScans` then decides, with two refinements:
  - A Project directly on the leaf that computes a target keeps the node,
    because that is the level's final tlist folded into the scan
    (`create_projection_plan`). A bare-column Project is left to the
    consumption / physical-tlist test, since it is goopg's own narrowing
    or a plain select list.
  - A constant-true Filter is not a qual. It is the residue of a qual that
    M0146-0007b moved into the body. Without this rule, Q78's ss/ws/cs
    under merge joins kept three labels PG strips.

Test: `TestExplainInlinedGroupedCTEKeepsSubqueryScan` covers three cases:
a computed member keeps the node, an identity member strips it, and a
moved qual under a join strips it. It fails on the base tree, and its
third case fails without the constant-true rule.

Movement: TPC-DS SF0.25 PLAN-PARITY match 20 → 21 (Q80 = PG). Q5's first
divergence moves from depth 4 to 6 (SF0.25) and 9 (SF1), SF1 Q80 from 4 to
12, and Q39 from 2 to 4 at both scales. Scan-type falls 43 → 42 at SF0.25
and 49 → 46 at SF1, and rendering falls 22 → 20 and 19 → 18. TPC-H is
identical. Evidence: `analysis/m0146/m0146-0005/slice48/`.

Ledgered:

- A UNION ALL member without FROM prints `Values (1 rows)`, where PG
  prints `Subquery Scan on "*SELECT* n" → Result`, or a bare `Result`.
- Q5 has 3 wrappers against PG's 4, and Q77 has 0 against 1.
- Q23, Q47 and Q57 keep one wrapper PG strips.
- Sublink-held plans are not visited by the wrap pass.

## Slice 49: M0146-0005aw — a set-operation arm's groups are estimated over its target expressions

TPC-DS Q8's INTERSECT printed its inputs in written order, where PG puts
`Subquery Scan on a1` first. PG's `generate_nonunion_paths` swaps INTERSECT
inputs when the left arm has more groups than the right. Each arm's count
comes from `build_setop_child_paths`, which runs `estimate_num_groups` over
the arm's target-list expressions (`get_tlist_exprs`). The left arm's
`substr(ca_zip, 1, 5)` therefore reduces to `ca_zip` and its statistics:
3203 groups against the grouped `a1` arm's 200, so PG swaps. goopg's
`setOpArmGroups` estimated over references to the arm's output columns.
A computed column is opaque to `examineGroupVar`, so it fell to the
200-group default, the two arms tied at 200, and no swap happened.

- `setOpArmGroups` now estimates over a top Project's target expressions,
  against the Project's child. This is the tlist PG uses, so an
  expression's variables and their statistics are reached. Arms that
  group, and non-Project tops, keep the existing rules.
- The same count sizes INTERSECT and EXCEPT output (`estimateSetOp`), as
  PG's `dNumGroups` does.

Test: a case in `TestSwapIntersectInputs` (a computed arm over a
1000-distinct column beats a 400-row arm). It fails on the base tree.

Movement: TPC-DS PLAN-PARITY match SF0.25 21 → 22 and SF1 19 → 20 (Q8 =
PG at both scales). scan-type and aggregation-strategy each fall by 1.
TPC-H is identical. Evidence: `analysis/m0146/m0146-0005/slice49/`.

## Slice 50: M0146-0005ax — an ordering crosses a computing Project and a Subquery Scan (convert_subquery_pathkeys)

TPC-DS Q51 sorted its final 100 rows again: `Limit → Sort → Subquery Scan on
y → WindowAgg → Sort (CASE …)`, where PG prints no top Sort. The window's
input is sorted on `CASE WHEN web.item_sk IS NOT NULL THEN web.item_sk ELSE
store.item_sk END`, which is the expression subquery x publishes as
`item_sk`. PG's `convert_subquery_pathkeys` matches that pathkey to the
target-list entry computing it, so the rows y publishes are already ordered
on `ORDER BY item_sk, d_date`. goopg's `inputNodePathkeys` crossed only
schema-identical wrappers. It stopped at the Project that computes the CASE
columns, and it had no arm for `SubqueryScan`.

- Project arm: a non-identity, non-isolated Project now delivers
  `projectEmissionPathkeys`. That takes the child's ordering and maps each
  key to the output position of the target that computes the same
  expression (`exprEqual`), stopping at the first unmapped key. So a
  permutation moves a key and a narrowing keeps it. A key whose expression
  contains a function call or a sublink is not carried, because the
  Project re-evaluates it and a volatile value need not reproduce the sort.
  `orderPreservingExpr` is a whitelist of deterministic node kinds, pinned
  as a classifier in `exprSwitchInventory`.
- SubqueryScan arm: the wrapper publishes its subplan's rows position for
  position under the reference's names, so it is a positional-identity
  step.

`inputNodePathkeys` also feeds CTE-scan pathkeys and the slice-47 Merge
Append, so orderings computed inside CTE bodies now reach merge joins. In
Q64, the two `cross_sales` scans now merge-join without a Sort. The body
really is ordered on the keys, so the claim is sound. PG plans that body
differently and nest-loops, and Q64's first divergence is unchanged.

Tests: `TestProjectEmissionPathkeysCarriesComputedOrdering` (new; fails on
base). In `TestProjectIsPositionalIdentityRefusesEverythingElse`, the
permutation and narrowing cases now expect the translated key, and the
computed, isolated-scope and empty-target cases still stop. Q51's ordered
LIMIT output is md5-identical to PG's.

Movement: TPC-DS PLAN-PARITY match SF0.25 22 → 23 and SF1 20 → 21 (Q21 =
PG at both scales). Q51's first divergence moves from depth 1 to 4. TPC-H
is identical. Evidence: `analysis/m0146/m0146-0005/slice50/`.

## Slice 51: M0146-0005ay — a window reads an input that is already ordered (create_one_window_path)

After slice 50, TPC-DS Q51's first divergence was in its CTE bodies:
goopg sorted between the WindowAgg and the GroupAggregate below it. The
GroupAggregate already emits rows ordered on `(ws_item_sk, d_date)`, which
is the window's `PARTITION BY ws_item_sk ORDER BY d_date`.
`create_one_window_path` stacks a Sort only when the input's pathkeys do
not already contain the window's PARTITION BY ++ ORDER BY. goopg's
`childDeliversSortKeys` only recognised a child that was itself a
matching Sort or a presorted WindowAgg. `addWindowPaths` always priced a
full sort, with `costWindow`'s presorted credit fixed at 0 (the S2b-3b
wiring left open).

- `childDeliversSortKeys` also answers `pathkeys_contained_in` against
  `inputNodePathkeys(child)`. That covers a sorted aggregate's group keys,
  a merge join's keys, a searched tree's stamp, and orderings carried
  through Projects (slice 50).
- `addWindowPaths` tracks the known ordering across the window chain. It
  starts from the input's pathkeys, and a WindowAgg passes its input's
  order through. A level whose required keys are contained pays no sort
  (`presortedCount` = all keys). Otherwise the level is priced as the full
  Sort that `createWindowPlan` builds, and its keys become the known
  ordering. Price and plan now agree.

Test: `TestExplainWindowOverSortedGroupsSkipsSort` pins PG 18.3's Sort
counts (verified on the scratch PG): a window on the group keys needs 1
sort, a window on another key needs 2. It fails on the base tree.

Movement: TPC-DS SF0.25 PLAN-PARITY match 23 → 24 (Q53 = PG). Q47/Q57's
CTE bodies now equal PG's. Their first-divergence record moves to the
main query's pre-existing `Subquery Scan on v2`, whose plan text is
identical to before. Q51's ordered output is still md5-identical to PG's.

| Category (all depths) | SF0.25 | SF1 |
|---|---|---|
| sort-strategy | 43 → 39 | 46 → 42 |
| parallelism | 41 → 36 | 51 → 48 |
| join-method | 37 → 33 | 38 → 36 |

In the regress runner, window.sql's three `COUNT(*) OVER (ORDER BY
t1.unique1)` nested-loop plans drop a Sort PG never had. TPC-H is
identical. Evidence: `analysis/m0146/m0146-0005/slice51/`.

## Slice 52: M0146-0005az — a kept CTE reference is priced by cost_ctescan

PG prices a scan of a CTE that stays a CTE with `cost_ctescan`. It charges
`cpu_tuple_cost` per stored tuple for the tuplestore, plus `cpu_tuple_cost`
and the restriction quals per tuple scanned. The referenced query is an
initPlan charged to the plan root, never to the scan. goopg priced the leaf
in two ways:

- The join search used the sub-plan shape (`costSubplanLeaf`). Unfiltered,
  that happened to equal `2*cpu_tuple_cost*rows`. A filtered leaf, though,
  charged its surviving rows (TPC-DS Q47's filtered `v1`: 38.27, where PG
  charges 144.38 for 3850 rows).
- EXPLAIN printed `cpu_tuple_cost*rows` (38.23, where PG prints 77.00).

- `costKeptCTEScanLeaf` (joinsearch.go) prices every non-inlined CTE leaf
  as the CTE's output rows × (`2*cpu_tuple_cost` + the local filter's
  conjunct count × `cpu_operator_cost`). The conjunct count is the
  currency `baseSeqScanCostInputs` uses.
- `DeriveLegacyDisplayCost` gains the matching `CTEScan` arm, so an
  unfiltered kept reference prints PG's figure.

Test: `TestExplainKeptCTEScanCostsTwoTuplesPerRow` fails on base (2.00
against the required 4.00).

Movement: no match or first-divergence change. Q47's CTE scans now print
76.46 for 3823 rows (PG 77.00 for 3850), and its filtered `v1` prints
105.13 (PG 144.38; PG counts every operator inside the CASE qual). The
join-order effect is mixed and recorded as measured. Q4/Q11's CTE-scan
join order moved toward PG at SF1 but away from it at SF0.25, where the
baseline had shared PG's Merge Join base. All-depth categories: SF0.25
join-method 33 → 35 and qual-placement 15 → 14; SF1 join-method 36 → 34
and qual-placement 14 → 11. TPC-H is identical, and the regress runner
shows no delta. Evidence: `analysis/m0146/m0146-0005/slice52/`.

Ledgered: the qual term counts conjuncts where `cost_qual_eval` counts
operators and function calls. That understates CASE-heavy CTE filters and
is what still separates Q4 at SF0.25.

## Slice 53: M0146-0005ba — scan quals are priced by cost_qual_eval's operator count

Slice 52's residual. PG prices a restriction qual with `cost_qual_eval`:
every operator and function call costs its procost (1 for built-ins) ×
`cpu_operator_cost`. AND, OR, NOT, CASE and NULL tests are free, and a
ScalarArrayOpExpr costs half its list per row, or a hash plus a
comparison once the list reaches nine constants. goopg charged one
`cpu_operator_cost` per top-level conjunct, in every scan-cost site: seq
and partial seq scans, index and index-only qpquals, bitmap heap quals,
and the new CTE leaf. TPC-DS Q47's filter on `v1` is seven operators in
three conjuncts.

- `qualEvalOps` (new qualevalcost.go) is `cost_qual_eval_walker` for
  goopg's expression kinds. It is built on `walkExprRefs`, which owns the
  recursion, does not enter sublink scopes, and fails closed. It returns
  startup and per-tuple operator counts.
- The shared currency changes in one place per population:
  `baseSeqScanCostInputs` (seq and partial seq), `localQualOpCount`
  (index, index-only and bitmap qpquals), and `costKeptCTEScanLeaf`. The
  index sites subtract the consumed index clauses' own cost
  (`indexClausesEvalOps`) rather than one per clause. `costSeqscan` and
  `costParallelSeqscan` take a float operator count.

Tests: `TestQualEvalOpsMatchesCostQualEval` checks Q47's filter at 7 and
the IN-list linear/hashed boundary at 8 and 9 elements.
`TestLocalQualOpCountMirrorsSeqRivalCount` and
`TestParamIndexQualOpCountAddsPopulations` are re-pinned to comparison
conjuncts, since a bare column or constant costs 0 under
`cost_qual_eval`.

Movement: TPC-H PLAN-PARITY match 10 → 11 (the Q15 view body equals
PG's), with join-order, scan-type, sort-strategy and parallelism each −1.
TPC-DS match counts are unchanged: SF0.25 scan-type −1 (Q83), and SF1
join-order +1 (Q68). Q47's filtered CTE scan now costs 143.36, which is
`cost_ctescan` exactly on goopg's 3823 rows (PG prints 144.38 over 3850).
Q4's SF0.25 CTE join order did not move. Evidence:
`analysis/m0146/m0146-0005/slice53/`.

Ledgered:

- The startup half (a hashed IN list's table build) is computed but not
  yet added to scan startup costs.
- Casts count 0, where PG's cast functions cost 1 and CoerceViaIO costs 2.
- Every function's procost is taken as 1.
- A sublink counts 1, where PG uses the SubPlan's per-call cost.

## Slice 54: M0146-0005bb — a statistics-less grouping variable still belongs to its relation

TPC-DS Q75's `all_sales` CTE groups the output of a UNION subquery by five
columns. goopg estimated 124831 groups, where PG estimates 12155; the
10× larger CTE scans then chose a Merge Join where PG hash-joins. PG's
`examine_variable` finds no statistics for a subquery output that is a
set operation, but it still records the variable's relation. That lets
`estimate_num_groups` clamp per relation: the product of the 200 defaults
is capped at `rel->tuples`, or a tenth of it when several variables come
from the relation (121550 / 10). goopg's unresolved variables had no
relation, so they multiplied freely up to the input rows.

- `examineGroupVar`'s fallback asks `groupVarSourceNode` for the variable's
  producing node. The walk crosses row-preserving wrappers and join sides
  like `resolveBaseColumn`, and stops at the first relation-level node (a
  set operation, an aggregate, a CTE or subquery scan, a computing
  Project). That node becomes the variable's relation, with its row
  estimate as `rel->tuples`. Base scans are unaffected, since
  `resolveBaseColumn` answers for them first.
- A partitioned or inherited table's UNION ALL expansion is excluded
  (`setOpExpandsTableHierarchy`: some member, under the appendrel's
  translation Project, scans a partition or an inheritance child). To PG
  that is one relation read through the parent's inherited statistics.
  The first attempt missed the Project wrapper and re-estimated two
  partition_aggregate.sql plans.

Tests: `TestGroupsOverSetOpClampPerRelation` (fails on base: 10000 groups
against 1000) and `TestGroupsOverPartitionAppendNotClampedAsSubquery`.

Movement: TPC-DS SF0.25 PLAN-PARITY match 24 → 25 (Q75 = PG). Q76's first
divergence moves from depth 1 to 6 at both scales. SF0.25 join-order
59 → 58, join-method 35 → 34 and rendering 21 → 20; SF1 join-method
34 → 32. Parallelism rises by 1 at each scale, from Q76's deeper records.
TPC-H is identical. Evidence: `analysis/m0146/m0146-0005/slice54/`.

Ledgered: the parent's inherited statistics for partitioned or inherited
tables are not implemented (goopg reads no `stainherit` rows), so those
variables keep the relation-less default. The variable key is the
consumer-side index, so two references to one source column are not
de-duplicated as PG's `add_unique_group_var` would.
