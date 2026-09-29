# M0146-0007: single-reference CTE inlining (`inline_cte`)

Status: slices 1-3 landed 2026-09-26; multi-reference
`NOT MATERIALIZED` and reference-site planning are open.

## PG behaviour

`SS_process_ctes` (postgres/src/backend/optimizer/plan/subselect.c) inlines
a CTE into an ordinary subquery (`inline_cte`) when all of these hold:

- it is written `NOT MATERIALIZED`, or it has no keyword and is referenced
  exactly once;
- it is not recursive;
- the query owning the WITH is a SELECT, and the body contains no DML;
- a multiply-referenced body has no outer self-reference;
- the body has no volatile function.

The reference then plans as a subquery RTE. Its quals are pushed into it
(`subquery_push_qual`), and setrefs removes the `Subquery Scan` when it
carries no qual and a trivial target list. So `WITH x AS (SELECT a, b
FROM t) SELECT * FROM x` explains as a bare `Seq Scan on t`
(`analysis/m0146/m0146-0007/slice1/pg-oracle-single-ref-cte.txt`).

goopg planned every CTE as a materialised `CTE Scan` with a `CTE <name>`
section. On TPC-DS about a dozen queries (Q5, Q33, Q54, Q56, Q58, Q60,
Q77, Q78, Q80, Q83, Q97, ...) first diverged from PG at that `CTE` node.

## Slice 1: inline the reference in place

goopg keeps the `CTEScan` node internally, and the existing
`pushQualsThroughSingleRefCTEs` pass keeps pushing quals into its body.
What changes is how that reference is run and rendered.

- `plannedCTE.inlinable` is PG's gate: `refs == 1`, a plain SELECT body
  (`inlineEligible`), a SELECT-owned WITH (`selectOwned`, set by
  `markSelectOwnedCTEs` after `planSelectWithSettings` pre-plans the list),
  not `MATERIALIZED`, and no volatile function (`planHasVolatileExpr`,
  sublinks included, through the pull-up gate's builtin list and routine
  lookup). `refs` is final only after the whole statement is planned. Every
  reader (pushdown pass, executor build, EXPLAIN) runs after that. `refs`
  can overcount (a set-op head operand is planned twice), which only
  declines inlining.
- `CTEScan.Inlined()` exposes the gate.
  - **Executor** (`cteScanOp.Open`): an inlined scan streams its body
    instead of buffering it in `ctx.CTERowCache`. There is no second
    reference to replay for, and a rescan re-executes the body, as a
    subquery does in PG.
  - **EXPLAIN**: `collectCTEHoist` claims no section for an inlined scan
    (it still descends for nested CTEs). Both text walkers hand an inlined
    scan with no attached qual straight to its body, which is setrefs'
    trivial-subqueryscan removal. With a qual, the scan prints as
    `Subquery Scan on <alias>`.
- The pushdown pass now uses the same gate. A `MATERIALIZED` CTE, a
  volatile body, or a DML statement's WITH no longer takes pushed quals.
  PG does not inline those, so it does not push into them either.

## Results

- The SF0.25 and SF1 census records at a `CTE` node are gone for 13
  queries. Their first divergence now names the next mechanism, mostly
  aggregation strategy (`MixedAggregate`, sorted grouping) or
  Incremental Sort. See `analysis/m0146/m0146-0007/slice1/`.
- Row counts unchanged (sweep 96/96, fire set PASS with 17 fires). TPC-H
  census identical. Regress: 41 pass-required cases using WITH or showing
  plans, same results as HEAD.

## Slice 2 (M0146-0007b): the pushed qual moves

PG's subquery_push_qual moves the qual. goopg now does the same when the
C-02c move proof holds from the CTE reference down to the placement.
- `pushConjunctIntoCTEBodyTraced` threads `pushTrace` through the body:
  - projections are exact when their layout checks hold;
  - grouping-key crossings are exact except over grouping sets, whose
    rollup rows have NULL keys only the outer copy rejects;
  - Sort, Gather Merge and HAVING are passthroughs.
- `pushConjunctTraced`'s `*Project` arm keeps the proof for a CTE-path
  descent (`pushTrace.cteMove`) whose ColumnRefs are all named. The
  positional name check then ran on every hop, which closes the
  unnamed-ref seam. The join pass stays placement-only there.
- A proven, unplanted conjunct leaves the residual; an emptied residual
  is the transparent `true` wrapper.

TPC-DS Q78's `ss` and `cs` lose their `Subquery Scan`. `ws` keeps its copy
(item 1 below). Evidence: `analysis/m0146/m0146-0007/slice2/`.

## Slice 3 (M0146-0007c): descent into a NestedLoopIndexJoin

`pushConjunctIntoNLI` gives the CTE-path descent a NestedLoopIndexJoin
arm.
- An outer-only conjunct descends into Outer.
- An inner-only conjunct of an INNER join joins the probe's `Cond` (PG's
  `Filter:` on the inner Index Scan). The probe is mutated in place, so
  the aliasing Memoize stays consistent.
- The move proof follows the *Join arm's containment rule.

Q78 now prints no `Subquery Scan`, and `ws`'s `d_year = 1998` sits on the
inner Index Scan as in PG. Evidence: `analysis/m0146/m0146-0007/slice3/`.

## Slice 4 (M0146-0007d): a CTE referenced only from sublinks still hoists

