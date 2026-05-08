# M0071-0001 Q9 Chained-NLI Investigation (2026-05-09)

**Scope:** Time-boxed (60 min) investigation of whether the
Q9 schema-annotation-vs-runtime-layout mismatch can be fixed
purely in the planner, per the M0071 milestone's
"planner-only fixes where feasible" directive.

**Outcome:** **DEFERRED → M0071-0005** (TupleSlot pipeline).
A planner-only fix that doesn't regress the existing
defensive gates is not feasible.

## Current state

Q9 SF=1 baseline (post-M0070 + M0071-0002/0003/0004
landings):

  Q9 OK 237.20 s rows=7   (canonical TPC-H SF=1 ≈ 175)

Same 7-row silent FN as M0067-0067 / M0068 / M0069 / M0070
baselines.

## Why a planner-only fix is structurally blocked

The recon (`Phase 1 Explore agent` 2026-05-09) and the
existing code comments at
`internal/planner/nl_index_join.go:399` and
`internal/planner/bushy.go:1548` document the same
constraint: **chained-NLI keys already align with the
runtime row layout — touching them breaks Q9.**

The two defensive gates exist precisely because earlier
attempts at rebinding the chained-NLI keys regressed Q9
worse:

- **M0064 (`nl_index_join.go:399`):** Name re-bind gated on
  `outerNode being *MultiHashJoin`. The comment at
  lines 388-398 records that for `*NestedLoopIndexJoin`
  outers (Q9's chained-NLI shape), the downstream remap
  walkers leave NLI keys in their pre-rewrite indices on
  purpose; rebinding moved the probe onto a different
  table's column at runtime — the `l_suppkey 15 → 24`
  regression. M0064 SETTLED on "do not rebind for NLI
  outers."

- **M0065 (`bushy.go:1548`):** `applyJoinTreePosMap` walks
  into NLI's Outer but stops at NLI's own keys. The comment
  at lines 1549-1556 says posMap remap and Name re-resolve
  both empirically break Q9; the safe thing is leave NLI
  keys alone in this walker.

- **M0067-0003 attempt:** re-enabled the composite-NLI
  hoist + Name-based rebind for chained-NLI. Q9 went
  7 rows → **1 row** (worse). Reverted.

The shared root cause: **the planner's schema annotation
diverges from the executor's runtime row layout for
chained NLI**. The planner has no unified
column-coordinate model that can stably reference a column
across multiple substituted-tree depths. Each substitution
(MHJ rewrite, NLI rewrite, posMap remap) reorders columns,
and the OuterColumnRef indices set by the binder become
stale.

The TupleSlot pipeline (M0071-0005) provides exactly this
unified model: each slot carries explicit
`(sourceIdx, sourceCol)` virtual coordinates that survive
operator substitutions. Without slot, every planner-side
rebind attempt has to re-derive coordinates manually, and
the M0064/M0065/M0067-0003 history shows that any rebind
"fix" tends to break something else.

## Hypotheses tried in this time-boxed session

1. **Hypothesis I: depth-aware rebind gate.** Restrict
   rebind to OUTERMOST NLI's keys, not chained inner NLI's
   keys. **Verdict:** the existing gate at
   `nl_index_join.go:399` already restricts rebind to
   non-NLI outers; the proposed depth-awareness would only
   re-enable rebinding for NLI outers, which the
   M0067-0003 attempt confirmed regresses Q9.

2. **Hypothesis II: posMap-driven rebind via
   `applyJoinTreePosMap` recursion.** The
   `bushy.go:1548` comment explicitly tested this and
   documents it broke Q9 the same way.

3. **Hypothesis III: Q9's 7 rows are correct under
   goopg's HammerDB schema variant.** Cross-checked with
   the M0067-0003 attempt's empirical result: with the
   composite-NLI hoist active, Q9 returned 1 row, not 175
   or anything close. If 7 were correct under the loaded
   data, the hoist would have produced ≥ 7. It produced 1
   — which means even fewer rows survived after the
   rebind moved keys to wrong slots. So 7 is NOT
   correct; 175 (canonical) is the target.

## Decision

Q9's row-count fix is carried to **M0071-0005** as part of
the TupleSlot pipeline migration. The slot model's
`(sourceIdx, sourceCol)` virtual addressing is the
structural fix that the M0064/M0065/M0067 history
identifies as the proper resolution.

This decision is consistent with the user's "planner-only
where feasible" directive: it confirms Q9 is NOT
planner-only feasible without regressing one of the
existing defensive gates.

## Files

This investigation produced no code changes — only this
analysis report. The existing defensive gates at
`internal/planner/nl_index_join.go:399` and
`internal/planner/bushy.go:1548` are preserved as
correct-by-design behaviour until M0071-0005 lands.

## References

- `analysis/tpch-m0067-baseline-2026-05-08.md:90-130`
  (M0067-0003 composite-NLI hoist attempt + 1-row
  regression).
- `analysis/tpch-m0064-baseline-2026-05-07.md` (Name
  re-bind gate motivation).
- `internal/planner/nl_index_join.go:380-413` (M0064 outer
  rebind gate — `outerNode == *MultiHashJoin` constraint).
- `internal/planner/bushy.go:1548-1557` (M0065 NLI walker
  stop-at-keys constraint).
- `docs/design/0068-0002-tuple-slot-pipeline.md` (slot
  pipeline design — M0071-0005 successor).
