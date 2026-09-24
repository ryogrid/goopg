# M0146-0002e: place an uncorrelated SubPlan restriction on its base relation

Status: landed 2026-09-24 `bb90e51a4`. Movement: yes, on plan shape. TPC-H Q16,
TPC-H Q22 and TPC-DS Q6 carry their SubPlan quals where PG does. The category
counts did not move in PG's direction (see Measurement). Follow-up:
M0146-0002f (parallel safety), M0146-0002g (label), M0146-0002h (SubPlan
selectivity).
Task: `.ralph/fix_plan.md` M0146-0002e (Kind: impl, Parent: M0146-0002d).
Recon: `m0146-0002d-subplan-restriction-placement.md`.
Evidence: `analysis/m0146/m0146-0002e/`.

## Rule

PG distributes a qual by the relids of its Vars (`distribute_qual_to_rels`,
`initsplan.c`). An uncorrelated SubPlan contributes no relids, so
`ps_suppkey NOT IN (SELECT …)` is a `partsupp` base restriction, and
`d_month_seq = (SELECT …)` is a `date_dim` base restriction. A qual with no
Vars at all is a pseudoconstant gating qual. goopg already keeps that case in
the residual, because `tableForCol` attributes it to no binding.

A bare `x IN (SELECT …)` / `x op ANY (SELECT …)` is not a restriction in PG.
`pull_up_sublinks` turns it into a semi join (`convert_ANY_sublink_to_join`),
correlated or not. PG leaves `NOT IN` / `op ALL` (ALL_SUBLINKs), and a sublink
under NOT, OR or any other operator, as SubPlans inside a distributed
restriction.

## Change (`internal/optimizer/local_filters.go`)

- `conjunctIsLocalEligible` walks with `scopeSignal` and decides each sublink
  itself. It admits an `InExpr` / `SubqueryExpr` / `ExistsExpr` only when
  `sublinkIsUncorrelated` holds:
  - there is a plan;
  - there are no PARAM_EXEC `Args`/`ParParam` (a lowered correlated sublink
    has no `OuterColumnRef` left in its plan);
  - `planHasOuterRef` (binder-aware) is false.
  Correlated sublinks, and the Array and MultiAssign forms, stay declined.
- `anySublinkPullupCandidate` keeps a bare non-negated, non-ALL `InExpr` in
  the join residual. There goopg's semi-join producers consume it: the
  jointree ANY pull-up, and the legacy unnest for the bodies the pull-up
  declines.
- `localizeExprToLeaf` rebases with `scopeIgnore`. Only the sublink's
  same-scope operand moves, and the inner plan is shared untouched.

### The first cut, and why the ANY exception is load-bearing

Without `anySublinkPullupCandidate`, TPC-DS Q95's two `ws_order_number IN (…
ws_wh …)` became leaf filters on `ws1`. That bypassed the semi joins, and every
parallel worker re-ran both SubPlans, each over the 8.7M-row CTE. The SF1
fire-set gate timed out (600 s) where the baseline had passed. PG pulls both up
into semi joins, so a leaf restriction was never PG's shape.

## Verification

- Values: a 10-case probe against PG 18.3 gives identical results
  (`value-probe.txt`). It covers NOT IN with NULLs on a join side, NOT IN in a
  LEFT JOIN ON (nullable side), IN, `NOT (x IN …)`, a scalar comparison, an
  uncorrelated EXISTS, an OR with a sublink, and a correlated NOT IN.
- Gates: units; tpch-spotcheck (Q12=2, Q13=33); sf025 96/96; acceptance arm 24
  MATCH (Q16 0.35 s, Q18 13.4 s, Q22 1.5 s, as before); fire-set PASS with no
  introduced timeouts.

## Measurement (canonical captures)

| corpus | match | categories changed |
|---|---|---|
| TPC-H (`PGSHAPED=1`) | 3 → 3 | parameterisation 6 → 7 (Q22) |
| TPC-DS SF0.25 | 4 → 4 | Q6: scan-type 60 → 59, aggregation-strategy 40 → 41, rendering 24 → 25 |

Per query:
- **Q16:** the NOT IN filter is on the `partsupp` scan, where PG puts it. The
  categories are unchanged, because the join still sits under a post-pass
  Gather. The partsupp partial path cannot be elected: `exprsParallelSafe`
  refuses every SubPlan (M0146-0002f).
- **TPC-DS Q6:** the join tree is now PG's (store_sales ⋈ date_dim with the
  InitPlan filter on `date_dim`, then NL customer → customer_address → item).
  Two differences remain:
  - PG renders the InitPlan at the plan's top, goopg under the scan that
    holds it (a rendering item, M0146-0002g);
  - PG keeps the correlated `i_current_price > 1.2 * (SubPlan)` on the item
    index scan, while goopg's legacy unnest turns it into a hash join over a
    grouped `item` (correlated sublinks: ledgered).
- **Q22:** the InitPlan qual is on the customer scan, as in PG. The
  parameterisation count rises because goopg's anti join is not PG's
  parameterised NL anti join over `order_customer_fkidx` (pre-existing; the
  classifier now attributes it differently).

Movement against PG is therefore in the plan trees (Q16, Q6 and Q22 qual
placement), not in the category counts.
