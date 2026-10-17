# M0146-0203 — a PARAM_EXEC SubPlan over one relation's columns is that relation's restriction

Status: done 2026-10-16 — TPC-H Q20 matches PG.

## Problem

TPC-H Q20 restricts `partsupp` (the IN body) by `ps_availqty > (SELECT 0.5 *
sum(l_quantity) FROM lineitem WHERE l_partkey = ps_partkey AND l_suppkey =
ps_suppkey ...)`. PG distributes a clause by the Vars of its testexpr and its
SubPlan's args (`pull_varnos`, initsplan.c). All of them are partsupp's, so
the clause is partsupp's base restriction: `Filter: (ps_availqty > (SubPlan
1))` on the partsupp index scan.

On goopg's flattened jointree arm, the kept-sublink rebase
(`keptRebase.rebaseOne`, pulledsublink.go) has already lowered the sublink to
PARAM_EXEC form: correlation in `Args`/`ParParam`, no outer references in the
plan.

1. `correlatedScalarSublinkBinding` (local_filters.go) declined every
   SubqueryExpr with Args.
2. `partitionConjunctsForJoinPlanningScoped` therefore left the clause in
   the search's residual, above the root.
3. Above the root, partsupp's columns are padded: it is the semi join's
   RHS. The DP trace read `seam-decline reason=residual-hits-pad`.
4. The planner fell back to the syntactic shape: `supplier` seq-scanned
   and nested-looped into a Memoize'd `nation_pk` probe, with the nation
   restriction on the join. PG plans a Hash Right Semi Join instead, over
   `nation` (filtered) → bitmap-probed `supplier`.

The search itself had already priced PG's plan cheapest.

## Change

- `correlatedScalarSublinkBinding` admits a SubqueryExpr whose
  `len(Args) == len(ParParam)`. The Args are same-scope slots that the walk
  visits, and `tableForCol` already attributes them to the binding.
  `localizeExprToLeaf` rebases them with the rest of the conjunct, and the
  inner plan reads parameters, not the host row.
- The admission is off while the non-PG scalar decorrelation
  (`GOOPG_SCALAR_UNNEST`, default off) is on. That pass reads the clause off
  the body's top qual holder (`TestPlanQ20DumpsTree`,
  `TestRecursiveUnnestInsideNonUnnestableIN`).
- Test: `TestParamSubPlanConjunctIsALeafRestriction`. It covers the partsupp
  binding, the cross-relation decline, and the Args/ParParam mismatch.

## Measured

Captures in `analysis/m0146/m0146-0203/`, compared with the M0146-0202
captures:

- **TPC-H:** match 18 → 19 (Q20 MATCH). No other plan changed.
- **TPC-DS SF0.25 and SF1:** plan text byte-identical (84/99, 71/99).

The acceptance arm executed Q20 with values identical (92 rows; 24 MATCH).
