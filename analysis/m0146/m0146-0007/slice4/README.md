# M0146-0007 slice 4 (M0146-0007d): a CTE referenced only from sublinks still hoists

TPC-DS Q14's `avg_sales` is referenced ONLY from inside scalar InitPlans
(`sum(...) > (SELECT average_sales FROM avg_sales)`, three probes).
`collectCTEHoist` (internal/executor/explain_cte.go) walked the plan spine
via planChildren only, so the CTEScan inside each InitPlan's body was never
claimed — every InitPlan rendered the CTE's whole Finalize-Aggregate body
inline, three copies. PG prints one `CTE avg_sales` section under the top
node (the CTE subplan rides the topmost `init_plans`, explain.c
ExplainSubPlans) and bare `CTE Scan on avg_sales` leaves at the probes.

## Change

`collectCTEHoist` walks `optimizer.NodeSubplans(n)` at every node after the
planChildren pass — the same expression-side enumeration explain_names.go
uses for qualifier attribution (slot-driven `ExprSubplans` covers
SubqueryExpr / ExistsExpr / InExpr / ArraySubqueryExpr / MultiAssignSubqRow;
kept in lockstep with walkPlanExprs by exprwalk_inventory_test.go). Claim
keying, dedup, inlined-scan descent and declSeq ordering are unchanged;
nested sublinks inside a reached body recurse through the same walk.

## Results

- `q14-goopg-sf025-before.txt` / `q14-goopg-sf025.txt`: statement 1 goes
  from three inline Finalize-Aggregate copies to one `CTE avg_sales`
  section plus three bare `CTE Scan on avg_sales` leaves, matching
  `q14-pg-sf025.txt` (bench/tpcds/plans-pg/Q14.txt).
- First-divergence class (fireset census, pg-plan-divergence-class.py on
  the live SF0.25 captures — `census-sf025-{baseline,candidate}-class.txt`):
  `D6-cte` 1 → 0, Q14 → `D1-sublink` (5 → 6). The residual is the
  Append-leg aggregate shape (MixedAggregate vs PG's
  GroupAggregate→Sort→Append) and the inner join strategy beneath it —
  routed outside this task.
- Closes the 2026-08-06 M0125-0049 ledger row "a CTE referenced ONLY from
  inside a sublink is not collected" (its "single reference prints once
  either way" caveat was wrong for N-probe CTEs — three InitPlans rendered
  three inline copies).
- Regression: `TestExplainSublinkOnlyCTEHoistsToSection`.

## Gates

See `gates.txt`.
