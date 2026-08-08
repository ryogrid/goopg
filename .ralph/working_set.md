Task: M0129-S6 COMPLETE — resjunk-ctid column path re-enable
Files:
- internal/planner/planner.go: numCtid := wireRowMarkCtidColumns(out, locks);
  recomputeIntermediateSchemas(out) when numCtid>0;
  fixColumnRefIndices fixes ALL ColumnRefs via (name,SourceTableIdx) lookup;
  ctid ColumnRefs now carry SourceTableIdx:-1
- internal/planner/locking_test.go: re-enabled TestPlanCtidRowMarkWiring +
  TestPlanCtidRowMarkMultiTable; NEW TestPlanCtidRowMarkSelfJoin
- docs/design/0129-0003-resjunk-ctid-column-path.md: status → accepted
- .ralph/fix_plan.md: M0129-S6 → [x]

Key symbols:
- recomputeIntermediateSchemas (planner.go): post-order recomputation of
  Join/NLIJ schemas from children's Output()
- fixColumnRefIndices + fixColumnRefsInExpr (planner.go): (name,srcIdx) lookup
  in child output; walks sub-expressions via exprChildSlots
- columnKey struct: {name string, srcIdx int16} for disambiguation

Hypothesis/Findings:
- Column path re-enabled with correct schema propagation.
- All planner unit tests pass including self-join ctid disambiguation.
- Isolation test (TestPort_IsolationEvalPlanQual) has a PRE-EXISTING failure
  (same diff at f96b669d — verified by stash test). The column-path disable
  was masking a slot-side-channel self-join TID-loss bug, not preventing
  a regression.
- Slot side-channel retained as belt-and-braces (CTE scans, VALUES).
- S6 subsumes S3 (sort-spill TID loss): a ctid datum in the row survives
  spill like any other column. S3 reduces to verifying the shape and closing
  root-0038.

Next step: M0129-S7 (clause-6 re-adjudication) — run the estimate-audit
comparison; no engine change expected.

Gates run:
- UNITS: PASS (all packages)
- SPOT: PASS (Q12=2 Q13=35)
- DS05: PASS (95/99, 0 deltas, plan shapes identical)
- ISOLATION: pre-existing failure (not a regression)
- pgbench smoke: PASS (401 TPS, 0 failures)

In-flight: none
