# M0146-0007: single-reference CTE inlining (`inline_cte`)

Status: slice 1 landed 2026-09-26; the qual move and multi-reference
`NOT MATERIALIZED` are open.

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

## Open (ledgered)

1. **Qual move.** goopg copies a pushed qual into the body and keeps the
   outer copy, so an inlined reference with a qual prints
   `Subquery Scan … Filter:` where PG moved the qual (TPC-DS Q78). Moving
   needs the C-02c move proof through the body's projections.
   `pushConjunctTraced`'s `*Project` arm keeps `proven` false because
   `remapConjunctThroughProjection` does not check an unnamed reference's
   side.
2. `NOT MATERIALIZED` on a multiply-referenced CTE: PG plans each
   reference separately; goopg shares one planned body.
3. The inlined body is still planned once, at the WITH, not at the
   reference site. So it is not pulled up into the parent's join search
   (PG's `pull_up_simple_subquery` for simple bodies), and its
   parallel-safety follows the CTE scan.
