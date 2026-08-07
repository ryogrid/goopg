M0128-P1.3 COMPLETE — FULL joins + buildJoinRelRestrictList + dedup

Task: M0128-P1.3 — FULL joins + outer-join clause distribution (03 §6)
Files:
  - internal/planner/joinrestrict.go: buildJoinRelRestrictList,
    isOuterJoinFilterClause, dedupRestrictInfoPtrs (91 new lines)
  - internal/planner/joinsearchlevel.go: makeJoinRel captures sjinfo/reversed
    from joinIsLegal, uses buildJoinRelRestrictList instead of clausesFor
  - internal/planner/specialjoin_test.go: 13 new tests (FULL-nesting legality
    matrix, clause distribution, isOuterJoinFilterClause, dedup)

Key symbols: buildJoinRelRestrictList, isOuterJoinFilterClause,
  dedupRestrictInfoPtrs

Hypothesis/Findings:
  - buildJoinRelRestrictList: wraps clausesFor + admits nullable-side filter
    clauses for outer joins (LEFT/FULL/RIGHT). Dedup by pointer equality.
  - makeJoinRel now captures sjinfo from joinIsLegal and uses it for
    restrictlist construction. If reversed, swaps rel1/rel2 per PG.
  - FULL arm of join_is_legal already correct: FULL matches via LHS/RHS
    branches, is rejected for RHS association (else→error), cannot commute.
    Added explicit tests for reversed orientation, RHS building, nested FULL.
  - LhsStrict remains unpopulated (pre-existing P1.2 gap) — the mustBeLeftJoin
    arm of joinIsLegal can never succeed. This is a known gap.
  - addPathsToJoinrel still produces only INNER paths — path-level join type
    plumbing is deferred to when the pin relaxes. Not a regression.
  - Pin still holds: SPOT Q12=2/Q13=35, DS05 95/95 zero row/checksum/plan-shape
    deltas. No behavioral change — the infrastructure is ready for pin removal.

Next step: M0128-P1.4 (semi/anti in-DP, 07 §5) OR M0128-P1.5 (COLLAPSE verdict,
09 §3.19) — consult the Current Priority banner.

Gates run: UNITS PASS, SMOKE PASS (13038 tps), SPOT PASS (Q12=2/Q13=35),
  DS05 PASS (95/95, zero deltas, 99/99 plan-shape identical),
  make ralph-state-guard REPAIRED+OK.

In-flight: none
