Task: M0145-0030 — group B: adjudicate the behavioural flip-triage tests
(+ knob-default script audit). Design doc
docs/design/0100-0149/m0145-0030-group-b-adjudication.md (disposition table).

Done: fix 1 (alias collision) — assignPulledSourceOffsets in
internal/optimizer/jointreepullup.go; test jointreepullup_srcidx_test.go.

Still failing under a local flip (jointreepipeline.go:27 `v == "1"` →
`v != "0"`, REVERT before commit):
- optimizer TestCreateGroupingPathsGucOnPkFdStaysHash: Strategy=1 (sorted),
  want Hashed — check against PG 18.3 which strategy it picks on that shape.
- optimizer TestPlannerSettingsReachScalarSubqueryJoin/cpu_tuple_cost/nested
  and TestScalarSubqueryPropagationKeepsDefaultPlan/nested: "2 costed inner
  plans, want 1" — likely the jointree arm plans the nested scalar subquery
  differently (PG-faithful or not? check EXPLAIN vs PG).
- executor TestExplainAnalyzeRowsRemovedByJoinFilter: confirm stale vs PG.
Script audit: scripts/tpcds-sf025-regression.sh:310,
scripts/tpch-estimate-audit-arm.sh:108 (knob default 0), sweep :842 → list
for the flip commit; do not change defaults now.

Disk: the fire-set gate leaves ~14G of tmp/<label>-*-data-* clones per run;
this loop freed ~150G by deleting loop-owned tmp/m0145-00*-data* clones
(ENOSPC mid-gate). Delete your own gate clones after each run.

Next step: TestCreateGroupingPathsGucOnPkFdStaysHash — read it, reproduce on
PG 18.3, decide PG-faithful vs defect.

Gates run: units, tpch-spotcheck (Q12=2 Q13=33), tpcds-sf025 (PASS=96,
same=99), acceptance arm (identical), tpcds-fireset (25 fires; plans
identical modulo qualifiers) — PASS.
In-flight: none.
