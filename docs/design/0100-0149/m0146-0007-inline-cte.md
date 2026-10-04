# M0146-0007: single-reference CTE inlining (`inline_cte`)

Status: slices 1-9 landed (the latest 2026-10-04, M0146-0007i: nested
pull-up); the items under "Still open" in each slice are ledgered.

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
2. (Done in slice 6 below.) `NOT MATERIALIZED` on a multiply-referenced
   CTE: PG plans each reference separately; goopg shared one planned body.
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

## Slice 6 (M0146-0007f): `NOT MATERIALIZED` inlines every reference

Open item 2 above. PG's `inline_cte` gate (`SS_process_ctes`,
`./postgres/src/backend/optimizer/plan/subselect.c`) also inlines a CTE
referenced more than once when it is written `NOT MATERIALIZED`, unless its
body contains an outer self-reference (`contain_outer_selfref`). Each
reference then gets its own copy of the body as an ordinary RTE_SUBQUERY,
which `pull_up_subqueries` may flatten. goopg shared one planned body, and
EXPLAIN printed `CTE x` plus one CTE Scan per reference. The witnesses are
regress `subselect` (`with x as not materialized (...) select * from x, x x2
where x.n = x2.n`) and `join` (`ctetable`, three references).

### The gate

`plannedCTE.eachRef` (`inlinesEachReference`) is computed once, when
`preplanWithClause` registers the entry (`eachReferenceInlineGate`). That
way a later sibling's body already inlines its references. Its terms:

- the WITH belongs to a SELECT (`preplanWithClause` now receives the owning
  statement; INSERT/UPDATE/DELETE pass nil);
- `NOT MATERIALIZED`;
- more than one AST reference (`countCTEReferences`, parse analysis'
  `cterefcount`);
- no volatile function;
- no WorkTableScan anywhere in the body, which is `contain_outer_selfref`
  for a non-recursive body (a recursive CTE nested inside the body
  over-reports, which only keeps the body shared);
- an uncorrelated body (`planHasOuterRef`). This term is goopg's own. A
  reference may sit at a deeper query level than the WITH, and planning the
  body there would shift its outer-reference levels. PG re-bases them
  (`IncrementVarSublevelsUp`).

### Where each reference is planned

