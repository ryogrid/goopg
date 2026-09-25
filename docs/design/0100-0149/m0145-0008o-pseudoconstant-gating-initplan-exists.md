# M0145-0008o: pseudoconstant quals gate the plan; an uncorrelated EXISTS is an InitPlan

Status: **LANDED 2026-09-25**. Task: `.ralph/fix_plan.md` M0145-0008o (Kind:
impl, Parent: M0145-0008j). Evidence: `analysis/m0145/m0145-0008o/`.

## What PG 18.3 does (scratch cluster, 2026-09-25)

```
SELECT a FROM p1 WHERE a > 1 AND EXISTS (SELECT 1 FROM p3)
Result
  One-Time Filter: (InitPlan 1).col1
  InitPlan 1
    ->  Seq Scan on p3
  ->  Seq Scan on p1
        Filter: (a > 1)
```

- `NOT EXISTS` renders `One-Time Filter: (NOT (InitPlan 1).col1)`.
- `WHERE (SELECT count(*) FROM p3) > 0` renders
  `One-Time Filter: ((InitPlan 1).col1 > 0)`.
- A join is gated the same way, with the Result above the join.
- `SELECT a, EXISTS (SELECT 1 FROM p3) FROM p1` has no gate (the sublink is
  not a qual), but the sublink is still `InitPlan 1`, listed under the scan.

The three mechanisms behind this:
- A qual with no Vars of the current level and no volatile functions is a
  pseudoconstant (`is_pseudo_constant_clause`).
- `create_gating_plan` (`postgres/src/backend/optimizer/plan/createplan.c`)
  evaluates it once, as a Result's `resconstantqual`.
- Every uncorrelated EXPR/EXISTS sublink is an initplan param (`make_subplan`,
  `postgres/src/backend/optimizer/plan/subselect.c`).

goopg evaluated the EXISTS per row, as `EXISTS(SubPlan 1)` in the scan's
Filter.

## What changed

- **`gatePseudoconstantQuals`** (`internal/optimizer/pseudoconstant_gate.go`)
  runs in `planSelectWithSettings` after the post-hoc passes
  (`pushSingleSideQualsIntoInnerJoinInputs`), before the upper stages.
  - It lifts pseudoconstant conjuncts out of the Filter chain at the top of
    the scope's FROM/WHERE tree, the WHERE residual.
  - It gates the tree with `Result{OneTimeFilter, Child}` above it, using
    identity targets. That is PG's placement: a WHERE-level pseudoconstant's
    qualscope is the whole jointree.
  - A pseudoconstant names no relation, so the join search cannot attribute
    it to a leaf, and it always reaches that residual.
  - The pass does not descend into the searched tree. Leaf Filters there
    carry path costs and boundary maps, and a pseudoconstant below an outer
    join must stay put.
- **`isPseudoconstantConjunct`** is a fail-closed whitelist over
  `walkExprRefs(scopeIgnore)`:
  - at least one sublink, and every sublink uncorrelated
    (`sublinkIsUncorrelated`, binder-aware);
  - otherwise only constants and pure operators;
  - no volatile builtin (`exprListHasVolatileBuiltin`);
  - a column, an outer-level reference, or any unrecognised kind declines.
- **`EstimateRows(*Result)` with a child** passes the child's rows through,
  as `create_gating_plan` copies `plan_rows`. It used to return 0 ("no
  estimate") for every Result.
- **EXPLAIN** (`internal/executor/operators_explain.go`):
  - `optimizer.SublinkIsInitPlan` marks an uncorrelated `ExistsExpr` (as well
    as the non-correlated scalar `SubqueryExpr`) as an InitPlan, for both the
    name and the subtree label.
  - An InitPlan EXISTS renders as `(InitPlan N).col1`.
  - A bare InitPlan param is left unparenthesised in `One-Time Filter:`, as
    PG's deparser does. `NOT` and operators keep their parens. (join.out's
    `((InitPlan 1).col1)` is a PlaceHolderVar, which PG parenthesises.)

## Tests

- `TestExplainRendersEveryStackedFilter` (executor) is now the exact
  PG-output pin for the four shapes above, plus values: the gate passes and
  `a > 1` decides; `NOT EXISTS` gates everything out.
- `TestScopedCacheDepthConsistency` covered two per-row, scoped,
  non-correlated sublinks. Its bare `EXISTS` conjunct is now a gate,
  evaluated once, so the EXISTS moved under `OR a > 0`, as the IN already
  was, to keep that coverage.
- `exprwalk_inventory_test.go` pins the two new hand-written Expr switches
  (both non-recursive classifiers).

## Measured

- Fire-set HEAD `63166b477` vs staged: `fires=none` at SF0.25, SF1 and
  TPC-H. Neither corpus has an uncorrelated-sublink WHERE conjunct or a
  target-list uncorrelated EXISTS.
- PG's own expected outputs with this shape: `join.out`, `rowtypes.out`,
  `updatable_views.out`.

## Gates (staged tree)

- units PASS; `tpch-spotcheck` PASS.
- fire-set (TPC-DS SF0.25/SF1, TPC-H) PASS.
- `tpcds-sf025 sweep` 96/96, shapes 99/99 same. The first attempt logged 12
  ERRORs: the memory guard killed the sweep's own server (12 GB) at 75% host
  RAM while the nightly batch was running. The re-run was clean.
- acceptance 24 MATCH; ea-ratchet 52/52.
- FORCE=1 was used while the nightly batch was live: values only.

Movement: none (no corpus plan changed).

## Not ported (ledgered)

- **Outer-level references.** A qual that reads only an enclosing query's
  columns is a pseudoconstant in PG (they are Params), and the gate is
  re-evaluated on rescan. goopg's Result evaluates its one-time filter at
  Open, so such quals are excluded.
- **Join-level pseudoconstants.** PG gates a pseudoconstant join qual (an
  ON-clause constant, including one on an outer join) at that join. The pass
  handles only the WHERE residual at the top of the scope.
