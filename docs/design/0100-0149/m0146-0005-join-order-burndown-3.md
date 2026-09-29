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
