Task: M0145-0018 — relax the `outer-over-derived` firewall (owner GO) — EXECUTED, uncommitted
Files: internal/optimizer/relfromjoinlist.go (firewall deleted), flaglabels.go (GOOPG_DERIVED_FIREWALL
  retired), scripts/planner-flags.env (regen), deleted outer_over_derived_test.go + firewall pins in
  4 test files, comments in joinsearchseam/jointreepullup/cardinality/3 test files, docs/design/
  0100-0149/m0145-0018-*.md (execution section), docs/design/README.md (index), .ralph/fix_plan.md ([x])
Key symbols: deleted problemPairsOuterWithDerived, derivedFirewallEnabled, leafIsDerivedInput,
  searchOneProblem decline site; kept isSemiAntiSyntheticLeaf (c11 fillable reader)
Hypothesis/Findings: waiver regression did NOT recur — pulled CTE leaves now carry EstimateRows
  cardinalities (rows=12/60) so Q77 degenerate branch elects Hash Left Join, not the waived NL.
  Q78 stays ~29s hash-join class at SF1.
Next step: `make ralph-state-guard`, then commit explicit paths (no -A) + push. Then re-read
  banner: 0005 chain is done; next is the 0007→0008 chain per banner.
Gates run: optimizer suite PASS; units PASS; tpch-spotcheck PASS (Q12=2,Q13=33); SF0.25 sweep
  96/96 PASS (plan channel: only Q77+Q78); seam census outer-over-derived 3->0 both arms; TPC-H
  acceptance 24/24 MATCH; SF1 Q77 5.76s/Q78 29.01s values identical; fire-set gate PASS
  (all fires, both corpora, both arms, introduced=none)
In-flight: none — first fire-set run wedged (11-min clone start beat 120s readiness loop; cleanup
  `wait` blocked forever); killed and re-ran clean after stopping the competing SF1 server.
  Incident recorded in design doc §Execution.
