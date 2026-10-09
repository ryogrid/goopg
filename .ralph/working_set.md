Task (NEXT LOOP): re-read the banner first. Owner-pending: M0146-0089 / 0084 / 0085 / 0086 / 0125 / 0126 (S2 placements),
  M0146-0068 (B8 option choice -> unblocks 27 records), SF1 cluster reload (15 RELPAGES records), M0146-0055 [!].
  S4: root M0146-0005 held; root M0146-0007 has ONE `none` slot left (last five: 0118 yes, 0119/0123/0124/0129 none).
  Root M0146-0014 (via 0014a): 0130 Movement yes.
  Done this loop: M0146-0130 (impl, Parent M0146-0014a) — code a6905e365. A correlated scalar sublink conjunct that
  reads its own leaf's column is filtered on that leaf at a non-zero binding (correlatedScalarSublinkLeaf /
  correlatedScalarSublinkBinding); SubqueryExpr/ExistsExpr.OuterRowPad pads the outer row (executor padOuterRow,
  subplan_lower slotFor); subtreeHasParallelRestrictedQual keeps a correlated SubPlan qual out of the partial
  aggregate/distinct splits only (a global hazard in subtreeHasUnsafeNode lost Q32/Q92 SF1's Gather — reverted).
  Q6 -> [parameterisation] both scales (PG shape node-for-node; only `$0` vs `i.i_category` rendering left);
  Q92 SF0.25 5 -> 2 categories. TPC-H plans byte-identical; regress join row-order only.
Files: internal/optimizer/{local_filters,subplan_lower,parallel,partialaggupper,distinctpaths,plan}.go,
  internal/executor/expr.go, tests correlated_leaf_offset_test.go / parallel_restricted_qual_test.go,
  docs/design/0100-0149/m0146-0130-correlated-sublink-leaf-and-parallel-restriction.md.
Key symbols: correlatedScalarSublinkBinding, OuterRowPad, padOuterRow, sublinkHandle.pad,
  subtreeHasParallelRestrictedQual.
Hypothesis/Findings: remaining Q6 nit is EXPLAIN exec-param owner rendering (ledgered, all bindings). New-task anchors
  for the rest: considerparallel.go computeParallelWorker (0132), upperorderedinput.go inputNodePathkeys (0133),
  pathKeyEqual (0134), querypathkeys.go groupClauseItems + partialaggupper.go no-split hashed arm (0138),
  getCheapestFractionalPathOrdered (0131).
  Method: private clone tmp/c20a/data-sf025 on 5537 with GOOPG_PGSHAPED_DP_TRACE=1, ANALYZE in-session; PG reference
  psql -p 65438 -U ryo -d tpcds025 (EXPLAIN only; never `analyze` in a command naming 65438). Stop debug servers
  before ea-ratchet (EA_PORT=5537). Acceptance arm needs ACCEPT_BASELINE=bench/tpch/baseline-digests.txt or the
  stamp reads NO-COMPARE. TPC-H plan A/B: REFERENCE= PLAN_ONLY=1 scripts/tpch-estimate-audit-arm.sh <label>
  (the default REFERENCE file is gone). Regress A/B: tmp/m130-reg.sh; baseline tmp/m130-reg-new is current HEAD.
  fix_plan IDs are backslash-escaped (`M0146\-0131`) — grep with that form.
LINEAGE: roots M0146-0055 [!] and M0146-0005 (S4) — do NOT select/file under them.
Next step: re-read the banner; then take M0146-0131..0140 in file order (item 3, root 0014 has budget) — start with the
  first one whose trace confirms the mechanism (smallest-looking: 0133, 0132, 0140).
Top-residual: join-order=48 at SF1 fire-set census (SF0.25: 40); by route: COSTTIE 31, B8 27 (owner), RELPAGES 15 (owner)
Skip-rationale: the largest routed residuals are owner-gated (B8 decision, SF1 reload) or near-ties; the filed
  mechanism tasks are the loop's remaining actionable work.
Gates run: units, tpch-spotcheck, acceptance arm (values PASS), fire set, SF0.25 sweep 96/96, ea-ratchet (1),
  TPC-H plan A/B (byte-identical), regress A/B 32 cases; ralph-state-guard; pgbench smoke via commit hook.
In-flight: none
