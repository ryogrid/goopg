# Working Set — Loop #21 end state

Task: M0143-0009 — `SELECT … FOR UPDATE` over a join dropped every row when a
locked leaf was not the rightmost scan (resjunk-ctid ColumnRef shift). DONE —
implemented, all 8 arms of `TestLockRowsJoinCtidShift` green, verified
end-to-end on a scratch cluster (:5533) including hash-join shapes.

Also done this loop (same commit):
- M0142-0005g recon recorded under its (still owner-frozen `[ ]`) entry:
  `patternsel.go` is a faithful `patternsel_common` port; the 2×
  `LIKE '%green%'` estimate divergence is corpus data + independent
  30k-row reservoir sampling, not planner logic (goopg 6/99 interior
  bounds vs PG 3/99; logical corpus counts differ too).
- M0143-0009 was filed this loop after the live wrong-results repro.
- AI-007 ledger row: RESOLUTION RECORD appended (status flip is owner-only).

Files changed:
- internal/optimizer/plan.go — `SchemaColumn.Resjunk` (PG resjunk analogue),
  `LockedRel.ColPos` (per-column EPQ-merge positions, -1 when the projection
  doesn't carry the column), `LockRows.Output()` strips resjunk positions
  anywhere in the row (trailing-`NumCtidCols` kept only as hand-built-plan
  fallback).
- internal/optimizer/planner.go — `wireRowMarkCtidColumns` now snapshots
  `oldW[n]` per node before tagging and matches leaves by (OID, effective
  alias); `rebaseRowMarkPlan` post-order walk rebases every expression via
  per-node old→new position remaps — `concatRemap` (merged =
  `leftRemap ++ (newLeftW + rightRemap[j])`), join Predicate/keys/UsingCols
  in MERGED coords (executor proves it via mergedKeySlot/keySlot.rebind),
  NLI probe keys outer-scoped, unary ancestors over childRemap, cached
  schemas rebuilt for Distinct/DistinctOn/Gather/GatherMerge/OrdinalityWrap/
  ProjectSet; walk-wide `seen` dedupes shared *ColumnRef objects
  (HashKeys[0] aliases LeftKey/RightKey by pointer). `resolveRowMarkCtidResnos`
  resolves ctid positions for non-Project roots, fixes `ColOffset` past
  earlier insertions, populates `ColPos`. `hasSelfJoinLockedTable` and the
  name-keyed `fixColumnRefIndices`/`fixColumnRefsInExpr`/`recomputeIntermediateSchemas`
  path retired.
- internal/executor/operators_lockrows.go — `junkPos` resjunk-position set
  replaces the trailing-N row trim; EPQ refetch-merge prefers `ColPos`.
- internal/executor/operators_distinct.go + operators_recursive_cte.go —
  `rowKeyExcluding` skips resjunk positions in whole-row dedup.
- internal/executor/operators_lockrows_test.go — `TestLockRowsJoinCtidShift`
  (8 arms: left/right/bare, swapped-FROM, `SELECT *` join-rooted, ORDER BY
  above join, DISTINCT, self-join); helper clones slot rows.
- internal/optimizer/locking_test.go — self-join pins updated for the
  retired AI-007 guard (2 ctid cols, distinct resnos).
- docs/design/0100-0149/m0143-0009-rowmark-ctid-global-expr-rebase.md +
  README row.

Key lesson: `HashKeys[0]` aliases `{LeftKey, RightKey}` BY POINTER and
`cloneKeyExpr` clones only bare ColumnRefs — in-place ref rebasing without a
dedupe set double-applies to shared objects (M0097-0060's class).

Gates run: `TestLockRowsJoinCtidShift` 8/8 PASS; `TestPlanCtidRowMark*`
3/3 PASS; executor + optimizer package suites PASS; scratch-cluster live
check PASS (NL+IndexScan, SeqScan+HashJoin, self-join, DISTINCT, `SELECT *`).

In-flight: scratch goopg server on :5533 (tmp/m0119-br/data, cgroup
rowmark-m143) still running — stop with `systemctl --user stop
rowmark-m143.scope` or reuse. emp143/dept143/big143a/big143b test tables left
in its postgres DB.

Next step: banner item 8 continues — M0119-0006 remains the living
ledger-drain milestone; next selectables per banner order: M0122-0008..0015,
then M0131.
