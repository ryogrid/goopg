# M0148-0001f — EXPLAIN Output() targetlist rendering and per-node properties

**Parent:** M0148-0001 `aggregates.sql` regress coverage closure · **Status:**
slice 1 (`Disabled: true`) landed `29d2af8d5` (run\-2 loop #22, 2026\-11\-02);
slice 2 (Output: placement and prefix) landed `2faedaef2` (run\-2 loop #23);
the task stays open for the Output: targetlist work
**Target:** aggregates hunks with `Output:` lines (expected\-lines 608, 646,
2946, 2982, the array\_agg\(DISTINCT\) block) and the merge\-join
`Disabled: true` blocks (2761, 2795, 2818)

## Slice 1 — `Disabled: true`

PG 18's `ExplainNode` (`explain.c`) prints `Disabled: true` in TEXT format
right after a node's line — before Output and every other property — when
`plan_is_disabled(plan)`: the plan's `disabled_nodes` exceed the sum over its
children (outer/inner plan, every Append / MergeAppend member, a Subquery
Scan's subplan). Non\-text formats always carry the `Disabled` property. A
node is disabled when an `enable_*` GUC turned *it* off; a Sort over a
disabled Seq Scan is not.

goopg never printed the line, so the three merge\-join blocks under
`enable_seqscan = off` differed by two lines each.

- `PlanCost.Disabled` (`internal/optimizer/plancost.go`) is stamped in the
  single `stampPlanCost` funnel from the producing path:
  `pathSelfDisabled(p)` is `p.DisabledNodes` > the sum over `p.Children`
  (create\_plan copies each path's disabled\_nodes onto its plan, so this is
  `plan_is_disabled` one level earlier). A `PathPrebuilt` re\-stamp (an upper
  rel re\-wrapping an input an earlier search built) keeps the node's own bit:
  the prebuilt path carries no count.
- Both text walkers (`walkPlanFiltered`, `walkPlanAnalyzeFiltered`) print the
  line after the node line, reading the collapsed `Filter` wrapper as well —
  a filtered scan's path stamp lands on that wrapper, exactly as its cost
  does. `planToJSON` adds the `Disabled` property.
- The line exposed a cost\-model infidelity, fixed in the same slice:
  `cost_incremental_sort` sets `disabled_nodes = input_disabled_nodes`
  (enable\_sort never reaches it; `enable_incremental_sort = off` simply never
  builds the path), but `incrementalSortPathWithGroups` folded enable\_sort in.

### Evidence

- Regress runner, HEAD vs change over 34 suites: aggregates 360 → 348 diff
  lines / 24 → 21 hunks (the three merge\-join blocks byte\-identical);
  incremental\_sort 9 → 8 hunks. Suites whose line count rose (create\_index,
  inherit, union, groupingsets, partition\_prune, join) gained only
  `Disabled: true` lines on goopg plans that already diverge: GUC\-disabled
  Seq Scans, Sorts and MixedAggregates goopg falls back to where PG has an
  enabled path (no GiST index path in create\_index; ordered Append/Merge
  Append in inherit; plain Aggregate under enable\_hashagg = off in
  groupingsets). Those are pre\-existing plan gaps the line now makes
  visible, ledgered.
- Pin: `TestExplainDisabledNodes`.

## Slice 2 — VERBOSE `Output:` placement and prefix

`ExplainNode` prints VERBOSE's `show_plan_tlist` directly after the node line
and `Disabled:`, before `Inner Unique` and the node\-type switch that prints
every qual and key. goopg printed `Output:` after them. And
`show_plan_tlist` qualifies its columns by `useprefix = es->rtable_size > 1`
alone — VERBOSE does not force it, unlike quals and keys (slice 6 of
M0148\-0001e) — while goopg printed bare names everywhere.

- Both text walkers emit `Output:` right after the `Disabled` line.
- `explainOutputNames` qualifies each output column that still carries its
  source binding (`SourceTableIdx`) through `columnIn` when
  `explainNames.qualifyTlist()` (plan\-visible relations > 1 or
  `rtableSize > 1`) holds; computed outputs keep their names. The text walkers
  and `planToJSON`'s `Output` property share it.
- Regress runner, HEAD vs change over 34 suites: PG `Output:` lines left
  unmatched 979 → 895; join 15034 → 14932 diff lines, subselect
  1438 → 1384, rangefuncs, select\_distinct and with smaller; 29 suites
  unchanged — placement alone reorders lines inside hunks whose Output
  content still differs. No goopg `Output:` line is qualified where PG's is
  bare.
- Pin: `TestExplainVerboseOutputPlacementAndPrefix`.

## Remaining (task stays open)

- Output: the pruned per\-node targetlist with qualified Vars, aggregate
  labels (`(sum(...))`, `PARTIAL variance(...)`, `ARRAY(SubPlan 1)`,
  `(InitPlan 1).col1`), Output placement (directly under the node line,
  before Group Key / Sort Key), `Function Call:` before `Output:`
  (`explain.c` `show_plan_tlist`, `ruleutils.c` deparse context).
- Group Key / Sort Key operand parenthesisation residue.
