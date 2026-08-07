M0128-P1.2 COMPLETE — join_is_legal + joinOrderRestriction + hasJoinRestriction for LEFT/FULL

Task: M0128-P1.2 — join_is_legal + joinOrderRestriction/hasJoinRestriction for LEFT joins
Files:
  - internal/planner/joinsearchlevel.go: joinIsLegal, joinOrderRestricted,
    hasJoinRestriction methods on searchCtx; joinIsLegal wired into makeJoinRel
  - internal/planner/joinsearch.go: joinInfoList field on searchCtx; updated
    newSearchCtx + buildInitialRels signatures
  - internal/planner/relfromjoinlist.go: joinInfoList on joinlistProblem,
    threaded through searchOneProblem → buildInitialRels
  - internal/planner/joinsearchseam.go: ctx.joinInfoList passed to
    joinlistProblem construction
  - internal/planner/specialjoin_test.go: 18 legality-matrix tests for all
    three functions
  - 10 test files: nil joinInfoList in newSearchCtx/buildInitialRels callers

Key symbols: searchCtx.joinIsLegal, searchCtx.joinOrderRestricted,
  searchCtx.hasJoinRestriction, joinlistProblem.joinInfoList

Hypothesis/Findings:
  - joinIsLegal: PG joinrels.c:350 port, LJO arm only. Checks every
    SpecialJoinInfo entry: RHS overlap fast-path, subset-in-RHS skip,
    already-contained skip, LHS⊆rel1+RHS⊆rel2→match, both-overlap-RHS
    commutation, LEFT association into RHS with must_be_leftjoin post-scan.
    Returns (sjinfo, reversed, nil) for special joins, (nil, false, nil)
    for plain inner joins, error for illegal pairs.
  - joinOrderRestricted: PG joinrels.c:1066 port, LJO/FULL arms. Checks
    if pair forms SJ, or both overlap RHS, or both overlap LHS. Skip FULL.
    Post-filters on hasRelevantJoinClause per PG.
  - hasJoinRestriction: PG joinrels.c:1178 port. Detects rel-SJ overlap
    beyond containment.
  - With pin in place (03 §4.4), all searched rels are inner-joinable, so
    joinInfoList entries reference pinned outer-join items not in the search
    → all three functions return zero-effect results. Verified: SPOT
    Q12=2/Q13=35, DS05 95/95 zero row/checksum/plan-shape deltas.

Next step: M0128-P1.3 (FULL joins + outer-join clause distribution, 03 §6)

Gates run: UNITS PASS, SPOT PASS (Q12=2/Q13=35), DS05 PASS (95/95, zero
  deltas), pgbench smoke PASS, make ralph-state-guard REPAIRED+OK.

In-flight: none
