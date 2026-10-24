# M0147-0013 — retire `GOOPG_SCALAR_UNNEST` (correlated scalar-sublink decorrelation)

Status: landed 2026-10-24.

Milestone: [M0147 lever retirement](../../milestones/0147-lever-retirement.md), phase 1. Lever count 28 → 27
(`scripts/lever-count.sh`).

## What was retired

goopg's post-planning decorrelation of a correlated scalar sublink. It handled
`x op (SELECT agg(...) FROM t WHERE t.k = outer.k ...)` and had two forms:

- a GROUP-BY-inner hash join (`unnestSubquery` / `buildUnnestedSubquery`);
- an aggregate-above-join form with an Ordinality tag for non-equi residual correlation
  (`unnestScalarWithResiduals`).

PG never does this. `pull_up_sublinks` (`postgres/src/backend/optimizer/prep/prepjointree.c`) converts only ANY and
EXISTS sublinks (`convert_ANY_sublink_to_join` / `convert_EXISTS_sublink_to_join` in `subselect.c`), so an
EXPR_SUBLINK always stays a SubPlan. M0145-0008y switched the decorrelation off by default once goopg had PG's
parameterised probes for those SubPlans (M0146-0012 / -0012a). From then on `GOOPG_SCALAR_UNNEST=on` was the only
way to reach it.

## Removed

`internal/optimizer/unnest.go`:

- The switch: `scalarUnnestOn`, `scalarUnnestFromEnv`, `SetScalarUnnestEnabled` and its `init` read.
- The scalar loop in `unnestSubqueriesInPlan`. The IN and EXISTS loops stay.
- `canUnnestSubquery`, `unnestSubquery` and `unnestScalarWithResiduals`.
- Every helper whose only callers were those three. The set was found with `deadcode ./cmd/...`, diffed before and
  after:
  - `buildUnnestedSubquery`, `cloneAggregateCall`, `cloneExprSubstituteAggIdx0`, `containsExpr`;
  - `findFilterContainingSubquery`, `findSubqueryInExpr`, `innerPlanIsIndexProbeCheap`;
  - `liftResidualConjuncts`, `nullPreservingScalarTarget`, `replaceExprInConjunct`;
  - `resolveSubColInSchema`, `shiftExprColumnIdx`, `subqueryANDReachable`;
  - the `nullOnEmptyAggregates` table.

  A second `deadcode` run found nothing new.

The gates that deferred to the decorrelation:

- `sublinkjoinclause.go`, `conjunctHasOnlyCorrelatedScalarSublinks`: the
  `subqueryUnnestEnabled() && canUnnestSubquery(x)` disjunct.
- `sublinkjoinclause.go`, `paramExecScalarSublinksOnly`: the early `return false`.
- `local_filters.go`: the `len(x.Args) > 0 && scalarUnnestOn.Load()` refusal on a PARAM\_EXEC SubPlan conjunct.

`flaglabels.go` moves `GOOPG_SCALAR_UNNEST` from the live table to `flagProvenanceRetired` (`retired(M0147-0013)`).
`scripts/planner-flags.env` is regenerated to match. Comments in `executor/expr.go` and `executor/subplan.go` no
longer claim a lockstep contract with the deleted `innerPlanIsIndexProbeCheap`.

**Kept.** `collectUnnestParamsAndResiduals`, `existsUnnestPlan` and the residual-lift machinery are kept, because the
EXISTS / IN pull-ups (PG's `convert_EXISTS_sublink_to_join` / `convert_ANY_sublink_to_join` analogues) use them.

## Tests

**Deleted.** These tested only the decorrelating arm:

- whole files: `scalar_unnest_switch_test.go`, `unnest_multi_param_test.go`, `joinsearchunnest_test.go`,
  `joinsearchunnestgroupkey_test.go`;
- in `unnest_test.go`: the three `TestCanUnnest*` tests, `TestRecursiveUnnestInsideNonUnnestableIN` and
  `TestScalarTwoKeyCorrelationStripsTautology`;
- in `unnest_guards_test.go`: the two `…StillUnnests` scalar guards;
- `TestIndexKeyScalarJoinInnerDecorrelates`;
- `TestPlanQ20DumpsTree`, which was a diagnostic dump;
- the helpers that only those tests used.

**Rebased onto PG's SubPlan.**

- `TestCTEScalarSublinkStaysSubPlan` (was `…DecorrelatesAndAgrees`): the Q30 shape keeps its SubPlan and returns
  `C20|900`, the value the old test's SubPlan control arm produced.
- `TestScalarResidualDuplicateOuterMultiplicity` (was `…Ordinality`): matrix row M17 keeps its SubPlan, and both
  duplicate outer rows survive.
- `TestSlice3CorrelatedBodyDeclinesParentAware`: keeps its unit half (`exprHasOuterRef`). Its end-to-end
  "Q2 still decorrelates" half is gone.

**Unchanged.** The `…StaysSubPlan` guard tests now pin PG's behaviour directly.

`exprwalk_inventory_test.go` drops the seven pinned type switches whose functions are deleted.

## Inert proof (AGENT.md C6)

- `scalarUnnestFromEnv("")` was false, so `canUnnestSubquery` returned false at its first statement on every default
  run.
- Its callers were therefore no-ops: `unnestSubquery` returned `nil, nil` and the scalar loop broke at once, and the
  `conjunctHasOnlyCorrelatedScalarSublinks` disjunct was false.
- The two `scalarUnnestOn.Load()` gates were false.
- The variable is set nowhere in scripts, CI, bench configs or the Makefile. The only consumers were tests, through
  `enableScalarUnnestForTest` / `SetScalarUnnestEnabled`.
- Measured: the fire-set gate's HEAD-vs-staged plan A/B finds **no changed query** on SF0.25 or SF1.

## Gates

- units PASS;
- spotcheck PASS;
- acceptance arm 24 MATCH;
- sf025 PASS=99 (61 ck; `SF025_NO_PLANS=1`), and the standalone plan capture shows same=99;
- fire-set gate PASS (in scope through `flaglabels.go`): fires=none on both corpora, PLAN-PARITY 99/99 on both;
- TPC-H census on the tree 22/22;
- TestPort\_RegressSuite PASS;
- TestPort\_Isolation PASS.

## Supersedes

The 2026-10-05 M0145-0008y ledger row, for its switch only: "GOOPG_SCALAR_UNNEST=on restores it" no longer holds.
The row's PG-side residuals (1) and (2), the base-restriction placement and planagg's min/max InitPlan, are
unaffected and stay open there.
