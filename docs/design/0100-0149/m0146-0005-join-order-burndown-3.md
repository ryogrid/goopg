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