EXPLAIN's `collectCTEHoist` (`internal/executor/explain_cte.go`) walked the
plan spine only: a CTE whose EVERY reference sits inside a sublink body was
never claimed, so each reference rendered the whole body inline — TPC-DS
Q14's `avg_sales` printed three copies of its Finalize-Aggregate subtree,
one under each of the three InitPlans probing it. Upstream plans the CTE
once (`SS_process_ctes`, subselect.c — the subplan rides the topmost
`init_plans`) and prints one `CTE avg_sales` section no matter where the
references live; PG's own InitPlan probes show bare `CTE Scan on avg_sales`
leaves.

- The collector now also walks `optimizer.NodeSubplans(n)` at every node —
  the same expression-side enumeration explain_names.go already uses, kept
  in lockstep with walkPlanExprs by exprwalk_inventory_test.go. Sublink
  bodies (scalar/EXISTS/IN/ARRAY/multi-assign, slot-driven via
  ExprSubplans) reach the walk; claim keying, dedup, inlined-scan descent
  and declSeq section order are all unchanged.
- Nested sublinks inside a reached body recurse through the same walk; a
  second reference to an already-claimed declaration still declines to
  descend.
- Verified on Q14 (SF0.25, private clone): one `CTE avg_sales` section and
  three bare `CTE Scan` InitPlan leaves, matching plans-pg/Q14.txt. The
  first-divergence record leaves the CTE boundary (`D6-cte` 1 → 0, Q14 →
  `D1-sublink`; the residual is the Append-leg aggregate shape plus its
  inner join strategy — routed outside this task).
- `TestExplainSublinkOnlyCTEHoistsToSection` pins the shape.
- The deferral-ledger row for this divergence (2026-08-06, M0125-0049) is
  marked resolved; its "single reference prints once either way" caveat was
  wrong for the multi-InitPlan case — N probes rendered N inline copies.

## Open (ledgered)

1. The join pass (`pushSingleSideQualsIntoInnerJoinInputs`) still declines
   at a NestedLoopIndexJoin and stays placement-only across a projection;
   both openings are CTE-path only.
2. `NOT MATERIALIZED` on a multiply-referenced CTE: PG plans each
   reference separately; goopg shares one planned body.
3. (FROM-clause references: done in slice 5 below.) The inlined body is still planned once, at the WITH, not at the
   reference site. So it is not pulled up into the parent's join search
   (PG's `pull_up_simple_subquery` for simple bodies), and its
   parallel-safety follows the CTE scan.

## Slice 5 (M0146-0007e): the reference site pulls a simple body up

Open item 3 above, for FROM-clause references. TPC-DS Q47/Q57's `v2` is a
single-reference CTE whose body is a plain join of three `v1` references.
PG's `inline_cte` turns it into an RTE_SUBQUERY before
`pull_up_subqueries`, so the body's relations join the main query's search
and its WHERE lands on the `v1` scan. goopg printed `Subquery Scan on v2`
with that WHERE as its filter.

- `cteAsDerivedItem` (new `ctepullup.go`) presents a FROM item naming an
  inlinable CTE to the M0146-0028 pull-up as the derived table
  `(<CTE body>) <alias>`. The same `is_simple_subquery` gate
  (`simpleDerivedPullupBody`) then decides. The CTE must pass
  `inlinable`'s gates except for the reference count, and must have no
  column-alias list.
- The reference count is parse analysis' `cterefcount`. `refs` is final
  only after planning, and this decision is made while the FROM list is
  planned, so `stampCTEReferenceCounts` counts relation references by name
  over the WITH-owning statement's AST (`countCTEReferences`). It skips
  `SelectStmt.From`, the flattened copy of `FromExprs`. A shadowing nested
  WITH and an over-deep AST only overcount, which declines the pull-up.
- The body was already planned once at the WITH, and that preplan added
  references to other CTEs (`bodyRefDeltas`, recorded around each body's
  preplan). Once the parent's pull-up succeeds, the parent re-plans those
  references and `takeBackPulledBodyRefs` subtracts the preplan's, once
  (`pulledUp`). Without this, a CTE read once through the pulled body looks
  doubly referenced and loses its own inlining.

Test: `TestExplainSimpleCTEReferencePullsUp`. A pulled `v` leaves no
Subquery Scan. The `g` it reads stays inlined, and fails without the
take-back. A CTE read twice keeps its `CTE v`.

Movement: Q47/Q57's depth-2 node now matches PG (`Merge Join`, not
`Subquery Scan on v2`). Their first-divergence category moves from
join-order to qual-placement at the same depth: goopg merges on `(rn + 1)
= rn`, while PG keeps it as a Join Filter. All-depth qual-placement goes
+2 at both scales, and match counts are unchanged. Q47/Q57 results are
md5-identical to PG's, and TPC-H is identical. Evidence:
`analysis/m0146/m0146-0007/slice5/`.

Still open (ledgered): a CTE reference inside a JOIN chain, a CTE with a
column-alias list, and CTE references in sublinks are not presented to the
pull-up. After the pull-up, EXPLAIN qualifies the CTE body's outer key by
the statement-wide binding table and prints `ss_item_sk` bare, where PG
prints `store_sales.ss_item_sk`; this is the same cross-level
SourceTableIdx collision as the M0146-0005as rendering row.