- **The FROM-list pull-up.** `cteAsDerivedItem` presents the reference as
  `(<body>) <alias>` to the M0146-0028 pull-up, as for a single reference.
  `splitInnerJoinChainForPullup` now converts CTE operands the same way, so
  references inside an INNER JOIN chain are pulled up too (single-reference
  CTEs included; this was slice 5's first ledgered gap).
- **The HAVING push.** `newPushQualItem` takes the body, so an outer qual on
  a grouping column moves below a grouped body's aggregate.
- **Everything else.** The `planScanRangeVar` CTE arm plans the reference as
  an ordinary subquery (`planCTEReferenceAsSubquery`): the written body
  under the reference's alias, with the CTE's column names (the reference's
  own alias list overrides a prefix).

In every case the preplanned body is dead, so its own references to other
CTEs are taken back once (`takeBackPulledBodyRefs`).

### Name resolution

A body planned at a reference site resolves its relation names there. That
scope also sees the CTE itself, later siblings and any WITH nested around
the reference. In `WITH t AS NOT MATERIALIZED (SELECT … FROM t) SELECT … FROM
t, t t2`, the body's `t` must stay the table. So:

- every entry records the CTE-name scope it was declared in
  (`plannedCTE.declScope`: the outer statements' entries and its earlier
  siblings);
- `planCTEReferenceAsSubquery` plans the body under that scope;
- `cteBodyNamesResolveAsDeclared` declines a pull-up when any unqualified
  relation name in the body would rebind. This also covers single-reference
  pull-ups, where until now only the overcounted `astRefs` happened to
  prevent it.

### Found on the way

- **M0146-0057, fixed here (wrong results).** A single-reference CTE read
  inside an IN or EXISTS sublink lost its body's WHERE:
  - `sublinkBodyIsSimple` reads a CTE name as a plain relation;
  - the body's FROM walk (`bindPulledBodyScope` → `planFromClause`) then
    pulled the CTE body up (slice 5);
  - its WHERE came back in `pulledQuals`, which the sublink splice never
    read. PG 18.3 returns 82 on the test data; goopg returned 166.

  Those quals now travel with the body's ON quals. A correlated or
  sublink-bearing pulled qual declines the pull-up. Slice 6 routes more
  references through this path, so it could not land without the fix.
- **M0146-0058, filed.** The ANY-derived arm has a sibling loss that does not
  involve CTEs: `pullUpAnyDerivedBody` lets the FROM pull-up flatten its
  wrapped body and then reads neither the body's WHERE nor its target
  expression.
- **M0146-0059, filed.** A kept CTE's row cache is keyed by declaration
  offset and name and outlives the statement. In a PL/pgSQL function, two
  statements declaring `x` at the same offset share rows.

### Effect

The probe set covered a simple body, a grouped body, a sublink, a JOIN chain,
nested CTEs, an alias list, the self-named body and the outer self-reference.

- **Shape.** No CTE remains in any of them. The simple, sublink and
  JOIN-chain shapes print PG's plan exactly. The grouped body pushes `x2.b =
  3` below its aggregate as PG does; the join method above it (Hash Join vs
  PG's Nested Loop) is a cost election.
- **Results.** Every result equals PG's.
- **TPC-DS and TPC-H** write no `NOT MATERIALIZED` CTE.
  - The fire set fired no query at either scale.
  - The sweep passed 96/96 and the TPC-H arm 24/24; tpch-spotcheck and
    ea-ratchet passed.
- **Regress A/B** over 19 planner cases: only `join` (`ctetable`: no CTE,
  three Values scans) and `subselect` (the NOT MATERIALIZED pair: two pulled
  scans, no CTE) changed, both toward PG.

Tests (both fail on HEAD):

- `TestNotMaterializedCTEInlinesEachReference`: the simple, grouped,
  JOIN-chain and self-named shapes with PG's values; a CTE written without
  the keyword and referenced twice stays one CTE.
- `TestSublinkOverInlinedCTEKeepsBodyWhere`: M0146-0057's IN, EXISTS and
  NOT MATERIALIZED forms return PG's 82.

Still open (ledgered 2026-10-04):

- an alias-list reference is planned per reference but not pulled up;
- a CTE read inside a pulled body is not pulled further (the expansion is
  one level deep);
- a correlated body stays shared;
- a qual the substitution makes variable-free (`now() = now()`) stays a
  merge clause, where PG makes it a One-Time Filter;
- non-recursive members of a WITH RECURSIVE list never inline;
- a default CTE read once only inside an inlined body counts each copy's
  references and stays shared.

## Slice 7 (M0146-0007g): row marks and WITH queries

Two regress `subselect` cases sit in the CTE-inlining section: "SELECT FOR
UPDATE cannot be inlined" and "Row marks are not pushed into CTEs". Both
follow from PG treating a CTE reference as RTE_CTE during parse analysis;
inlining is a later planner step.

- **A bare locking clause skips WITH queries.** `transformLockingClause`
  (`./postgres/src/backend/parser/analyze.c`) loops over the range table and
  ignores RTE_CTE. So `WITH x AS (SELECT * FROM t) SELECT * FROM x FOR
  UPDATE` locks nothing, and PG's plan has no LockRows.
  - goopg emitted a lock for the CTE's synthetic relation and failed at run
    time with `short read at block`.
  - Naming a CTE in the OF list is the 0A000 error `FOR UPDATE cannot be
    applied to a WITH query` (with the clause's own strength). goopg
    accepted it.
- **A locking body is never inlined.** `SS_process_ctes` inlines only when
  `!contain_dml(cte->ctequery)`, and `contain_dml_walker` counts a query with
  row marks as DML. So a CTE whose body says FOR UPDATE stays a CTE. It runs
  once and locks every row it returns, whatever the outer query filters.
  goopg inlined it and printed `Subquery Scan on x`.

### Change

- `rangeBinding.cteRef` marks every CTE reference: a kept CTE Scan, an
  inlined one, and a reference 0007f plans as a subquery.
- `resolveLockedRels` skips such a binding under a bare clause and raises the
  0A000 error for an OF target. `lockStrengthSQL` is `LCS_asString`.
- A clause that marks no relation leaves the plan without a LockRows.
- `selectTreeHasLocking` clears `inlineEligible` for a body holding a locking
  clause at any query level. That turns off the single-reference inlining,
  the 0007f per-reference inlining, the pull-up and the qual descent.

### Effect

- `WITH x … SELECT * FROM x FOR UPDATE` returns PG's rows with PG's plan, a
  bare Seq Scan.
- A two-session check over `x JOIN rm_v … FOR UPDATE` matches PG:
  - an UPDATE of the CTE's table proceeds;
  - an UPDATE of `rm_v` waits.
- Regress A/B over 10 cases: the two `subselect` cases print PG's plan, and
  nothing else changed.
- The fire set fired nothing; no TPC-DS or TPC-H query locks rows. The
  sweep passed 96/96 and the TPC-H arm 24/24; tpch-spotcheck and ea-ratchet
  passed.

Test: `TestRowMarksAndWithQueries` fails on HEAD with the short read. It
covers the rows, the absent LockRows, a still-locked joined table, the three
strengths' error text, and the two locking-body CTEs staying CTEs.

Still open (ledgered 2026-10-04):

- the error cursor sits on `FOR`, where PG points at the OF-list name;
- the analyzer's locking messages ignore the clause strength;
- a nested clause that marks only CTEs still blocks inlining;
- PG keeps `Subquery Scan on ss` over the inner LockRows;
- EXPLAIN under LockRows prints a Merge Join without its input Sorts.

## Slice 8 (M0146-0007h): a pseudoconstant function qual gates its scope

Once 0007f pulls up both references of regress `subselect`'s NOT
MATERIALIZED pair, `x.n = x2.n` reads `now() = now()`. PG puts such a
conjunct on a gating Result (`create_gating_plan`,
`./postgres/src/backend/optimizer/plan/createplan.c`). It is evaluated once
as a `One-Time Filter`, and the Result emits nothing when it is false.

PG's test (`is_pseudo_constant_clause`) asks only two things: no Vars of the
current level, and no volatile function. goopg's gate (M0145-0008o)
admitted only conjuncts holding an uncorrelated sublink. So these all stayed
per-row Filters on the scan:

- `SELECT * FROM t WHERE now() = now()`;
- `CURRENT_USER = 'postgres'`;
- the `now() > '2000-01-01'::timestamptz` half of a mixed WHERE.

### Change

- `isPseudoconstantConjunct` admits non-volatile function calls and the
  typed literals (`TypedStringLit`, `IntervalLit`). A conjunct must still
  contain a sublink or a function call. A conjunct of constants alone stays
  out: PG folds it, and a constant FALSE makes the relation dummy (a
  childless Result), which this pass does not model.
- Volatility is PG's. `exprListHasVolatileBuiltin` also reads
  `catalog.BuiltinProcIsVolatile`, which is generated from `pg_proc.dat` by
  M0146-0028f. The hand-written list missed volatile builtins such as
  `pg_try_advisory_lock` and `set_config`, and a gate would have run those
  once instead of per row.

### Found by the regress A/B: pg_relation_is_publishable

psql's `\d` query ANDs `pg_catalog.pg_relation_is_publishable('<oid>')`
beside `pc.oid = '<oid>'`.

- As a per-row Filter, goopg's AND short-circuited on the first conjunct, so
  the missing builtin was never called.
- Gated, it is evaluated once whatever the rows hold, as in PG. `\d` then
  failed with "function … does not exist" (47 times in `inherit`).

The function is now implemented as PG's `is_publishable_class`
(`./postgres/src/backend/catalog/pg_publication.c`). It is true for a
permanent ordinary or partitioned table with an OID of at least 16384, and
NULL for a missing relation.

### Effect

- **Probe shapes.** goopg now gates every shape PG gates, also inside
  Aggregate, Limit and Append members:
  - `now() = now()`;
  - `CURRENT_USER = …`;
  - the mixed WHERE, with `f1 > 2` left on the scan;
  - the 0007f pull-up pair.
- **TPC-DS and TPC-H.** Neither has a function-only pseudoconstant
  conjunct.
  - The fire set fired nothing.
  - The sweep passed 96/96 and the TPC-H arm 24/24; tpch-spotcheck and
    ea-ratchet passed.
- **Regress A/B** over 23 cases (after the builtin): only a known `join`
  row-order flip and `memoize` build-time noise.
  - The `subselect` pair itself is still a Merge Join on `n = n`. Its body
    nests a second derived table (`SELECT * FROM (SELECT f1, now() AS n …)
    ss`), and the pull-up expands one level, so `n` stays a subquery column
    (0007f's item (b)).

Tests (both fail on HEAD):

- `TestPseudoconstantFunctionQualGatesScope`: four gated shapes with their
  rows; `random()` and `pg_try_advisory_lock()` stay per-row.
- `TestRelationIsPublishable`: PG's values for a table, an unlogged table,
  a temp table, a view, `pg_class` and a missing OID; the `\d`-shaped query
  over a non-matching OID.

Still open (ledgered 2026-10-04):

- constant FALSE as a dummy relation;
- folding immutable calls over constants (`length('abc') = 3`);
- Params and enclosing-level Vars;
- pseudoconstant join ON quals gated at their join.

## Slice 9 (M0146-0007i): nested simple subqueries are pulled up too

PG's `pull_up_simple_subquery` (`./postgres/src/backend/optimizer/prep/prepjointree.c`)
runs `pull_up_subqueries` on the subquery before splicing it into the
parent, so every nesting level flattens. goopg's pull-up (M0146-0028, and
0007e/f for CTE references) expanded one level. A derived table, or a NOT
MATERIALIZED CTE reference, inside a pulled body stayed a subquery. In
`SELECT … FROM (SELECT … FROM (SELECT a, b FROM n_t WHERE b < 5) s, n_u …)
q WHERE q.b = 1`, PG filters `n_t` by `(b < 5) AND (b = 1)`. goopg kept
`Filter: (n_t.b = 1)` above the Hash Join (0007f's ledgered item (b)).

### Change

- **Expansion.** `expandDerivedPullups` recurses into each pulled body's
  join-free items. CTE references convert through `cteAsDerivedItem` as at
  the top. Each candidate records its `parent` and `depth`, and its expanded
  item range nests inside the parent's. The depth is bounded at 8.
- **Gate.** `simpleDerivedPullupBody` admits a join-free derived FROM item
  that is itself simple.
- **Resolution.** `resolvePulledDerived` resolves innermost bodies first,
  all in the same parent coordinates.
  - A body context sees its own leaves (`pulledHidden` cleared only for the
    bindings it owns directly) and its direct children through their alias
    name views.
  - Only the statement's own items' views are returned for parent-level
    names.
  - Every level's WHERE joins the scope's quals.
- **Ownership.** `pulledCandidateOwning` and `pulledCandidateOwningBinding`
  return the innermost owning body. Outer-join demotion reads that body's
  WHERE, and each body reduces only the outer joins of items it owns
  directly. The reduced items are written back by index, because
  `applyDemotion` may rewrite the FromExpr itself.

### Effect

- **Probe.** The two-level derived query prints PG's plan. The
  three-level one carries PG's qual order. The NOT MATERIALIZED CTE read
  inside another one's body is pulled with it. Every result equals PG's.
- **Regress A/B** over 22 cases: noise only (a known `join` row-order flip,
  `memoize` build times).
- **Gates.**
  - The fire set fired nothing; no TPC-DS or TPC-H query nests a simple
    subquery inside a pulled one.
  - The sweep passed 96/96 and the TPC-H arm 24/24; tpch-spotcheck and
    ea-ratchet passed.

Test: `TestNestedSimpleSubqueriesPullUp` (fails on HEAD). It checks the
two-level and three-level derived queries, with PG's filter text and no
Subquery Scan, and the nested NOT MATERIALIZED CTEs, with PG's rows.

Still open (ledgered 2026-10-04):

- a `SELECT *` body is not pulled up at any level. This is why regress
  `subselect`'s NOT MATERIALIZED pair still differs;
- a derived item that is a join operand inside a pulled body;
- the EC member printed in a pulled join clause;
- the pre-existing LEFT JOIN qual placement.
