M0128-P1.1 COMPLETE — SpecialJoinInfo representation + construction

Task: M0128-P1.1 — SpecialJoinInfo representation + construction
Files:
  - internal/planner/specialjoin.go (NEW): SpecialJoinInfo struct, makeSpecialJoinInfo,
    joinlistRelSet, collectSpecialJoinInfos
  - internal/planner/collapse.go: sjinfo field on joinlistItem, SpecialJoinInfo
    construction in deconstructFromItem for LEFT/RIGHT/FULL on pinned items
  - internal/planner/planner.go: joinInfoList on resolveContext, populated after
    deconstructJointree
  - internal/planner/specialjoin_test.go (NEW): 15 tests covering basic/nested/
    mixed special joins, empty cases, resolveContext population, field correctness

Key symbols: SpecialJoinInfo, makeSpecialJoinInfo, joinlistRelSet,
  collectSpecialJoinInfos, joinlistItem.sjinfo, resolveContext.joinInfoList

Hypothesis/Findings:
  - SpecialJoinInfo is built bottom-up during deconstructFromItem for every
    LEFT/RIGHT/FULL join (SEMI/ANTI parser support varies — structural path
    exists). Each pinned item carries its sjinfo.
  - min_lefthand/min_righthand are conservative for LEFT (syn = min); the true
    clause-based computation is P1.2's job.
  - commute fields, lhs_strict, ojrelid, and semi fields are zero/nil — all
    populated when consumed (P1.2/P1.4).
  - resolveContext.joinInfoList is populated from the joinlist's
    collectSpecialJoinInfos immediately after deconstruction.
  - Zero behavioral change verified (SPOT Q12=2/Q13=35, DS05 95 PASS/0 MISMATCH,
    99/99 plan shapes identical).

Next step: M0128-P1.2 (join_is_legal + have_join_order_restriction for LEFT joins
  — consume P1.1's entries, replace constant-false stubs in joinsearchlevel.go)

Gates run: UNITS PASS, SPOT PASS (Q12=2/Q13=35), DS05 PASS (95/95, zero
  row/checksum/plan-shape deltas), make ralph-state-guard REPAIRED+OK.

In-flight: none
