Task: M0145-0030 — group B: adjudicate the behavioural flip-triage tests
(plus the knob-default script audit). M0145-0029 CLOSED this loop.

State under a local flip (jointreepipeline.go:27 `v == "1"` → `v != "0"`,
REVERT before commit), after all of M0145-0029: 5 of the 11 group-B tests
still fail (NLI semi/anti residual, parallel NLI jointype, hashed-IN probe,
RunFastJoinConcrete now PASS):
- optimizer TestCreateGroupingPathsGucOnPkFdStaysHash: Strategy=1 (sorted),
  want Hashed.
- optimizer TestPlannerSettingsReachScalarSubqueryJoin/cpu_tuple_cost/nested
  and TestScalarSubqueryPropagationKeepsDefaultPlan/nested: nested plan has
  2 costed inner plans, want 1.
- optimizer TestExplainSelfCorrelatedExistsDoesNotAliasCollide: REAL EXPLAIN
  BUG on the jointree arm — `Hash Cond: (t1.a = t1.a)`, `Join Filter:
  (t1.b <> t1.b)`; the inner side must render t2.
- executor TestExplainAnalyzeRowsRemovedByJoinFilter: triage already classed
  it stale (PG pushes the nullable-side ON qual down as a scan filter).
Script audit still to do: scripts/tpcds-sf025-regression.sh:310 and
scripts/tpch-estimate-audit-arm.sh:108 default the knob to 0; the sweep's
:842 control pass must become `=0` — list them for the flip commit, do NOT
change defaults now.

Next step: start with the alias-collision EXPLAIN bug (a real defect): trace
how the semi-join's inner-side ColumnRefs get their alias on the jointree
arm vs legacy; then the grouping strategy and nested-subquery tests, each
checked against a private PG 18.3.

Gates run: none this loop (docs-only closure commit; the pre-commit pgbench
smoke runs on commit). The previous loop's gate set on 98f68622d all PASS.
In-flight: none.
