Task: M0129-S9.3 DONE — LEFT→ANTI demotion in reduce_outer_joins

Files:
- internal/planner/reduce_outer_joins.go: added collectForcedNullTableNames +
  forced-null propagation through applyDemotion (accumulatedFN), LEFT→ANTI check
  (right-side table in both accumulatedFN AND localNN → ANTI).
- internal/planner/reduce_outer_joins_test.go: added 9 tests covering basic ANTI,
  fixed-constant ON, preserved-side guard (no false demotion), no-ON-clause guard,
  INNER-wins-over-ANTI precedence, IS-NOT-NULL doesn't trigger ANTI, forced-null
  propagation through INNER, multi-join chain ANTI, OR non-examination guard.

Key symbols:
- collectForcedNullTableNames(e): IS NULL → table names (PG find_forced_null_vars analogue)
- applyDemotion: now accepts upperFN + maintains accumulatedFN in parallel with accumulatedNN
- LEFT→ANTI condition: accumulatedFN[rightName] && localNN[rightName]
  (PG reduce_outer_joins_pass2: overlap(nonnullable_vars, forced_null_vars) ∩ right_state->relids)

Hypothesis/Findings:
- PG's find_forced_null_vars only checks NullTest IS_NULL + AND at top level;
  goopg's collectForcedNullTableNames mirrors this at table-level granularity.
- LEFT→ANTI is checked AFTER LEFT→INNER; INNER trumps ANTI when both fire.
- The forced-null set propagates by the same rules as nonnullable (INNER merges,
  LEFT/ANTI pass through, RIGHT/FULL reset).
- DS05 showed zero plan movement (99/99 same plan shapes) — the triggering
  pattern (LEFT JOIN ... ON ... WHERE right_side_col IS NULL) wasn't in any query.

Next step: M0129-S9.4 — RIGHT→LEFT flip + FULL→RIGHT partial reduction.
  Named prerequisite: parser.FromExpr is a Base RangeVar + flat []JoinExpr;
  the parser/AST nested-join representation IS PART OF THIS SUBTASK.
  Last task in the S9 group.

Gates run:
- go build ./...: PASS
- All 32 reduce_outer_joins tests: PASS (23 existing + 9 new)
- Full planner suite: PASS (1.148s)
- Pre-commit (units): PASS
- tpch-spotcheck: PASS (Q12=2 Q13=35, 31.5s)
- DS05: PASS (95/99, zero row/checksum/plan deltas)

In-flight: none
