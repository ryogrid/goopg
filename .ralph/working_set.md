Task: M0129-S9.2 DONE — ON-clause propagation in reduce_outer_joins

Files:
- internal/planner/reduce_outer_joins.go: refactored reduceOuterJoins + applyDemotion
  to compute local nonnullable rels from each JoinExpr.On clause and propagate
  per join type (INNER merges local→accumulated; LEFT preserves upper only;
  RIGHT resets to right-side local; FULL resets to empty). Removed early-return
  when WHERE is nil so ON-clause-only propagation works.
- internal/planner/reduce_outer_joins_test.go: added 7 new tests covering:
  INNER ON → RIGHT demotion (core S9.2 enablement), LEFT check (no false
  demotion), self-demotion guard (LEFT ON does not demote itself), multi-INNER
  chain propagation, WHERE+ON interplay, LEFT ON non-propagation guard, and
  FULL JOIN reset guard.

Key symbols:
- applyDemotion(item, upperNN, tableMap, cat): now accepts tableMap+cat for
  ON-clause collection; uses evolving accumulatedNN
- containsName(names, name): new helper wrapping slices.Contains
- localNN per join: collectNonNullableTableNames(j.On, tableMap, cat)

Hypothesis/Findings:
- PG reduce_outer_joins_pass2 only uses UPPER nonnullable_rels for demotion,
  never the local ON clause's own findings. Our implementation matches: localNN
  is computed but only used for propagation after the join type is resolved,
  never for self-demotion.
- The key new capability: an INNER JOIN's strict ON clause (e.g. a.x = b.y)
  makes a and b non-nullable in the result, which can then demote a subsequent
  RIGHT JOIN whose nullable side includes those tables.
- LEFT JOIN ON findings do NOT propagate (b can be null-extended), matching PG.

Next step: M0129-S9.3 — LEFT→ANTI. find_forced_null_vars analogue (IS NULL on
  nullable-side columns). PG reduce_outer_joins_pass2 lines 3388-3403:
  find_nonnullable_vars(j->quals), intersect with forced_null_vars from above,
  check overlap with right_state->relids → demote LEFT to ANTI.

Gates run:
- go build ./...: PASS
- All 23 reduce_outer_joins tests: PASS
- Full planner suite: PASS (1.146s)
- Pre-commit (units): PASS
- tpch-spotcheck: PASS (Q12=2 Q13=35, 29.6s)
- make ralph-state-guard: PASS (auto-repaired)

In-flight: none
