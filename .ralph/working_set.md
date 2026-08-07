M0128-P4.1 COMPLETE — reduce_outer_joins reduction half landed

Task: M0128-P4.1 — demote outer joins to inner when a strict WHERE qual
  constrains the nullable side (PG prepjointree.c reduce_outer_joins)

Files:
  - internal/planner/reduce_outer_joins.go: new — reduceOuterJoins,
    applyDemotion, collectNonNullableTableNames, isStrictCompareOp,
    collectColumnRefTableNames, rangeVarNames/rangeVarPrimaryName, anyNameIn
  - internal/planner/reduce_outer_joins_test.go: new — 16 tests covering
    LEFT/RIGHT/FULL demotion matrix, IS NOT NULL/IS NULL, OR conservative,
    multi-join chain, LIKE non-strict, alias, <>, inner-unaffected
  - internal/planner/planner.go: hook reduceOuterJoins before
    deconstructJointree in planFromClause
  - .ralph/fix_plan.md: M0128-P4.1 checked off
  - .ralph/deferral_ledger.md: ledger row for RIGHT→LEFT flip, FULL→RIGHT,
    ON-clause propagation, LEFT→ANTI, strictness catalog

Key symbols: reduceOuterJoins, applyDemotion, collectNonNullableTableNames,
  isStrictCompareOp

Hypothesis/Findings:
  - goopg's flat FromExpr (Base + []JoinExpr, strictly left-deep) makes the
    algorithm simpler than PG's recursive two-pass: each JoinExpr.Right is a
    single RangeVar, so name matching against parsed ColumnRef table names
    suffices
  - The hook point (before deconstructJointree, after plan tree is built) lets
    demoted joins enter the joinlist as plain INNER
  - Zero plan-shape changes in DS05: no TPC-DS query has WHERE-on-nullable-side
    of an outer join (the demotion only fires on specific query patterns)
  - The reduction is a pessimization fix — conservative (misses demotions
    without a strictness catalog, but never falsely demotes)
  - PG's find_nonnullable_rels depends on func_strict() → pg_proc.proisstrict;
    goopg has no such catalog, so only hardcoded comparison operators are
    treated as strict

Next step: M0128-P5.1 (EXPLAIN range-table name dedup) or next M0128 task per
  fix_plan.md ordering (P4.1 → P5.1)

Gates run: UNITS PASS, SPOT PASS (Q12=2/Q13=35), DS05 PASS (95/99, zero
  row/checksum/plan deltas)

In-flight: none
